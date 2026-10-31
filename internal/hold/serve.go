package hold

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ServeOptions shape a holder.
type ServeOptions struct {
	// A stdin line containing StartMark starts a turn; a stdout line
	// containing EndMark ends it. Empty: the holder never sees turns, and a
	// new reader gets everything it still has.
	StartMark, EndMark string
	// Stdout lines containing one of KeepMarks are kept for the CLI's whole
	// life and given to readers that replay (Header.Kept).
	KeepMarks []string
	Meta      string // returned in every header
	// With nobody attached and no turn running, end the CLI after
	// IdleDetached (counted from whichever came last: the detach or the end
	// of the turn). A running turn is never cut; MaxDetached > 0 caps even
	// that.
	IdleDetached, MaxDetached time.Duration
	// Linger is how long an ended CLI's output waits for someone to collect it.
	Linger time.Duration
}

const (
	maxBuf     = 256 << 20 // stdout kept for readers; older bytes are dropped
	maxKept    = 8 << 20
	errTailMax = 16 << 10
	chunk      = 256 << 10
)

type holder struct {
	o   ServeOptions
	log *log.Logger

	mu        sync.Mutex
	cond      *sync.Cond
	buf       []byte // stdout from base on
	base      int64
	busy      bool
	turnStart int64
	lastEnd   int64
	errTail   []byte
	errTotal  int64
	inTotal   int64
	kept      []keptLine
	keptBytes int
	cur       *client
	detached  time.Time
	turnEnded time.Time
	started   time.Time
	exited    bool
	code      int
	acked     chan struct{}
	ackOnce   sync.Once
	done      chan struct{} // the CLI ended

	cmd      *exec.Cmd
	stdinMu  sync.Mutex
	stdin    io.WriteCloser
	stopOnce sync.Once
	asked    bool // stopped on request: nobody needs the exit for long
	stopping bool
	out, in  lines
}

type keptLine struct {
	end  int64
	line string
}

type client struct {
	conn     net.Conn
	pos      int64
	errSeen  int64
	gone     bool
	exitSent bool
}

// Serve runs the holder of slot: starts argv and serves attach clients until
// the CLI has ended and its exit was collected (or Linger passed).
func Serve(slot string, o ServeOptions, argv []string) int {
	if o.IdleDetached <= 0 {
		o.IdleDetached = 24 * time.Hour
	}
	if o.Linger <= 0 {
		o.Linger = 30 * time.Minute
	}
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-hold: nothing to run")
		return 2
	}
	dir, err := Dir(slot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold:", err)
		return 2
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold:", err)
		return 1
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold:", err)
		return 1
	}
	// The lock file stays: removing it would let two holders lock two files.
	// One on its way out holds it for a moment more; one that still serves
	// keeps the slot.
	for i := 0; syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil; i++ {
		if c, err := dial(dir); err == nil || i == 100 {
			if c != nil {
				c.Close()
			}
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	logf, err := os.OpenFile(filepath.Join(dir, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logf, _ = os.Open(os.DevNull)
	}
	h := &holder{o: o, log: log.New(logf, "", log.LstdFlags), acked: make(chan struct{}), done: make(chan struct{}), detached: time.Now(), started: time.Now()}
	h.cond = sync.NewCond(&h.mu)
	h.out.fn = h.outLine
	h.in.fn = h.inLine
	sock := sockPath(dir)
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		h.log.Print("listen: ", err)
		return 1
	}
	_ = os.Chmod(sock, 0o600)
	defer l.Close() // removes the socket

	h.start(argv)
	go h.accept(l)
	h.monitor()
	return 0
}

func (h *holder) start(argv []string) {
	fail := func(err error) {
		h.log.Print("start: ", err)
		h.mu.Lock()
		h.errTail = []byte("pocket-hold: " + err.Error() + "\n")
		h.errTotal = int64(len(h.errTail))
		h.exited, h.code = true, 127
		h.cond.Broadcast()
		h.mu.Unlock()
		close(h.done)
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		fail(err)
		return
	}
	cmd := &exec.Cmd{Path: bin, Args: argv, SysProcAttr: &syscall.SysProcAttr{Setpgid: true}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail(err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fail(err)
		return
	}
	if err := cmd.Start(); err != nil {
		fail(err)
		return
	}
	h.cmd, h.stdin = cmd, stdin
	h.log.Printf("pid %d: %s", cmd.Process.Pid, argv[0])
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); h.read(stdout, h.addOut) }()
	go func() { defer readers.Done(); h.read(stderr, h.addErr) }()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = ee.ExitCode()
			}
		} else if err != nil {
			code = 1
		}
		h.log.Printf("pid %d: exit %d", cmd.Process.Pid, code)
		h.mu.Lock()
		h.exited, h.code = true, code
		h.cond.Broadcast()
		h.mu.Unlock()
		close(h.done)
	}()
}

func (h *holder) read(r io.Reader, add func([]byte)) {
	b := make([]byte, 64<<10)
	for {
		n, err := r.Read(b)
		if n > 0 {
			h.mu.Lock()
			add(b[:n])
			h.cond.Broadcast()
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// addOut keeps stdout for readers; with h.mu held.
func (h *holder) addOut(p []byte) {
	h.buf = append(h.buf, p...)
	h.out.feed(p)
	if over := len(h.buf) - maxBuf; over > 0 {
		h.cut(h.base + int64(over))
	}
}

func (h *holder) addErr(p []byte) {
	h.errTotal += int64(len(p))
	h.errTail = append(h.errTail, p...)
	if over := len(h.errTail) - errTailMax; over > 0 {
		h.errTail = append([]byte(nil), h.errTail[over:]...)
	}
}

// cut drops stdout before off; with h.mu held.
func (h *holder) cut(off int64) {
	if off <= h.base {
		return
	}
	if total := h.base + int64(len(h.buf)); off > total {
		off = total
	}
	h.buf = append([]byte(nil), h.buf[off-h.base:]...)
	h.base = off
}

func (h *holder) total() int64 { return h.base + int64(len(h.buf)) }

// replayFrom is where a reader that has seen nothing of the running turn
// starts; with h.mu held.
func (h *holder) replayFrom() int64 {
	if h.busy {
		return h.turnStart
	}
	return h.lastEnd
}

func (h *holder) outLine(line []byte, end int64) {
	for _, m := range h.o.KeepMarks {
		if m != "" && bytes.Contains(line, []byte(m)) {
			h.kept = append(h.kept, keptLine{end, string(line)})
			h.keptBytes += len(line)
			for h.keptBytes > maxKept && len(h.kept) > 1 {
				h.keptBytes -= len(h.kept[0].line)
				h.kept = h.kept[1:]
			}
			break
		}
	}
	if h.busy && h.o.EndMark != "" && bytes.Contains(line, []byte(h.o.EndMark)) {
		h.busy = false
		h.lastEnd = end
		h.turnEnded = time.Now()
	}
}

func (h *holder) inLine(line []byte, _ int64) {
	if h.busy || h.o.StartMark == "" || !bytes.Contains(line, []byte(h.o.StartMark)) {
		return
	}
	h.busy = true
	h.turnStart = h.total()
	// what came before was read: the turn was asked for after it
	keep := h.turnStart
	if h.cur != nil && h.cur.pos < keep {
		keep = h.cur.pos
	}
	h.cut(keep)
}

func (h *holder) writeIn(p []byte) {
	h.stdinMu.Lock()
	defer h.stdinMu.Unlock()
	h.mu.Lock()
	h.in.feed(p)
	h.inTotal += int64(len(p))
	h.mu.Unlock()
	if h.stdin != nil {
		_, _ = h.stdin.Write(p)
	}
}

// stop ends the CLI as Pocket AI always has: stdin closed, then SIGTERM and
// SIGKILL to its group if it lingers.
func (h *holder) stop(why string) {
	h.stopOnce.Do(func() {
		h.log.Print("stop: ", why)
		h.mu.Lock()
		h.asked = why == "asked"
		h.stopping = true
		h.mu.Unlock()
		h.stdinMu.Lock()
		if h.stdin != nil {
			_ = h.stdin.Close()
		}
		h.stdinMu.Unlock()
		if h.cmd == nil || h.cmd.Process == nil {
			return
		}
		pid := h.cmd.Process.Pid
		go func() {
			select {
			case <-h.done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGTERM)
				select {
				case <-h.done:
				case <-time.After(5 * time.Second):
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}()
	})
}

func (h *holder) accept(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go h.serveConn(c)
	}
}

func (h *holder) header() Header {
	hd := Header{Busy: h.busy, Exited: h.exited, Stopping: h.stopping, Meta: h.o.Meta, Stderr: string(h.errTail), In: h.inTotal}
	if h.cmd != nil && h.cmd.Process != nil {
		hd.PID = h.cmd.Process.Pid
	}
	return hd
}

func (h *holder) serveConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, p, err := readFrame(r)
	if err != nil || typ != fHello {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	var hi hello
	if json.Unmarshal(p, &hi) != nil {
		return
	}
	h.mu.Lock()
	hd := h.header()
	if hi.Peek || hi.Stop {
		hd.Offset = h.total()
		h.mu.Unlock()
		b, _ := json.Marshal(hd)
		_ = writeFrame(c, fHeader, b)
		if hi.Stop {
			h.stop("asked")
		}
		return
	}
	from := hi.From
	switch {
	case from == FromTurn:
		from = h.turnStart
	case from < 0:
		from = h.replayFrom()
	}
	if total := h.total(); from > total {
		from = total
	}
	if from < h.base {
		hd.Gap, from = true, h.base
	}
	hd.Offset = from
	hd.Live = h.total()
	if hi.From < 0 {
		for _, k := range h.kept {
			if k.end <= from {
				hd.Kept = append(hd.Kept, k.line)
			}
		}
	}
	cl := &client{conn: c, pos: from, errSeen: h.errTotal}
	if old := h.cur; old != nil {
		old.gone = true
		old.conn.Close()
	}
	h.cur = cl
	h.cond.Broadcast()
	h.mu.Unlock()
	b, _ := json.Marshal(hd)
	if writeFrame(c, fHeader, b) != nil {
		h.drop(cl)
		return
	}
	go h.send(cl)
	for {
		typ, p, err := readFrame(r)
		if err != nil {
			break
		}
		switch typ {
		case fIn:
			h.writeIn(p)
		case fStop:
			h.stop("asked")
		case fAck:
			h.ackOnce.Do(func() { close(h.acked) })
		}
	}
	h.drop(cl)
}

func (h *holder) drop(cl *client) {
	h.mu.Lock()
	cl.gone = true
	if h.cur == cl {
		h.cur = nil
		h.detached = time.Now()
	}
	h.cond.Broadcast()
	h.mu.Unlock()
	cl.conn.Close()
}

// send streams stdout and stderr to cl as they come, then the exit.
func (h *holder) send(cl *client) {
	for {
		h.mu.Lock()
		for !cl.gone && cl.pos >= h.total() && cl.errSeen >= h.errTotal && !(h.exited && !cl.exitSent) {
			h.cond.Wait()
		}
		if cl.gone || cl.exitSent {
			h.mu.Unlock()
			return
		}
		if cl.pos < h.base {
			cl.pos = h.base
		}
		var out, errb []byte
		if total := h.total(); cl.pos < total {
			end := min(total, cl.pos+chunk)
			out = append([]byte(nil), h.buf[cl.pos-h.base:end-h.base]...)
			cl.pos = end
		}
		if n := h.errTotal - cl.errSeen; n > 0 {
			n = min(n, int64(len(h.errTail)))
			errb = append([]byte(nil), h.errTail[int64(len(h.errTail))-n:]...)
			cl.errSeen = h.errTotal
		}
		exit := out == nil && errb == nil && h.exited
		if exit {
			cl.exitSent = true
		}
		code := h.code
		h.mu.Unlock()
		var err error
		if errb != nil {
			err = writeFrame(cl.conn, fErr, errb)
		}
		if out != nil && err == nil {
			err = writeFrame(cl.conn, fOut, out)
		}
		if exit && err == nil {
			b, _ := json.Marshal(exitInfo{Code: code})
			err = writeFrame(cl.conn, fExit, b)
		}
		if err != nil {
			return // the reader sees the client go (and reads an ack it sent first)
		}
	}
}

// monitor returns when the holder is done: the CLI ended and its exit was
// collected, or nobody came for it.
func (h *holder) monitor() {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			h.mu.Lock()
			linger := h.o.Linger
			if h.asked {
				linger = 10 * time.Second
			}
			h.mu.Unlock()
			select {
			case <-h.acked:
			case <-time.After(linger):
				h.log.Print("nobody collected the exit")
			}
			return
		case <-tick.C:
		}
		h.mu.Lock()
		away := h.cur == nil
		since := time.Since(h.detached)
		quiet := time.Since(latest(h.detached, h.turnEnded, h.started))
		busy := h.busy
		h.mu.Unlock()
		switch {
		case away && !busy && quiet > h.o.IdleDetached:
			h.stop("idle with nobody attached")
		case away && h.o.MaxDetached > 0 && since > h.o.MaxDetached:
			h.stop("nobody attached")
		}
	}
}

func latest(ts ...time.Time) time.Time {
	var l time.Time
	for _, t := range ts {
		if t.After(l) {
			l = t
		}
	}
	return l
}
