package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

// A link's connections to its CLI come from a transport; the resume logic
// (offsets, input sent again, parking) in link.go is the same for all.
//
//   - procTransport: a process, as always: the CLI itself (plain), or
//     pocket-hold attach in front of it (held), here or over ssh.
//   - holdTransport: the holder spoken to in-process over a net.Conn
//     (hold.Open), with a start-only step before a start. Over its Unix socket
//     here; a relay stream to another machine is the same with another dial.

// conn is one connection to a CLI.
type conn interface {
	Stdin() io.WriteCloser
	// Stdout is the CLI's stdout; held, after pocket-hold's header line.
	Stdout() *bufio.Reader
	// Wait collects the connection once its stdout has ended.
	Wait() error
	// End ends the connection: gently (attach lets go of the holder) or,
	// force, at once. A held CLI runs on either way.
	End(force bool)
}

// transport makes a link's connections.
type transport interface {
	// open connects: held, attaches from stdout offset from (or starts the
	// CLI, unless only). hd.Gone with a nil conn: only, and no holder.
	// stderr is what the attempt printed, for why it failed.
	open(from int64, only bool) (c conn, hd hold.Header, stderr string, err error)
}

// errSlotBusy: another start holds the agent's slot and is not ready yet;
// nothing was started. What runs there is asked before the next start.
var errSlotBusy = errors.New("another start of this agent is under way and not ready yet (slot busy)")

// HoldStart is a holder start for Hold.Dial: pocket-hold attach -start-only
// for the slot, with everything a process start has (profile, PATH,
// folder, environment, scope, meta).
type HoldStart struct {
	// Cmd runs it here, or over ssh on Options.Remote: the same command the
	// process transport would run, with -start-only. Not started yet.
	Cmd *exec.Cmd
	// Script is the sh script Cmd runs on Options.Remote (what to give a
	// link's exec instead of ssh); empty here.
	Script string
}

// procConn is a connection that is a process.
type procConn struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	r     *bufio.Reader
}

func (c *procConn) Stdin() io.WriteCloser { return c.stdin }
func (c *procConn) Stdout() *bufio.Reader { return c.r }
func (c *procConn) Wait() error           { return c.cmd.Wait() }

// End signals the process group: SIGTERM (attach detaches), SIGKILL.
func (c *procConn) End(force bool) {
	if c.cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, sig)
}

type procTransport struct {
	l *link
	// remote, when set, is the agent's Remote for this connection only
	// (without its forwards, or behind a guard: Hold.Route).
	remote *Remote
}

// holdArgs is pocket-hold attach's command line in front of the CLI.
func (l *link) holdArgs(from int64, only, startOnly bool) []string {
	wrap := []string{l.h.Bin, "attach", "-from", strconv.FormatInt(from, 10), "-start-mark", l.m.start, "-end-mark", l.m.end}
	if only {
		wrap = append(wrap, "-only")
	}
	if startOnly {
		wrap = append(wrap, "-start-only")
	}
	for _, k := range l.m.keep {
		wrap = append(wrap, "-keep-mark", k)
	}
	if l.h.MetaFile != "" {
		wrap = append(wrap, "-meta-file", l.h.MetaFile)
	} else if l.h.Meta != "" {
		wrap = append(wrap, "-meta", l.h.Meta)
	}
	if len(l.h.Scope) > 0 {
		wrap = append(wrap, "-scope", strings.Join(l.h.Scope, " "))
	}
	return append(wrap, l.h.Slot, "--")
}

func (l *link) stderrTo(own *tail) io.Writer {
	if l.errb != nil {
		return io.MultiWriter(l.errb, own)
	}
	return own
}

func (t procTransport) open(from int64, only bool) (conn, hold.Header, string, error) {
	l := t.l
	var hd hold.Header
	own := &tail{n: 4096} // this connection's stderr, for why it failed
	var wrap []string
	if l.h != nil {
		wrap = l.holdArgs(from, only, false)
	}
	o := l.o
	if t.remote != nil {
		o.Remote = t.remote
	}
	cmd, err := cliCommand(o, wrap, l.bin, l.args)
	if err != nil {
		return nil, hd, own.String(), err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = l.stderrTo(own)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, hd, own.String(), err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, hd, own.String(), err
	}
	if err := cmd.Start(); err != nil {
		return nil, hd, own.String(), fmt.Errorf("start %s: %w", l.bin, err)
	}
	if hook := testForwardHook.Load(); hook != nil && o.Remote != nil && len(o.Remote.Forwards) > 0 {
		(*hook)(cmd, o.Remote.Forwards)
	}
	c := &procConn{cmd: cmd, stdin: stdin, r: bufio.NewReaderSize(stdout, 1<<20)}
	if l.h == nil {
		return c, hd, own.String(), nil
	}
	type res struct {
		line []byte
		err  error
	}
	got := make(chan res, 1)
	go func() {
		line, err := c.r.ReadBytes('\n')
		got <- res{line, err}
	}()
	var x res
	select {
	case x = <-got:
	case <-time.After(90 * time.Second):
		x.err = errors.New("no answer from pocket-hold")
	}
	if x.err == nil && json.Unmarshal(x.line, &hd) == nil && hd.Type == hold.HeaderType {
		if !hd.SlotBusy {
			if hd.Gone {
				_ = cmd.Wait()
				return nil, hd, own.String(), nil
			}
			return c, hd, own.String(), nil
		}
		x.err = errSlotBusy
	}
	c.End(true)
	werr := cmd.Wait()
	if x.err == nil {
		x.err = fmt.Errorf("unexpected output from pocket-hold: %s", oneLine(string(x.line), 200))
	} else if werr != nil && !errors.Is(x.err, errSlotBusy) {
		x.err = werr
		if msg := own.String(); msg != "" {
			x.err = fmt.Errorf("%s (%w)", oneLine(msg, 200), werr) // not a bare "exit status 255"
		}
	}
	return nil, hd, own.String(), x.err
}

// holdConn is a connection to a holder spoken to in-process: it reads and
// writes exactly what pocket-hold attach would (hold.AttachStream).
type holdConn struct {
	out  io.ReadCloser
	in   io.WriteCloser
	r    *bufio.Reader
	wait func() int
}

func (c *holdConn) Stdin() io.WriteCloser { return c.in }
func (c *holdConn) Stdout() *bufio.Reader { return c.r }

// Wait gives attach's exit code as a process's Wait would.
func (c *holdConn) Wait() error {
	if code := c.wait(); code != 0 {
		return &attachExit{code: code}
	}
	return nil
}

// End lets go of the holder: there is no process to ask first.
func (c *holdConn) End(bool) { _ = c.out.Close() }

// attachExit is an in-process attach's exit code, as a process would have
// exited with it.
type attachExit struct{ code int }

func (e *attachExit) Error() string { return "exit status " + strconv.Itoa(e.code) }

// holdTransport speaks to the holder in-process over what dial returns; a
// start first runs start (pocket-hold attach -start-only). A reattach waits
// for catchUp first, when set (Hold.CatchUp).
type holdTransport struct {
	l       *link
	dial    func(ctx context.Context, slot string) (net.Conn, error)
	start   func(ctx context.Context, s HoldStart) (hold.Header, error)
	catchUp func(ctx context.Context) (release func(), err error)
}

// testForwardHook, set by tests, sees every per-agent connection that
// carries forwards (to check that no port is ever had both ways).
var testForwardHook atomic.Pointer[func(cmd *exec.Cmd, forwards []string)]

// SetTestForwardHook makes fn see every per-agent connection that carries
// forwards, right after its ssh started (tests of other packages); nil ends it.
func SetTestForwardHook(fn func(cmd *exec.Cmd, forwards []string)) {
	if fn == nil {
		testForwardHook.Store(nil)
		return
	}
	testForwardHook.Store(&fn)
}

func (t holdTransport) open(from int64, only bool) (conn, hold.Header, string, error) {
	l := t.l
	var st hold.Header
	if !only {
		var stderr string
		var err error
		st, stderr, err = t.startOnly()
		if err != nil {
			return nil, st, stderr, err
		}
		if st.SlotBusy {
			return nil, st, stderr, errSlotBusy
		}
	}
	release := func() {}
	if only && t.catchUp != nil {
		r, err := t.catchUp(l.ctx)
		if err != nil {
			return nil, hold.Header{}, "", err
		}
		release = r
	}
	ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
	nc, err := t.dial(ctx, l.h.Slot)
	cancel()
	if err != nil {
		release()
		if errors.Is(err, hold.ErrNoHolder) {
			if only {
				return nil, hold.Header{Gone: true}, "", nil
			}
			err = fmt.Errorf("the agent's holder went before it could be reached: %w", err)
		}
		return nil, hold.Header{}, "", err
	}
	hc, hd, err := hold.Open(nc, from)
	if err != nil {
		release()
		return nil, hd, "", fmt.Errorf("pocket-hold: %w", err)
	}
	hd.Started = st.Started
	own := &tail{n: 4096}
	out, in, wait := hold.AttachStream(hc, hd, l.stderrTo(own))
	cr := &catchUpReader{ReadCloser: out, n: -1, release: sync.OnceFunc(release)}
	c := &holdConn{out: cr, in: in, r: bufio.NewReaderSize(cr, 1<<20), wait: wait}
	line, err := c.r.ReadBytes('\n') // the header line: hd already
	if err != nil {
		c.End(true)
		_ = c.Wait()
		return nil, hd, own.String(), fmt.Errorf("pocket-hold: %w", err)
	}
	// caught up once what the holder had when it answered has come
	cr.until(int64(len(line)) + max(hd.Live-hd.Offset, 0))
	return c, hd, own.String(), nil
}

// startOnly starts the holder (when none runs) with pocket-hold attach
// -start-only, made like the process transport's command.
func (t holdTransport) startOnly() (hold.Header, string, error) {
	l := t.l
	var hd hold.Header
	own := &tail{n: 4096}
	wrap := l.holdArgs(hold.FromReplay, false, true)
	cmd, err := cliCommand(l.o, wrap, l.bin, l.args)
	if err != nil {
		return hd, "", err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = l.stderrTo(own)
	s := HoldStart{Cmd: cmd}
	if l.o.Remote != nil {
		s.Script = remoteScript(l.o.Remote.Slot, l.o.Cwd, l.o.Env, wrap, l.bin, l.args)
	}
	ctx, cancel := context.WithTimeout(l.ctx, 90*time.Second)
	defer cancel()
	if t.start != nil {
		hd, err = t.start(ctx, s)
		return hd, own.String(), err
	}
	hd, err = runStartOnly(ctx, cmd)
	if err != nil {
		if msg := own.String(); msg != "" {
			err = fmt.Errorf("%s (%w)", oneLine(msg, 200), err)
		}
	}
	return hd, own.String(), err
}

// runStartOnly runs a start-only command and reads the header it printed
// (exit 0, or ExitSlotBusy with SlotBusy set).
func runStartOnly(ctx context.Context, cmd *exec.Cmd) (hold.Header, error) {
	var hd hold.Header
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return hd, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the holder has a session of its own
		<-done
		return hd, fmt.Errorf("no answer from pocket-hold: %w", ctx.Err())
	}
	line, _, _ := bytes.Cut(out.Bytes(), []byte("\n"))
	if json.Unmarshal(line, &hd) != nil || hd.Type != hold.HeaderType {
		if err == nil {
			err = fmt.Errorf("unexpected output from pocket-hold: %s", oneLine(string(line), 200))
		}
		return hd, err
	}
	var ee *exec.ExitError
	if err != nil && !(hd.SlotBusy && errors.As(err, &ee) && ee.ExitCode() == hold.ExitSlotBusy) {
		return hd, err
	}
	return hd, nil
}

// catchUpReader counts what a hold stream delivered and calls release once
// it has had n bytes (the replay up to the holder's Live offset), or ends.
type catchUpReader struct {
	io.ReadCloser
	mu      sync.Mutex
	got     int64
	n       int64 // -1 until known
	release func()
}

func (r *catchUpReader) until(n int64) {
	r.mu.Lock()
	r.n = n
	done := r.got >= n
	r.mu.Unlock()
	if done {
		r.release()
	}
}

func (r *catchUpReader) Read(p []byte) (int, error) {
	k, err := r.ReadCloser.Read(p)
	r.mu.Lock()
	r.got += int64(k)
	done := err != nil || (r.n >= 0 && r.got >= r.n)
	r.mu.Unlock()
	if done {
		r.release()
	}
	return k, err
}

func (r *catchUpReader) Close() error {
	r.release()
	return r.ReadCloser.Close()
}
