package relay

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Proto is the link protocol this build speaks (the number in the magic lines).
const Proto = 1

// The magic lines before yamux starts (docs/machine-link-impl.md).
const (
	linkLine     = "POCKET-LINK 1"
	relayPrefix  = "POCKET-RELAY"
	helloPrefix  = "POCKET-RELAY 1 "
	refusePrefix = "POCKET-RELAY-REFUSED "
	errorPrefix  = "POCKET-RELAY-ERROR "
)

// Liveness defaults: each side pings this often and declares the link dead
// when none of its own pings was answered for DefaultDead.
const (
	DefaultPing = 5 * time.Second
	DefaultDead = 30 * time.Second
)

// Version names this build in the relay's hello (set with -ldflags, else
// the VCS revision the binary was built from). It is for people to read: the
// server decides whether a relay is the one it uploaded by Sum, not by this.
var Version = ""

// selfSum is the content hash of the running binary, named the way
// hold.Binary names the uploaded file (the first 6 bytes of its sha256, in
// hex): the server compares it with the file it uploaded.
var selfSum = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
})

func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
	}
	return "dev"
}

// Hello is what the relay says once it runs (its POCKET-RELAY line).
type Hello struct {
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Generation int64  `json:"generation"`
	Test       bool   `json:"test"`
	// Sum is the content hash of the relay's binary (selfSum): the same
	// build as the server's is the same file, whatever Version says.
	Sum string `json:"sum,omitempty"`
}

// HandshakeError: the relay's first line was not a hello this build can
// use (stray output, another protocol, an error from the relay about its
// start). It will not fix itself by trying again.
type HandshakeError struct{ Msg string }

func (e *HandshakeError) Error() string { return e.Msg }

// Refused is a relay start or fenced action the fence turned down.
type Refused struct {
	Reason     string `json:"reason"` // newer | other-entry | off | busy
	Entry      int64  `json:"entry"`
	Generation int64  `json:"generation"`
	Msg        string `json:"error,omitempty"`
}

func (r *Refused) Error() string {
	if r.Msg != "" {
		return "relay refused (" + r.Reason + "): " + r.Msg
	}
	return "relay refused (" + r.Reason + ")"
}

// muxConfig is the yamux setup of both ends: small per-stream windows so one
// stream cannot fill the ssh channel, our own pings instead of yamux's.
func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.AcceptBacklog = 256
	c.EnableKeepAlive = false
	c.MaxStreamWindowSize = 256 << 10
	c.StreamOpenTimeout = 30 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.LogOutput = io.Discard
	return c
}

// Stream kinds.
const (
	kindControl = "control"
	kindHold    = "hold"
	kindExec    = "exec"
	kindTools   = "tools"
	kindPty     = "pty"
)

// openHeader starts every stream; the acceptor answers with an openAnswer.
type openHeader struct {
	Kind   string   `json:"kind"`
	Slot   string   `json:"slot,omitempty"`
	Script string   `json:"script,omitempty"`
	Args   []string `json:"args,omitempty"`
	Stdin  bool     `json:"stdin,omitempty"`
	Port   int      `json:"port,omitempty"`
	Cols   int      `json:"cols,omitempty"`
	Rows   int      `json:"rows,omitempty"`
	Dir    string   `json:"dir,omitempty"`
}

type openAnswer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

const maxHeader = 4 << 20

// writeJSONFrame writes a 4-byte big-endian length and the JSON of v, in one
// write: nothing of another writer can come between.
func writeJSONFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf)
	return err
}

// readJSONFrame reads exactly one frame from r (unbuffered: the raw bytes
// after it belong to whoever reads next).
func readJSONFrame(r io.Reader, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxHeader {
		return errors.New("stream header too large")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// exec frames: a type byte, a 4-byte big-endian length, data.
const (
	xIn     = 'i' // server → relay: stdin bytes
	xEOF    = 'E' // server → relay: stdin ends
	xCancel = 'c' // server → relay: end the command's process group
	xStart  = 'S' // relay → server: {"pid":N}
	xOut    = 'o' // relay → server: stdout
	xErr    = 'e' // relay → server: stderr
	xExit   = 'x' // relay → server: {"code":N,"signal":"…"}, the last frame

	maxExecFrame = 1 << 20
	execChunk    = 32 << 10
)

type execStarted struct {
	PID int `json:"pid"`
}

type execExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"` // the command could not start
}

func writeFrame(w io.Writer, typ byte, p []byte) error {
	buf := make([]byte, 5+len(p))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(len(p)))
	copy(buf[5:], p)
	_, err := w.Write(buf)
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxExecFrame {
		return 0, nil, errors.New("exec frame too large")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return hdr[0], p, nil
}

// readLine reads one line of at most max bytes, without the newline.
func readLine(r *bufio.Reader, max int) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			if len(b) > 0 {
				return string(b), io.ErrUnexpectedEOF
			}
			return "", err
		}
		if c == '\n' {
			return strings.TrimSuffix(string(b), "\r"), nil
		}
		if len(b) >= max {
			return string(b), errors.New("line too long")
		}
		b = append(b, c)
	}
}

// parseRelayLine reads the relay's first line: its hello, a refusal or an
// error. Anything else is stray output (a login script printing, a
// mismatched binary) and fails the start.
func parseRelayLine(line string) (Hello, error) {
	var h Hello
	switch {
	case strings.HasPrefix(line, helloPrefix):
		if err := json.Unmarshal([]byte(line[len(helloPrefix):]), &h); err != nil {
			return h, &HandshakeError{fmt.Sprintf("bad hello from the relay: %v", err)}
		}
		return h, nil
	case strings.HasPrefix(line, refusePrefix):
		r := &Refused{}
		if err := json.Unmarshal([]byte(line[len(refusePrefix):]), r); err != nil {
			return h, &HandshakeError{fmt.Sprintf("bad refusal from the relay: %q", line)}
		}
		return h, r
	case strings.HasPrefix(line, errorPrefix):
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(line[len(errorPrefix):]), &e)
		return h, &HandshakeError{"relay: " + e.Error}
	case strings.HasPrefix(line, relayPrefix+" "):
		v, _, _ := strings.Cut(line[len(relayPrefix)+1:], " ")
		return h, &HandshakeError{fmt.Sprintf("the relay speaks link protocol %s, this Pocket AI knows %d", v, Proto)}
	}
	return h, &HandshakeError{fmt.Sprintf("unexpected output before the link started: %q", clip(line, 200))}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// rwc joins a reader and a writer into the conn yamux runs on.
type rwc struct {
	io.Reader
	io.Writer
	close func() error
}

func (c rwc) Close() error { return c.close() }
