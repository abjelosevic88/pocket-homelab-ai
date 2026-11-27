package server

// The machine link's phase 2 and 3 gates against a real machine over ssh
// (docs/machine-link-plan.md, "Phase 2 gates", "Phase 3 gates"): a real
// relay (pocket-hold relay, the build's own binary uploaded there), real
// holders, and fake Claude CLIs that answer each message with a numbered run
// of lines and a Pocket tools call through their tools port.
//
// Opt in with POCKET_LINK_SSH_TEST=<ssh target> (localhost works) and run
// them alone, with a private XDG_RUNTIME_DIR for ssh's shared connections:
//
//	XDG_RUNTIME_DIR=$(mktemp -d) POCKET_LINK_SSH_TEST=localhost go test -run TestLinkE2E -v ./internal/server/
//
// Everything lives in a fresh home there (agent.SetTestRemoteHome): the
// machine's own ~/.pocket-ai is never touched, and every process a test
// started is ended by its slot or the fence at the end.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// fakeLinkClaude answers each user message with "lines N" in it with N
// numbered assistant messages (20 ms apart), then a Pocket tools call
// (pocket-hold probe to its tools port: the answer comes from this test's
// endpoint for that agent) as "tool <answer>", then the result. A "sleep S"
// in the message waits S seconds before the result. Every start appends its
// pid to $HOME/starts-$SLOT.
const fakeLinkClaude = `#!/bin/sh
echo $$ >> "$HOME/starts-$SLOT"
n=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      n=$((n+1))
      k=$(printf '%s' "$line" | sed -n 's/.*lines \([0-9][0-9]*\).*/\1/p'); [ -n "$k" ] || k=0
      s=$(printf '%s' "$line" | sed -n 's/.*sleep \([0-9][0-9]*\).*/\1/p')
      i=0
      while [ $i -lt $k ]; do i=$((i+1)); printf '{"type":"assistant","message":{"id":"m%s-%s","content":[{"type":"text","text":"line %s"}]}}\n' $n $i $i; sleep 0.02; done
      [ -n "$s" ] && sleep "$s"
      t=$("$PH" probe "$TP" 2>&1 | tr -d '\n"\\')
      printf '{"type":"assistant","message":{"id":"m%s-t","content":[{"type":"text","text":"tool %s"}]}}\n' $n "$t"
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}';;
  esac
done`

// e2eSink records what a session said.
type e2eSink struct {
	mu     sync.Mutex
	texts  []string // assistant texts, in order, once per item key
	keys   map[string]int
	dups   []string
	turns  int
	exited chan error
	links  []bool
}

func newE2ESink() *e2eSink { return &e2eSink{keys: map[string]int{}, exited: make(chan error, 1)} }

func (k *e2eSink) Item(it agent.Item) {
	if it.Kind != "assistant" || it.Text == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.keys[it.Key]; ok {
		k.dups = append(k.dups, it.Key+"="+it.Text)
		return
	}
	k.keys[it.Key] = len(k.texts)
	k.texts = append(k.texts, it.Text)
}
func (k *e2eSink) Delta(string, string, string)  {}
func (k *e2eSink) SessionID(string)              {}
func (k *e2eSink) Context(agent.Context)         {}
func (k *e2eSink) Limits(string, []agent.Window) {}
func (k *e2eSink) Request(agent.Request)         {}
func (k *e2eSink) TurnStarted()                  {}
func (k *e2eSink) TurnDone(agent.TurnInfo)       { k.mu.Lock(); k.turns++; k.mu.Unlock() }
func (k *e2eSink) Subagents([]agent.Subagent)    {}
func (k *e2eSink) Tasks([]agent.Task)            {}
func (k *e2eSink) Link(up bool)                  { k.mu.Lock(); k.links = append(k.links, up); k.mu.Unlock() }
func (k *e2eSink) Exited(err error)              { k.exited <- err }
func (k *e2eSink) count() (turns int, texts []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.turns, slices.Clone(k.texts)
}

// e2e is one machine over real ssh, with a link manager on a real database.
type e2e struct {
	t      *testing.T
	ctx    context.Context
	target string
	home   string // the test's home there
	st     *store.Store
	h      *store.Host
	m      *linkManager
	bin    string // pocket-hold there (the installed, content-hashed one)
	cli    string // the fake CLI there

	mu     sync.Mutex
	relays []*exec.Cmd // ssh processes of relays, newest last
	agents map[string]*e2eAgent
	// forwards: per-agent ssh connections with an -R forward, by port
	forwards map[int][]*exec.Cmd
	bad      []string // ownership violations seen
	hooks    func(o relayDialOptions) relayDialOptions
	conns    func(c linkConn) linkConn
}

type e2eAgent struct {
	left  sync.Once
	id    string
	port  int // tools port there
	ln    net.Listener
	hits  atomic.Int64
	leave chan struct{}
	sess  agent.Session
	sink  *e2eSink
	la    *linkAgent
	m     *linkManager
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	target := os.Getenv("POCKET_LINK_SSH_TEST")
	if target == "" {
		t.Skip("POCKET_LINK_SSH_TEST not set")
	}
	if _, _, ok := holdBinaryForTest(); !ok {
		t.Skip("pocket-hold is not built in (go generate ./internal/hold)")
	}
	e := &e2e{t: t, ctx: context.Background(), target: target, agents: map[string]*e2eAgent{}, forwards: map[int][]*exec.Cmd{}}
	r := &agent.Remote{Target: target}
	out, _, err := r.Run(agent.PlainSSH(e.ctx), 30*time.Second, `mktemp -d /tmp/pl.XXXXXX`)
	if err != nil {
		t.Fatal(err)
	}
	e.home = lastLine(strings.TrimSpace(out))
	if !strings.HasPrefix(e.home, "/tmp/pl.") {
		t.Fatalf("no temp home there: %q", out)
	}
	agent.SetTestRemoteHome(e.home)
	t.Cleanup(e.cleanup)

	db := filepath.Join(t.TempDir(), "t.db")
	e.st, err = store.Open(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e.h = &store.Host{Name: "e2e", Target: target, Info: store.HostInfo{OK: true, OS: "Linux x86_64"}}
	if err := e.st.CreateHost(e.ctx, e.h); err != nil {
		t.Fatal(err)
	}
	forgetHolder(e.h.ID)
	e.m = e.manager()
	e.bin, err = e.m.install(e.ctx, e.h)
	if err != nil {
		t.Fatal(err)
	}
	e.cli = e.home + "/claude"
	if err := r.Put(agent.PlainSSH(e.ctx), time.Minute, e.cli, []byte(fakeLinkClaude)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(agent.PlainSSH(e.ctx), 30*time.Second, `chmod 700 "$1"`, e.cli); err != nil {
		t.Fatal(err)
	}
	agent.SetTestForwardHook(e.sawForward)
	t.Cleanup(func() { agent.SetTestForwardHook(nil) })
	return e
}

func holdBinaryForTest() ([]byte, string, bool) {
	goos, goarch := platform("Linux x86_64")
	return hold.Binary(goos, goarch)
}

// manager is a link manager as a new Pocket AI process makes it: same
// database, its own lease; relays started for real and recorded.
func (e *e2e) manager() *linkManager {
	m := newLinkManager(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if os.Getenv("POCKET_LINK_E2E_LOG") != "" {
		m.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	relayCmd := m.relayCmd
	m.relayCmd = func(ctx context.Context, h *store.Host, script string, args ...string) (*exec.Cmd, error) {
		cmd, err := relayCmd(ctx, h, script, args...)
		if err == nil {
			e.mu.Lock()
			e.relays = append(e.relays, cmd)
			e.mu.Unlock()
		}
		return cmd, err
	}
	dial := m.dial
	m.dial = func(ctx context.Context, cmd *exec.Cmd, o relayDialOptions) (linkConn, error) {
		e.mu.Lock()
		hook, wrap := e.hooks, e.conns
		e.mu.Unlock()
		if hook != nil {
			o = hook(o)
		}
		c, err := dial(ctx, cmd, o)
		if err == nil && wrap != nil && !o.Test {
			c = wrap(c)
		}
		return c, err
	}
	m.start(e.ctx)
	deadline := time.Now().Add(10 * time.Second)
	for !m.lease.Held() {
		if time.Now().After(deadline) {
			e.t.Fatal("no controller lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return m
}

func (e *e2e) run(script string, args ...string) string {
	e.t.Helper()
	out, errOut, err := (&agent.Remote{Target: e.target}).Run(agent.PlainSSH(e.ctx), time.Minute, script, args...)
	if err != nil {
		e.t.Fatalf("%s: %v %s", script, err, errOut)
	}
	return out
}

func (e *e2e) cleanup() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.agents))
	var list []*e2eAgent
	for id, a := range e.agents {
		ids = append(ids, id)
		list = append(list, a)
	}
	e.mu.Unlock()
	for _, a := range list {
		a.letGo()
	}
	r := &agent.Remote{Target: e.target}
	ctx := agent.PlainSSH(context.Background())
	if e.m != nil {
		e.m.close()
	}
	// every holder of the test, then whatever relay the fence records, then the home
	_, _, _ = r.Run(ctx, time.Minute, `b=$1; shift; for s; do "$b" stop "$s" >/dev/null 2>&1; done; `+
		`for f in "$HOME"/.pocket-ai/relay/*.fence; do [ -r "$f" ] || continue; p=$(sed -n 's/.*"pid":\([0-9]*\).*/\1/p' "$f"); `+
		`[ -n "$p" ] && [ "$p" != 0 ] && ps -o args= -p "$p" 2>/dev/null | grep -q " relay " && kill "$p"; done; sleep 1`, append([]string{e.bin}, ids...)...)
	// whatever else of this test still runs there (a holder of an agent a
	// failed test lost track of): every process under its own home, by pid
	_, _, _ = r.Run(ctx, time.Minute, `for p in $(ps -eo pid=,args= | awk -v h="$1/" 'index($0, h) && !/awk/ {print $1}'); do [ "$p" != $$ ] && kill "$p" 2>/dev/null; done; true`, e.home)
	if os.Getenv("POCKET_LINK_E2E_KEEP") == "" {
		_, _, _ = r.Run(ctx, time.Minute, `case "$1" in /tmp/pl.*) rm -rf -- "$1";; esac`, e.home)
	}
	agent.SetTestRemoteHome("")
}

// link is the machine's link, switched to mode (stored first, then the
// same Switch the button does), and up.
func (e *e2e) link(m *linkManager, mode string) *machineLink {
	e.t.Helper()
	l, err := m.get(e.ctx, e.h.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	if mode != store.LinkOff {
		f := newLinkFlow(mode)
		l.op.Lock()
		err := l.switchLocked(e.ctx, f, mode)
		l.op.Unlock()
		if err != nil {
			e.t.Fatal(err)
		}
		e.waitUp(l)
	}
	return l
}

func (e *e2e) waitUp(l *machineLink) {
	e.t.Helper()
	e.waitFor(60*time.Second, "the link to come up", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.upLocked()
	})
}

func (e *e2e) waitFor(d time.Duration, what string, ok func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startAgent starts (or, adopt, takes back) agent id under m, held, with
// its own tools endpoint here.
func (e *e2e) startAgent(m *linkManager, id string, port int, adopt bool) *e2eAgent {
	e.t.Helper()
	a, err := e.tryStartAgent(m, id, port, adopt)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *e2e) tryStartAgent(m *linkManager, id string, port int, adopt bool) (*e2eAgent, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a := &e2eAgent{id: id, port: port, ln: ln, leave: make(chan struct{}), sink: newE2ESink(), m: m}
	go serveProbe(ln, &a.hits)
	local := ln.Addr().String()
	rem := &agent.Remote{Target: e.target, Slot: id, Forwards: []string{fmt.Sprintf("127.0.0.1:%d:%s", port, local)}}
	o := agent.Options{Bin: e.cli, Cwd: e.home, Mode: "default", Remote: rem,
		Env:  []string{"PH=" + e.bin, "TP=" + strconv.Itoa(port), "SLOT=" + id},
		Hold: &agent.Hold{Bin: e.bin, Slot: id, Leave: a.leave, Adopt: adopt}}
	a.la = m.wireAgent(e.ctx, e.h, id, &o, port, local)
	s, err := agent.StartClaude(e.ctx, o, a.sink)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("start %s: %v", id, err)
	}
	a.sess = s
	m.attachAgent(e.h.ID, a.la, s, func() relay.Priority { return relay.Idle }, func(text string) {
		a.sink.mu.Lock()
		a.sink.texts = append(a.sink.texts, "notice: "+text)
		a.sink.mu.Unlock()
	})
	e.mu.Lock()
	e.agents[id] = a
	e.mu.Unlock()
	return a, nil
}

// letGo lets go of a's CLI (it runs on there).
func (a *e2eAgent) letGo() {
	a.left.Do(func() {
		close(a.leave)
		_ = a.sess.Close()
		a.ln.Close()
	})
}

// serveProbe answers pocket-hold probe (relay.ProbeLine) on ln.
func serveProbe(ln net.Listener, hits *atomic.Int64) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			line, err := bufio.NewReader(c).ReadString('\n')
			if err == nil && strings.TrimSpace(line) == relay.ProbeLine {
				hits.Add(1)
				_, _ = io.WriteString(c, relay.ProbeAnswer+"\n")
			}
		}()
	}
}

// turn sends text and waits for that turn's end; it returns the texts said
// in that turn.
func (e *e2e) turn(a *e2eAgent, text string, d time.Duration) []string {
	e.t.Helper()
	before, texts0 := a.sink.count()
	if err := a.sess.Send(agent.Input{Text: text}); err != nil {
		e.t.Fatalf("%s: send: %v", a.id, err)
	}
	e.waitFor(d, a.id+"'s turn", func() bool { n, _ := a.sink.count(); return n > before })
	_, texts := a.sink.count()
	return texts[len(texts0):]
}

// checkTurn: the turn said line 1..n once each, in order, and its tool
// call came back.
func (e *e2e) checkTurn(a *e2eAgent, got []string, n int) {
	e.t.Helper()
	i := 0
	for _, s := range got {
		if strings.HasPrefix(s, "line ") {
			i++
			if s != "line "+strconv.Itoa(i) {
				e.t.Fatalf("%s: line %d is %q (lost or read twice): %v", a.id, i, s, got)
			}
		}
	}
	if i != n {
		e.t.Fatalf("%s: %d lines, want %d: %v", a.id, i, n, got)
	}
	if got[len(got)-1] != "tool "+relay.ProbeAnswer {
		e.t.Fatalf("%s: the tool call did not come back: %v", a.id, got)
	}
	a.sink.mu.Lock()
	dups := slices.Clone(a.sink.dups)
	a.sink.mu.Unlock()
	if len(dups) > 0 {
		e.t.Fatalf("%s: output read twice: %v", a.id, dups)
	}
}

// leaveAll lets go of every agent of m (Pocket AI stopping): they run on.
func (e *e2e) leaveAll(m *linkManager) {
	e.mu.Lock()
	var list []*e2eAgent
	for _, a := range e.agents {
		if a.m == m {
			list = append(list, a)
		}
	}
	e.mu.Unlock()
	for _, a := range list {
		a.letGo()
	}
	for _, a := range list {
		select {
		case <-a.sess.Done(): // detached: no Exited (the CLI runs on)
		case <-time.After(30 * time.Second):
			buf := make([]byte, 8<<20)
			buf = buf[:runtime.Stack(buf, true)]
			_ = os.WriteFile("/tmp/mlp2-stack.txt", buf, 0o600)
			e.t.Fatalf("%s did not let go", a.id)
		}
	}
}

// crash ends m as a crash would: the ssh processes of its relay and of
// its agents' own connections are killed, then its agents let go and its
// lease goes (a crash's lease runs out by itself; the machine sees the same).
func (e *e2e) crash(m *linkManager) {
	e.mu.Lock()
	var kill []*exec.Cmd
	kill = append(kill, e.relays...)
	for _, cs := range e.forwards {
		kill = append(kill, cs...)
	}
	e.relays = nil
	e.mu.Unlock()
	for _, c := range kill {
		if c.Process != nil {
			_ = c.Process.Signal(syscall.SIGKILL)
		}
	}
	e.leaveAll(m)
	m.close()
}

// killLink kills the current relay's ssh process: the network to the
// machine is gone, as far as the link can tell.
func (e *e2e) killLink() {
	e.mu.Lock()
	var c *exec.Cmd
	if len(e.relays) > 0 {
		c = e.relays[len(e.relays)-1]
	}
	e.mu.Unlock()
	if c == nil || c.Process == nil {
		e.t.Fatal("no relay to kill")
	}
	_ = c.Process.Signal(syscall.SIGKILL)
}

// sawForward: an agent's own ssh connection with an -R forward starts.
// Nobody may think the relay owns that port right now.
func (e *e2e) sawForward(cmd *exec.Cmd, forwards []string) {
	for _, f := range forwards {
		p, _ := strconv.Atoi(strings.Split(f, ":")[1])
		e.mu.Lock()
		e.forwards[p] = append(e.forwards[p], cmd)
		m := e.m
		e.mu.Unlock()
		if l := m.existing(e.h.ID); l != nil {
			l.mu.Lock()
			if l.conn != nil && l.bound[p] {
				e.mu.Lock()
				e.bad = append(e.bad, fmt.Sprintf("an -R forward for %d started while the relay owns it", p))
				e.mu.Unlock()
			}
			l.mu.Unlock()
		}
	}
}

// liveForwards: ports with an agent's own ssh connection alive here.
func (e *e2e) liveForwards() map[int]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[int]bool{}
	for p, cs := range e.forwards {
		keep := cs[:0]
		for _, c := range cs {
			if c.Process != nil && syscall.Kill(c.Process.Pid, 0) == nil && c.ProcessState == nil {
				keep = append(keep, c)
				out[p] = true
			}
		}
		e.forwards[p] = keep
	}
	return out
}

// watchOwners checks, until stop, that no port is ever had both ways: a
// port the relay reports listening on, while an agent's own ssh connection
// forwarding it was alive both before and after the relay was asked.
func (e *e2e) watchOwners(m func() *linkManager) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-time.After(30 * time.Millisecond):
			}
			l := m().existing(e.h.ID)
			if l == nil {
				continue
			}
			l.mu.Lock()
			c := l.conn
			l.mu.Unlock()
			if c == nil {
				continue
			}
			before := e.liveForwards()
			ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
			st, err := c.Status(ctx)
			cancel()
			if err != nil {
				continue
			}
			after := e.liveForwards()
			for _, p := range st.Ports {
				if before[p] && after[p] {
					e.mu.Lock()
					e.bad = append(e.bad, fmt.Sprintf("port %d: the relay listens and an agent's ssh forward is alive", p))
					e.mu.Unlock()
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		e.mu.Lock()
		bad := slices.Clone(e.bad)
		e.mu.Unlock()
		if len(bad) > 0 {
			e.t.Fatalf("a port had two owners: %v", bad)
		}
	}
}

func (e *e2e) route(id string) string {
	r, err := e.st.GetAgentRoute(e.ctx, id)
	if err != nil {
		return ""
	}
	return r.Route
}

func (e *e2e) relayPorts(l *machineLink) []int {
	l.mu.Lock()
	c := l.conn
	l.mu.Unlock()
	if c == nil {
		return nil
	}
	st, err := c.Status(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return st.Ports
}

// starts is how many CLIs ever started in slot id there.
func (e *e2e) starts(id string) int {
	out := e.run(`cat "$HOME/starts-$1" 2>/dev/null | wc -l`, id)
	n, _ := strconv.Atoi(strings.TrimSpace(lastLine(out)))
	return n
}

func freeTestPort() int { return 20000 + rand.IntN(40000) }

// A new agent on the link: bind, start over exec, hold stream; its first
// tool call works, and it never had an -R forward.
func TestLinkE2ENewAgent(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkAgents)
	a := e.startAgent(e.m, "n1", freeTestPort(), false)
	e.checkTurn(a, e.turn(a, "lines 20", 60*time.Second), 20)
	if r := e.route("n1"); r != store.RouteLink {
		t.Fatalf("route %q", r)
	}
	if ps := e.relayPorts(l); !slices.Contains(ps, a.port) {
		t.Fatalf("relay ports %v, want %d", ps, a.port)
	}
	if len(e.liveForwards()) != 0 || e.starts("n1") != 1 {
		t.Fatalf("forwards %v, starts %d", e.liveForwards(), e.starts("n1"))
	}
	stop()
}

// The network dies mid-turn: the link notices, comes back, syncs, and the
// agent reattaches at its offset: no line lost or read twice, and its tools
// work again.
func TestLinkE2ENetworkKilledMidTurn(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkAgents)
	var list []*e2eAgent
	for i := 0; i < 3; i++ {
		list = append(list, e.startAgent(e.m, fmt.Sprintf("k%d", i), freeTestPort(), false))
	}
	for _, a := range list {
		if err := a.sess.Send(agent.Input{Text: "lines 150"}); err != nil {
			t.Fatal(err)
		}
	}
	e.waitFor(30*time.Second, "the turns to be under way", func() bool {
		_, texts := list[0].sink.count()
		return len(texts) > 20
	})
	e.killLink()
	start := time.Now()
	e.waitFor(90*time.Second, "the link to come back", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.upLocked() && len(e.relays) > 1
	})
	t.Logf("link back after %s", time.Since(start).Round(time.Millisecond))
	for _, a := range list {
		e.waitFor(60*time.Second, a.id+"'s turn", func() bool { n, _ := a.sink.count(); return n >= 1 })
		_, texts := a.sink.count()
		e.checkTurn(a, texts, 150)
	}
	t.Logf("turns done %s after the cut", time.Since(start).Round(time.Millisecond))
	for _, a := range list {
		e.checkTurn(a, e.turn(a, "lines 5", 60*time.Second), 5)
		if e.starts(a.id) != 1 {
			t.Fatalf("%s started %d times", a.id, e.starts(a.id))
		}
	}
	stop()
}

// A server restart with many held agents on one machine: the new process
// takes the lease, starts a new relay (the old one is taken over), syncs
// the agents' ports, and every agent is taken back over the link and
// reaches its tools endpoint. POCKET_LINK_E2E_AGENTS sets how many (20).
func TestLinkE2ERestartManyAgents(t *testing.T) {
	e := newE2E(t)
	n := 20
	if v, err := strconv.Atoi(os.Getenv("POCKET_LINK_E2E_AGENTS")); err == nil && v > 0 {
		n = v
	}
	e.link(e.m, store.LinkAgents)
	ports := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a, err := e.tryStartAgent(e.m, id, freeTestPort(), false)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ports[id] = a.port
			mu.Unlock()
		}()
	}
	wg.Wait()
	// some output piles up while nobody is attached
	e.mu.Lock()
	var list []*e2eAgent
	for _, a := range e.agents {
		list = append(list, a)
	}
	e.mu.Unlock()
	for _, a := range list {
		_ = a.sess.Send(agent.Input{Text: "lines 30"})
	}
	e.waitFor(60*time.Second, "the first turns", func() bool {
		for _, a := range list {
			if n, _ := a.sink.count(); n < 1 {
				return false
			}
		}
		return true
	})
	for _, a := range list {
		_ = a.sess.Send(agent.Input{Text: "lines 100 sleep 2"})
	}
	time.Sleep(300 * time.Millisecond)

	// restart: this process lets go of its agents (they run on), a new one comes
	start := time.Now()
	e.leaveAll(e.m)
	e.m.close()
	m2 := e.manager()
	e.mu.Lock()
	e.m = m2
	e.agents = map[string]*e2eAgent{}
	e.mu.Unlock()
	stop := e.watchOwners(func() *linkManager { return m2 })
	var reattached, tooled sync.Map
	for id, port := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := e.tryStartAgent(m2, id, port, true)
			if err != nil {
				t.Error(err)
				return
			}
			reattached.Store(id, time.Since(start))
			// its turn from before the restart ends; then one tool call
			e.waitFor(90*time.Second, id+"'s turn from before", func() bool { n, _ := a.sink.count(); return n >= 1 })
			got := e.turn(a, "lines 0", 60*time.Second)
			if got[len(got)-1] != "tool "+relay.ProbeAnswer {
				t.Errorf("%s: tool call after the restart: %v", id, got)
			}
			tooled.Store(id, time.Since(start))
		}()
	}
	wg.Wait()
	var lastBack, lastTool time.Duration
	count := 0
	reattached.Range(func(_, v any) bool { lastBack = max(lastBack, v.(time.Duration)); count++; return true })
	tooled.Range(func(_, v any) bool { lastTool = max(lastTool, v.(time.Duration)); return true })
	t.Logf("%d agents: all reattached after %s, all reached their tools endpoint after %s (including the turns cut by the restart draining)", count, lastBack.Round(time.Millisecond), lastTool.Round(time.Millisecond))
	if count != n || lastTool > 60*time.Second {
		t.Fatalf("%d of %d back; tools after %s", count, n, lastTool)
	}
	for id := range ports {
		if e.starts(id) != 1 {
			t.Fatalf("%s started %d times", id, e.starts(id))
		}
	}
	stop()
}

// Agents started before the link (on their own ssh connections with -R
// forwards) are moved onto it mid-turn by the Switch, one at a time; they
// make tool calls after the enable, after a reconnect, and after Switch
// back (again mid-turn), and no port ever has two owners.
func TestLinkE2ECutoverAndSwitchBackMidTurn(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkOff)
	var list []*e2eAgent
	for i := 0; i < 3; i++ {
		list = append(list, e.startAgent(e.m, fmt.Sprintf("c%d", i), freeTestPort(), false))
	}
	for _, a := range list {
		e.checkTurn(a, e.turn(a, "lines 3", 60*time.Second), 3)
		if r := e.route(a.id); r != "" && r != store.RouteSSH {
			t.Fatalf("%s: route %q before the link", a.id, r)
		}
	}
	if len(e.liveForwards()) != 3 {
		t.Fatalf("own connections: %v", e.liveForwards())
	}
	midTurn := func(n int, during func()) {
		t.Helper()
		before := map[*e2eAgent]int{}
		for _, a := range list {
			before[a], _ = a.sink.count()
			if err := a.sess.Send(agent.Input{Text: fmt.Sprintf("lines %d", n)}); err != nil {
				t.Fatal(err)
			}
		}
		e.waitFor(30*time.Second, "the turns under way", func() bool {
			_, texts := list[len(list)-1].sink.count()
			return strings.HasPrefix(texts[len(texts)-1], "line ")
		})
		during()
		for _, a := range list {
			e.waitFor(90*time.Second, a.id+"'s turn", func() bool { k, _ := a.sink.count(); return k > before[a] })
			_, texts := a.sink.count()
			from := len(texts) - 1
			for from > 0 && texts[from-1] != "tool "+relay.ProbeAnswer && !strings.HasPrefix(texts[from-1], "notice") {
				from--
			}
			e.checkTurn(a, texts[from:], n)
		}
	}
	// Switch with the agents mid-turn
	start := time.Now()
	midTurn(250, func() {
		f := newLinkFlow(store.LinkAgents)
		l.op.Lock()
		err := l.switchLocked(e.ctx, f, store.LinkAgents)
		l.op.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		e.waitFor(90*time.Second, "every agent on the link", func() bool {
			for _, a := range list {
				if e.route(a.id) != store.RouteLink {
					return false
				}
			}
			return true
		})
		t.Logf("3 agents moved onto the link in %s", time.Since(start).Round(time.Millisecond))
	})
	if f := e.liveForwards(); len(f) != 0 {
		t.Fatalf("own connections left after the cutover: %v", f)
	}
	ps := e.relayPorts(l)
	for _, a := range list {
		if !slices.Contains(ps, a.port) {
			t.Fatalf("relay ports %v miss %d", ps, a.port)
		}
		e.checkTurn(a, e.turn(a, "lines 2", 60*time.Second), 2) // tools after the enable
	}
	// a reconnect
	e.killLink()
	e.waitFor(90*time.Second, "the link back", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.upLocked() && len(e.relays) > 1
	})
	for _, a := range list {
		e.checkTurn(a, e.turn(a, "lines 2", 90*time.Second), 2) // tools after a reconnect
	}
	// Switch back, mid-turn
	start = time.Now()
	midTurn(250, func() {
		if err := l.switchBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("switched back in %s", time.Since(start).Round(time.Millisecond))
	})
	for _, a := range list {
		if r := e.route(a.id); r != store.RouteSSH {
			t.Fatalf("%s: route %q after switch back", a.id, r)
		}
		e.checkTurn(a, e.turn(a, "lines 2", 60*time.Second), 2) // tools after switch back
		if e.starts(a.id) != 1 {
			t.Fatalf("%s started %d times", a.id, e.starts(a.id))
		}
	}
	if f := e.liveForwards(); len(f) != 3 {
		t.Fatalf("own connections after switch back: %v", f)
	}
	if out := e.run(`cat "$HOME"/.pocket-ai/relay/*.fence`); !strings.Contains(out, `"off":true`) || !strings.Contains(out, `"pid":0`) {
		t.Fatalf("fence after switch back: %s", out)
	}
	stop()
}

// unknownOnce is a link whose first start-only exec runs there but whose
// end never comes back (a lost acknowledgement).
type unknownOnce struct {
	linkConn
	done atomic.Bool
}

func (c *unknownOnce) Exec(ctx context.Context, req linkExecRequest) (linkExecResult, error) {
	res, err := c.linkConn.Exec(ctx, req)
	if err == nil && strings.Contains(req.Script, "-start-only") && c.done.CompareAndSwap(false, true) {
		return linkExecResult{}, fmt.Errorf("%w: the link went", errRelayUnknown)
	}
	return res, err
}

// Agent starts over the link are atomic per slot: concurrent starts make
// one CLI; a start whose acknowledgement is lost is settled against the
// slot (one CLI, the thread goes on); a start script cancelled after the
// holder detached leaves the holder running and nothing of the script.
func TestLinkE2EStartsPerSlot(t *testing.T) {
	e := newE2E(t)
	var unknown *unknownOnce
	e.conns = func(c linkConn) linkConn { unknown = &unknownOnce{linkConn: c}; return unknown }
	l := e.link(e.m, store.LinkAgents)

	t.Run("lost acknowledgement", func(t *testing.T) {
		a := e.startAgent(e.m, "u1", freeTestPort(), false)
		if !unknown.done.Load() {
			t.Fatal("the start's end was not lost")
		}
		e.checkTurn(a, e.turn(a, "lines 3", 60*time.Second), 3)
		if n := e.starts("u1"); n != 1 {
			t.Fatalf("%d CLIs", n)
		}
	})

	t.Run("concurrent starts", func(t *testing.T) {
		// what linkconn's startOnly makes for this slot: today's start script
		la := &linkAgent{thread: "s1", slot: "s1", bin: e.bin}
		wrap := []string{e.bin, "attach", "-from", "-1", "-start-only", "s1", "--"}
		script := agent.RemoteScriptForTest("s1", e.home, []string{"PH=" + e.bin, "TP=1", "SLOT=s1"}, wrap, e.cli, nil)
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := l.startOnly(e.ctx, la, agent.HoldStart{Script: script})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("a start: %v", err)
			}
		}
		if n := e.starts("s1"); n != 1 {
			t.Fatalf("%d CLIs for one slot", n)
		}
		e.run(`"$1" stop s1`, e.bin)
	})

	t.Run("cancelled start script", func(t *testing.T) {
		ctx, cancel := context.WithCancel(e.ctx)
		pr, pw := io.Pipe()
		got := make(chan error, 1)
		go func() {
			_, err := l.conn.Exec(ctx, linkExecRequest{Script: agent.RemoteScriptText(`"$1" attach -start-only c9 -- /bin/sh -c 'echo $$ >> "$HOME/starts-c9"; exec sleep 600'; echo; exec sleep 31.5`, e.bin), Stdout: pw})
			pw.Close()
			got <- err
		}()
		line, err := bufio.NewReader(pr).ReadString('\n')
		if err != nil || !strings.Contains(line, `"type":"pocket_hold"`) {
			t.Fatalf("no header: %q %v", line, err)
		}
		go io.Copy(io.Discard, pr)
		time.Sleep(300 * time.Millisecond) // the script is in its sleep now
		cancel()
		if err := <-got; err == nil {
			t.Fatal("the cancelled exec returned no error")
		}
		e.waitFor(10*time.Second, "the script's process group to go", func() bool {
			return strings.TrimSpace(e.run(`ps -eo args= | grep -c "^sleep 31.5" || true`)) == "0"
		})
		if out := e.run(`"$1" status c9`, e.bin); strings.Contains(out, `"gone":true`) || !strings.Contains(out, `"pid"`) {
			t.Fatalf("the holder did not survive: %s", out)
		}
		if n := e.starts("c9"); n != 1 {
			t.Fatalf("%d CLIs", n)
		}
		e.run(`"$1" stop c9`, e.bin)
	})
}

// restartAs is a new process after m: it takes the agents back (adopt).
func (e *e2e) restartAs(ports map[string]int) *linkManager {
	e.t.Helper()
	m2 := e.manager()
	e.mu.Lock()
	e.m = m2
	e.agents = map[string]*e2eAgent{}
	e.mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, len(ports))
	for id, port := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.tryStartAgent(m2, id, port, true); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		e.t.Fatal(err)
	}
	return m2
}

func (e *e2e) portsOf(prefix string) map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]int{}
	for id, a := range e.agents {
		if strings.HasPrefix(id, prefix) {
			out[id] = a.port
		}
	}
	return out
}

func (e *e2e) countRoute(ids map[string]int, route string) int {
	n := 0
	for id := range ids {
		if e.route(id) == route {
			n++
		}
	}
	return n
}

// everyAgentWorks: each agent makes a turn with a tool call, on route, and
// was started once.
func (e *e2e) everyAgentWorks(ids map[string]int, route string) {
	e.t.Helper()
	e.mu.Lock()
	agents := e.agents
	e.mu.Unlock()
	for id := range ids {
		a := agents[id]
		// a turn it was in when it was taken back ends first
		e.waitFor(90*time.Second, id+" idle", func() bool {
			_, texts := a.sink.count()
			return len(texts) == 0 || strings.HasPrefix(texts[len(texts)-1], "tool ")
		})
		e.checkTurn(a, e.turn(a, "lines 2", 90*time.Second), 2)
		if r := e.route(id); r != route {
			e.t.Fatalf("%s: route %q, want %q", id, r, route)
		}
		if n := e.starts(id); n != 1 {
			e.t.Fatalf("%s started %d times", id, n)
		}
	}
}

// A crash after k of N agents moved, in both directions: after the restart
// every agent is on exactly one path, with working tools.
func TestLinkE2ECrashMidMove(t *testing.T) {
	e := newE2E(t)
	const n, k = 6, 3
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkOff)
	for i := 0; i < n; i++ {
		e.startAgent(e.m, fmt.Sprintf("x%d", i), freeTestPort(), false)
	}
	ids := e.portsOf("x")
	for _, a := range e.agents {
		_ = a.sess.Send(agent.Input{Text: "lines 300"}) // busy while it moves
	}

	// cutover, crash after k moved
	f := newLinkFlow(store.LinkAgents)
	l.op.Lock()
	if err := l.switchLocked(e.ctx, f, store.LinkAgents); err != nil {
		t.Fatal(err)
	}
	l.op.Unlock()
	e.waitFor(60*time.Second, "k agents moved", func() bool { return e.countRoute(ids, store.RouteLink) >= k })
	e.crash(e.m)
	t.Logf("crashed with routes: %d link, %d to-link, %d ssh", e.countRoute(ids, store.RouteLink), e.countRoute(ids, store.RouteToLink), e.countRoute(ids, store.RouteSSH))
	m2 := e.restartAs(ids)
	l2, _ := m2.get(e.ctx, e.h.ID)
	e.waitFor(90*time.Second, "every agent on the link", func() bool {
		if e.countRoute(ids, store.RouteLink) == n {
			return true
		}
		l2.mu.Lock()
		st, le, up := l2.state, l2.lastErr, l2.upLocked()
		l2.mu.Unlock()
		var rs []string
		for id := range ids {
			rs = append(rs, id+"="+e.route(id))
		}
		t.Logf("state %s up %v err %q routes %v", st, up, le, rs)
		time.Sleep(2 * time.Second)
		return false
	})
	e.everyAgentWorks(ids, store.RouteLink)
	if ps := e.relayPorts(l2); len(ps) != n {
		t.Fatalf("relay ports %v", ps)
	}

	// switch back, crash after k moved
	for _, a := range e.agents {
		_ = a.sess.Send(agent.Input{Text: "lines 300"})
	}
	ctx, cancel := context.WithCancel(e.ctx)
	go func() { _ = l2.switchBack(ctx) }()
	e.waitFor(60*time.Second, "k agents back", func() bool { return e.countRoute(ids, store.RouteSSH) >= k })
	cancel()
	e.crash(m2)
	t.Logf("crashed with routes: %d link, %d to-ssh, %d ssh", e.countRoute(ids, store.RouteLink), e.countRoute(ids, store.RouteToSSH), e.countRoute(ids, store.RouteSSH))
	e.restartAs(ids)
	e.waitFor(90*time.Second, "every agent on ssh", func() bool { return e.countRoute(ids, store.RouteSSH) == n })
	e.everyAgentWorks(ids, store.RouteSSH)
	if out := e.run(`cat "$HOME"/.pocket-ai/relay/*.fence`); !strings.Contains(out, `"off":true`) || !strings.Contains(out, `"pid":0`) {
		t.Fatalf("fence: %s", out)
	}
	stop()
}

// bindHook is a link whose Bind can be made to fail for chosen ports, or
// to tell the test once it went through.
type bindHook struct {
	linkConn
	fail  func(port int) bool
	after func(port int)
}

func (c *bindHook) Bind(ctx context.Context, port int) error {
	if c.fail != nil && c.fail(port) {
		return fmt.Errorf("relay: simulated bind failure on %d", port)
	}
	err := c.linkConn.Bind(ctx, port)
	if err == nil && c.after != nil {
		c.after(port)
	}
	return err
}

// A crash after a bind succeeded but before the route was recorded: the
// relay owns the port and the route says to-link. Reconciliation records
// what the relay holds; after a real crash (the relay goes with the
// connection) the agent comes back onto the link with one owner.
func TestLinkE2ECrashAfterBind(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkAgents)

	// same link: the move state is settled against the relay
	a := e.startAgent(e.m, "b0", freeTestPort(), false)
	e.checkTurn(a, e.turn(a, "lines 1", 60*time.Second), 1)
	_ = e.st.SetAgentRoute(e.ctx, &store.AgentRoute{ThreadID: "b0", HostID: e.h.ID, Port: a.port, Route: store.RouteToLink})
	l.mu.Lock()
	c := l.conn
	l.mu.Unlock()
	if err := l.afterConnect(e.ctx, c, e.bin); err != nil {
		t.Fatal(err)
	}
	if r := e.route("b0"); r != store.RouteLink {
		t.Fatalf("reconciled to %q", r)
	}
	if ps := e.relayPorts(l); !slices.Contains(ps, a.port) {
		t.Fatalf("relay ports %v", ps)
	}

	// a real crash right after the bind of an agent moving over
	e2 := e.startAgentOn(store.LinkOff, "b1")
	_ = e2
	bound := make(chan int, 1)
	e.conns = func(c linkConn) linkConn {
		return &bindHook{linkConn: c, after: func(p int) {
			select {
			case bound <- p:
			default:
			}
		}}
	}
	l.op.Lock()
	_ = l.m.st.SetHostLinkMode(e.ctx, e.h.ID, store.LinkAgents)
	l.op.Unlock()
	ids := e.portsOf("b")
	e.killLink() // the next relay has the hook
	select {
	case <-bound:
	case <-time.After(60 * time.Second):
		t.Fatal("no bind")
	}
	e.crash(e.m)
	t.Logf("crashed with b1 %q", e.route("b1"))
	e.conns = nil
	e.restartAs(ids)
	e.waitFor(90*time.Second, "both on the link", func() bool { return e.countRoute(ids, store.RouteLink) == 2 })
	e.everyAgentWorks(ids, store.RouteLink)
	stop()
}

// startAgentOn starts agent id while the machine's stored mode is mode
// (the link itself stays as it is).
func (e *e2e) startAgentOn(mode, id string) *e2eAgent {
	e.t.Helper()
	l, _ := e.m.get(e.ctx, e.h.ID)
	l.mu.Lock()
	was := l.mode
	l.mode = mode
	l.mu.Unlock()
	a := e.startAgent(e.m, id, freeTestPort(), false)
	l.mu.Lock()
	l.mode = was
	l.mu.Unlock()
	return a
}

// A bind fails for one agent during the cutover: it stays on its own
// connection (mixed routes), the others move. After a restart sync lists
// only the relay's ports. Then a full fallback with those mixed routes:
// every relay listener is gone before any forward is restored.
func TestLinkE2EBindFailsThenFallback(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkOff)
	for i := 0; i < 3; i++ {
		e.startAgent(e.m, fmt.Sprintf("f%d", i), freeTestPort(), false)
	}
	ids := e.portsOf("f")
	failPort := ids["f1"]
	e.conns = func(c linkConn) linkConn {
		return &bindHook{linkConn: c, fail: func(p int) bool { return p == failPort }}
	}
	f := newLinkFlow(store.LinkAgents)
	l.op.Lock()
	if err := l.switchLocked(e.ctx, f, store.LinkAgents); err != nil {
		t.Fatal(err)
	}
	l.op.Unlock()
	e.waitFor(60*time.Second, "the cutover", func() bool {
		return e.route("f0") == store.RouteLink && e.route("f2") == store.RouteLink && e.route("f1") == store.RouteSSH
	})
	e.everyAgentWorks(map[string]int{"f0": 0, "f2": 0}, store.RouteLink)
	e.everyAgentWorks(map[string]int{"f1": 0}, store.RouteSSH)

	// restart with mixed routes
	e.leaveAll(e.m)
	e.m.close()
	m2 := e.restartAs(ids)
	l2, _ := m2.get(e.ctx, e.h.ID)
	e.waitUp(l2)
	ps := e.relayPorts(l2)
	slices.Sort(ps)
	want := []int{ids["f0"], ids["f2"]}
	slices.Sort(want)
	if !slices.Equal(ps, want) {
		t.Fatalf("sync after the restart: relay ports %v, want %v (not f1's %d)", ps, want, failPort)
	}
	e.everyAgentWorks(map[string]int{"f0": 0, "f2": 0}, store.RouteLink)
	e.everyAgentWorks(map[string]int{"f1": 0}, store.RouteSSH)

	// full fallback with mixed routes
	var mu sync.Mutex
	var relayGone time.Time
	var forwardsAt []time.Time
	plain := m2.plain
	m2.plain = func(ctx context.Context, h *store.Host, script string, args ...string) (string, string, error) {
		out, errOut, err := plain(ctx, h, script, args...)
		if script == fenceScript && slices.Contains(args, "free-ports") {
			mu.Lock()
			relayGone = time.Now() // end-relay was before it, and the ports are free now
			mu.Unlock()
		}
		return out, errOut, err
	}
	agent.SetTestForwardHook(func(cmd *exec.Cmd, fw []string) {
		e.sawForward(cmd, fw)
		p, _ := strconv.Atoi(strings.Split(fw[0], ":")[1])
		if p != failPort {
			mu.Lock()
			forwardsAt = append(forwardsAt, time.Now())
			mu.Unlock()
		}
	})
	l2.op.Lock()
	h, _ := m2.hostOf(e.ctx, e.h.ID)
	l2.fallBackLocked(e.ctx, h, "a test")
	l2.op.Unlock()
	e.waitFor(60*time.Second, "everyone on ssh", func() bool { return e.countRoute(ids, store.RouteSSH) == 3 })
	e.everyAgentWorks(ids, store.RouteSSH)
	mu.Lock()
	defer mu.Unlock()
	if relayGone.IsZero() || len(forwardsAt) < 2 {
		t.Fatalf("relay gone at %v, forwards %v", relayGone, forwardsAt)
	}
	for _, at := range forwardsAt {
		if at.Before(relayGone) {
			t.Fatalf("a forward was restored at %v, before the relay's listeners were gone at %v", at, relayGone)
		}
	}
	if out := e.run(`cat "$HOME"/.pocket-ai/relay/*.fence`); !strings.Contains(out, `"pid":0`) {
		t.Fatalf("fence: %s", out)
	}
	stop()
}

// startStale starts a relay as a stale controller would: with gen, over
// plain ssh, without going through the state machine.
func (e *e2e) startStale(gen int64) error {
	cmd, err := plainRemoteOf(e.h).LinkCommand(e.ctx, relayScript, e.bin, e.m.installationID(), strconv.FormatInt(e.h.ID, 10), strconv.FormatInt(gen, 10), randomToken(), "")
	if err != nil {
		return err
	}
	c, err := startRelay(e.ctx, cmd, relayDialOptions{Installation: e.m.installationID(), Entry: e.h.ID, Generation: gen, Token: "x"})
	if err == nil {
		c.Close()
	}
	return err
}

// An old relay's reconnect during Switch back is refused by the fence, and
// the switch back completes; after it, one is refused as "off".
func TestLinkE2EOldRelayDuringSwitchBack(t *testing.T) {
	e := newE2E(t)
	l := e.link(e.m, store.LinkAgents)
	for i := 0; i < 3; i++ {
		e.startAgent(e.m, fmt.Sprintf("o%d", i), freeTestPort(), false)
	}
	ids := e.portsOf("o")
	e.waitFor(60*time.Second, "all on the link", func() bool { return e.countRoute(ids, store.RouteLink) == 3 })
	l.mu.Lock()
	gen := l.gen
	l.mu.Unlock()
	refused := make(chan error, 1)
	go func() {
		e.waitFor(60*time.Second, "the first agent back", func() bool { return e.countRoute(ids, store.RouteSSH) >= 1 })
		refused <- e.startStale(gen) // the relay of before, reconnecting
	}()
	if err := l.switchBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	var ref *relayRefused
	if err := <-refused; !errors.As(err, &ref) {
		t.Fatalf("an old relay during switch back: %v", err)
	}
	t.Logf("during switch back: %v", ref)
	e.everyAgentWorks(ids, store.RouteSSH)
	if err := e.startStale(gen); !errors.As(err, &ref) || ref.Reason != "off" {
		t.Fatalf("after switch back: %v", err)
	}
}

// A stored tools port held by an unrelated program: that program is left
// alone through cutover and switch back; the agent runs without its tools
// and its thread says why.
func TestLinkE2EUnrelatedProgramOnPort(t *testing.T) {
	e := newE2E(t)
	stop := e.watchOwners(func() *linkManager { return e.m })
	l := e.link(e.m, store.LinkAgents)
	// this test process plays the unrelated program, on the agent's port there
	var other net.Listener
	var port int
	for {
		port = freeTestPort()
		var err error
		if other, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
	}
	defer other.Close()
	var otherHits atomic.Int64
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			otherHits.Add(1)
			c.Close()
		}
	}()
	a := e.startAgent(e.m, "p0", port, false)
	got := e.turn(a, "lines 1", 60*time.Second)
	notice := false
	_, all := a.sink.count()
	for _, s := range all {
		if strings.HasPrefix(s, "notice: Pocket tools are unavailable") {
			notice = true
		}
	}
	if !notice || got[len(got)-1] == "tool "+relay.ProbeAnswer {
		t.Fatalf("no word of the tools, or tools through someone else's port: %v", all)
	}
	if r := e.route("p0"); r != store.RouteLink {
		t.Fatalf("route %q", r)
	}
	if err := l.switchBack(e.ctx); err != nil {
		t.Logf("switch back: %v", err)
	}
	e.waitFor(60*time.Second, "p0 on ssh", func() bool { return e.route("p0") == store.RouteSSH })
	got = e.turn(a, "lines 1", 60*time.Second)
	if got[len(got)-1] == "tool "+relay.ProbeAnswer {
		t.Fatalf("tools through someone else's port: %v", got)
	}
	c, err := net.DialTimeout("tcp", other.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("the unrelated program's port is gone: %v", err)
	}
	c.Close()
	if a.hits.Load() != 0 {
		t.Fatal("the agent's endpoint was reached through someone else's port")
	}
	stop()
}

// Pocket AI updated while the machine is on the link: the new process
// uploads its own pocket-hold and runs that relay (the old one is taken
// over), without anyone pressing anything; the agents go on.
func TestLinkE2EUpdate(t *testing.T) {
	e := newE2E(t)
	e.link(e.m, store.LinkAgents)
	for i := 0; i < 2; i++ {
		e.startAgent(e.m, fmt.Sprintf("v%d", i), freeTestPort(), false)
	}
	ids := e.portsOf("v")
	e.waitFor(60*time.Second, "all on the link", func() bool { return e.countRoute(ids, store.RouteLink) == 2 })
	oldPID := e.run(`sed -n 's/.*"pid":\([0-9]*\).*/\1/p' "$HOME"/.pocket-ai/relay/*.fence`)

	// the new build: another pocket-hold (same code, other bytes: another content hash)
	data, _, _ := holdBinaryForTest()
	data = append(slices.Clone(data), []byte("\n# a newer build\n")...)
	sum := sha256.Sum256(data)
	newName := "pocket-hold-" + hex.EncodeToString(sum[:6])
	e.leaveAll(e.m)
	e.m.close()
	forgetHolder(e.h.ID)
	newInstall := func(ctx context.Context, h *store.Host) (string, error) {
		path := e.home + "/.pocket-ai/bin/" + newName
		r := plainRemoteOf(h)
		if out, _, _ := r.Run(agent.PlainSSH(ctx), 20*time.Second, `[ -x "$1" ] && echo there`, path); strings.TrimSpace(out) != "there" {
			if err := r.Put(agent.PlainSSH(ctx), time.Minute, path, data); err != nil {
				return "", err
			}
			if _, _, err := r.Run(agent.PlainSSH(ctx), 20*time.Second, `chmod 700 "$1"`, path); err != nil {
				return "", err
			}
		}
		return path, nil
	}
	m2 := newLinkManager(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m2.install = newInstall
	m2.start(e.ctx)
	e.mu.Lock()
	e.m = m2
	e.agents = map[string]*e2eAgent{}
	e.mu.Unlock()
	l2, _ := m2.get(e.ctx, e.h.ID)
	e.waitUp(l2)
	l2.mu.Lock()
	hello := l2.conn.Hello()
	l2.mu.Unlock()
	if "pocket-hold-"+hello.Sum != newName {
		t.Fatalf("the relay runs %s, not the new %s", hello.Sum, newName)
	}
	newPID := e.run(`sed -n 's/.*"pid":\([0-9]*\).*/\1/p' "$HOME"/.pocket-ai/relay/*.fence`)
	if args := e.run(`ps -o args= -p "$1"`, strings.TrimSpace(newPID)); !strings.Contains(args, newName) || strings.TrimSpace(newPID) == strings.TrimSpace(oldPID) {
		t.Fatalf("relay pid %s (was %s): %s", newPID, oldPID, args)
	}
	for id, port := range ids {
		e.startAgent(m2, id, port, true)
	}
	e.everyAgentWorks(ids, store.RouteLink)
}

// holdStreams is a link that keeps the hold streams it opened, so a test
// can cut one.
type holdStreams struct {
	linkConn
	mu      sync.Mutex
	streams map[string]net.Conn
}

func (c *holdStreams) OpenHold(ctx context.Context, slot string) (net.Conn, error) {
	s, err := c.linkConn.OpenHold(ctx, slot)
	if err == nil {
		c.mu.Lock()
		c.streams[slot] = s
		c.mu.Unlock()
	}
	return s, err
}

// Phase 3: on the link an agent keeps its stream. An agent the server no
// longer tracks (its stream closed) is ended by its holder after the idle
// time, as today; one whose stream stays open is not. A busy agent whose
// stream alone is lost is reattached at the right offset and its turn is
// never cut.
func TestLinkE2EPhase3Streams(t *testing.T) {
	e := newE2E(t)
	var hs *holdStreams
	e.conns = func(c linkConn) linkConn { hs = &holdStreams{linkConn: c, streams: map[string]net.Conn{}}; return hs }
	l := e.link(e.m, store.LinkAgents)

	t.Run("idle expiry", func(t *testing.T) {
		start := func(slot string) {
			e.run(`"$1" attach -start-only -idle 3s "$2" -- /bin/sh -c 'exec sleep 600'`, e.bin, slot)
		}
		start("i-tracked")
		start("i-dropped")
		attach := func(slot string) net.Conn {
			nc, err := l.OpenHold(e.ctx, slot)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := hold.Open(nc, hold.FromReplay); err != nil {
				t.Fatal(err)
			}
			return nc
		}
		tracked := attach("i-tracked")
		defer tracked.Close()
		dropped := attach("i-dropped")
		dropped.Close() // the server stops tracking it
		// the holder looks every 30 s
		e.waitFor(75*time.Second, "the untracked agent's holder to end it", func() bool {
			out := e.run(`"$1" status i-dropped`, e.bin)
			return strings.Contains(out, `"gone":true`) || strings.Contains(out, `"exited":true`)
		})
		time.Sleep(31 * time.Second) // one more look of its holder, well past the idle time
		if out := e.run(`"$1" status i-tracked`, e.bin); strings.Contains(out, `"gone":true`) || strings.Contains(out, `"exited":true`) {
			t.Fatalf("a tracked agent with its stream open was ended: %s", out)
		}
		e.run(`"$1" stop i-tracked`, e.bin)
	})

	t.Run("busy agent loses its stream", func(t *testing.T) {
		a := e.startAgent(e.m, "q0", freeTestPort(), false)
		before, _ := a.sink.count()
		if err := a.sess.Send(agent.Input{Text: "lines 200"}); err != nil {
			t.Fatal(err)
		}
		e.waitFor(30*time.Second, "the turn under way", func() bool {
			_, texts := a.sink.count()
			return len(texts) > 30
		})
		hs.mu.Lock()
		s := hs.streams["q0"]
		hs.mu.Unlock()
		s.Close() // only this agent's stream goes; the link stays
		e.waitFor(60*time.Second, "the turn", func() bool { n, _ := a.sink.count(); return n > before })
		_, texts := a.sink.count()
		e.checkTurn(a, texts, 200)
		if e.starts("q0") != 1 {
			t.Fatal("started twice")
		}
		if !e.m.keepsStream(e.h.ID, "q0") {
			t.Fatal("an agent on the link would be parked")
		}
	})
}

// "Use one connection" for agents, for real: Install, Connect, Test (env,
// a test holder, a tools round trip), Confirm. Cancel at Confirm leaves no
// relay, test holder or bound port, and no fence; Switch moves the agents.
func TestLinkE2EButtonAgents(t *testing.T) {
	e := newE2E(t)
	l := e.link(e.m, store.LinkOff)
	a := e.startAgent(e.m, "w0", freeTestPort(), false)
	f, err := l.begin(store.LinkAgents)
	if err != nil {
		t.Fatal(err)
	}
	flow := func() *linkFlow {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.flow.copy()
	}
	e.waitFor(90*time.Second, "Confirm", func() bool { f := flow(); return f == nil || f.State != "running" })
	f = flow()
	if f.State != "confirm" {
		t.Fatalf("flow %+v", f)
	}
	t.Logf("test step: %s; confirm: %s", f.Steps[2].Detail, f.Confirm)
	if !strings.Contains(f.Steps[2].Detail, "test holder") || !strings.Contains(f.Confirm, "1 running agent") {
		t.Fatalf("flow %+v", f)
	}
	l.cancelFlow()
	time.Sleep(time.Second)
	left := e.run(`ls "$HOME/.pocket-ai/hold" 2>/dev/null | grep -c linktest || true; ls "$HOME/.pocket-ai/relay" 2>/dev/null | grep -c fence || true; ps -eo args= | grep -c "[p]ocket-hold.* relay .*-test" || true`)
	if strings.Fields(left)[0] != "0" || strings.Fields(left)[1] != "0" {
		t.Fatalf("left after Cancel (test holders, fences, test relays): %q", left)
	}
	if e.route("w0") != "" || len(e.liveForwards()) != 1 {
		t.Fatal("the running agent was touched")
	}
	if _, err := l.begin(store.LinkAgents); err != nil {
		t.Fatal(err)
	}
	e.waitFor(90*time.Second, "Confirm", func() bool { f := flow(); return f != nil && f.State == "confirm" })
	if err := l.confirm(e.ctx, store.LinkAgents); err != nil {
		t.Fatal(err)
	}
	e.waitFor(60*time.Second, "the agent on the link", func() bool { return e.route("w0") == store.RouteLink })
	e.everyAgentWorks(map[string]int{"w0": a.port}, store.RouteLink)
}

// Phase 5: a new machine's first check sets it up on the link (agents);
// POCKET_LINK_DEFAULT=off leaves it on ssh per agent with the button.
func TestLinkE2ENewMachineDefault(t *testing.T) {
	e := newE2E(t)
	t.Setenv("POCKET_LINK_DEFAULT", "off")
	e.m.setUpNew(e.h)
	time.Sleep(time.Second)
	if h, _ := e.st.GetHost(e.ctx, e.h.ID); h.LinkMode != store.LinkOff {
		t.Fatal("POCKET_LINK_DEFAULT=off did not keep it off")
	}
	t.Setenv("POCKET_LINK_DEFAULT", "")
	h, _ := e.st.GetHost(e.ctx, e.h.ID)
	t.Setenv("POCKET_HOLD", "") // main_test.go turns holding off for the other tests
	if ok, why := e.m.support(h); !ok {
		t.Fatal(why)
	}
	e.m.setUpNew(h)
	e.waitFor(120*time.Second, "the new machine on the link", func() bool {
		h, _ := e.st.GetHost(e.ctx, e.h.ID)
		if h.LinkMode == store.LinkAgents {
			return true
		}
		if l := e.m.existing(e.h.ID); l != nil {
			l.mu.Lock()
			f := l.flow.copy()
			l.mu.Unlock()
			if f != nil && f.State == "failed" {
				t.Fatalf("the new machine's setup failed: %+v", f)
			}
		}
		return false
	})
	l, _ := e.m.get(e.ctx, e.h.ID)
	e.waitUp(l)
	a := e.startAgent(e.m, "d0", freeTestPort(), false)
	e.checkTurn(a, e.turn(a, "lines 1", 60*time.Second), 1)
	if e.route("d0") != store.RouteLink {
		t.Fatal("not on the link")
	}
	// switched back, it is never switched on again by default
	if err := l.switchBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = e.st.GetHost(e.ctx, e.h.ID)
	e.m.setUpNew(h)
	time.Sleep(time.Second)
	if h, _ := e.st.GetHost(e.ctx, e.h.ID); h.LinkMode != store.LinkOff || h.LinkGeneration == 0 {
		t.Fatalf("after switch back: mode %q generation %d", h.LinkMode, h.LinkGeneration)
	}
}
