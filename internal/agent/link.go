package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

// Hold runs a CLI under pocket-hold on its machine (internal/hold): the CLI
// outlives the connection to it, and a later start attaches to it instead
// of starting another.
type Hold struct {
	Bin   string   // pocket-hold there
	Slot  string   // the Pocket thread
	Meta  string   // kept with a holder this start creates, returned on attach
	Adopt bool     // only attach to a running CLI; never start one
	Scope []string // here: start the holder under this (systemd-run …) so it leaves the service
	// Leave closes when Pocket AI is stopping: closing the session then
	// only lets go of the CLI, and a dropped connection is not reattached.
	Leave <-chan struct{}
}

func (h *Hold) leaving() bool {
	if h == nil || h.Leave == nil {
		return false
	}
	select {
	case <-h.Leave:
		return true
	default:
		return false
	}
}

// ErrNotHeld: Hold.Adopt found no running CLI to attach to.
var ErrNotHeld = errors.New("no running agent to attach to")

// errDetached ends a link that let go of a held CLI (Pocket AI stopping):
// the CLI runs on, so nothing about it ended.
var errDetached = errors.New("detached")

// marks are what the holder recognizes turns by, per CLI protocol, and the
// lines it keeps for the CLI's whole life (sub-agents and background shells
// outlive the turn that started them; a new reader needs their story).
type marks struct {
	start, end string
	keep       []string
}

var (
	claudeMarks = marks{`"type":"user"`, `"type":"result"`,
		[]string{`"subtype":"task_started"`, `"subtype":"task_updated"`, `"subtype":"task_notification"`, `"subtype":"background_tasks_changed"`}}
	codexMarks = marks{`"method":"turn/start"`, `"method":"turn/completed"`, nil} // its sub-agents live within a turn
)

// reconnectFor is how long a held link tries to get its CLI back after the
// connection dropped before it reports the loss.
var reconnectFor = 3 * time.Minute

// link is a CLI's stdio. Plain, it is the process itself. Held, it is
// pocket-hold attach, and when the connection drops the link attaches again
// where it stopped: no output read twice or lost, stdin the holder may not
// have got sent again. The session using it notices nothing.
type link struct {
	ctx  context.Context
	o    Options
	h    *Hold // nil: plain
	m    marks
	bin  string
	args []string
	errb *tail
	log  *slog.Logger

	Header hold.Header // what the first attach found

	wmu    sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	r      *bufio.Reader
	down   chan struct{} // non-nil while reconnecting; closed when back
	ended  bool
	inLog  []byte // stdin written, from inBase on (held)
	inBase int64

	off      int64 // stdout read so far (held)
	exit     *int  // the holder reported the CLI's exit
	stopping bool
	quit     chan struct{} // closed by Detach or Close: stop reconnecting
	quitOnce sync.Once
	detached bool
}

const inLogMax = 32 << 20

func openLink(ctx context.Context, o Options, m marks, bin string, args []string, errb *tail) (*link, error) {
	l := &link{ctx: ctx, o: o, h: o.Hold, m: m, bin: bin, args: args, errb: errb, log: o.Log, quit: make(chan struct{})}
	if l.log == nil {
		l.log = slog.Default()
	}
	from := int64(hold.FromReplay)
	cmd, stdin, r, hd, err := l.connect(from, l.h != nil && l.h.Adopt)
	if err != nil {
		return nil, err
	}
	if hd.Gone {
		_ = cmd.Wait()
		return nil, ErrNotHeld
	}
	l.cmd, l.stdin, l.r, l.Header, l.off = cmd, stdin, r, hd, hd.Offset
	return l, nil
}

// connect starts the CLI (or attach to it) and, held, reads the header.
func (l *link) connect(from int64, only bool) (*exec.Cmd, io.WriteCloser, *bufio.Reader, hold.Header, error) {
	var hd hold.Header
	var wrap []string
	if l.h != nil {
		wrap = []string{l.h.Bin, "attach", "-from", strconv.FormatInt(from, 10), "-start-mark", l.m.start, "-end-mark", l.m.end}
		if only {
			wrap = append(wrap, "-only")
		}
		for _, k := range l.m.keep {
			wrap = append(wrap, "-keep-mark", k)
		}
		if l.h.Meta != "" {
			wrap = append(wrap, "-meta", l.h.Meta)
		}
		if len(l.h.Scope) > 0 {
			wrap = append(wrap, "-scope", strings.Join(l.h.Scope, " "))
		}
		wrap = append(wrap, l.h.Slot, "--")
	}
	cmd, err := cliCommand(l.o, wrap, l.bin, l.args)
	if err != nil {
		return nil, nil, nil, hd, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = l.errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, hd, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, hd, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, hd, fmt.Errorf("start %s: %w", l.bin, err)
	}
	r := bufio.NewReaderSize(stdout, 1<<20)
	if l.h == nil {
		return cmd, stdin, r, hd, nil
	}
	type res struct {
		line []byte
		err  error
	}
	got := make(chan res, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		got <- res{line, err}
	}()
	var x res
	select {
	case x = <-got:
	case <-time.After(90 * time.Second):
		x.err = errors.New("no answer from pocket-hold")
	}
	if x.err == nil && json.Unmarshal(x.line, &hd) == nil && hd.Type == hold.HeaderType {
		return cmd, stdin, r, hd, nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	werr := cmd.Wait()
	if x.err == nil {
		x.err = fmt.Errorf("unexpected output from pocket-hold: %s", oneLine(string(x.line), 200))
	} else if werr != nil {
		x.err = werr
	}
	return nil, nil, nil, hd, x.err
}

// Lines calls fn with each line of the CLI's stdout (no newline) until it
// ends, reattaching a held CLI whose connection dropped. It returns how the
// CLI ended: a plain process's Wait error, or for a held one its exit code
// (nil for 0), errDetached, or the error of the connection that was lost.
func (l *link) Lines(fn func(line []byte)) error {
	r := l.r
	for {
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				break // a partial line is not counted: it comes again
			}
			if l.h != nil {
				l.off += int64(len(line))
				if bytes.HasPrefix(line, []byte(`{"type":"`+hold.ExitType+`"`)) {
					l.off -= int64(len(line)) // attach's, not the CLI's
					var x struct{ Code int }
					_ = json.Unmarshal(line, &x)
					l.exit = &x.Code
					continue
				}
			}
			fn(line[:len(line)-1])
		}
		l.wmu.Lock()
		cmd := l.cmd
		l.wmu.Unlock()
		err := cmd.Wait()
		if l.h == nil {
			l.finish()
			return err
		}
		if l.exit != nil {
			l.finish()
			if *l.exit == 0 {
				return nil
			}
			return &heldExit{code: *l.exit}
		}
		if l.isDetached() {
			l.finish()
			return errDetached
		}
		l.markDown()
		if l.isStopping() {
			// the stop may not have arrived: say it again on a fresh connection
			l.stopHeld()
			l.finish()
			return nil
		}
		l.log.Warn("agent connection lost, reattaching", "slot", l.h.Slot, "err", err, "stderr", oneLine(l.errb.String(), 300))
		nr, ok := l.reattach()
		if !ok {
			l.finish()
			if l.isDetached() {
				return errDetached
			}
			return err
		}
		r = nr
	}
}

// heldExit is a held CLI's exit code, as attach reported it.
type heldExit struct{ code int }

func (e *heldExit) Error() string { return "exit status " + strconv.Itoa(e.code) }

func (l *link) reattach() (*bufio.Reader, bool) {
	deadline := time.Now().Add(reconnectFor)
	delay := time.Second
	for {
		select {
		case <-l.quit:
			return nil, false
		case <-l.h.Leave:
			l.Detach()
			return nil, false
		case <-time.After(delay):
		}
		cmd, stdin, r, hd, err := l.connect(l.off, true)
		if err == nil && hd.Gone {
			_ = cmd.Wait()
			l.log.Warn("agent gone on reattach", "slot", l.h.Slot)
			return nil, false
		}
		if err == nil {
			if hd.Offset != l.off {
				l.log.Warn("agent output skipped on reattach", "slot", l.h.Slot, "had", l.off, "from", hd.Offset)
				l.off = hd.Offset
			}
			l.wmu.Lock()
			// what the holder did not get of what was written goes again
			if sent := l.inBase + int64(len(l.inLog)); hd.In < sent {
				if hd.In >= l.inBase {
					_, err = stdin.Write(l.inLog[hd.In-l.inBase:])
				} else {
					l.log.Warn("agent input lost on reattach", "slot", l.h.Slot, "holder", hd.In, "kept from", l.inBase)
				}
			}
			if err == nil {
				l.cmd, l.stdin = cmd, stdin
				close(l.down)
				l.down = nil
				l.wmu.Unlock()
				l.log.Info("agent reattached", "slot", l.h.Slot, "offset", l.off)
				return r, true
			}
			l.wmu.Unlock()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		delay = min(delay*2, 15*time.Second)
	}
}

func (l *link) markDown() {
	l.wmu.Lock()
	if l.down == nil && !l.ended {
		l.down = make(chan struct{})
	}
	l.wmu.Unlock()
}

func (l *link) finish() {
	l.wmu.Lock()
	l.ended = true
	if l.down != nil {
		close(l.down)
		l.down = nil
	}
	l.wmu.Unlock()
	l.quitOnce.Do(func() { close(l.quit) })
}

// Write sends p to the CLI's stdin. Held, it waits out a reattach.
func (l *link) Write(p []byte) error {
	for {
		l.wmu.Lock()
		if l.ended {
			l.wmu.Unlock()
			return ErrClosed
		}
		if wait := l.down; wait != nil {
			l.wmu.Unlock()
			<-wait
			continue
		}
		_, err := l.stdin.Write(p)
		if err == nil && l.h != nil {
			l.inLog = append(l.inLog, p...)
			if over := len(l.inLog) - inLogMax; over > 0 {
				l.inLog = append([]byte(nil), l.inLog[over:]...)
				l.inBase += int64(over)
			}
		}
		if err != nil && l.h != nil && !l.ended {
			// the connection went under it: write again once it is back
			if l.down == nil {
				l.down = make(chan struct{})
			}
			l.wmu.Unlock()
			continue
		}
		l.wmu.Unlock()
		return err
	}
}

// Close ends the CLI: stdin closed (held: the stop line first, so the
// holder ends it), then SIGTERM and SIGKILL to what runs here if it lingers.
func (l *link) Close(done <-chan struct{}) {
	if l.h != nil {
		if l.h.leaving() {
			l.Detach() // Pocket AI is stopping: the CLI stays for the next one
			return
		}
		l.wmu.Lock()
		l.stopping = true
		up := l.down == nil && !l.ended
		l.wmu.Unlock()
		if up {
			_ = l.writeNow([]byte(hold.StopLine + "\n"))
		} else {
			l.quitOnce.Do(func() { close(l.quit) })
		}
	}
	l.wmu.Lock()
	stdin, cmd := l.stdin, l.cmd
	l.wmu.Unlock()
	_ = stdin.Close()
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	grace := 3 * time.Second
	if l.h != nil {
		grace = 15 * time.Second // the holder escalates on its own; attach leaves after
	}
	go func() {
		select {
		case <-done:
		case <-time.After(grace):
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	}()
}

func (l *link) writeNow(p []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ended || l.down != nil {
		return ErrClosed
	}
	_, err := l.stdin.Write(p)
	return err
}

// Detach lets go of a held CLI and leaves it running; plain, it closes.
func (l *link) Detach() {
	if l.h == nil {
		return
	}
	l.wmu.Lock()
	l.detached = true
	cmd := l.cmd
	l.wmu.Unlock()
	l.quitOnce.Do(func() { close(l.quit) })
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func (l *link) isDetached() bool {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.detached || l.h.leaving()
}

func (l *link) isStopping() bool {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.stopping
}

// stopHeld ends the held CLI over a connection of its own.
func (l *link) stopHeld() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	if l.o.Remote != nil {
		_, _, err = l.o.Remote.Run(ctx, 30*time.Second, `"$1" stop "$2"`, l.h.Bin, l.h.Slot)
	} else {
		err = exec.CommandContext(ctx, l.h.Bin, "stop", l.h.Slot).Run()
	}
	if err != nil {
		l.log.Warn("could not stop a held agent", "slot", l.h.Slot, "err", err)
	}
}

// cliCommand runs bin with args where o says (this server or o.Remote),
// behind wrap when given (pocket-hold attach … --).
func cliCommand(o Options, wrap []string, bin string, args []string) (*exec.Cmd, error) {
	if o.Remote != nil {
		return o.Remote.command(o.Cwd, o.Env, wrap, bin, args)
	}
	if len(wrap) > 0 {
		all := append(append(append([]string{}, wrap[1:]...), bin), args...)
		return LocalCmd(nil, o.Cwd, o.Env, wrap[0], all...)
	}
	return LocalCmd(nil, o.Cwd, o.Env, bin, args...)
}
