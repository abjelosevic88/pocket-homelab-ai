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
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	// MetaFile is where Meta is when it carries the Pocket tools key
	// (private.go); openLink sets it, callers do not.
	MetaFile string
	// Leave closes when Pocket AI is stopping: closing the session then
	// only lets go of the CLI, and a dropped connection is not reattached.
	Leave <-chan struct{}
	// FromTurn: adopting, replay the whole last turn, ended or not (the
	// thread never saw it end: Pocket AI was down when it did).
	FromTurn bool

	// Dial, when set, reaches the holder of a slot directly, and Pocket AI
	// speaks the holder protocol in-process over it (hold.Open) instead of
	// running pocket-hold attach for every connection: hold.DialSlot for a
	// holder on this server; a relay stream for one on another machine. Its
	// error wraps hold.ErrNoHolder when no holder listens there. A start
	// (not Adopt) first runs pocket-hold attach -start-only, through
	// StartOnly. Nil, the default: pocket-hold attach as a process.
	Dial func(ctx context.Context, slot string) (net.Conn, error)
	// StartOnly runs a holder start for Dial and returns the header it
	// printed (SlotBusy set when another start has the slot). Nil: the
	// start's Cmd is run as it is, here or over ssh.
	StartOnly func(ctx context.Context, start HoldStart) (hold.Header, error)

	// Route, when set, is asked before every connection to the CLI (its
	// start, each reattach) which way that one goes: over Dial and
	// StartOnly (a machine's link), or pocket-hold attach over the agent's
	// own ssh connection with its forwards (the per-agent path). The answer
	// may differ from one connection to the next: that is how an agent moves
	// between the two (Reroute). An error means no connection now; it is
	// tried again like a lost one. Nil: Dial decides, as always.
	Route func(ctx context.Context, start bool) (HoldPath, error)
	// CatchUp, when set, is asked before a reattach over Dial: release is
	// called once the stream has caught up with what the holder had when it
	// answered (its Live offset), or ends. It caps how many streams of one
	// machine catch up at once after its link came back.
	CatchUp func(ctx context.Context) (release func(), err error)
}

// HoldPath is the way one connection of a held CLI goes (Hold.Route).
type HoldPath struct {
	// Link: over Hold.Dial (and StartOnly for a start). Otherwise the
	// per-agent ssh path: pocket-hold attach with Remote.Forwards.
	Link bool
	// NoForwards: the per-agent path without the tools forward (its port
	// is not to be had; the thread was told why).
	NoForwards bool
	// Guard is sh run on the machine before the per-agent connection's
	// command; a non-zero exit there ends it with exit 75 (the machine's
	// fence refused: a newer link owns it).
	Guard string
	// Free frees the per-agent path's forwarded ports when ssh says one is
	// still held (instead of Remote.FreeForwards; fenced on a machine with
	// a fence).
	Free func(ctx context.Context) error
	// Done is told how the connection went: nil once it is up.
	Done func(err error)
}

// Reroutable is a session whose connection can be made again at once, so
// its next connection goes the way Hold.Route now says (link.Reroute).
type Reroutable interface {
	Reroute() bool
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

// A held link tries to get its CLI back for as long as it takes (the
// machine may be asleep or offline for hours), up to retryMax apart; it
// stops when the holder says the CLI is gone, or the session is closed.
var retryMax = time.Minute

// unstopped: slots whose stop could not reach their holder (the machine was
// unreachable). The thread's next start ends that CLI first instead of
// taking it back.
var unstopped sync.Map

// TakeUnstopped reports (once) whether slot's CLI was stopped while its
// machine could not be reached, so it may still run there.
func TakeUnstopped(slot string) bool {
	_, ok := unstopped.LoadAndDelete(slot)
	return ok
}

// LinkSink is the optional part of a Sink told when a held CLI's
// connection has been down for a moment, and when it is back.
type LinkSink interface {
	Link(up bool)
}

// ReplaySink is the optional part of a Sink told while an adopted CLI's
// output that was already shown before is read again: it may keep that out
// of view and show the result once, instead of flickering through it.
type ReplaySink interface {
	Replay(on bool)
}

// link is a CLI's stdio. Plain, it is the process itself. Held, it is
// pocket-hold attach (or the holder spoken to in-process: Hold.Dial), and
// when the connection drops the link attaches again where it stopped: no
// output read twice or lost, stdin the holder may not have got sent again.
// The session using it notices nothing.
type link struct {
	ctx  context.Context
	o    Options
	h    *Hold // nil: plain
	m    marks
	bin  string
	args []string
	errb *tail
	log  *slog.Logger
	t    transport

	Header hold.Header // what the first attach found

	wmu     sync.Mutex
	c       conn // the current connection
	stdin   io.WriteCloser
	r       *bufio.Reader
	down    bool   // reconnecting
	pending []byte // written while down: goes out when back
	ended   bool
	inLog   []byte // stdin written, from inBase on (held)
	inBase  int64
	onLink  func(up bool)

	off      int64 // stdout read so far (held)
	onReplay func(on bool)
	quiet    bool
	exit     *int // the holder reported the CLI's exit
	stopping bool
	quit     chan struct{} // closed by Detach or Close: stop reconnecting
	quitOnce sync.Once
	detached bool

	// Parking (Park): a held CLI with nothing going on needs no connection.
	// let: the current connection was ended on purpose; parked: nothing has
	// asked for it back yet; parkOff: stdout read when it went (valid while
	// resting); waited: the connection was collected (a process's pid may
	// belong to something else by now), so it is never ended again.
	let, parked, resting, waited bool
	parkOff                      int64
	wake                         chan struct{}
}

const inLogMax = 32 << 20

func openLink(ctx context.Context, o Options, m marks, bin string, args []string, errb *tail, sink Sink) (*link, error) {
	l := &link{ctx: ctx, o: o, h: o.Hold, m: m, bin: bin, args: args, errb: errb, log: o.Log, quit: make(chan struct{}), wake: make(chan struct{}, 1)}
	if ls, ok := sink.(LinkSink); ok {
		l.onLink = ls.Link
	}
	if rs, ok := sink.(ReplaySink); ok {
		l.onReplay = rs.Replay
	}
	if l.log == nil {
		l.log = slog.Default()
	}
	if l.h != nil && l.h.Meta != "" {
		h := *l.h
		f, err := privateMeta(ctx, o, h.Slot, h.Meta)
		if err != nil {
			return nil, fmt.Errorf("holder meta: %w", err)
		}
		if f != "" {
			h.Meta, h.MetaFile = "", f
		}
		l.h = &h
	}
	from := int64(hold.FromReplay)
	if l.h != nil && l.h.FromTurn {
		from = hold.FromTurn
	}
	c, hd, err := l.connect(from, l.h != nil && l.h.Adopt)
	if err != nil {
		return nil, err
	}
	if hd.Gone {
		return nil, ErrNotHeld
	}
	l.c, l.stdin, l.r, l.Header, l.off = c, c.Stdin(), c.Stdout(), hd, hd.Offset
	return l, nil
}

// connect starts the CLI (or attach to it) and, held, reads the header. A
// held CLI on another machine is reached on the same forwarded port every
// time (it was started with it); when a dropped connection the machine has
// not noticed still holds that port, ssh refuses the forward (exit 255):
// that connection is ended (FreeForwards returns once the port is free) and
// this one tried once more.
func (l *link) connect(from int64, only bool) (conn, hold.Header, error) {
	t, p, err := l.transport(!only)
	if err != nil {
		return nil, hold.Header{}, err
	}
	c, hd, stderr, err := t.open(from, only)
	if err != nil && l.h != nil && l.o.Remote != nil && strings.Contains(stderr, "remote port forwarding failed") {
		l.log.Warn("agent port still held by an old connection; ending it", "slot", l.h.Slot, "machine", l.o.Remote.Target)
		ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
		free := l.o.Remote.FreeForwards
		if p.Free != nil {
			free = p.Free
		}
		ferr := free(ctx)
		cancel()
		if ferr != nil {
			l.log.Warn("could not free the agent port", "slot", l.h.Slot, "err", ferr)
		}
		c, hd, stderr, err = t.open(from, only)
		if err != nil && ferr != nil && strings.Contains(stderr, "remote port forwarding failed") {
			err = fmt.Errorf("%w (%v)", err, ferr) // why the port is still held: another program on it
		}
	}
	// ssh could not reach the machine at all (nothing started there): a
	// network blip, usually gone in a few seconds — try again, 1 s then 3 s
	for try := 0; err != nil && try < 2 && l.o.Remote != nil && notConnected(err, stderr); try++ {
		l.log.Info("machine not reachable; trying again", "machine", l.o.Remote.Target, "err", strings.TrimSpace(stderr))
		select {
		case <-l.ctx.Done():
			if p.Done != nil {
				p.Done(err)
			}
			return c, hd, err
		case <-time.After(time.Duration(1+2*try) * time.Second):
		}
		c, hd, stderr, err = t.open(from, only)
	}
	if p.Done != nil {
		p.Done(err)
	}
	return c, hd, err
}

// transport is the way the next connection goes: Hold.Route's answer when
// it is set, else the in-process holder client when Hold.Dial is set, else
// pocket-hold attach (or the plain CLI) as a process.
func (l *link) transport(start bool) (transport, HoldPath, error) {
	var p HoldPath
	if l.h == nil {
		return procTransport{l: l}, p, nil
	}
	if l.h.Route == nil {
		if l.h.Dial != nil {
			return holdTransport{l: l, dial: l.h.Dial, start: l.h.StartOnly, catchUp: l.h.CatchUp}, p, nil
		}
		return procTransport{l: l}, p, nil
	}
	p, err := l.h.Route(l.ctx, start)
	if err != nil {
		return nil, p, err
	}
	if p.Link {
		return holdTransport{l: l, dial: l.h.Dial, start: l.h.StartOnly, catchUp: l.h.CatchUp}, p, nil
	}
	t := procTransport{l: l}
	if l.o.Remote != nil && (p.NoForwards || p.Guard != "") {
		r := *l.o.Remote
		if p.NoForwards {
			r.Forwards = nil
		}
		r.Guard = p.Guard
		t.remote = &r
	}
	return t, p, nil
}

// Lines calls fn with each line of the CLI's stdout (no newline) until it
// ends, reattaching a held CLI whose connection dropped. It returns how the
// CLI ended: a plain process's Wait error, or for a held one its exit code
// (nil for 0), errDetached, or the error of the connection that was lost.
func (l *link) Lines(fn func(line []byte)) error {
	r := l.r
	if l.h != nil && !l.Header.Started && l.off < l.Header.Live {
		l.quietStart()
	}
	defer l.quietEnd()
	for {
		for {
			if l.quiet && l.off >= l.Header.Live {
				l.quietEnd()
			}
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
		c := l.c
		l.wmu.Unlock()
		err := c.Wait()
		if l.h == nil {
			l.finish()
			return err
		}
		l.wmu.Lock()
		l.waited = true
		l.wmu.Unlock()
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
		let := l.rest()
		if l.isDetached() {
			// Pocket AI is stopping (or let go of it) while it was parked
			l.finish()
			return errDetached
		}
		if l.isStopping() {
			// the stop may not have arrived: say it again on a fresh connection
			l.stopHeld()
			l.finish()
			return nil
		}
		if let {
			l.log.Info("agent connection taken back", "slot", l.h.Slot)
		} else {
			l.log.Warn("agent connection lost, reattaching", "slot", l.h.Slot, "err", err, "stderr", oneLine(l.errb.String(), 300))
		}
		nr, ok := l.reattach(let)
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

// quietStart: what follows was shown before (an adopted CLI's replay, or
// the earlier turns' kept lines); quietEnd: live output again.
func (l *link) quietStart() {
	if !l.quiet && l.onReplay != nil {
		l.quiet = true
		l.onReplay(true)
	}
}

func (l *link) quietEnd() {
	if l.quiet {
		l.quiet = false
		l.onReplay(false)
	}
}

// heldExit is a held CLI's exit code, as attach reported it.
type heldExit struct{ code int }

func (e *heldExit) Error() string { return "exit status " + strconv.Itoa(e.code) }

// reattach connects to the holder again where the output stopped; now: at
// once (a parked connection asked back), not after the pause a lost one gets.
func (l *link) reattach(now bool) (*bufio.Reader, bool) {
	delay := time.Second
	if now {
		delay = 0
	}
	told := false // the sink knows it is down (not for a blip)
	defer func() {
		if told && l.onLink != nil {
			l.wmu.Lock()
			up := !l.down && !l.ended
			l.wmu.Unlock()
			if up {
				l.onLink(true)
			}
		}
	}()
	for try := 0; ; try++ {
		if try == 2 && l.onLink != nil {
			told = true
			l.onLink(false)
		}
		select {
		case <-l.quit:
			return nil, false
		case <-l.h.Leave:
			l.Detach()
			return nil, false
		case <-time.After(delay):
		}
		c, hd, err := l.connect(l.off, true)
		if err == nil && hd.Gone {
			l.log.Warn("agent gone on reattach", "slot", l.h.Slot)
			return nil, false
		}
		if err == nil {
			if hd.Offset != l.off {
				l.log.Warn("agent output skipped on reattach", "slot", l.h.Slot, "had", l.off, "from", hd.Offset)
				l.off = hd.Offset
			}
			stdin := c.Stdin()
			l.wmu.Lock()
			if l.detached {
				// let go of while this connection was being made: it goes too
				l.wmu.Unlock()
				c.End(false)
				_ = c.Wait()
				return nil, false
			}
			// what the holder did not get of what was written goes again
			if sent := l.inBase + int64(len(l.inLog)); hd.In < sent {
				if hd.In >= l.inBase {
					_, err = stdin.Write(l.inLog[hd.In-l.inBase:])
				} else {
					l.log.Warn("agent input lost on reattach", "slot", l.h.Slot, "holder", hd.In, "kept from", l.inBase)
				}
			}
			// then what was written meanwhile
			if err == nil && len(l.pending) > 0 {
				if _, err = stdin.Write(l.pending); err == nil {
					l.logIn(l.pending)
					l.pending = nil
				}
			}
			if err == nil {
				l.c, l.stdin = c, stdin
				l.down, l.waited, l.let = false, false, false
				l.wmu.Unlock()
				l.log.Info("agent reattached", "slot", l.h.Slot, "offset", l.off)
				return c.Stdout(), true
			}
			l.wmu.Unlock()
			c.End(true)
			_ = c.Wait()
		}
		delay = min(max(delay*2, time.Second), retryMax)
	}
}

// Park ends a held CLI's connection while nothing happens on it: the CLI
// stays under its holder, and the link takes it back where the output
// stopped as soon as something is written to it or Wake is called. False
// when it is not held, not connected, or has something to deliver.
//
// A connection per idle agent is what filled a NAT's ports to one machine
// (57 open for days, new ones timing out); whoever parks must watch the
// holder for output (Parked) and wake the link, since the CLI can start
// working on its own (Remote Control, a background task ending).
func (l *link) Park() bool {
	if l.h == nil || l.h.leaving() {
		return false
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ended || l.down || l.waited || l.detached || l.stopping || l.parked || len(l.pending) > 0 || l.c == nil {
		return false
	}
	l.parked, l.let = true, true
	l.c.End(false) // not collected yet (waited): still ours
	return true
}

// Reroute ends the current connection on purpose and makes the next one at
// once, so it goes the way Hold.Route now says (a move between the machine's
// link and the agent's own ssh connection). A parked link is woken; one that
// is reconnecting asks the route anyway. False when it is not held, or ended.
func (l *link) Reroute() bool {
	if l.h == nil || l.h.leaving() {
		return false
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ended || l.detached || l.stopping {
		return false
	}
	if l.parked {
		return l.wakeLocked()
	}
	if l.down || l.waited || l.c == nil {
		return true // between connections: the next one asks the route
	}
	l.let = true
	l.c.End(false)
	return true
}

// Wake asks a parked link's connection back; false when it was not parked.
func (l *link) Wake() bool {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.wakeLocked()
}

func (l *link) wakeLocked() bool {
	if !l.parked {
		return false
	}
	l.parked = false
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return true
}

// Parked: the link is parked, and how much of the CLI's stdout it had read
// when its connection went (the holder's offset is past it once the CLI has
// said something); -1 for the moment the connection takes to end.
func (l *link) Parked() (off int64, ok bool) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if !l.parked {
		return 0, false
	}
	if !l.resting {
		return -1, true
	}
	return l.parkOff, true
}

// rest waits, the connection gone, while the link is parked: until it is
// woken, closed or let go of. It reports whether the connection was ended on
// purpose (Park) rather than lost.
func (l *link) rest() bool {
	for {
		l.wmu.Lock()
		let := l.let
		if !l.parked {
			l.resting = false
			l.wmu.Unlock()
			return let
		}
		l.parkOff, l.resting = l.off, true
		l.wmu.Unlock()
		select {
		case <-l.wake: // or one left from an earlier time: looked at again
		case <-l.quit:
			l.unpark()
		case <-l.h.Leave:
			l.unpark()
		}
	}
}

func (l *link) unpark() {
	l.wmu.Lock()
	l.parked = false
	l.wmu.Unlock()
}

func (l *link) markDown() {
	l.wmu.Lock()
	if !l.ended {
		l.down = true
	}
	l.wmu.Unlock()
}

func (l *link) finish() {
	l.wmu.Lock()
	l.ended = true
	l.down = false
	l.pending = nil
	l.wmu.Unlock()
	l.quitOnce.Do(func() { close(l.quit) })
}

// Write sends p to the CLI's stdin. Held and reconnecting, it keeps p to
// send once the connection is back, and does not wait for that.
func (l *link) Write(p []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ended {
		return ErrClosed
	}
	l.wakeLocked() // parked: it comes back for this
	if l.down {
		l.pending = append(l.pending, p...)
		return nil
	}
	_, err := l.stdin.Write(p)
	if err == nil && l.h != nil {
		l.logIn(p)
	}
	if err != nil && l.h != nil {
		// the connection went under it: it goes out once it is back
		l.down = true
		l.pending = append(l.pending, p...)
		return nil
	}
	return err
}

// logIn keeps what the holder may still need again; with l.wmu held.
func (l *link) logIn(p []byte) {
	l.inLog = append(l.inLog, p...)
	if over := len(l.inLog) - inLogMax; over > 0 {
		l.inLog = append([]byte(nil), l.inLog[over:]...)
		l.inBase += int64(over)
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
		up := !l.down && !l.ended
		l.wmu.Unlock()
		if up {
			_ = l.writeNow([]byte(hold.StopLine + "\n"))
		} else {
			l.quitOnce.Do(func() { close(l.quit) })
		}
	}
	l.wmu.Lock()
	stdin, c, waited := l.stdin, l.c, l.waited
	l.wmu.Unlock()
	_ = stdin.Close()
	if waited {
		return // nothing of ours runs any more (parked, or between connections)
	}
	grace := 3 * time.Second
	if l.h != nil {
		grace = 15 * time.Second // the holder escalates on its own; attach leaves after
	}
	go func() {
		select {
		case <-done:
		case <-time.After(grace):
			c.End(false)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				c.End(true)
			}
		}
	}()
}

func (l *link) writeNow(p []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ended || l.down {
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
	if l.c != nil && !l.waited {
		l.c.End(false)
	}
	l.wmu.Unlock()
	l.quitOnce.Do(func() { close(l.quit) })
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
	viaDial := l.h.Dial != nil
	if viaDial {
		var c net.Conn
		if c, err = l.h.Dial(ctx, l.h.Slot); err == nil {
			_, err = hold.StopVia(c)
		} else if errors.Is(err, hold.ErrNoHolder) {
			err = nil // nothing runs there to stop
		} else if l.h.Route != nil && l.o.Remote != nil {
			// the machine's link is not there (or not for this agent): ask
			// pocket-hold there over ssh
			viaDial = false
		}
	}
	if viaDial {
	} else if l.o.Remote != nil {
		_, _, err = l.o.Remote.Run(ctx, 30*time.Second, `"$1" stop "$2"`, l.h.Bin, l.h.Slot)
	} else {
		err = exec.CommandContext(ctx, l.h.Bin, "stop", l.h.Slot).Run()
	}
	if err != nil {
		l.log.Warn("could not stop a held agent", "slot", l.h.Slot, "err", err)
		unstopped.Store(l.h.Slot, true)
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
