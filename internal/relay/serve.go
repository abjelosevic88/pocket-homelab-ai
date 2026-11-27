package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/hashicorp/yamux"
)

// Options shape the machine side of a link (pocket-hold relay).
type Options struct {
	Installation string
	Entry        int64
	Generation   int64
	Token        string
	// Test: a candidate relay for the button's test step. It never reads or
	// writes the fence, takes over nothing, and binds only what the server
	// binds through control.
	Test bool
	// Home holds the fence (~/.pocket-ai/relay); "" is $HOME. Holder sockets
	// always follow $HOME (hold.Dir).
	Home string
	// Ping and Dead: 0 is DefaultPing / DefaultDead (shorter in tests).
	Ping, Dead time.Duration
	// Stop ends the relay as if its link had gone (a signal).
	Stop <-chan struct{}
	Log  *log.Logger
}

// ErrBadStart: the first line on stdin was not the link's magic line.
var ErrBadStart = errors.New("not a Pocket AI link")

type relay struct {
	o       Options
	sess    *yamux.Session
	started time.Time
	log     *log.Logger

	mu     sync.Mutex
	ctl    *control
	ports  map[int]*portListener
	execs  map[*execRun]struct{}
	counts map[string]int
	closed bool

	ctlReady chan struct{}
	done     chan struct{}
	doneOnce sync.Once
	err      error
}

type portListener struct {
	port  int
	l     net.Listener
	mu    sync.Mutex
	conns map[net.Conn]net.Conn // accepted conn → its tools stream (nil until open)
}

// Serve runs the machine side of one link over rw (the ssh session's stdin
// and stdout): the handshake, the fence (run mode), then the streams until the
// link is dead or closed. A *Refused or ErrBadStart is returned when the link
// never started.
func Serve(rw io.ReadWriteCloser, o Options) error {
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = h
	}
	br := bufio.NewReaderSize(rw, 64<<10)
	first := make(chan error, 1)
	go func() {
		line, err := readLine(br, 256)
		if err == nil && line != linkLine {
			err = fmt.Errorf("%w: %q", ErrBadStart, clip(line, 80))
		}
		first <- err
	}()
	var err error
	select {
	case err = <-first:
	case <-time.After(30 * time.Second):
		err = fmt.Errorf("%w: nothing came", ErrBadStart)
	case <-o.Stop:
		err = errors.New("stopped")
	}
	if err != nil {
		if !errors.Is(err, ErrBadStart) {
			err = fmt.Errorf("%w: %v", ErrBadStart, err)
		}
		writeLine(rw, errorPrefix, map[string]string{"error": err.Error()})
		return err
	}

	if !o.Test {
		if err := checkRunOptions(o); err != nil {
			writeLine(rw, errorPrefix, map[string]string{"error": err.Error()})
			return err
		}
		if err := fenceStart(o.Home, o.Installation, o.Entry, o.Generation, o.Token); err != nil {
			var rf *Refused
			if errors.As(err, &rf) {
				writeLine(rw, refusePrefix, rf)
			} else {
				writeLine(rw, errorPrefix, map[string]string{"error": err.Error()})
			}
			return err
		}
	}
	r := &relay{o: o, log: o.Log, started: time.Now(), ports: map[int]*portListener{}, execs: map[*execRun]struct{}{},
		counts: map[string]int{}, ctlReady: make(chan struct{}), done: make(chan struct{})}
	defer r.shutdown()

	hello := Hello{PID: os.Getpid(), Version: version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Generation: o.Generation, Test: o.Test, Sum: selfSum()}
	if err := writeLine(rw, helloPrefix, hello); err != nil {
		return err
	}
	sess, err := yamux.Server(rwc{Reader: br, Writer: rw, close: rw.Close}, muxConfig())
	if err != nil {
		return err
	}
	r.sess = sess
	go r.accept()

	dead := o.Dead
	if dead <= 0 {
		dead = DefaultDead
	}
	noCtl := time.NewTimer(dead)
	defer noCtl.Stop()
	for {
		select {
		case <-r.done:
			return r.err
		case <-sess.CloseChan():
			r.die(errors.New("link closed"))
		case <-o.Stop:
			r.die(errors.New("stopped"))
		case <-noCtl.C:
			select {
			case <-r.ctlReady:
			default:
				r.die(errors.New("no control stream came"))
			}
		}
	}
}

func checkRunOptions(o Options) error {
	switch {
	case !slotRE.MatchString(o.Installation):
		return fmt.Errorf("bad installation %q", o.Installation)
	case o.Entry <= 0:
		return errors.New("no machine entry")
	case o.Generation <= 0:
		return errors.New("no generation")
	case !tokenRE.MatchString(o.Token):
		return errors.New("bad token")
	}
	return nil
}

func writeLine(w io.Writer, prefix string, v any) error {
	b, _ := json.Marshal(v)
	_, err := w.Write([]byte(prefix + string(b) + "\n"))
	return err
}

func (r *relay) die(err error) {
	r.doneOnce.Do(func() {
		r.err = err
		close(r.done)
	})
}

// shutdown: listeners first (a successor waits for the ports), the fence
// record, then the commands still running.
func (r *relay) shutdown() {
	r.mu.Lock()
	r.closed = true
	ports := make([]*portListener, 0, len(r.ports))
	for _, pl := range r.ports {
		ports = append(ports, pl)
	}
	r.ports = map[int]*portListener{}
	execs := make([]*execRun, 0, len(r.execs))
	for e := range r.execs {
		execs = append(execs, e)
	}
	r.mu.Unlock()
	for _, pl := range ports {
		pl.close()
	}
	if !r.o.Test {
		fenceUpdate(r.o.Home, r.o.Installation, r.o.Token, func(fe *Fence) {
			fe.PID, fe.Token, fe.Ports = 0, "", nil
		})
	}
	if r.sess != nil {
		// yamux's Close waits for its reader (stdin, which only ends when the
		// ssh session does) and its writer (stuck on a stalled link): do not
		// wait for it, the process ends anyway
		closed := make(chan struct{})
		go func() { _ = r.sess.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(500 * time.Millisecond):
		}
	}
	for _, e := range execs {
		// output a dead link will never take must not keep the command's
		// end (and with it the relay) waiting: its writes fail now
		if e.stream != nil {
			_ = e.stream.SetWriteDeadline(time.Now())
		}
		e.kill()
	}
	for _, e := range execs {
		select {
		case <-e.gone:
		case <-time.After(killAfter + 500*time.Millisecond):
		}
	}
}

func (r *relay) accept() {
	for {
		s, err := r.sess.AcceptStream()
		if err != nil {
			r.die(fmt.Errorf("link closed: %w", err))
			return
		}
		go r.stream(s)
	}
}

// testHook, when set by a test, runs after a stream's header was read.
var testHook atomic.Pointer[func(h openHeader)]

// stream serves one stream. A panic in it ends only this stream.
func (r *relay) stream(s net.Conn) {
	var kind string
	defer func() {
		if p := recover(); p != nil {
			r.log.Printf("stream %s: panic: %v\n%s", kind, p, debug.Stack())
			_ = s.Close()
		}
	}()
	defer s.Close()
	var h openHeader
	_ = s.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err := readJSONFrame(s, &h); err != nil {
		return
	}
	_ = s.SetReadDeadline(time.Time{})
	kind = h.Kind
	if hook := testHook.Load(); hook != nil {
		(*hook)(h)
	}
	r.count(kind, 1)
	defer r.count(kind, -1)
	switch kind {
	case kindControl:
		r.serveControl(s)
	case kindHold:
		r.serveHold(s, h)
	case kindExec:
		r.serveExec(s, h)
	default:
		_ = writeJSONFrame(s, openAnswer{Error: "unknown stream kind " + strconv.Quote(kind)})
	}
}

func (r *relay) count(kind string, d int) {
	r.mu.Lock()
	r.counts[kind] += d
	if r.counts[kind] <= 0 {
		delete(r.counts, kind)
	}
	r.mu.Unlock()
}

func (r *relay) serveControl(s net.Conn) {
	r.mu.Lock()
	if r.ctl != nil {
		r.mu.Unlock()
		_ = writeJSONFrame(s, openAnswer{Error: "the link has its control stream"})
		return
	}
	r.ctl = newControl(s, r.o.Ping, r.o.Dead, r.handle)
	r.mu.Unlock()
	if err := writeJSONFrame(s, openAnswer{OK: true}); err != nil {
		r.die(err)
		return
	}
	close(r.ctlReady)
	r.ctl.run()
	r.die(r.ctl.err)
}

// handle answers the server's control requests.
func (r *relay) handle(m msg) msg {
	switch m.Op {
	case "bind":
		return reply(m, r.bind(m.Port))
	case "unbind":
		r.unbind(m.Port)
		r.savePorts()
		return reply(m, nil)
	case "sync":
		bound, failed := r.sync(m.Ports)
		rp := reply(m, nil)
		rp.Bound, rp.Failed = bound, failed
		return rp
	case "status":
		rp := reply(m, nil)
		rp.PID = os.Getpid()
		rp.Uptime = time.Since(r.started).Seconds()
		r.mu.Lock()
		rp.Streams = map[string]int{}
		for k, n := range r.counts {
			rp.Streams[k] = n
		}
		rp.Ports = r.portList()
		r.mu.Unlock()
		return rp
	}
	return reply(m, fmt.Errorf("unknown op %q", m.Op))
}

// portList: with r.mu held.
func (r *relay) portList() []int {
	ps := make([]int, 0, len(r.ports))
	for p := range r.ports {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	return ps
}

func (r *relay) savePorts() {
	if r.o.Test {
		return
	}
	r.mu.Lock()
	ps := r.portList()
	r.mu.Unlock()
	fenceUpdate(r.o.Home, r.o.Installation, r.o.Token, func(fe *Fence) { fe.Ports = ps })
}

func (r *relay) bind(port int) error {
	if err := r.listen(port); err != nil {
		return err
	}
	r.savePorts()
	return nil
}

// listen opens 127.0.0.1:port (nothing else is ever bound) unless it is open.
func (r *relay) listen(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("bad port %d", port)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("the relay is ending")
	}
	if _, ok := r.ports[port]; ok {
		return nil
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return errors.New("in use")
		}
		return err
	}
	pl := &portListener{port: port, l: l, conns: map[net.Conn]net.Conn{}}
	r.ports[port] = pl
	go r.acceptTools(pl)
	return nil
}

func (r *relay) unbind(port int) {
	r.mu.Lock()
	pl := r.ports[port]
	delete(r.ports, port)
	r.mu.Unlock()
	if pl != nil {
		pl.close()
	}
}

func (r *relay) sync(want []int) ([]int, map[int]string) {
	r.mu.Lock()
	var drop []int
	for p := range r.ports {
		if !slices.Contains(want, p) {
			drop = append(drop, p)
		}
	}
	r.mu.Unlock()
	for _, p := range drop {
		r.unbind(p)
	}
	bound := []int{}
	failed := map[int]string{}
	for _, p := range want {
		if err := r.listen(p); err != nil {
			failed[p] = err.Error()
		} else if !slices.Contains(bound, p) {
			bound = append(bound, p)
		}
	}
	r.savePorts()
	if len(failed) == 0 {
		failed = nil
	}
	return bound, failed
}

// close ends the listener and every connection it accepted (with its stream).
func (pl *portListener) close() {
	_ = pl.l.Close()
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for c, s := range pl.conns {
		_ = c.Close()
		if s != nil {
			_ = s.Close()
		}
	}
	pl.conns = map[net.Conn]net.Conn{}
}

func (r *relay) acceptTools(pl *portListener) {
	for {
		c, err := pl.l.Accept()
		if err != nil {
			return
		}
		go r.toolsConn(pl, c)
	}
}

// toolsConn carries one connection a CLI made to its tools port to the
// server, as a tools stream.
func (r *relay) toolsConn(pl *portListener, c net.Conn) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Printf("tools %d: panic: %v\n%s", pl.port, p, debug.Stack())
			_ = c.Close()
		}
	}()
	pl.mu.Lock()
	if pl.conns == nil {
		pl.mu.Unlock()
		c.Close()
		return
	}
	pl.conns[c] = nil
	pl.mu.Unlock()
	forget := func(s net.Conn) {
		pl.mu.Lock()
		delete(pl.conns, c)
		pl.mu.Unlock()
		_ = c.Close()
		if s != nil {
			_ = s.Close()
		}
	}
	s, err := r.sess.OpenStream()
	if err != nil {
		forget(nil)
		return
	}
	pl.mu.Lock()
	if _, ok := pl.conns[c]; !ok { // unbound meanwhile
		pl.mu.Unlock()
		forget(s)
		return
	}
	pl.conns[c] = s
	pl.mu.Unlock()
	r.count(kindTools, 1)
	defer r.count(kindTools, -1)
	var ans openAnswer
	_ = s.SetReadDeadline(time.Now().Add(30 * time.Second))
	if writeJSONFrame(s, openHeader{Kind: kindTools, Port: pl.port}) != nil || readJSONFrame(s, &ans) != nil || !ans.OK {
		forget(s)
		return
	}
	_ = s.SetReadDeadline(time.Time{})
	splice(s, c)
	forget(s)
}

// serveHold connects the stream to the slot's holder socket; the server
// speaks the holder protocol through it.
func (r *relay) serveHold(s net.Conn, h openHeader) {
	dir, err := hold.Dir(h.Slot)
	if err != nil {
		_ = writeJSONFrame(s, openAnswer{Error: err.Error()})
		return
	}
	c, err := net.DialTimeout("unix", filepath.Join(dir, "s"), 3*time.Second)
	if err != nil {
		msg := "could not reach the holder of slot " + h.Slot + ": " + err.Error()
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			msg = noHolderMsg + h.Slot + ": " + err.Error()
		}
		_ = writeJSONFrame(s, openAnswer{Error: msg})
		return
	}
	if err := writeJSONFrame(s, openAnswer{OK: true}); err != nil {
		c.Close()
		return
	}
	splice(s, c)
}

// noHolderMsg starts a hold stream's refusal when nothing listens for the
// slot (the client maps it to hold.ErrNoHolder).
const noHolderMsg = "no holder for slot "

// splice copies both ways until either side ends, then closes both: the
// holder protocol has no half-close.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		_, _ = io.CopyBuffer(dst, src, buf)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	// wake the other copy if it waits on a read that will not come
	_ = a.SetReadDeadline(time.Now())
	_ = b.SetReadDeadline(time.Now())
	<-done
}
