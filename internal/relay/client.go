package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/hashicorp/yamux"
)

// DialOptions are what the server starts a link with.
type DialOptions struct {
	Installation string
	Entry        int64
	Generation   int64
	Token        string
	Test         bool
	Tools        func(port int) (net.Conn, error) // dial the local target registered for port
	OnDead       func(err error)
	// PingEvery and DeadAfter: 0 is DefaultPing / DefaultDead.
	PingEvery, DeadAfter time.Duration
}

// Exec errors: ErrNotSent means the request provably never reached the relay
// (safe to run elsewhere); ErrUnknown that it went out and no exit came back.
var (
	ErrNotSent = errors.New("the command was not sent over the link")
	ErrUnknown = errors.New("the link went before the command's exit came back: its result is unknown")
)

// Client is the server's end of one link.
type Client struct {
	o     DialOptions
	hello Hello
	sess  *yamux.Session
	ctl   *control
	cmd   *exec.Cmd
	stdin io.Closer
	tail  *tailBuffer

	exited chan struct{} // the ssh process ended (nil without one)

	done     chan struct{}
	doneOnce sync.Once
	errMu    sync.Mutex
	err      error
}

// ErrClosed: the link was closed by Close.
var ErrClosed = errors.New("link closed")

// Start runs cmd (the ssh process, not started yet; its Stdin and Stdout
// must be unset), does the handshake and returns a live client. Refusals
// come back as *Refused.
func Start(ctx context.Context, cmd *exec.Cmd, o DialOptions) (*Client, error) {
	if cmd.Stdin != nil || cmd.Stdout != nil {
		return nil, errors.New("relay.Start: the command's stdin and stdout are the link's")
	}
	// our own pipes (not StdoutPipe): Wait may then run while we read
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	var tail *tailBuffer
	if cmd.Stderr == nil {
		tail = &tailBuffer{max: 4 << 10}
		cmd.Stderr = tail
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	kill := func() {
		_ = cmd.Process.Kill()
		inW.Close()
		outR.Close()
	}
	conn := rwc{Reader: outR, Writer: inW, close: func() error { inW.Close(); return outR.Close() }}
	withProc := func(c *Client) { c.cmd, c.stdin, c.tail, c.exited = cmd, inW, tail, exited }
	c, err := handshake(ctx, conn, o, withProc, func(err error) error {
		kill()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
		}
		if tail != nil {
			if s := strings.TrimSpace(tail.String()); s != "" {
				var rf *Refused
				if !errors.As(err, &rf) {
					err = fmt.Errorf("%w (stderr: %s)", err, clip(s, 500))
				}
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	go func() {
		<-exited
		c.die(errors.New("the ssh process ended" + c.stderrNote()))
	}()
	return c, nil
}

// NewClient runs the server side over an already connected byte stream (a
// test pipe, or a future transport): the handshake, then the link.
func NewClient(ctx context.Context, conn io.ReadWriteCloser, o DialOptions) (*Client, error) {
	return handshake(ctx, conn, o, nil, func(err error) error { conn.Close(); return err })
}

// handshake: setup fills in the client before anything can end it (the ssh
// process to kill).
func handshake(ctx context.Context, conn io.ReadWriteCloser, o DialOptions, setup func(*Client), fail func(error) error) (*Client, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		// a takeover on the machine may take 15 s before the hello
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	type res struct {
		h   Hello
		err error
	}
	got := make(chan res, 1)
	go func() {
		if _, err := io.WriteString(conn, linkLine+"\n"); err != nil {
			got <- res{err: fmt.Errorf("could not start the link: %w", err)}
			return
		}
		line, err := readLine(br, 64<<10)
		if err != nil {
			if line != "" {
				got <- res{err: &HandshakeError{fmt.Sprintf("unexpected output before the link started: %q", clip(line, 200))}}
				return
			}
			got <- res{err: fmt.Errorf("the relay ended before the link started: %w", err)}
			return
		}
		h, err := parseRelayLine(line)
		got <- res{h, err}
	}()
	var r res
	select {
	case r = <-got:
	case <-ctx.Done():
		return nil, fail(fmt.Errorf("link start: %w", ctx.Err()))
	}
	if r.err != nil {
		return nil, fail(r.err)
	}
	if r.h.Test != o.Test || (!o.Test && r.h.Generation != o.Generation) {
		return nil, fail(&HandshakeError{fmt.Sprintf("the relay answered for generation %d (test %v), not %d (test %v)", r.h.Generation, r.h.Test, o.Generation, o.Test)})
	}
	sess, err := yamux.Client(rwc{Reader: br, Writer: conn, close: conn.Close}, muxConfig())
	if err != nil {
		return nil, fail(err)
	}
	c := &Client{o: o, hello: r.h, sess: sess, done: make(chan struct{})}
	if setup != nil {
		setup(c)
	}
	s, err := c.open(ctx, openHeader{Kind: kindControl})
	if err != nil {
		sess.Close()
		return nil, fail(fmt.Errorf("control stream: %w", err))
	}
	c.ctl = newControl(s, o.PingEvery, o.DeadAfter, nil)
	go func() {
		c.ctl.run()
		c.die(c.ctl.err)
	}()
	go func() {
		<-sess.CloseChan()
		c.die(errors.New("link closed"))
	}()
	go c.accept()
	return c, nil
}

// die ends the link once: the session, the ssh process, then OnDead.
func (c *Client) die(err error) {
	c.doneOnce.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
		if c.ctl != nil {
			c.ctl.die(err)
		}
		if c.cmd != nil && c.cmd.Process != nil {
			if err == ErrClosed {
				// a clean end: stdin closes, the relay clears its fence record
				_ = c.stdin.Close()
				select {
				case <-c.exited:
				case <-time.After(3 * time.Second):
				}
			}
			_ = c.cmd.Process.Kill()
		}
		// Close waits for yamux's reader and writer: never for long
		closed := make(chan struct{})
		go func() { _ = c.sess.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
		if c.o.OnDead != nil && err != ErrClosed {
			go c.o.OnDead(err)
		}
	})
}

func (c *Client) stderrNote() string {
	if c.tail == nil {
		return ""
	}
	if s := strings.TrimSpace(c.tail.String()); s != "" {
		return " (stderr: " + clip(s, 500) + ")"
	}
	return ""
}

// Hello is the relay's POCKET-RELAY line.
func (c *Client) Hello() Hello { return c.hello }

// Done is closed when the link is dead or closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err says why the link ended (nil while it lives).
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Close ends the link; the relay exits.
func (c *Client) Close() error {
	c.die(ErrClosed)
	return nil
}

// RTT is the round trip of the last answered ping (0 before the first).
func (c *Client) RTT() time.Duration { return c.ctl.RTT() }

// open opens a stream with header h and waits for the relay's answer.
func (c *Client) open(ctx context.Context, h openHeader) (net.Conn, error) {
	s, err := c.sess.OpenStream()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = s.SetDeadline(time.Now()) })
	defer stop()
	_ = s.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err := writeJSONFrame(s, h); err != nil {
		s.Close()
		return nil, ctxErr(ctx, err)
	}
	var ans openAnswer
	if err := readJSONFrame(s, &ans); err != nil {
		s.Close()
		return nil, ctxErr(ctx, err)
	}
	if !stop() {
		s.Close()
		return nil, ctx.Err()
	}
	_ = s.SetDeadline(time.Time{})
	if !ans.OK {
		s.Close()
		return nil, &openRefused{ans.Error}
	}
	return s, nil
}

type openRefused struct{ msg string }

func (e *openRefused) Error() string { return "relay: " + e.msg }

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// OpenHold opens a stream to slot's holder socket on the machine. The server
// speaks the holder protocol over it (hold.Open).
// The error wraps hold.ErrNoHolder when no holder listens for slot there.
func (c *Client) OpenHold(ctx context.Context, slot string) (net.Conn, error) {
	s, err := c.open(ctx, openHeader{Kind: kindHold, Slot: slot})
	var rf *openRefused
	if errors.As(err, &rf) && strings.HasPrefix(rf.msg, noHolderMsg) {
		return nil, fmt.Errorf("%w: %s", hold.ErrNoHolder, rf.msg)
	}
	return s, err
}

// ExecRequest is one command for the relay to run.
type ExecRequest struct {
	Script string
	Args   []string
	Stdin  io.Reader // nil: /dev/null; else copied in i frames, then E
	Stdout io.Writer
	Stderr io.Writer
}

// ExecResult: Started tells whether the relay said it started the command.
type ExecResult struct {
	Code    int
	Signal  string
	Started bool
}

// Exec runs req on the machine. ctx ending cancels the command there (its
// process group is ended) and returns ctx's error.
func (c *Client) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	var res ExecResult
	select {
	case <-c.done:
		return res, ErrNotSent
	default:
	}
	s, err := c.sess.OpenStream()
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrNotSent, err)
	}
	defer s.Close()
	if err := writeJSONFrame(s, openHeader{Kind: kindExec, Script: req.Script, Args: req.Args, Stdin: req.Stdin != nil}); err != nil {
		// part of it may have gone out
		return res, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	var wmu sync.Mutex
	send := func(typ byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(s, typ, p)
	}
	type frame struct {
		typ byte
		p   []byte
	}
	frames := make(chan frame, 16)
	readErr := make(chan error, 1)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		var ans openAnswer
		if err := readJSONFrame(s, &ans); err != nil {
			readErr <- fmt.Errorf("%w: %v", ErrUnknown, err)
			return
		}
		if !ans.OK {
			// the relay looked at it and ran nothing
			readErr <- fmt.Errorf("%w: relay: %s", ErrNotSent, ans.Error)
			return
		}
		br := bufio.NewReaderSize(s, 64<<10)
		for {
			typ, p, err := readFrame(br)
			if err != nil {
				readErr <- fmt.Errorf("%w: %v", ErrUnknown, err)
				return
			}
			select {
			case frames <- frame{typ, p}:
			case <-quit:
				return
			}
			if typ == xExit {
				return
			}
		}
	}()
	if req.Stdin != nil {
		go func() {
			buf := make([]byte, execChunk)
			for {
				n, err := req.Stdin.Read(buf)
				if n > 0 && send(xIn, buf[:n]) != nil {
					return
				}
				if err != nil {
					_ = send(xEOF, nil)
					return
				}
			}
		}()
	}
	cancelled := false
	var grace <-chan time.Time
	ctxDone := ctx.Done()
	for {
		select {
		case f := <-frames:
			switch f.typ {
			case xStart:
				res.Started = true
			case xOut:
				if req.Stdout != nil {
					_, _ = req.Stdout.Write(f.p)
				}
			case xErr:
				if req.Stderr != nil {
					_, _ = req.Stderr.Write(f.p)
				}
			case xExit:
				var x execExit
				_ = json.Unmarshal(f.p, &x)
				res.Code, res.Signal = x.Code, x.Signal
				if cancelled {
					return res, ctx.Err()
				}
				if x.Error != "" && !res.Started {
					return res, fmt.Errorf("%w: relay could not start it: %s", ErrNotSent, x.Error)
				}
				return res, nil
			}
		case err := <-readErr:
			if cancelled {
				return res, ctx.Err()
			}
			return res, err
		case <-c.done:
			if cancelled {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("%w: %v", ErrUnknown, c.Err())
		case <-ctxDone:
			ctxDone = nil
			cancelled = true
			// the cancel must not wait behind stdin the command does not read
			go func() {
				_ = s.SetWriteDeadline(time.Now().Add(2 * time.Second))
				_ = send(xCancel, nil)
			}()
			grace = time.After(killAfter + 2*time.Second)
		case <-grace:
			return res, ctx.Err()
		}
	}
}

// Bind asks the relay to listen on 127.0.0.1:port; it returns once it does.
func (c *Client) Bind(ctx context.Context, port int) error {
	_, err := c.ctl.call(ctx, msg{Op: "bind", Port: port}, 30*time.Second)
	return err
}

// Unbind closes the listener on port and its connections; it returns once closed.
func (c *Client) Unbind(ctx context.Context, port int) error {
	_, err := c.ctl.call(ctx, msg{Op: "unbind", Port: port}, 30*time.Second)
	return err
}

// Sync makes the relay listen on exactly ports.
func (c *Client) Sync(ctx context.Context, ports []int) (bound []int, failed map[int]string, err error) {
	r, err := c.ctl.call(ctx, msg{Op: "sync", Ports: ports}, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	return r.Bound, r.Failed, nil
}

// Status is the relay's own view.
type Status struct {
	PID     int
	Uptime  time.Duration
	Streams map[string]int
	Ports   []int
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	r, err := c.ctl.call(ctx, msg{Op: "status"}, 30*time.Second)
	if err != nil {
		return Status{}, err
	}
	ps := slices.Clone(r.Ports)
	return Status{PID: r.PID, Uptime: time.Duration(r.Uptime * float64(time.Second)), Streams: r.Streams, Ports: ps}, nil
}

// accept serves the streams the relay opens: tools connections.
func (c *Client) accept() {
	for {
		s, err := c.sess.AcceptStream()
		if err != nil {
			return
		}
		go c.serveTools(s)
	}
}

func (c *Client) serveTools(s net.Conn) {
	defer func() {
		if p := recover(); p != nil {
			_ = s.Close() // one bad tools stream ends only itself
		}
	}()
	defer s.Close()
	var h openHeader
	_ = s.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err := readJSONFrame(s, &h); err != nil {
		return
	}
	_ = s.SetReadDeadline(time.Time{})
	if h.Kind != kindTools {
		_ = writeJSONFrame(s, openAnswer{Error: "the server accepts only tools streams"})
		return
	}
	if c.o.Tools == nil {
		_ = writeJSONFrame(s, openAnswer{Error: "no tools here"})
		return
	}
	t, err := c.o.Tools(h.Port)
	if err != nil {
		_ = writeJSONFrame(s, openAnswer{Error: err.Error()})
		return
	}
	if err := writeJSONFrame(s, openAnswer{OK: true}); err != nil {
		t.Close()
		return
	}
	splice(s, t)
}

// tailBuffer keeps the last max bytes written to it (the ssh's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append([]byte(nil), t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
