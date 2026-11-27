package server

// The per-agent routes of the machine link against a fake relay
// (machine_link_test.go's linkWorld). The same gates over real ssh are in
// machine_link_e2e_test.go.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// fakeAgent is an agent on the fake machine; its Reroute runs its route
// once, as its next connection would.
type fakeAgent struct {
	a     *linkAgent
	l     *machineLink
	paths []agent.HoldPath
	err   error
}

// Reroute: the tests make each connection by hand (connect); a cutover in
// the background (converge) finds nothing to move.
func (f *fakeAgent) Reroute() bool { return false }

func (f *fakeAgent) connect() {
	p, err := f.l.agentPath(context.Background(), f.a, false)
	f.err = err
	if err == nil {
		f.paths = append(f.paths, p)
		if p.Done != nil {
			p.Done(nil)
		}
	}
}

func (w *linkWorld) agent(l *machineLink, id string, port int) *fakeAgent {
	a := &linkAgent{thread: id, slot: id, bin: "/home/me/.pocket-ai/bin/pocket-hold-abc", port: port, local: "127.0.0.1:9"}
	f := &fakeAgent{a: a, l: l}
	l.mu.Lock()
	l.agents[id] = a
	a.sess = f
	l.mu.Unlock()
	return f
}

func (w *linkWorld) route(id string) string {
	r, err := w.st.GetAgentRoute(context.Background(), id)
	if err != nil {
		return ""
	}
	return r.Route
}

func (w *linkWorld) conn() *fakeConn {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conns[len(w.conns)-1]
}

func (c *fakeConn) bound() []int {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	var ps []int
	for p := range c.ports {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	return ps
}

// fenceCalls lists the fenced actions with their arguments after the
// generation ("free-ports -slot a1 41000").
func (w *linkWorld) fenceCalls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.plains {
		script, args, _ := strings.Cut(p, "\x00")
		if script == fenceScript {
			f := strings.Fields(args)
			out = append(out, strings.Join(f[4:], " "))
		}
	}
	return out
}

func TestAgentRoutesMoveOverAndBack(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkAgents)
	if l.ensure(context.Background()) != tryUp {
		t.Fatal("not up")
	}
	a1 := w.agent(l, "a1", 41001)
	a2 := w.agent(l, "a2", 41002)
	ctx := context.Background()

	// a new connection on a machine that wants its agents on the link:
	// to-link, the port freed (fenced, with the slot), bind, link
	a1.connect()
	if a1.err != nil || !a1.paths[0].Link || w.route("a1") != store.RouteLink {
		t.Fatalf("a1: %v %+v %q", a1.err, a1.paths, w.route("a1"))
	}
	if !slices.Contains(w.fenceCalls(), "free-ports -slot a1 41001") || !slices.Equal(w.conn().bound(), []int{41001}) {
		t.Fatalf("fence %v, bound %v", w.fenceCalls(), w.conn().bound())
	}
	// its next connection: the port is the relay's already
	n := len(w.fenceCalls())
	a1.connect()
	if !a1.paths[1].Link || len(w.fenceCalls()) != n {
		t.Fatal("a reconnect freed or bound again")
	}

	// a bind that fails puts the agent back on its own connection, behind
	// the fence's check; the next agent still moves
	c := w.conn()
	c.pmu.Lock()
	c.ports = map[int]bool{41001: true}
	c.pmu.Unlock()
	failing := &failBind{fakeConn: c, port: 41002}
	l.mu.Lock()
	l.conn = failing
	l.mu.Unlock()
	a2.connect()
	if a2.err != nil || a2.paths[0].Link || w.route("a2") != store.RouteSSH || a2.paths[0].Guard == "" || a2.paths[0].Free == nil {
		t.Fatalf("a2: %v %+v %q", a2.err, a2.paths, w.route("a2"))
	}
	if !strings.Contains(a2.paths[0].Guard, "fence -installation inst-1 -entry") || !strings.Contains(a2.paths[0].Guard, " check") {
		t.Fatalf("guard %q", a2.paths[0].Guard)
	}
	l.mu.Lock()
	stay := a2.a.stay
	l.conn = c
	l.mu.Unlock()
	if stay == "" {
		t.Fatal("no reason kept")
	}

	// switch back, per agent: to-ssh, unbind, the port free, ssh
	l.mu.Lock()
	l.mode = store.LinkOff
	l.mu.Unlock()
	a1.connect()
	if a1.err != nil || a1.paths[len(a1.paths)-1].Link || w.route("a1") != store.RouteSSH || len(c.bound()) != 0 {
		t.Fatalf("a1 back: %v %+v %q %v", a1.err, a1.paths, w.route("a1"), c.bound())
	}
	if calls := w.fenceCalls(); calls[len(calls)-1] != "free-ports -slot a1 41001" {
		t.Fatalf("the port was not confirmed free: %v", calls)
	}
	_ = ctx
}

type failBind struct {
	*fakeConn
	port int
}

func (c *failBind) Bind(ctx context.Context, p int) error {
	if p == c.port {
		return errors.New("relay: no")
	}
	return c.fakeConn.Bind(ctx, p)
}

// An unrelated program on an agent's port: left alone, the agent goes on
// over the link without its tools, and its thread is told why.
func TestAgentRoutesUnrelatedProgram(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkAgents)
	l.ensure(context.Background())
	w.plainFn = func(script string, args []string) (string, string, error) {
		if script == fenceScript && slices.Contains(args, "free-ports") {
			return "", "pocket-hold fence: port 41003 is in use by python3 (pid 77), not by an agent's connection", errors.New("exit status 1")
		}
		return "", "", nil
	}
	f := w.agent(l, "a3", 41003)
	var told []string
	l.mu.Lock()
	f.a.notify = func(s string) { told = append(told, s) }
	l.mu.Unlock()
	f.connect()
	if f.err != nil || !f.paths[0].Link || len(w.conn().bound()) != 0 {
		t.Fatalf("%v %+v %v", f.err, f.paths, w.conn().bound())
	}
	if len(told) != 1 || !strings.Contains(told[0], "python3") {
		t.Fatalf("told %v", told)
	}
	f.connect() // tried again, not told again
	if len(told) != 1 {
		t.Fatalf("told twice: %v", told)
	}
}

// After every (re)connect, before anyone uses the link: moves are settled
// against what the relay holds, and sync lists only the relay's ports.
func TestAgentRoutesReconcileAndSync(t *testing.T) {
	w := newLinkWorld(t, nil)
	ctx := context.Background()
	l := w.link(store.LinkAgents)
	set := func(id string, port int, route string) {
		_ = w.st.SetAgentRoute(ctx, &store.AgentRoute{ThreadID: id, HostID: w.host.ID, Port: port, Route: route})
	}
	set("on", 41010, store.RouteLink)
	set("own", 41011, store.RouteSSH)
	set("half", 41012, store.RouteToLink)  // crashed before the bind: reversed
	set("bound", 41013, store.RouteToLink) // crashed after the bind: the relay has it
	set("back", 41014, store.RouteToSSH)   // switch back cut short, machine still wants the link
	c := newFakeConn(false)
	_ = c.Bind(ctx, 41013)
	_ = c.Bind(ctx, 41014)
	_ = c.Bind(ctx, 41099) // nobody's: sync drops it
	if err := l.afterConnect(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"on": store.RouteLink, "own": store.RouteSSH, "half": store.RouteSSH, "bound": store.RouteLink, "back": store.RouteLink} {
		if got := w.route(id); got != want {
			t.Fatalf("%s: %q, want %q", id, got, want)
		}
	}
	if b := c.bound(); !slices.Equal(b, []int{41010, 41013, 41014}) {
		t.Fatalf("sync bound %v", b)
	}
}

// A full fallback with mixed routes: the relay is ended (fenced) and the
// relay's ports freed before any route becomes ssh again; agents waiting
// for their port then take their own connections.
func TestAgentRoutesFallbackOrder(t *testing.T) {
	w := newLinkWorld(t, nil)
	ctx := context.Background()
	l := w.link(store.LinkAgents)
	l.ensure(ctx)
	f1 := w.agent(l, "m1", 41021)
	f2 := w.agent(l, "m2", 41022)
	f1.connect()
	_ = w.st.SetAgentRoute(ctx, &store.AgentRoute{ThreadID: "m2", HostID: w.host.ID, Port: 41022, Route: store.RouteSSH})
	var order []string
	w.plainFn = func(script string, args []string) (string, string, error) {
		if script == fenceScript {
			order = append(order, args[4]+" route m1="+w.route("m1"))
		}
		return "", "", nil
	}
	h, _ := w.m.hostOf(ctx, w.host.ID)
	l.op.Lock()
	l.fallBackLocked(ctx, h, "a test")
	l.op.Unlock()
	if len(order) != 2 || order[0] != "end-relay route m1=link" || order[1] != "free-ports route m1=link" {
		t.Fatalf("order %v", order)
	}
	if w.route("m1") != store.RouteSSH || w.route("m2") != store.RouteSSH {
		t.Fatal("routes not back on ssh")
	}
	f1.connect()
	f2.connect()
	if f1.paths[len(f1.paths)-1].Link || f2.paths[len(f2.paths)-1].Link {
		t.Fatal("an agent on the link after the fallback")
	}
}

// A switch back cut short by a restart is finished by the next process:
// the fence goes off, the routes become ssh.
func TestAgentRoutesFinishSwitchBack(t *testing.T) {
	w := newLinkWorld(t, nil)
	ctx := context.Background()
	_ = w.st.SetHostLinkMode(ctx, w.host.ID, store.LinkOff)
	_ = w.st.SetAgentRoute(ctx, &store.AgentRoute{ThreadID: "s1", HostID: w.host.ID, Port: 41031, Route: store.RouteLink})
	_ = w.st.SetAgentRoute(ctx, &store.AgentRoute{ThreadID: "s2", HostID: w.host.ID, Port: 41032, Route: store.RouteToSSH})
	w.m.resumeAll()
	deadline := 200
	for w.route("s1") != store.RouteSSH || w.route("s2") != store.RouteSSH {
		if deadline--; deadline == 0 {
			t.Fatalf("routes %q %q, fences %v", w.route("s1"), w.route("s2"), w.fenceCalls())
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := w.fenceCalls()
	if len(calls) < 2 || calls[0] != "off" || !strings.HasPrefix(calls[1], "free-ports") {
		t.Fatalf("fence %v", calls)
	}
}

// A machine entry that is deleted releases its fence there.
func TestAgentRoutesForgetReleases(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkAgents)
	l.ensure(context.Background())
	w.m.forget(w.host.ID)
	if calls := w.fenceCalls(); len(calls) == 0 || calls[len(calls)-1] != "release" {
		t.Fatalf("fence %v", calls)
	}
	if !w.conns[len(w.conns)-1].closed.Load() {
		t.Fatal("relay left running")
	}
}

// Phase 3: agents on the link are not parked; agents on their own
// connection are.
func TestAgentRoutesNoParkingOnTheLink(t *testing.T) {
	w := newLinkWorld(t, nil)
	l := w.link(store.LinkAgents)
	l.ensure(context.Background())
	on := w.agent(l, "k1", 41041)
	on.connect()
	own := w.agent(l, "k2", 41042)
	_ = w.st.SetAgentRoute(context.Background(), &store.AgentRoute{ThreadID: "k2", HostID: w.host.ID, Port: 41042, Route: store.RouteSSH})
	if !w.m.keepsStream(w.host.ID, "k1") || w.m.keepsStream(w.host.ID, "k2") {
		t.Fatal("keepsStream")
	}
	_ = own
	l.mu.Lock()
	l.fallback = &store.LinkFallback{Why: "x"}
	l.mu.Unlock()
	if w.m.keepsStream(w.host.ID, "k1") {
		t.Fatal("a fallen-back machine's agents are parked as before")
	}
}
