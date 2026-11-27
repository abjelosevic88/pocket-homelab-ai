package hold

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// Proto is the holder protocol this build speaks. Holders send it in their
// header; one without it (0) is from before the machine link and speaks the
// same frames. A client refuses a holder newer than itself instead of
// guessing what changed.
const Proto = 1

// Frame types a Conn reads (Read): the CLI's stdout, its stderr, its exit.
const (
	FrameOut  = fOut
	FrameErr  = fErr
	FrameExit = fExit
)

// ErrNoHolder: nothing listens for the slot (no holder runs there).
var ErrNoHolder = errors.New("no holder")

// ProtoError is a holder that speaks a newer protocol than this build.
type ProtoError struct{ Got int }

func (e *ProtoError) Error() string {
	return fmt.Sprintf("holder speaks protocol %d, this Pocket AI knows %d: update Pocket AI", e.Got, Proto)
}

func checkProto(hd Header) error {
	if hd.Proto > Proto {
		return &ProtoError{Got: hd.Proto}
	}
	return nil
}

// DialSlot connects to the holder of slot on this machine (its Unix socket).
// The error wraps ErrNoHolder when none runs there.
func DialSlot(ctx context.Context, slot string) (net.Conn, error) {
	dir, err := Dir(slot)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "unix", sockPath(dir))
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w: %v", ErrNoHolder, err)
		}
		return nil, err
	}
	return c, nil
}

// Conn is an attached connection to a holder: the client half of
// pocket-hold attach, over any net.Conn (a Unix socket here, a relay stream
// there). Attaching takes the holder from whoever had it.
type Conn struct {
	c   net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

// Open does the hello/header exchange on c (already connected to a holder)
// and attaches from stdout offset from (or FromReplay, FromTurn). On an
// error c is closed.
func Open(c net.Conn, from int64) (*Conn, Header, error) {
	r := bufio.NewReader(c)
	hd, err := handshakeR(c, r, hello{From: from})
	if err != nil {
		c.Close()
		return nil, hd, err
	}
	return &Conn{c: c, r: r}, hd, nil
}

// Read returns the next frame from the holder: FrameOut, FrameErr or
// FrameExit (the last; its data is {"code":N}).
func (c *Conn) Read() (typ byte, p []byte, err error) { return readFrame(c.r) }

func (c *Conn) send(typ byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.c, typ, p)
}

// WriteIn sends p to the CLI's stdin.
func (c *Conn) WriteIn(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), 16<<20)
		if err := c.send(fIn, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Stop ends the CLI.
func (c *Conn) Stop() error { return c.send(fStop, nil) }

// Ack tells the holder the exit was delivered: it can go.
func (c *Conn) Ack() error { return c.send(fAck, nil) }

// Close lets go of the holder (the CLI runs on).
func (c *Conn) Close() error { return c.c.Close() }

// Peek asks the holder on c for its state without attaching, and closes c.
func Peek(c net.Conn) (Header, error) {
	defer c.Close()
	return handshake(c, hello{Peek: true})
}

// StopVia ends the CLI of the holder on c (without waiting for it), and
// closes c.
func StopVia(c net.Conn) (Header, error) {
	defer c.Close()
	return handshake(c, hello{Stop: true})
}

// AttachStream makes c look exactly like pocket-hold attach's stdout and
// stdin, in-process: stdout is the header line (hd, Type set), the CLI's
// stdout, then the exit line; stdin takes lines for the CLI and StopLine.
// The exit is acknowledged only once its line was read from stdout. wait
// gives attach's exit code: the CLI's, 254 when the holder was lost, 141
// when stdout was closed before the exit was delivered. Closing stdout
// lets go of the holder.
func AttachStream(c *Conn, hd Header, stderr io.Writer) (stdout io.ReadCloser, stdin io.WriteCloser, wait func() int) {
	if stderr == nil {
		stderr = io.Discard
	}
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	s := &streamOut{PipeReader: outR, c: c}
	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		var lost error
		code, lost = stream(c, hd, outW, inR, stderr)
		if lost != nil && !s.isClosed() {
			fmt.Fprintln(stderr, "pocket-hold: lost the holder:", lost)
		}
		if lost != nil && s.isClosed() {
			code = 129 // let go on purpose: what a signal to attach gives
		}
		c.Close()
		inR.CloseWithError(io.ErrClosedPipe) // writing to it fails, as to an ended process
		outW.Close()
	}()
	return s, inW, func() int { <-done; return code }
}

type streamOut struct {
	*io.PipeReader
	c      *Conn
	mu     sync.Mutex
	closed bool
}

func (s *streamOut) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.c.Close()
	return s.PipeReader.Close()
}

func (s *streamOut) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// stream is attach after the header was read: it prints the header line,
// relays stdin lines to the holder and the holder's frames to stdout and
// stderr, and returns attach's exit code. lost is why the holder went
// (code 254).
func stream(c *Conn, hd Header, stdout io.Writer, stdin io.Reader, stderr io.Writer) (code int, lost error) {
	hd.Type = HeaderType
	b, _ := json.Marshal(hd)
	if _, err := stdout.Write(append(b, '\n')); err != nil {
		return 141, nil
	}
	if hd.Stderr != "" && !hd.Started {
		_, _ = io.WriteString(stderr, hd.Stderr)
	}
	go func() {
		in := bufio.NewReaderSize(stdin, 1<<20)
		for {
			line, err := in.ReadBytes('\n')
			if err == nil { // a line cut off by stdin closing is not sent: its writer sends it again
				if bytes.Equal(bytes.TrimSpace(line), []byte(StopLine)) {
					if c.Stop() != nil {
						return
					}
				} else if c.WriteIn(line) != nil {
					return
				}
			}
			if err != nil {
				return // stdin closed: detach, keep relaying until we are ended
			}
		}
	}()
	for {
		typ, p, err := c.Read()
		if err != nil {
			return 254, err
		}
		switch typ {
		case fOut:
			if _, err := stdout.Write(p); err != nil {
				return 141, nil
			}
		case fErr:
			_, _ = stderr.Write(p)
		case fExit:
			var x exitInfo
			_ = json.Unmarshal(p, &x)
			x.Type = ExitType
			b, _ := json.Marshal(x)
			if _, err := stdout.Write(append(b, '\n')); err != nil {
				return 141, nil // not delivered: the holder keeps it for the next one
			}
			_ = c.Ack()
			if x.Code < 0 || x.Code > 255 {
				return 255, nil
			}
			return x.Code, nil
		}
	}
}
