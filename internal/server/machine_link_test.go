package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// ---------- fakes ----------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) } // loops are off
// advance moves the clock and renews the lease, as the lease loop would.
func (w *linkWorld) advance(d time.Duration) {
	w.clock.add(d)
	w.m.lease.renew(context.Background())
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeConn is a relay: env answers, other scripts answer by exec.
type fakeConn struct {
	hello  linkHello
	done   chan struct{}
	once   sync.Once
	err    error
	rtt    time.Duration
	exec   func(req linkExecRequest) (linkExecResult, error)
	execs  atomic.Int64
	closed atomic.Bool
	pmu    sync.Mutex
	ports  map[int]bool
}

func newFakeConn(test bool) *fakeConn {
	return &fakeConn{hello: linkHello{PID: 4242, Version: Version, OS: "linux", Arch: "amd64", Test: test, Sum: "abc"}, done: make(chan struct{}), rtt: 7 * time.Millisecond}
}

func (c *fakeConn) Hello() linkHello { return c.hello }
func (c *fakeConn) Exec(_ context.Context, req linkExecRequest) (linkExecResult, error) {
	c.execs.Add(1)
	if c.exec != nil {
		return c.exec(req)
	}
	if req.Stdout == nil {
		req.Stdout = io.Discard
	}
	if strings.HasSuffix(req.Script, "\nenv") {
		_, _ = io.WriteString(req.Stdout, envOverLink)
		return linkExecResult{Started: true}, nil
	}
	_, _ = io.WriteString(req.Stdout, "via-link\n")
	return linkExecResult{Started: true}, nil
}
func (c *fakeConn) Status(context.Context) (relayStatus, error) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	st := relayStatus{PID: 4242}
	for p := range c.ports {
		st.Ports = append(st.Ports, p)
	}
	return st, nil
}
func (c *fakeConn) RTT() time.Duration    { return c.rtt }
func (c *fakeConn) Done() <-chan struct{} { return c.done }
func (c *fakeConn) Err() error            { return c.err }
func (c *fakeConn) Close() error {
	c.closed.Store(true)
	c.once.Do(func() { close(c.done) })
	return nil
}
func (c *fakeConn) kill(err error) {
	c.err = err
	c.once.Do(func() { close(c.done) })
}
func (c *fakeConn) OpenHold(context.Context, string) (net.Conn, error) {
	return nil, errors.New("fake relay: no holders")
}
func (c *fakeConn) Bind(_ context.Context, p int) error {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if c.ports == nil {
		c.ports = map[int]bool{}
	}
	c.ports[p] = true
	return nil
}
func (c *fakeConn) Unbind(_ context.Context, p int) error {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	delete(c.ports, p)
	return nil
}
func (c *fakeConn) Sync(_ context.Context, ps []int) ([]int, map[int]string, error) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.ports = map[int]bool{}
	for _, p := range ps {
		c.ports[p] = true
	}
	return ps, nil, nil
}

const (
	envOverSSH  = "HOME=/home/me\nPATH=/home/me/.local/bin:/usr/bin:/bin\nLANG=C.UTF-8\nSSH_CONNECTION=10.0.0.1 50000 10.0.0.2 22\nSHLVL=1\nPWD=/home/me\n_=/usr/bin/env\n"
	envOverLink = "HOME=/home/me\nPATH=/home/me/.local/bin:/usr/bin:/bin\nLANG=C.UTF-8\nSSH_CONNECTION=10.0.0.1 50001 10.0.0.2 22\nSHLVL=2\nPWD=/home/me\n_=/usr/bin/env\n"
)

// linkWorld is a link manager on a real database with a fake machine.
type linkWorld struct {
	t     *testing.T
	st    *store.Store
	m     *linkManager
	clock *fakeClock
	host  *store.Host

	mu       sync.Mutex
	dials    []relayDialOptions
	conns    []*fakeConn
	dialFn   func(o relayDialOptions) (linkConn, error)
	plains   []string // script\x00args
	plainFn  func(script string, args []string) (string, string, error)
	sshFails bool // plain ssh does not work either
}

func newLinkWorld(t *testing.T, st *store.Store) *linkWorld {
	t.Helper()
	if st == nil {
		var err error
		st, err = store.Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
	}
	w := &linkWorld{t: t, st: st, clock: &fakeClock{t: time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC)}}
	w.m = newLinkManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.m.clock, w.m.loops, w.m.off, w.m.installation = w.clock, false, false, "inst-1"
	w.wire()
	w.host = &store.Host{Name: "box", Target: "me@box", Info: store.HostInfo{OK: true, OS: "Linux x86_64"}}
	if err := st.CreateHost(context.Background(), w.host); err != nil {
		t.Fatal(err)
	}
	if !w.m.lease.renew(context.Background()) {
		t.Fatal("no lease")
	}
	t.Cleanup(func() {
		w.m.mu.Lock()
		for id := range w.m.links {
			linkRegistry.Delete(id)
		}
		w.m.mu.Unlock()
	})
	return w
}

// wire puts the fake machine under w.m.
func (w *linkWorld) wire() {
	w.m.install = func(context.Context, *store.Host) (string, error) {
		return "/home/me/.pocket-ai/bin/pocket-hold-abc", nil
	}
	w.m.support = func(*store.Host) (bool, string) { return true, "" }
	w.m.relayCmd = func(ctx context.Context, h *store.Host, script string, args ...string) (*exec.Cmd, error) {
		return exec.Command("true"), nil // never started: the fake dial stands in for the relay
	}
	w.m.plain = func(_ context.Context, _ *store.Host, script string, args ...string) (string, string, error) {
		w.mu.Lock()
		w.plains = append(w.plains, script+"\x00"+strings.Join(args, " "))
		fn, fails := w.plainFn, w.sshFails
		w.mu.Unlock()
		if fails {
			return "", "ssh: connect to host box port 22: Connection refused", errors.New("exit status 255")
		}
		if fn != nil {
			if out, errOut, err := fn(script, args); out != "" || errOut != "" || err != nil {
				return out, errOut, err
			}
		}
		switch script {
		case "echo pocket-ok":
			return "pocket-ok\n", "", nil
		case "env":
			return envOverSSH, "", nil
		}
		return "", "", nil
	}
	w.m.dial = func(_ context.Context, _ *exec.Cmd, o relayDialOptions) (linkConn, error) {
		w.mu.Lock()
		w.dials = append(w.dials, o)
		fn := w.dialFn
		w.mu.Unlock()
		if fn != nil {
			return fn(o)
		}
		c := newFakeConn(o.Test)
		w.mu.Lock()
		w.conns = append(w.conns, c)
		w.mu.Unlock()
		return c, nil
	}
}

// link is the machine's link with mode stored first.
func (w *linkWorld) link(mode string) *machineLink {
	w.t.Helper()
	ctx := context.Background()
	if err := w.st.SetHostLinkMode(ctx, w.host.ID, mode); err != nil {
		w.t.Fatal(err)
	}
	l, err := w.m.get(ctx, w.host.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return l
}

func (w *linkWorld) dialCount(test bool) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, d := range w.dials {
		if d.Test == test {
			n++
		}
	}
	return n
}

// fences lists the fenced actions run over plain ssh ("end-relay 3", "off 4").
func (w *linkWorld) fences() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.plains {
		script, args, _ := strings.Cut(p, "\x00")
		if script == fenceScript {
			f := strings.Fields(args) // bin inst entry gen action …
			out = append(out, f[4]+" "+f[3])
		}
	}
	return out
}

func (l *machineLink) snapshot() (state string, connected bool, fb *store.LinkFallback, taken string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state, l.conn != nil, l.fallback, l.takenOver
}

// ---------- the state machine ----------

// However many callers ask at once, a machine's link is started once.
func TestLinkStartIsSerialised(t *testing.T) {
	w := newLinkWorld(t, nil)
	var inDial atomic.Int32
	w.dialFn = func(o relayDialOptions) (linkConn, error) {
		if inDial.Add(1) > 1 {
			t.Error("two starts at once")
		}
		time.Sleep(30 * time.Millisecond)
		inDial.Add(-1)
		return newFakeConn(false), nil
	}
	l := w.link(store.LinkStatus)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := l.ensure(context.Background()); got != tryUp {
				t.Errorf("ensure: %v", got)
			}
		}()
	}
	wg.Wait()
	if n := w.dialCount(false); n != 1 {
		t.Fatalf("%d starts", n)
	}
	if st, up, _, _ := l.snapshot(); st != linkConnected || !up {
		t.Fatalf("state %s up %v", st, up)
	}
	if g := w.dials[0].Generation; g != 1 || w.dials[0].Installation != "inst-1" || w.dials[0].Entry != w.host.ID || w.dials[0].Token == "" {
		t.Fatalf("dial options %+v", w.dials[0])
	}
}

// One failed start is a reconnect; three in a row over at least 5 minutes
// (and plain ssh working) are one fallback.
func TestLinkFallbackAfterThreeFailuresOverFiveMinutes(t *testing.T) {
	w := newLinkWorld(t, nil)
	w.dialFn = func(relayDialOptions) (linkConn, error) { return nil, errors.New("ssh: Connection reset by peer") }
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	steps := []time.Duration{0, time.Minute, time.Minute, time.Minute} // 3 failures in 2 min, a 4th at 3 min
	for i, d := range steps {
		w.advance(d)
		if got := l.ensure(ctx); got != tryAgain {
			t.Fatalf("failure %d: %v", i+1, got)
		}
		if _, _, fb, _ := l.snapshot(); fb != nil {
			t.Fatalf("fell back after %d failures within %s", i+1, time.Duration(i)*time.Minute)
		}
	}
	w.advance(2 * time.Minute) // the run of failures spans 5 minutes now
	if got := l.ensure(ctx); got != tryStop {
		t.Fatalf("5 minutes of failures: %v", got)
	}
	st, _, fb, _ := l.snapshot()
	if st != linkFallenBack || fb == nil || !strings.Contains(fb.Why, "5 times in a row over 5m") {
		t.Fatalf("state %s fallback %+v", st, fb)
	}
	h, _ := w.st.GetHost(ctx, w.host.ID)
	if h.LinkFallback == nil || h.LinkMode != store.LinkStatus {
		t.Fatalf("stored: %q %+v", h.LinkMode, h.LinkFallback)
	}
	// sticky: no more starts, and exactly one fallback
	w.advance(time.Hour)
	for i := 0; i < 3; i++ {
		if got := l.ensure(ctx); got != tryStop {
			t.Fatalf("after fallback: %v", got)
		}
	}
	if n := w.dialCount(false); n != 5 {
		t.Fatalf("%d starts", n)
	}
	if f := w.fences(); len(f) != 1 || !strings.HasPrefix(f[0], "end-relay ") {
		t.Fatalf("fenced actions %v", f)
	}
	if l.UseFor(agent.LinkStatus) {
		t.Fatal("a fallen-back link carries commands")
	}
}

// Exactly three failed starts spread over 5 minutes: one fallback, on the third.
func TestLinkFallbackOnTheThirdFailure(t *testing.T) {
	w := newLinkWorld(t, nil)
	w.dialFn = func(relayDialOptions) (linkConn, error) { return nil, errors.New("ssh: Connection timed out") }
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	for i, want := range []linkTry{tryAgain, tryAgain, tryStop} {
		if got := l.ensure(ctx); got != want {
			t.Fatalf("failure %d: %v", i+1, got)
		}
		w.advance(150 * time.Second)
	}
	if f := w.fences(); len(f) != 1 {
		t.Fatalf("fallbacks: %v", f)
	}
	if st, _, fb, _ := l.snapshot(); st != linkFallenBack || fb == nil {
		t.Fatalf("state %s", st)
	}
}

// Without plain ssh there is nothing to fall back to: the link keeps trying.
func TestLinkNoFallbackWithoutSSH(t *testing.T) {
	w := newLinkWorld(t, nil)
	w.dialFn = func(relayDialOptions) (linkConn, error) { return nil, errors.New("ssh: No route to host") }
	w.sshFails = true
	l := w.link(store.LinkStatus)
	for i := 0; i < 6; i++ {
		if got := l.ensure(context.Background()); got != tryAgain {
			t.Fatalf("try %d: %v", i, got)
		}
		w.advance(2 * time.Minute)
	}
	if _, _, fb, _ := l.snapshot(); fb != nil {
		t.Fatal("fell back with ssh down too")
	}
}

// A handshake or version mismatch will not fix itself: fallback at once.
func TestLinkFallbackAtOnceOnHandshake(t *testing.T) {
	w := newLinkWorld(t, nil)
	w.dialFn = func(relayDialOptions) (linkConn, error) {
		return nil, &relayHandshakeError{Msg: "unexpected output before the link started: Welcome to box"}
	}
	l := w.link(store.LinkStatus)
	if got := l.ensure(context.Background()); got != tryStop {
		t.Fatalf("ensure %v", got)
	}
	if st, _, fb, _ := l.snapshot(); st != linkFallenBack || fb == nil || !strings.Contains(fb.Why, "Welcome to box") {
		t.Fatalf("state %s %+v", st, fb)
	}
	if n := w.dialCount(false); n != 1 {
		t.Fatalf("%d starts", n)
	}
}

// Back from a fallback only after two passing full tests in a row.
func TestLinkBackAfterTwoPassingProbes(t *testing.T) {
	w := newLinkWorld(t, nil)
	broken := true
	w.dialFn = func(o relayDialOptions) (linkConn, error) {
		if broken {
			return nil, &relayHandshakeError{Msg: "relay protocol 2, this Pocket AI speaks 1"}
		}
		return newFakeConn(o.Test), nil
	}
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	l.ensure(ctx)
	if _, _, fb, _ := l.snapshot(); fb == nil {
		t.Fatal("no fallback")
	}
	// still broken: the probe fails
	if back, err := l.probe(ctx); back || err == nil {
		t.Fatalf("broken probe: %v %v", back, err)
	}
	broken = false
	if back, err := l.probe(ctx); back || err != nil {
		t.Fatalf("first pass moved it back: %v %v", back, err)
	}
	// a failure in between starts the count again
	broken = true
	_, _ = l.probe(ctx)
	broken = false
	if back, _ := l.probe(ctx); back {
		t.Fatal("back after a failure and one pass")
	}
	if back, err := l.probe(ctx); !back || err != nil {
		t.Fatalf("two passes in a row: %v %v", back, err)
	}
	if h, _ := w.st.GetHost(ctx, w.host.ID); h.LinkFallback != nil {
		t.Fatal("fallback still stored")
	}
	if got := l.ensure(ctx); got != tryUp {
		t.Fatalf("after coming back: %v", got)
	}
	// every probe's candidate was a test-mode relay, and exited
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, d := range w.dials {
		if i > 0 && i < len(w.dials)-1 && !d.Test {
			t.Fatalf("probe %d was not a test relay", i)
		}
	}
}

// A relay refused for a newer generation or another entry: this process
// never reconnects to that machine on its own.
func TestLinkRefusalStopsReconnecting(t *testing.T) {
	for _, reason := range []string{"newer", "other-entry", "off"} {
		t.Run(reason, func(t *testing.T) {
			w := newLinkWorld(t, nil)
			other := &store.Host{Name: "box (old entry)", Target: "me@box.lan"}
			_ = w.st.CreateHost(context.Background(), other)
			w.dialFn = func(relayDialOptions) (linkConn, error) {
				return nil, &relayRefused{Reason: reason, Entry: other.ID, Generation: 99}
			}
			l := w.link(store.LinkStatus)
			for i := 0; i < 3; i++ {
				if got := l.ensure(context.Background()); got != tryStop {
					t.Fatalf("ensure %d: %v", i, got)
				}
			}
			st, _, fb, taken := l.snapshot()
			if st != linkTakenOver || taken == "" || fb != nil {
				t.Fatalf("state %s taken %q fallback %+v", st, taken, fb)
			}
			if reason == "other-entry" && !strings.Contains(taken, "box (old entry)") {
				t.Fatalf("the card does not name the owning entry: %q", taken)
			}
			if n := w.dialCount(false); n != 1 {
				t.Fatalf("%d starts", n)
			}
			if len(w.fences()) != 0 {
				t.Fatal("a refused machine was fenced")
			}
		})
	}
}

// A dropped link reconnects (a new generation); "busy" is an ordinary failure.
func TestLinkReconnectsWithNewGeneration(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	l.ensure(ctx)
	w.conns[0].kill(errors.New("no answer to pings for 30s"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st, up, _, _ := l.snapshot(); !up && st == linkReconnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the drop was not noticed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l.view(ctx, w.host).LastError == "" {
		t.Fatal("no last error on the card")
	}
	w.dialFn = func(relayDialOptions) (linkConn, error) { return nil, &relayRefused{Reason: "busy"} }
	if got := l.ensure(ctx); got != tryAgain {
		t.Fatalf("busy: %v", got)
	}
	w.dialFn = nil
	if got := l.ensure(ctx); got != tryUp {
		t.Fatalf("reconnect: %v", got)
	}
	if g := w.dials[len(w.dials)-1].Generation; g != 3 {
		t.Fatalf("generation %d", g)
	}
}

// What goes over the link, and how its errors come back (at most once).
func TestLinkExecRouting(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	if l.UseFor(agent.LinkStatus) {
		t.Fatal("UseFor before it is up")
	}
	l.ensure(ctx)
	if !l.UseFor(agent.LinkStatus) || l.UseFor(agent.LinkCommands) || l.UseFor(agent.LinkAgents) {
		t.Fatal("status mode carries status scripts only")
	}
	l.mu.Lock()
	l.mode = store.LinkAgents
	l.mu.Unlock()
	if !l.UseFor(agent.LinkStatus) || !l.UseFor(agent.LinkCommands) || !l.UseFor(agent.LinkAgents) {
		t.Fatal("agents mode: agents and every command")
	}
	l.mu.Lock()
	l.mode = store.LinkStatus
	l.mu.Unlock()

	c := w.conns[0]
	for _, tc := range []struct {
		err  error
		want error
	}{{errRelayNotSent, agent.ErrLinkNotSent}, {errRelayUnknown, agent.ErrLinkUnknown}} {
		c.exec = func(linkExecRequest) (linkExecResult, error) { return linkExecResult{}, tc.err }
		if _, err := l.Exec(ctx, "true", nil, nil, io.Discard, io.Discard); !errors.Is(err, tc.want) {
			t.Fatalf("%v → %v", tc.err, err)
		}
	}
	c.exec = func(linkExecRequest) (linkExecResult, error) { return linkExecResult{Code: 2}, nil }
	if code, err := l.Exec(ctx, "exit 2", nil, nil, io.Discard, io.Discard); code != 2 || err != nil {
		t.Fatalf("exit code %d %v", code, err)
	}

	// through remoteOf: status scripts over the link, the rest and every
	// PlainSSH call over ssh, and an unknown result is never run twice
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	sh := "#!/bin/sh\necho x >> " + calls + "\necho via-ssh\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(sh), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sshCalls := func() int { b, _ := os.ReadFile(calls); return strings.Count(string(b), "x") }
	c.exec = nil
	h, _ := w.st.GetHost(ctx, w.host.ID)
	run := func(ctx context.Context) string {
		out, _, _ := remoteOf(h).Run(ctx, 5*time.Second, "echo hi")
		return strings.TrimSpace(out)
	}
	if got := run(agent.StatusScript(ctx)); got != "via-link" {
		t.Fatalf("status script: %q", got)
	}
	if got := run(ctx); got != "via-ssh" {
		t.Fatalf("plain command in status mode: %q", got)
	}
	if got := run(agent.PlainSSH(agent.StatusScript(ctx))); got != "via-ssh" {
		t.Fatalf("PlainSSH: %q", got)
	}
	before := c.execs.Load()
	c.exec = func(linkExecRequest) (linkExecResult, error) { return linkExecResult{}, errRelayUnknown }
	l.mu.Lock()
	l.mode = store.LinkAgents
	l.mu.Unlock()
	n := sshCalls()
	if _, _, err := remoteOf(h).Run(ctx, 5*time.Second, "touch /tmp/x"); err == nil || !strings.Contains(err.Error(), "the result is unknown: the link to me@box dropped while it ran") {
		t.Fatalf("unknown: %v", err)
	}
	if sshCalls() != n || c.execs.Load() != before+1 {
		t.Fatal("a command with an unknown result ran again")
	}
	if got := run(agent.Repeatable(ctx)); got != "via-ssh" || sshCalls() != n+1 {
		t.Fatalf("repeatable after unknown: %q", got)
	}
}

// Switch back advances the fence ("off", a new generation) and leaves the
// machine on ssh; the emergency switch advances it too, keeping the mode.
func TestLinkSwitchBackAndEmergencyOff(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkStatus)
	ctx := context.Background()
	l.ensure(ctx)
	c := w.conns[0]
	if err := l.switchBack(ctx); err != nil {
		t.Fatal(err)
	}
	if !c.closed.Load() {
		t.Fatal("the relay is still open")
	}
	if f := w.fences(); len(f) != 1 || f[0] != "off 2" {
		t.Fatalf("fenced actions %v", f)
	}
	if h, _ := w.st.GetHost(ctx, w.host.ID); h.LinkMode != store.LinkOff {
		t.Fatalf("mode %q", h.LinkMode)
	}
	if machineLinkOf(w.host.ID) != nil || l.UseFor(agent.LinkStatus) {
		t.Fatal("still attached to the machine")
	}
	if got := l.ensure(ctx); got != tryStop {
		t.Fatalf("ensure after switch back: %v", got)
	}

	w2 := newLinkWorld(t, nil)
	l2 := w2.link(store.LinkStatus)
	l2.ensure(ctx)
	w2.m.off = true
	if err := l2.emergencyOff(ctx); err != nil {
		t.Fatal(err)
	}
	if f := w2.fences(); len(f) != 1 || f[0] != "off 2" {
		t.Fatalf("emergency fence %v", f)
	}
	if st, up, _, _ := l2.snapshot(); st != linkEmergencyOff || up || l2.UseFor(agent.LinkStatus) {
		t.Fatalf("state %s up %v", st, up)
	}
	if h, _ := w2.st.GetHost(ctx, w2.host.ID); h.LinkMode != store.LinkStatus {
		t.Fatalf("the emergency switch changed the stored mode: %q", h.LinkMode)
	}
	if got := l2.ensure(ctx); got != tryStop {
		t.Fatalf("ensure with POCKET_LINK=off: %v", got)
	}
}

func TestEnvDiff(t *testing.T) {
	if d := envDiff(envOverLink, envOverSSH); len(d) != 0 {
		t.Fatalf("expected differences reported: %v", d)
	}
	got := envDiff(envOverLink+"EXTRA=1\nMULTI=a\nb\n", envOverSSH+"LANG2=x\nMULTI=a\nc\n")
	want := []string{"EXTRA (only over the link)", "LANG2 (only over ssh)", "MULTI (differs)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v", got)
	}
	for _, d := range got {
		if strings.Contains(d, "=") {
			t.Fatalf("a value leaked: %q", d)
		}
	}
}

// ---------- the controller lease ----------

// Two processes of one installation on one database: only the holder
// allocates; the old one stops its links when it loses the lease; the lease
// goes over on release and on expiry.
func TestControllerLeaseTwoProcesses(t *testing.T) {
	a := newLinkWorld(t, nil)
	b := &linkWorld{t: t, st: a.st, clock: a.clock}
	b.m = newLinkManager(a.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.m.clock, b.m.loops, b.m.off = a.clock, false, false
	b.m.install, b.m.support, b.m.relayCmd, b.m.plain, b.m.dial = a.m.install, a.m.support, a.m.relayCmd, a.m.plain, a.m.dial
	ctx := context.Background()

	if b.m.lease.renew(ctx) {
		t.Fatal("b took a held lease")
	}
	if _, err := b.m.lease.allocate(ctx, a.host.ID); !errors.Is(err, store.ErrNotLeaseHolder) {
		t.Fatalf("b allocated: %v", err)
	}
	la := a.link(store.LinkStatus)
	if la.ensure(ctx) != tryUp {
		t.Fatal("a could not start")
	}
	lb, _ := b.m.get(ctx, a.host.ID)
	if got := lb.ensure(ctx); got != tryStop {
		t.Fatalf("b started a link without the lease: %v", got)
	}

	// release (a deploy): a's links stop at once, b takes over
	a.m.lease.release(ctx)
	if st, up, _, _ := la.snapshot(); up || st != linkStopped {
		t.Fatalf("a after release: %s %v", st, up)
	}
	if !a.conns[0].closed.Load() {
		t.Fatal("a's relay is still open")
	}
	if !b.m.lease.renew(ctx) {
		t.Fatal("b could not take a released lease")
	}
	if got := lb.ensure(ctx); got != tryUp {
		t.Fatalf("b: %v", got)
	}
	if g := a.dials[len(a.dials)-1].Generation; g != 2 {
		t.Fatalf("b's generation %d", g)
	}
	if got := la.ensure(ctx); got != tryStop {
		t.Fatalf("a restarted without the lease: %v", got)
	}

	// expiry: b stops renewing (hung), a takes it after 30 s; b notices
	// its own lease ran out, and stops its links when it tries to renew
	a.clock.add(20 * time.Second)
	if a.m.lease.renew(ctx) {
		t.Fatal("a took it before it ran out")
	}
	a.clock.add(11 * time.Second)
	if b.m.lease.Held() {
		t.Fatal("b thinks it still holds an expired lease")
	}
	if !a.m.lease.renew(ctx) {
		t.Fatal("a could not take an expired lease")
	}
	b.m.lease.expire() // its watchdog: the lease ran out without a renewal
	if st, up, _, _ := lb.snapshot(); up || st != linkStopped {
		t.Fatalf("b after losing: %s %v", st, up)
	}
	if _, err := b.m.lease.allocate(ctx, a.host.ID); !errors.Is(err, store.ErrNotLeaseHolder) {
		t.Fatalf("stale b allocated: %v", err)
	}
	if _, err := a.m.lease.allocate(ctx, a.host.ID); err != nil {
		t.Fatalf("a: %v", err)
	}
}

// ---------- the button, through the API ----------

func linkAPIWorld(t *testing.T) (*Server, *client, *linkWorld) {
	t.Helper()
	s, h, _ := testServer(t)
	t.Cleanup(s.CloseLinks)
	admin, _ := person(t, s, h, "owner@example.com", "admin", true)
	w := &linkWorld{t: t, st: s.store, m: s.links}
	w.wire()
	w.host = &store.Host{Name: "box", Target: "me@box", Info: store.HostInfo{OK: true, OS: "Linux x86_64"}}
	if err := s.store.CreateHost(context.Background(), w.host); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !s.links.lease.Held() {
		if time.Now().After(deadline) {
			t.Fatal("the server did not take the controller lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s, admin, w
}

func linkURL(id int64) string { return "/api/v1/hosts/" + itoa(id) + "/link" }

// waitLink polls the card until ok says so.
func waitLink(t *testing.T, c *client, id int64, ok func(v linkView) bool) linkView {
	t.Helper()
	var v linkView
	c.want("GET", linkURL(id), nil, 200, &v)
	deadline := time.Now().Add(10 * time.Second)
	for !ok(v) {
		if time.Now().After(deadline) {
			t.Fatalf("the link never got there: %+v (flow %+v)", v, v.Flow)
		}
		since := v.Version
		v = linkView{}
		c.want("GET", linkURL(id)+"?wait=1&since="+itoa(since), nil, 200, &v)
	}
	return v
}

func flowAt(state string) func(linkView) bool {
	return func(v linkView) bool { return v.Flow != nil && v.Flow.State == state }
}

func TestUseOneConnectionCancelAtConfirm(t *testing.T) {
	s, admin, w := linkAPIWorld(t)
	id := w.host.ID
	admin.want("POST", linkURL(id), map[string]any{"mode": "status"}, 202, nil)
	v := waitLink(t, admin, id, flowAt("confirm"))
	for i, want := range []string{"done", "done", "done", "running", "waiting"} {
		if v.Flow.Steps[i].State != want {
			t.Fatalf("step %s: %s (want %s)", v.Flow.Steps[i].ID, v.Flow.Steps[i].State, want)
		}
	}
	if !strings.Contains(v.Flow.Steps[2].Detail, "round trip") || v.Flow.Confirm == "" {
		t.Fatalf("test step %+v confirm %q", v.Flow.Steps[2], v.Flow.Confirm)
	}
	// only a test-mode candidate ran, and it has exited; the fence was not touched
	if w.dialCount(true) != 1 || w.dialCount(false) != 0 || !w.conns[0].closed.Load() || len(w.fences()) != 0 {
		t.Fatalf("dials test=%d run=%d closed=%v fences=%v", w.dialCount(true), w.dialCount(false), w.conns[0].closed.Load(), w.fences())
	}
	v = linkView{}
	admin.want("POST", linkURL(id), map[string]any{"cancel": true}, 200, &v)
	if v.Flow != nil || v.Mode != store.LinkOff || v.Connected {
		t.Fatalf("after cancel: %+v", v)
	}
	if h, _ := s.store.GetHost(context.Background(), id); h.LinkMode != store.LinkOff {
		t.Fatalf("mode %q", h.LinkMode)
	}
	if len(w.fences()) != 0 || w.dialCount(false) != 0 || machineLinkOf(id) != nil {
		t.Fatal("cancel left something behind")
	}
	// a confirm after the cancel has nothing to confirm
	admin.want("POST", linkURL(id), map[string]any{"mode": "status", "confirm": true}, 400, nil)
}

func TestUseOneConnectionFailingTest(t *testing.T) {
	s, admin, w := linkAPIWorld(t)
	id := w.host.ID
	w.plainFn = func(script string, _ []string) (string, string, error) {
		if script == "env" {
			return envOverSSH + "SECRET_TOKEN=abc123\n", "", nil
		}
		return "", "", nil
	}
	admin.want("POST", linkURL(id), map[string]any{"mode": "status"}, 202, nil)
	v := waitLink(t, admin, id, flowAt("failed"))
	test := v.Flow.Steps[2]
	if test.ID != "test" || test.State != "failed" || !strings.Contains(test.Error, "SECRET_TOKEN") || strings.Contains(test.Error, "abc123") {
		t.Fatalf("test step %+v", test)
	}
	if v.Flow.Steps[3].State != "skipped" || v.Flow.Steps[4].State != "skipped" {
		t.Fatalf("later steps %+v", v.Flow.Steps)
	}
	if h, _ := s.store.GetHost(context.Background(), id); h.LinkMode != store.LinkOff || v.Mode != store.LinkOff {
		t.Fatal("a failed test changed the mode")
	}
	if !w.conns[0].closed.Load() || len(w.fences()) != 0 {
		t.Fatal("the candidate was left running or the fence touched")
	}
	admin.want("POST", linkURL(id), map[string]any{"mode": "status", "confirm": true}, 400, nil)
}

func TestUseOneConnectionSwitchAndBack(t *testing.T) {
	s, admin, w := linkAPIWorld(t)
	id := w.host.ID
	ctx := context.Background()
	// who may press it: the machine's editor only
	other, _ := person(t, s, admin.h, "member@example.com", "member", true)
	if code, _ := other.call("POST", linkURL(id), map[string]any{"mode": "status"}); code < 400 {
		t.Fatalf("a member could switch the server's machine: %d", code)
	}
	admin.want("POST", linkURL(id), map[string]any{"mode": "status"}, 202, nil)
	waitLink(t, admin, id, flowAt("confirm"))
	admin.want("POST", linkURL(id), map[string]any{"mode": "agents", "confirm": true}, 400, nil)
	var v linkView
	admin.want("POST", linkURL(id), map[string]any{"mode": "status", "confirm": true}, 200, &v)
	if v.Mode != store.LinkStatus || v.Flow == nil || v.Flow.State != "done" {
		t.Fatalf("after confirm: %+v", v)
	}
	v = waitLink(t, admin, id, func(v linkView) bool { return v.Connected })
	if v.State != linkConnected || v.RTTMs <= 0 || !v.MoveAgents {
		t.Fatalf("card: %+v", v)
	}
	if w.dialCount(false) != 1 || w.dials[len(w.dials)-1].Generation != 1 {
		t.Fatalf("run-mode starts %d", w.dialCount(false))
	}
	h, _ := s.store.GetHost(ctx, id)
	if h.LinkMode != store.LinkStatus {
		t.Fatalf("stored mode %q", h.LinkMode)
	}
	// the machine check now goes over the link
	if l := machineLinkOf(id); l == nil || !l.UseFor(agent.LinkStatus) {
		t.Fatal("status scripts do not use the link")
	}
	// Switch back
	v = linkView{}
	admin.want("DELETE", linkURL(id), nil, 200, &v)
	if v.Mode != store.LinkOff || v.Connected || v.State != linkOff {
		t.Fatalf("after switch back: %+v", v)
	}
	if f := w.fences(); len(f) != 1 || f[0] != "off 2" {
		t.Fatalf("fence %v", f)
	}
	if h, _ := s.store.GetHost(ctx, id); h.LinkMode != store.LinkOff || machineLinkOf(id) != nil {
		t.Fatal("still on the link")
	}
}

func TestUseOneConnectionUnsupported(t *testing.T) {
	_, admin, w := linkAPIWorld(t)
	w.m.support = func(h *store.Host) (bool, string) {
		return false, "There is no pocket-hold for " + h.Info.OS + " in this build."
	}
	_, b := admin.call("POST", linkURL(w.host.ID), map[string]any{"mode": "status"})
	if !strings.Contains(string(b), "no pocket-hold for Linux x86_64") {
		t.Fatalf("refusal %s", b)
	}
	var v linkView
	admin.want("GET", linkURL(w.host.ID), nil, 200, &v)
	if v.Supported || v.Unsupported == "" {
		t.Fatalf("card %+v", v)
	}
}
