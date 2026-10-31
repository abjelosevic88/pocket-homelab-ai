// Package hold keeps a coding CLI running when the connection to it goes.
//
// A holder (pocket-hold serve) is started per conversation on the machine the
// CLI runs on. It owns the CLI's stdin, stdout and stderr, outlives the ssh
// link and Pocket AI itself (a redeploy, a dropped network), and keeps the
// output of the current turn. The next connection (pocket-hold attach) picks
// up where the last one stopped: from an exact byte offset when the same
// session reconnects, or from the start of the running turn when a new Pocket
// AI process adopts it.
//
// Pocket AI talks to attach exactly as it would to the CLI: stdout is the
// CLI's stdout after one header line, stdin goes to the CLI. A stdin line
// {"pocket_hold":"stop"} ends the CLI; anything else that ends the attach
// client (a dropped link, a signal, stdin closing) only detaches.
package hold

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Frames between attach and serve: a type byte, a big-endian length, data.
const (
	fHello  = 'h' // client → holder: hello JSON
	fIn     = 'i' // client → holder: bytes for the CLI's stdin
	fStop   = 'q' // client → holder: end the CLI
	fAck    = 'a' // client → holder: the exit was delivered
	fHeader = 'H' // holder → client: header JSON
	fOut    = 'o' // holder → client: CLI stdout
	fErr    = 'e' // holder → client: CLI stderr
	fExit   = 'x' // holder → client: exit JSON, after all output

	maxFrame = 64 << 20
)

// StopLine on the attach client's stdin ends the CLI.
const StopLine = `{"pocket_hold":"stop"}`

// FromReplay asks for the output since the start of the running turn (or
// the end of the last one when none runs): what a new reader has not seen.
const FromReplay = -1

// FromTurn asks for the output since the start of the last turn, whether it
// still runs or not: for a reader that never saw that turn end.
const FromTurn = -2

type hello struct {
	From int64 `json:"from"`
	Peek bool  `json:"peek,omitempty"` // header only; the attached client stays
	Stop bool  `json:"stop,omitempty"` // end the CLI, then close
}

// Header is the holder's state, sent on every attach; attach prints it (with
// Type set) as the first stdout line.
type Header struct {
	Type     string `json:"type,omitempty"`
	Started  bool   `json:"started,omitempty"` // attach started this holder and its CLI
	Gone     bool   `json:"gone,omitempty"`    // no holder (attach -only)
	PID      int    `json:"pid,omitempty"`
	Busy     bool   `json:"busy,omitempty"`     // a turn is running
	Offset   int64  `json:"offset"`             // stdout offset the stream starts at
	In       int64  `json:"in"`                 // stdin bytes the CLI got so far
	Live     int64  `json:"live,omitempty"`     // stdout offset where the replay ends and live output begins
	Gap      bool   `json:"gap,omitempty"`      // output before Offset that was asked for is gone
	Exited   bool   `json:"exited,omitempty"`   // the CLI ended; its output and exit follow
	Stopping bool   `json:"stopping,omitempty"` // it was told to end
	Meta     string `json:"meta,omitempty"`     // what the starter stored with it
	Stderr   string `json:"stderr,omitempty"`   // the CLI's last stderr
	// Kept: stdout lines with a keep mark from before Offset (a replay only):
	// what a new reader needs to know of earlier turns, like sub-agents and
	// background shells still running.
	Kept []string `json:"kept,omitempty"`
}

// HeaderType marks the header line on stdout.
const HeaderType = "pocket_hold"

// ExitType marks the line attach prints after the CLI's last output.
const ExitType = "pocket_hold_exit"

type exitInfo struct {
	Type string `json:"type,omitempty"`
	Code int    `json:"code"`
}

var slotRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Dir is where the holder of slot keeps its socket and log.
func Dir(slot string) (string, error) {
	if !slotRE.MatchString(slot) {
		return "", fmt.Errorf("bad slot %q", slot)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pocket-ai", "hold", slot), nil
}

func sockPath(dir string) string { return filepath.Join(dir, "s") }

func writeFrame(w io.Writer, typ byte, p []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(p)))
	if len(p) == 0 {
		_, err := w.Write(hdr[:])
		return err
	}
	_, err := w.Write(append(hdr[:], p...))
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("frame too large")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return hdr[0], p, nil
}

func dial(dir string) (net.Conn, error) {
	return net.DialTimeout("unix", sockPath(dir), 3*time.Second)
}

// Status asks the holder of slot for its state without attaching. ok is
// false when no holder runs there.
func Status(slot string) (h Header, ok bool, err error) {
	dir, err := Dir(slot)
	if err != nil {
		return h, false, err
	}
	c, err := dial(dir)
	if err != nil {
		return h, false, nil
	}
	defer c.Close()
	h, err = handshake(c, hello{Peek: true})
	return h, err == nil, err
}

// Stop ends the CLI of slot's holder, if one runs. It does not wait for it.
func Stop(slot string) error {
	dir, err := Dir(slot)
	if err != nil {
		return err
	}
	c, err := dial(dir)
	if err != nil {
		return nil
	}
	defer c.Close()
	_, err = handshake(c, hello{Stop: true})
	return err
}

func handshake(c net.Conn, h hello) (Header, error) {
	return handshakeR(c, bufio.NewReader(c), h)
}

func handshakeR(c net.Conn, r *bufio.Reader, h hello) (Header, error) {
	var hd Header
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	defer c.SetDeadline(time.Time{})
	b, _ := json.Marshal(h)
	if err := writeFrame(c, fHello, b); err != nil {
		return hd, err
	}
	typ, p, err := readFrame(r)
	if err != nil {
		return hd, err
	}
	if typ != fHeader {
		return hd, fmt.Errorf("unexpected frame %q", typ)
	}
	return hd, json.Unmarshal(p, &hd)
}

// lines calls fn with each complete line written through it and the
// offset just past its newline.
type lines struct {
	part []byte
	off  int64
	fn   func(line []byte, end int64)
}

func (l *lines) feed(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(l.part) < 128<<20 {
				l.part = append(l.part, p...)
			}
			l.off += int64(len(p))
			return
		}
		l.off += int64(i + 1)
		if len(l.part) > 0 {
			l.fn(append(l.part, p[:i]...), l.off)
			l.part = l.part[:0]
		} else {
			l.fn(p[:i], l.off)
		}
		p = p[i+1:]
	}
}
