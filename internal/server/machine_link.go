package server

// One link per machine (docs/machine-link-plan.md, machine-link-impl.md):
// a relay (pocket-hold relay) started over the machine's ssh, carrying its
// short commands (phase 1: read-only status scripts only) and, from phase 2,
// its agents and their tools.
//
// Each machine's link is driven by one state machine, machineLink. Its
// operations — start, reconnect, fallback, enable (the "Use one connection"
// button), switch back, retry and the emergency off switch — take its op
// lock, so they run one at a time, however many callers ask at once. Its
// state for the card and for UseFor sits behind mu, which is never held
// while waiting on the machine.
//
// States: off (mode off) → connecting → connected ⇄ reconnecting; from
// there fallen-back (sticky, probed every 30 min, back after two passing
// full tests in a row), taken-over (a newer generation or another machine
// entry owns the account: never reconnected by this process), stopped (this
// process lost the controller lease) and emergency-off (POCKET_LINK=off).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Link states, as the card shows them.
const (
	linkOff          = "off"
	linkConnecting   = "connecting"
	linkConnected    = "connected"
	linkReconnecting = "reconnecting"
	linkFallenBack   = "fallen-back"
	linkTakenOver    = "taken-over"
	linkStopped      = "stopped"
	linkEmergencyOff = "emergency-off"
)

var (
	// linkFallbackFails failed starts in a row, spread over at least
	// linkFallbackSpan, make a machine fall back (a single failed reconnect
	// does not).
	linkFallbackFails = 3
	linkFallbackSpan  = 5 * time.Minute
	// linkProbeEvery: a machine that fell back is tested this often; two
	// passing tests in a row move it back.
	linkProbeEvery  = 30 * time.Minute
	linkProbePasses = 2
	// linkBackoffMax: reconnects wait up to this long, as agents do.
	linkBackoffMax = time.Minute
	// linkConfirmFor: a tested machine may be switched within this long.
	linkConfirmFor = 15 * time.Minute
)

// linkClock is the time the links go by (tests move it by hand).
type linkClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// linkRegistry: host id → its machineLink, for remoteOf (agent.Remote.Link).
var linkRegistry sync.Map

// machineLinkOf is the link remoteOf attaches to a machine's Remote; nil
// when the machine has none.
func machineLinkOf(id int64) agent.RemoteLink {
	if v, ok := linkRegistry.Load(id); ok {
		return v.(*machineLink)
	}
	return nil
}

// linkManager owns every machine's link in this process.
type linkManager struct {
	st    *store.Store
	log   *slog.Logger
	clock linkClock
	lease *controllerLease
	// off: POCKET_LINK=off, the server-wide emergency switch.
	off bool
	// loops: reconnect and probe loops run (off in unit tests, which drive
	// the operations by hand).
	loops bool

	// The world, replaced in tests.
	hostOf   func(ctx context.Context, id int64) (*store.Host, error)
	install  func(ctx context.Context, h *store.Host) (string, error) // pocket-hold there, over plain ssh
	plain    func(ctx context.Context, h *store.Host, script string, args ...string) (string, string, error)
	relayCmd func(ctx context.Context, h *store.Host, script string, args ...string) (*exec.Cmd, error)
	support  func(h *store.Host) (bool, string)
	dial     func(ctx context.Context, cmd *exec.Cmd, o relayDialOptions) (linkConn, error)
	onChange func(hostID int64)

	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	links        map[int64]*machineLink
	installation string
}

func newLinkManager(st *store.Store, log *slog.Logger) *linkManager {
	m := &linkManager{st: st, log: log, clock: realClock{}, links: map[int64]*machineLink{},
		off: os.Getenv("POCKET_LINK") == "off", loops: true, dial: startRelay}
	m.lease = newControllerLease(st, log, func() time.Time { return m.clock.Now() })
	m.lease.onLost = m.stopAll
	m.lease.onGained = m.resumeAll
	m.hostOf = func(ctx context.Context, id int64) (*store.Host, error) { return st.GetHost(ctx, id) }
	m.plain = func(ctx context.Context, h *store.Host, script string, args ...string) (string, string, error) {
		return plainRemoteOf(h).Run(agent.PlainSSH(ctx), 30*time.Second, script, args...)
	}
	m.relayCmd = func(ctx context.Context, h *store.Host, script string, args ...string) (*exec.Cmd, error) {
		return plainRemoteOf(h).LinkCommand(ctx, script, args...)
	}
	m.install = linkInstall
	m.support = linkSupport
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// start: the installation id, then the lease; links come up once it is held.
func (m *linkManager) start(ctx context.Context) {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Unlock()
	if id, err := m.st.InstallationID(ctx); err == nil {
		m.mu.Lock()
		m.installation = id
		m.mu.Unlock()
	} else if m.log != nil {
		m.log.Warn("machine links: no installation id", "err", err)
	}
	go m.lease.run(m.ctx)
}

// close stops every link and gives the lease up (shutdown).
func (m *linkManager) close() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	cancel()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	m.lease.release(ctx)
	m.mu.Lock()
	for id := range m.links {
		linkRegistry.Delete(id)
	}
	m.mu.Unlock()
}

func (m *linkManager) installationID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installation
}

func (m *linkManager) baseCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// get is a machine's state machine, made on first use from what is stored.
func (m *linkManager) get(ctx context.Context, id int64) (*machineLink, error) {
	m.mu.Lock()
	l := m.links[id]
	m.mu.Unlock()
	if l != nil {
		return l, nil
	}
	h, err := m.hostOf(ctx, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.links[id]; l != nil {
		return l, nil
	}
	l = &machineLink{m: m, id: id, name: h.Name, mode: firstNonEmpty(h.LinkMode, store.LinkOff), fallback: h.LinkFallback,
		gen: h.LinkGeneration, changed: make(chan struct{}), kick: make(chan struct{}, 1),
		agents: map[string]*linkAgent{}, bound: map[int]bool{}, gate: relay.NewGate(relay.DefaultCatchUp)}
	l.state = l.restingState()
	m.links[id] = l
	if l.mode != store.LinkOff {
		linkRegistry.Store(id, l)
	}
	return l, nil
}

// forget drops a machine's link (the machine entry is being deleted, while
// its row is still there): the relay is ended and the fence there released
// (fenced, over plain ssh), so the account is not locked to a deleted entry.
func (m *linkManager) forget(id int64) {
	l, err := m.get(m.baseCtx(), id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.baseCtx(), 45*time.Second)
	defer cancel()
	if err := l.release(ctx); err != nil && m.log != nil {
		m.log.Warn("machine link: could not release the fence of a removed machine", "machine", l.machineName(), "err", err)
	}
	m.mu.Lock()
	delete(m.links, id)
	m.mu.Unlock()
	linkRegistry.Delete(id)
	l.drop("the machine was removed", linkOff)
}

// reset closes a machine's link without touching its mode: it was edited
// (another address), and the next start goes to the new one.
func (m *linkManager) reset(id int64) {
	m.mu.Lock()
	l := m.links[id]
	m.mu.Unlock()
	if l != nil {
		l.drop("the machine was edited", "")
		l.kickLoop()
	}
}

// stopAll: the lease is lost. Every link closes at once, without waiting
// for an operation in progress (whose start notices and gives up).
func (m *linkManager) stopAll() {
	m.mu.Lock()
	list := make([]*machineLink, 0, len(m.links))
	for _, l := range m.links {
		list = append(list, l)
	}
	m.mu.Unlock()
	for _, l := range list {
		l.stopLoop()
		l.drop("another Pocket AI process drives the links now", linkStopped)
	}
}

// resumeAll: the lease is held (again). Machines that asked for the link get
// it back; with POCKET_LINK=off their fences are advanced instead.
func (m *linkManager) resumeAll() {
	ctx := m.baseCtx()
	hosts, err := m.st.ListHosts(ctx)
	if err != nil {
		return
	}
	for _, h := range hosts {
		if firstNonEmpty(h.LinkMode, store.LinkOff) == store.LinkOff {
			// a switch back that a restart cut short: its agents' routes
			// still name the link; finish it (the fence there goes off,
			// the relay's ports are freed, the routes become ssh)
			if routes, err := m.st.ListAgentRoutes(ctx, h.ID); err == nil && slices.ContainsFunc(routes, func(r *store.AgentRoute) bool { return r.Route != store.RouteSSH }) {
				if l, err := m.get(ctx, h.ID); err == nil {
					go l.finishSwitchBack(ctx)
				}
			}
			continue
		}
		l, err := m.get(ctx, h.ID)
		if err != nil {
			continue
		}
		if m.off {
			go func() { _ = l.emergencyOff(ctx) }()
			continue
		}
		l.mu.Lock()
		if l.state == linkStopped {
			l.state = l.restingState()
		}
		l.mu.Unlock()
		l.startLoop()
	}
}

// machineLink is one machine's link and its state machine.
type machineLink struct {
	m  *linkManager
	id int64

	op sync.Mutex // one operation at a time

	mu         sync.Mutex // the fields below
	name       string
	mode       string
	state      string
	conn       linkConn
	gen        int64 // the generation the current (or last) run-mode relay was started with
	lastErr    string
	lastErrAt  time.Time
	fails      []time.Time // failed starts in a row
	fallback   *store.LinkFallback
	probeOK    int // passing full tests in a row while fallen back
	takenOver  string
	commands   int64
	rtt        time.Duration // the last test's round trip, when not connected
	starts     int           // run-mode relay starts made
	busy       string        // "retrying", "switching back": an operation the card waits for
	flow       *linkFlow
	loopCancel context.CancelFunc
	version    int64
	changed    chan struct{} // closed and replaced on every change (GET ?wait=1)
	kick       chan struct{} // wakes the loop (Retry link passed, the machine was edited)

	// phase 2 (machine_link_agents.go)
	agents        map[string]*linkAgent // held agents on this machine, by thread
	bound         map[int]bool          // ports the current relay listens on (bind/sync acknowledged)
	gate          *relay.Gate           // hold streams catching up at once
	converging    bool
	convergeAgain bool
	removed       bool // the machine entry is being deleted
}

// restingState is the state for the stored mode and fallback, before any
// start. Callers hold mu or own l.
func (l *machineLink) restingState() string {
	switch {
	case l.mode == store.LinkOff:
		return linkOff
	case l.m.off:
		return linkEmergencyOff
	case l.fallback != nil:
		return linkFallenBack
	}
	return linkConnecting
}

// bump records a change for the card. Callers hold mu.
func (l *machineLink) bump() {
	l.version++
	close(l.changed)
	l.changed = make(chan struct{})
	if l.m.onChange != nil {
		go l.m.onChange(l.id)
	}
}

func (l *machineLink) setErr(msg string) {
	l.lastErr, l.lastErrAt = msg, l.m.clock.Now()
}

// drop closes the current relay (if any) and sets state (unless "").
func (l *machineLink) drop(why, state string) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.bound = map[int]bool{}
	if state != "" {
		l.state = state
	} else if c != nil && l.state == linkConnected {
		l.state = linkReconnecting
	}
	if why != "" && c != nil {
		l.setErr(why)
	}
	l.bump()
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// ---------- agent.RemoteLink ----------

// UseFor: the link is up and the machine's mode lets it carry kind.
func (l *machineLink) UseFor(kind agent.LinkUse) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m.off || l.conn == nil || l.state != linkConnected || l.fallback != nil || l.takenOver != "" {
		return false
	}
	switch l.mode {
	case store.LinkStatus:
		return kind == agent.LinkStatus
	case store.LinkAgents:
		return true
	}
	return false
}

// Exec runs a script over the link (agent.Remote's short commands).
func (l *machineLink) Exec(ctx context.Context, script string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	l.mu.Lock()
	c := l.conn
	if c != nil {
		l.commands++
	}
	l.mu.Unlock()
	if c == nil {
		return 0, agent.ErrLinkNotSent
	}
	res, err := c.Exec(ctx, linkExecRequest{Script: script, Args: args, Stdin: stdin, Stdout: stdout, Stderr: stderr})
	switch {
	case errors.Is(err, errRelayNotSent):
		return 0, agent.ErrLinkNotSent
	case errors.Is(err, errRelayUnknown):
		return 0, agent.ErrLinkUnknown
	case err != nil:
		return 0, err
	}
	if res.Signal != "" && res.Code == 0 {
		res.Code = 128
	}
	return res.Code, nil
}

// OpenHold is a hold stream to slot's holder over the link.
func (l *machineLink) OpenHold(ctx context.Context, slot string) (net.Conn, error) {
	return l.dialHold(ctx, slot)
}

// BindTools makes the relay listen on remotePort (acknowledged). Agents'
// ports are bound by their route (agentPath); this is for callers outside
// an agent's connection.
func (l *machineLink) BindTools(ctx context.Context, _ string, remotePort int, _ string) error {
	l.mu.Lock()
	c := l.conn
	l.mu.Unlock()
	if c == nil {
		return errLinkDown
	}
	if err := c.Bind(ctx, remotePort); err != nil {
		return err
	}
	l.mu.Lock()
	if l.conn == c {
		l.bound[remotePort] = true
	}
	l.mu.Unlock()
	return nil
}

// UnbindTools closes the relay's listener on remotePort (acknowledged).
func (l *machineLink) UnbindTools(ctx context.Context, _ string, remotePort int) error {
	l.mu.Lock()
	c := l.conn
	l.mu.Unlock()
	if c == nil {
		return errLinkDown
	}
	if err := c.Unbind(ctx, remotePort); err != nil {
		return err
	}
	l.mu.Lock()
	if l.conn == c {
		delete(l.bound, remotePort)
	}
	l.mu.Unlock()
	return nil
}

// ---------- starting ----------

// linkTry is how one ensure went.
type linkTry int

const (
	tryUp    linkTry = iota // connected (it was, or now is)
	tryAgain                // failed: try again after the backoff
	tryStop                 // nothing to do: off, fallen back, taken over, stopped
)

// ensure brings the link up if the machine wants one and it is down: one
// start at a time, however many callers ask. Those that waited find it up.
func (l *machineLink) ensure(ctx context.Context) linkTry {
	l.op.Lock()
	defer l.op.Unlock()
	l.mu.Lock()
	switch {
	case l.mode == store.LinkOff, l.fallback != nil, l.takenOver != "", l.m.off:
		l.mu.Unlock()
		return tryStop
	case l.conn != nil:
		l.mu.Unlock()
		return tryUp
	}
	l.mu.Unlock()
	if !l.m.lease.Held() {
		l.mu.Lock()
		l.state = linkStopped
		l.setErr("another Pocket AI process drives the links now")
		l.bump()
		l.mu.Unlock()
		return tryStop
	}
	return l.startLocked(ctx)
}

// startLocked starts a run-mode relay. Callers hold op.
func (l *machineLink) startLocked(ctx context.Context) linkTry {
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		l.mu.Lock()
		l.setErr("the machine is not set up any more")
		l.bump()
		l.mu.Unlock()
		return tryStop
	}
	l.mu.Lock()
	l.name = h.Name
	if l.state != linkReconnecting {
		l.state = linkConnecting
	}
	l.bump()
	l.mu.Unlock()

	conn, gen, bin, err := l.dialRun(ctx, h)
	if err == nil {
		if err = checkHello(conn.Hello(), bin, false); err != nil {
			_ = conn.Close()
		}
	}
	if err == nil && !l.m.lease.Held() {
		// the lease went while it started: this process may not drive it
		_ = conn.Close()
		err = store.ErrNotLeaseHolder
	}
	l.mu.Lock()
	removed := l.removed
	l.mu.Unlock()
	if err == nil && removed {
		_ = conn.Close()
		return tryStop
	}
	if err == nil {
		l.mu.Lock()
		l.gen = gen // fenced actions of the reconcile carry it
		l.mu.Unlock()
		// before anyone uses it: routes settled, the relay's ports bound
		if err = l.afterConnect(ctx, conn, bin); err != nil {
			_ = conn.Close()
		}
	}
	if err != nil {
		return l.startFailed(ctx, h, gen, err)
	}
	l.mu.Lock()
	l.conn, l.gen, l.state, l.fails = conn, gen, linkConnected, nil
	l.lastErr = ""
	l.bump()
	l.mu.Unlock()
	go l.watch(conn)
	go l.converge(l.m.baseCtx())
	return tryUp
}

// dialRun allocates a generation (only the lease holder can) and starts the
// relay with it.
func (l *machineLink) dialRun(ctx context.Context, h *store.Host) (linkConn, int64, string, error) {
	bin, err := l.m.install(ctx, h)
	if err != nil {
		return nil, 0, "", err
	}
	gen, err := l.m.lease.allocate(ctx, l.id)
	if err != nil {
		return nil, 0, bin, err
	}
	l.mu.Lock()
	l.starts++
	// from now the fence there may have it: per-agent connections' checks
	// and cleanups carry it (an older one would be refused)
	l.gen = max(l.gen, gen)
	l.mu.Unlock()
	token := randomToken()
	// the relay lives as long as this process drives links, not as long as
	// whoever asked for it first
	cmd, err := l.m.relayCmd(l.m.baseCtx(), h, relayScript, bin, l.m.installationID(), strconv.FormatInt(l.id, 10), strconv.FormatInt(gen, 10), token, "")
	if err != nil {
		return nil, gen, bin, err
	}
	conn, err := l.m.dial(ctx, cmd, relayDialOptions{Installation: l.m.installationID(), Entry: l.id, Generation: gen, Token: token, Tools: l.toolsDial})
	return conn, gen, bin, err
}

// relayScript starts pocket-hold's relay: $1 the binary, then installation,
// entry, generation, token, and "1" for a test-mode candidate.
const relayScript = `exec "$1" relay -installation "$2" -entry "$3" -generation "$4" -token "$5" ${6:+-test}`

// startFailed decides what a failed start means. Callers hold op.
func (l *machineLink) startFailed(ctx context.Context, h *store.Host, gen int64, err error) linkTry {
	now := l.m.clock.Now()
	if gen > 0 {
		// the newest generation this process allocated: what a fenced end
		// of whatever started with it must carry
		l.mu.Lock()
		l.gen = gen
		l.mu.Unlock()
	}
	var ref *relayRefused
	if errors.As(err, &ref) && ref.Reason != "busy" {
		// a newer generation or another entry owns the account: never
		// reconnect to it from here (that would be a back-and-forth takeover)
		why := ref.Error()
		if ref.Reason == "other-entry" && ref.Entry > 0 {
			if o, e := l.m.hostOf(ctx, ref.Entry); e == nil {
				why = "the machine entry “" + o.Name + "” in this Pocket AI owns the link to that account"
			}
		}
		l.mu.Lock()
		l.takenOver, l.state = why, linkTakenOver
		l.setErr(why)
		l.bump()
		l.mu.Unlock()
		return tryStop
	}
	if errors.Is(err, store.ErrNotLeaseHolder) {
		l.mu.Lock()
		l.state = linkStopped
		l.setErr("another Pocket AI process drives the links now")
		l.bump()
		l.mu.Unlock()
		return tryStop
	}
	msg := oneLine(err.Error(), 300)
	var hs *relayHandshakeError
	if errors.As(err, &hs) {
		// a mismatch will not fix itself
		if l.plainWorks(ctx, h) {
			l.fallBackLocked(ctx, h, "the link did not start: "+msg)
			return tryStop
		}
	}
	l.mu.Lock()
	l.fails = append(l.fails, now)
	n, first := len(l.fails), l.fails[0]
	l.setErr(msg)
	if l.state == linkConnected || l.state == linkConnecting && n > 1 {
		l.state = linkReconnecting
	}
	l.bump()
	l.mu.Unlock()
	if n >= linkFallbackFails && now.Sub(first) >= linkFallbackSpan && l.plainWorks(ctx, h) {
		l.fallBackLocked(ctx, h, fmt.Sprintf("the link failed to start %d times in a row over %s (%s)", n, now.Sub(first).Round(time.Minute), msg))
		return tryStop
	}
	return tryAgain
}

// plainWorks: the machine answers over plain ssh (a fallback needs it).
func (l *machineLink) plainWorks(ctx context.Context, h *store.Host) bool {
	out, _, err := l.m.plain(ctx, h, `echo pocket-ok`)
	return err == nil && strings.Contains(out, "pocket-ok")
}

// watch notices the relay ending.
func (l *machineLink) watch(c linkConn) {
	<-c.Done()
	l.mu.Lock()
	if l.conn != c {
		l.mu.Unlock()
		return
	}
	l.conn = nil
	l.bound = map[int]bool{}
	if l.state == linkConnected {
		l.state = linkReconnecting
	}
	msg := "the link ended"
	if err := c.Err(); err != nil {
		msg = "the link dropped: " + oneLine(err.Error(), 300)
	}
	l.setErr(msg)
	l.bump()
	l.mu.Unlock()
	_ = c.Close()
	l.kickLoop()
}

// ---------- fallback ----------

// fallBackLocked moves the whole machine to ssh per agent: the relay is ended
// over plain ssh (fenced), the ports it owned are freed, and only then are
// the agents' own forwards restored. Sticky until two passing tests or
// "Retry link". Callers hold op.
func (l *machineLink) fallBackLocked(ctx context.Context, h *store.Host, why string) {
	l.mu.Lock()
	c, gen := l.conn, l.gen
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	notes := l.releaseRelayLocked(ctx, h, gen, "end-relay")
	fb := &store.LinkFallback{At: store.FormatTime(l.m.clock.Now()), Why: why}
	_ = l.m.st.SetHostLinkFallback(ctx, l.id, fb)
	l.mu.Lock()
	l.fallback, l.state, l.probeOK, l.fails = fb, linkFallenBack, 0, nil
	l.setErr(strings.Join(append([]string{why}, notes...), "; "))
	l.bump()
	l.mu.Unlock()
	if l.m.log != nil {
		l.m.log.Warn("machine link: fell back to ssh per agent", "machine", h.Name, "why", why)
	}
}

// releaseRelayLocked ends this machine's relay over plain ssh with a fenced
// action (end-relay, or off with a new generation), then frees the ports the
// relay owned and records those agents as back on ssh: every relay listener
// goes before any forward is restored. It returns what did not work.
func (l *machineLink) releaseRelayLocked(ctx context.Context, h *store.Host, gen int64, action string) []string {
	var notes []string
	bin, err := l.m.install(ctx, h)
	if err != nil {
		return []string{"could not reach pocket-hold there: " + oneLine(err.Error(), 200)}
	}
	if gen > 0 {
		if err := l.fence(ctx, h, bin, gen, action); err != nil {
			notes = append(notes, "ending the relay there: "+oneLine(err.Error(), 200))
		}
	}
	routes, _ := l.m.st.ListAgentRoutes(ctx, l.id)
	var ports []string
	for _, r := range routes {
		if r.Route != store.RouteSSH && r.Port > 0 {
			ports = append(ports, strconv.Itoa(r.Port))
		}
	}
	if len(ports) > 0 && gen > 0 {
		if err := l.fence(ctx, h, bin, gen, "free-ports", ports...); err != nil {
			if !strings.Contains(err.Error(), relay.NotAnAgent) {
				notes = append(notes, "freeing the tools ports there: "+oneLine(err.Error(), 200))
				return notes // their forwards are not restored over a port that may be held
			}
			// another program has one of them: it is left alone, and that
			// agent's forward fails on its own (its thread says why)
			notes = append(notes, oneLine(err.Error(), 200))
		}
	}
	// every relay listener is gone: now (and only now) the moved agents may
	// have their own forwards again; each one waiting for its port
	// (agentPath) takes the per-agent path at its next connection
	for _, r := range routes {
		if r.Route != store.RouteSSH {
			r.Route = store.RouteSSH
			_ = l.m.st.SetAgentRoute(ctx, r)
		}
	}
	l.mu.Lock()
	l.bump()
	l.mu.Unlock()
	return notes
}

// fence runs a fenced action through pocket-hold over plain ssh. Exit 3 is a
// refusal, with its reason as JSON on stdout.
func (l *machineLink) fence(ctx context.Context, h *store.Host, bin string, gen int64, action string, args ...string) error {
	a := append([]string{bin, l.m.installationID(), strconv.FormatInt(l.id, 10), strconv.FormatInt(gen, 10), action}, args...)
	out, errOut, err := l.m.plain(ctx, h, fenceScript, a...)
	if err == nil {
		return nil
	}
	var code interface{ ExitCode() int }
	if errors.As(err, &code) && code.ExitCode() == 3 {
		ref := &relayRefused{}
		_ = json.Unmarshal([]byte(lastLine(out)), &struct {
			Reason     *string `json:"reason"`
			Entry      *int64  `json:"entry"`
			Generation *int64  `json:"generation"`
			Error      *string `json:"error"`
		}{&ref.Reason, &ref.Entry, &ref.Generation, &ref.Msg})
		return ref
	}
	return errors.New(sshError(errOut, err))
}

// fenceScript: $1 pocket-hold, $2 installation, $3 entry, $4 generation,
// $5 the action, then its arguments.
const fenceScript = `b=$1; i=$2; e=$3; g=$4; shift 4; exec "$b" fence -installation "$i" -entry "$e" -generation "$g" "$@"`

// probe runs the full test once while the machine is fallen back; after
// linkProbePasses passing tests in a row the fallback is cleared and the
// link starts again. It reports whether the machine is back.
func (l *machineLink) probe(ctx context.Context) (bool, error) {
	l.op.Lock()
	defer l.op.Unlock()
	return l.probeLocked(ctx)
}

func (l *machineLink) probeLocked(ctx context.Context) (bool, error) {
	l.mu.Lock()
	if l.fallback == nil {
		l.mu.Unlock()
		return true, nil
	}
	l.mu.Unlock()
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		return false, err
	}
	_, err = l.fullTest(ctx, h, nil)
	l.mu.Lock()
	if err != nil {
		l.probeOK = 0
		l.setErr("the link test failed: " + oneLine(err.Error(), 300))
		l.bump()
		l.mu.Unlock()
		return false, err
	}
	l.probeOK++
	back := l.probeOK >= linkProbePasses
	if back {
		l.fallback, l.probeOK, l.fails, l.state = nil, 0, nil, linkConnecting
		l.lastErr = ""
	}
	l.bump()
	l.mu.Unlock()
	if back {
		_ = l.m.st.SetHostLinkFallback(ctx, l.id, nil)
		l.kickLoop()
	}
	return back, nil
}

// retry is "Retry link": the full test, twice in a row, now. A machine that
// is not fallen back (it is reconnecting, or was taken over) is simply
// started again.
func (l *machineLink) retry(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.setBusy("retrying")
	defer l.setBusy("")
	l.mu.Lock()
	fallen := l.fallback != nil
	if !fallen {
		// taken over: the person asks to take it back, with a new generation
		l.takenOver = ""
		l.fails = nil
		if l.state == linkTakenOver {
			l.state = linkConnecting
		}
		l.bump()
	}
	l.mu.Unlock()
	if !fallen {
		if l.startIfDown(ctx) == tryAgain {
			l.mu.Lock()
			err := l.lastErr
			l.mu.Unlock()
			l.kickLoop()
			return errors.New(err)
		}
		return nil
	}
	for i := 0; i < linkProbePasses; i++ {
		back, err := l.probeLocked(ctx)
		if err != nil {
			return err
		}
		if back {
			break
		}
	}
	l.startIfDown(ctx)
	return nil
}

// startIfDown is ensure for a caller that holds op.
func (l *machineLink) startIfDown(ctx context.Context) linkTry {
	l.mu.Lock()
	switch {
	case l.mode == store.LinkOff, l.fallback != nil, l.takenOver != "", l.m.off:
		l.mu.Unlock()
		return tryStop
	case l.conn != nil:
		l.mu.Unlock()
		return tryUp
	}
	l.mu.Unlock()
	if !l.m.lease.Held() {
		return tryStop
	}
	return l.startLocked(ctx)
}

func (l *machineLink) setBusy(what string) {
	l.mu.Lock()
	l.busy = what
	l.bump()
	l.mu.Unlock()
}

// ---------- switch back, emergency off ----------

// switchBack returns the machine to ssh per agent: a new generation marked
// "off" on the machine's fence (which ends the relay there, and makes any
// delayed start or cleanup from before find an older generation), then the
// mode off. It goes off here even when the machine cannot be reached; the
// error says the fence could not be advanced.
func (l *machineLink) switchBack(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.setBusy("switching back")
	defer l.setBusy("")
	l.stopLoop()
	// the requested mode is off from here: a restart in the middle finishes
	// the switch back (resumeAll)
	if err := l.m.st.SetHostLinkMode(ctx, l.id, store.LinkOff); err != nil {
		return err
	}
	l.mu.Lock()
	l.mode = store.LinkOff
	l.bump()
	l.mu.Unlock()
	// the agents one at a time, while the relay serves the rest: to-ssh,
	// unbind, the port free, their own forward, ssh
	l.moveAll(ctx, store.RouteSSH)
	// then the relay exits, and the fence goes to a new generation marked
	// off; agents that did not move are put back like in a fallback (every
	// relay listener gone before any forward)
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.bound = map[int]bool{}
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	var notes []string
	if h, err := l.m.hostOf(ctx, l.id); err == nil {
		notes = l.advanceFenceLocked(ctx, h)
	}
	_ = l.m.st.SetHostLinkFallback(ctx, l.id, nil)
	linkRegistry.Delete(l.id)
	l.mu.Lock()
	l.mode, l.state, l.fallback, l.takenOver, l.fails, l.probeOK = store.LinkOff, linkOff, nil, "", nil, 0
	l.lastErr = ""
	if len(notes) > 0 {
		l.setErr(strings.Join(notes, "; "))
	}
	l.bump()
	l.mu.Unlock()
	if len(notes) > 0 {
		return errors.New(strings.Join(notes, "; "))
	}
	return nil
}

// finishSwitchBack completes a switch back a restart cut short.
func (l *machineLink) finishSwitchBack(ctx context.Context) {
	l.op.Lock()
	defer l.op.Unlock()
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		return
	}
	if notes := l.advanceFenceLocked(ctx, h); len(notes) > 0 {
		l.mu.Lock()
		l.setErr(strings.Join(notes, "; "))
		l.bump()
		l.mu.Unlock()
	} else if l.m.log != nil {
		l.m.log.Info("machine link: finished a switch back", "machine", h.Name)
	}
}

// advanceFenceLocked allocates a new generation and marks the machine's fence
// "off" with it (ending the relay there), then frees what the relay owned.
func (l *machineLink) advanceFenceLocked(ctx context.Context, h *store.Host) []string {
	gen, err := l.m.lease.allocate(ctx, l.id)
	if err != nil {
		return []string{"the fence there was not advanced: " + err.Error()}
	}
	l.mu.Lock()
	l.gen = gen
	l.mu.Unlock()
	return l.releaseRelayLocked(ctx, h, gen, "off")
}

// emergencyOff (POCKET_LINK=off): the link stays off here, the fence there is
// advanced; the stored mode is kept for when the switch is removed.
func (l *machineLink) emergencyOff(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.stopLoop()
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.state = linkEmergencyOff
	l.bump()
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		return err
	}
	if notes := l.advanceFenceLocked(ctx, h); len(notes) > 0 {
		l.mu.Lock()
		l.setErr(strings.Join(notes, "; "))
		l.bump()
		l.mu.Unlock()
		return errors.New(strings.Join(notes, "; "))
	}
	return nil
}

// ---------- the loop: reconnect, probe ----------

// startLoop keeps the link up while the machine wants it: reconnects with a
// backoff up to a minute, and probes every 30 minutes while fallen back.
func (l *machineLink) startLoop() {
	if !l.m.loops {
		return
	}
	l.mu.Lock()
	if l.loopCancel != nil {
		l.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(l.m.baseCtx())
	l.loopCancel = cancel
	l.mu.Unlock()
	go l.loop(ctx)
}

func (l *machineLink) stopLoop() {
	l.mu.Lock()
	cancel := l.loopCancel
	l.loopCancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *machineLink) kickLoop() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func (l *machineLink) loop(ctx context.Context) {
	defer func() {
		l.mu.Lock()
		l.loopCancel = nil
		l.mu.Unlock()
	}()
	wait := time.Second
	for ctx.Err() == nil {
		l.mu.Lock()
		fallen, off, taken := l.fallback != nil, l.mode == store.LinkOff || l.m.off, l.takenOver != ""
		l.mu.Unlock()
		if off {
			return
		}
		if fallen || taken {
			select {
			case <-ctx.Done():
				return
			case <-l.kick: // Retry link, or an edit
			case <-l.m.clock.After(linkProbeEvery):
				if fallen {
					_, _ = l.probe(ctx)
				}
			}
			continue
		}
		switch l.ensure(ctx) {
		case tryUp:
			wait = time.Second
			select {
			case <-ctx.Done():
				return
			case <-l.kick: // it ended (watch) or was edited
			}
		case tryStop:
			l.mu.Lock()
			stopped := l.state == linkStopped
			l.mu.Unlock()
			if stopped {
				return // resumeAll starts it again with the lease
			}
			select { // fallen back or taken over now (next round), or the machine is gone
			case <-ctx.Done():
				return
			case <-l.kick:
			case <-l.m.clock.After(linkBackoffMax):
			}
		case tryAgain:
			select {
			case <-ctx.Done():
				return
			case <-l.kick:
			case <-l.m.clock.After(wait):
			}
			wait = min(wait*2, linkBackoffMax)
		}
	}
}

// ---------- the button: Install, Connect, Test, Confirm, Switch ----------

type linkStep struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"` // waiting | running | done | failed | skipped
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// linkFlow is one press of "Use one connection".
type linkFlow struct {
	ID       string     `json:"id"`
	Mode     string     `json:"mode"`
	State    string     `json:"state"` // running | confirm | done | failed | cancelled
	Steps    []linkStep `json:"steps"`
	Confirm  string     `json:"confirm,omitempty"` // what will move
	Error    string     `json:"error,omitempty"`
	testedAt time.Time
	cancel   context.CancelFunc
}

func newLinkFlow(mode string) *linkFlow {
	steps := []linkStep{
		{ID: "install", Label: "Install"},
		{ID: "connect", Label: "Connect"},
		{ID: "test", Label: "Test"},
		{ID: "confirm", Label: "Confirm"},
		{ID: "switch", Label: "Switch"},
	}
	for i := range steps {
		steps[i].State = "waiting"
	}
	return &linkFlow{ID: randomToken()[:12], Mode: mode, State: "running", Steps: steps}
}

func (f *linkFlow) copy() *linkFlow {
	if f == nil {
		return nil
	}
	c := *f
	c.Steps = append([]linkStep(nil), f.Steps...)
	return &c
}

// step updates one step of the flow and tells the card.
func (l *machineLink) step(f *linkFlow, id, state, detail, errMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range f.Steps {
		if f.Steps[i].ID == id {
			f.Steps[i].State = state
			if detail != "" {
				f.Steps[i].Detail = detail
			}
			f.Steps[i].Error = errMsg
		}
	}
	if state == "failed" {
		f.State, f.Error = "failed", errMsg
		for i := range f.Steps {
			if f.Steps[i].State == "waiting" {
				f.Steps[i].State = "skipped"
			}
		}
	}
	l.bump()
}

var errLinkBusy = badRequest("the link to this machine is being set up already")

// begin starts the button's flow for mode: Install, Connect and Test run now
// (in the background, one operation of the machine's state machine), then
// it waits at Confirm. Nothing it does touches running agents or the fence.
func (l *machineLink) begin(mode string) (*linkFlow, error) {
	l.mu.Lock()
	if l.flow != nil && l.flow.State == "running" {
		l.mu.Unlock()
		return nil, errLinkBusy
	}
	if l.mode == mode && l.fallback == nil && l.takenOver == "" {
		l.mu.Unlock()
		return nil, badRequest("this machine uses one connection already")
	}
	f := newLinkFlow(mode)
	ctx, cancel := context.WithCancel(l.m.baseCtx())
	f.cancel = cancel
	l.flow = f
	l.bump()
	l.mu.Unlock()
	go func() {
		defer cancel()
		l.op.Lock()
		defer l.op.Unlock()
		l.prepareLocked(ctx, f)
	}()
	return f.copy(), nil
}

// prepareLocked: Install, Connect, Test, then wait at Confirm. Callers hold op.
func (l *machineLink) prepareLocked(ctx context.Context, f *linkFlow) {
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		l.step(f, "install", "failed", "", "the machine is not set up any more")
		return
	}
	if _, err := l.fullTest(ctx, h, f); err != nil {
		return // fullTest marked the step
	}
	l.mu.Lock()
	if f.State != "running" { // cancelled meanwhile
		l.mu.Unlock()
		return
	}
	f.testedAt = l.m.clock.Now()
	f.State = "confirm"
	if f.Mode == store.LinkAgents {
		n := 0
		for _, a := range l.agents {
			if a.sess != nil {
				n++
			}
		}
		running := "No agents run there now; new ones start on the link."
		switch n {
		case 0:
		case 1:
			running = "1 running agent moves over; its Pocket tools pause for a few seconds while it moves."
		default:
			running = strconv.Itoa(n) + " running agents move over, one at a time; each one's Pocket tools pause for a few seconds while it moves."
		}
		f.Confirm = "Agents on " + h.Name + ", their Pocket tools, status checks and commands will use the link. " + running
	} else {
		f.Confirm = "Status checks of " + h.Name + " (its logins and the machine check) will use the link. Running agents keep their own connections."
	}
	l.mu.Unlock()
	l.step(f, "confirm", "running", "Waiting for you to confirm", "")
}

// linkTestResult is what the Test step found.
type linkTestResult struct {
	RTT        time.Duration
	Unexpected []string // environment variables that differ between the two paths
}

// fullTest: Install (the current pocket-hold, over plain ssh), Connect (a
// test-mode candidate relay: handshake and version, no fence, nothing
// bound), Test (env over the candidate and over plain ssh, the round trip),
// then the candidate exits. f (nil for a probe) gets each step's progress.
func (l *machineLink) fullTest(ctx context.Context, h *store.Host, f *linkFlow) (linkTestResult, error) {
	var res linkTestResult
	mark := func(id, state, detail, errMsg string) {
		if f != nil {
			l.step(f, id, state, detail, errMsg)
		}
	}
	mark("install", "running", "", "")
	bin, err := l.m.install(ctx, h)
	if err != nil {
		mark("install", "failed", "", oneLine(err.Error(), 300))
		return res, fmt.Errorf("install: %w", err)
	}
	mark("install", "done", "pocket-hold is on "+h.Name, "")

	mark("connect", "running", "", "")
	token := randomToken()
	cmd, err := l.m.relayCmd(ctx, h, relayScript, bin, l.m.installationID(), strconv.FormatInt(l.id, 10), "0", token, "1")
	var cand linkConn
	probe := newLinkProbe()
	if err == nil {
		cand, err = l.m.dial(ctx, cmd, relayDialOptions{Installation: l.m.installationID(), Entry: l.id, Token: token, Test: true, Tools: probe.dial})
	}
	if err == nil {
		err = checkHello(cand.Hello(), bin, true)
	}
	if err != nil {
		if cand != nil {
			_ = cand.Close()
		}
		mark("connect", "failed", "", oneLine(err.Error(), 300))
		return res, fmt.Errorf("connect: %w", err)
	}
	// the candidate ends with the test, whatever happens
	defer func() { _ = cand.Close() }()
	hello := cand.Hello()
	mark("connect", "done", "relay "+firstNonEmpty(hello.Version, "?")+" on "+strings.TrimSpace(hello.OS+" "+hello.Arch), "")

	mark("test", "running", "", "")
	res, err = l.testCandidate(ctx, h, cand)
	agents := f != nil && f.Mode == store.LinkAgents
	if f == nil {
		l.mu.Lock()
		agents = l.mode == store.LinkAgents
		l.mu.Unlock()
	}
	if err == nil && agents {
		mark("test", "running", "Same environment both ways; now a test holder and a tools round trip", "")
		err = l.testAgents(ctx, cand, bin, probe)
	}
	if err != nil {
		mark("test", "failed", "", oneLine(err.Error(), 300))
		return res, fmt.Errorf("test: %w", err)
	}
	_ = cand.Close()
	select { // the candidate exits before the test counts as done
	case <-cand.Done():
	case <-ctx.Done():
	case <-l.m.clock.After(10 * time.Second):
	}
	l.mu.Lock()
	l.rtt = res.RTT
	l.mu.Unlock()
	done := "Same environment both ways; round trip " + fmtRTT(res.RTT)
	if agents {
		done += "; a test holder and a tools round trip worked"
	}
	mark("test", "done", done, "")
	return res, nil
}

// checkHello: the relay is the build this Pocket AI uploaded, in the mode it
// was asked for. The link protocol was checked by the magic lines already
// (relay.Proto); the build is checked by content: pocket-hold is uploaded
// under a name with its content hash (hold.Binary), and the relay reports
// the hash of the file it runs from. Version strings are not compared: the
// server's comes from git describe, the relay's from -ldflags or the VCS
// revision, and the two need not be spelled alike for the same build.
func checkHello(h linkHello, bin string, test bool) error {
	if h.Test != test {
		if test {
			return &relayHandshakeError{Msg: "the relay did not start in test mode"}
		}
		return &relayHandshakeError{Msg: "the relay started in test mode"}
	}
	base := path.Base(bin)
	if want, ok := strings.CutPrefix(base, "pocket-hold-"); ok && want != "" && h.Sum != want {
		return &relayHandshakeError{Msg: "the relay there is not the pocket-hold this Pocket AI uploaded (" + firstNonEmpty(h.Sum, "no content hash") + ", expected " + want + "; version " + firstNonEmpty(h.Version, "?") + ")"}
	}
	return nil
}

// testCandidate: the environment of a command over the candidate and over
// plain ssh (the same script text both ways), and the round trip.
func (l *machineLink) testCandidate(ctx context.Context, h *store.Host, cand linkConn) (linkTestResult, error) {
	var res linkTestResult
	var viaLink, errb bytes.Buffer
	start := l.m.clock.Now()
	r, err := cand.Exec(ctx, linkExecRequest{Script: agent.RemoteScriptText("env"), Stdout: &viaLink, Stderr: &errb})
	took := l.m.clock.Now().Sub(start)
	if err != nil {
		return res, fmt.Errorf("env over the link: %w", err)
	}
	if r.Code != 0 {
		return res, fmt.Errorf("env over the link exited %d: %s", r.Code, oneLine(errb.String(), 200))
	}
	viaSSH, errOut, err := l.m.plain(ctx, h, "env")
	if err != nil {
		return res, fmt.Errorf("env over ssh: %s", sshError(errOut, err))
	}
	res.Unexpected = envDiff(viaLink.String(), viaSSH)
	if res.RTT = cand.RTT(); res.RTT <= 0 {
		res.RTT = took
	}
	if len(res.Unexpected) > 0 {
		return res, fmt.Errorf("commands would see another environment over the link: %s", strings.Join(res.Unexpected, ", "))
	}
	return res, nil
}

// confirm is the second press: Switch stores the mode, and the link starts.
func (l *machineLink) confirm(ctx context.Context, mode string) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.mu.Lock()
	f := l.flow
	switch {
	case f == nil || f.State != "confirm":
		l.mu.Unlock()
		return badRequest("test the machine first (Use one connection)")
	case f.Mode != mode:
		l.mu.Unlock()
		return badRequest("the machine was tested for “" + f.Mode + "”, not “" + mode + "”")
	case l.m.clock.Now().Sub(f.testedAt) > linkConfirmFor:
		f.State = "failed"
		f.Error = "the test is too old: start again"
		l.bump()
		l.mu.Unlock()
		return badRequest("the test is too old: start again")
	}
	l.mu.Unlock()
	return l.switchLocked(ctx, f, mode)
}

// switchLocked is the Switch step: the mode is stored, the link starts (or,
// up in status mode already, carries agents now), and the running agents
// move over one at a time (converge). Callers hold op.
func (l *machineLink) switchLocked(ctx context.Context, f *linkFlow, mode string) error {
	l.step(f, "confirm", "done", "Confirmed", "")
	l.step(f, "switch", "running", "", "")
	if err := l.m.st.SetHostLinkMode(ctx, l.id, mode); err != nil {
		l.step(f, "switch", "failed", "", err.Error())
		return err
	}
	_ = l.m.st.SetHostLinkFallback(ctx, l.id, nil)
	linkRegistry.Store(l.id, l)
	l.mu.Lock()
	l.mode, l.fallback, l.takenOver, l.fails, l.probeOK = mode, nil, "", nil, 0
	for _, a := range l.agents {
		a.stay = "" // a new press tries every agent again
	}
	if l.conn == nil || l.state != linkConnected {
		l.state = l.restingState()
	}
	f.State = "done"
	l.mu.Unlock()
	done := "Status checks use the link"
	if mode == store.LinkAgents {
		done = "Agents, their tools and commands use the link"
	}
	l.step(f, "switch", "done", done, "")
	l.startLoop()
	if mode == store.LinkAgents {
		go l.converge(l.m.baseCtx())
	}
	return nil
}

// cancelFlow: Cancel at Confirm (or while it tests). The candidate relay has
// exited (or is ended now with the flow's context); the fence was never
// touched; the mode stays as it was.
func (l *machineLink) cancelFlow() {
	l.mu.Lock()
	f := l.flow
	if f == nil {
		l.mu.Unlock()
		return
	}
	if f.State == "running" || f.State == "confirm" {
		f.State = "cancelled"
		for i := range f.Steps {
			if f.Steps[i].State == "waiting" || f.Steps[i].State == "running" {
				f.Steps[i].State = "skipped"
			}
		}
	}
	cancel := f.cancel
	l.flow = nil
	l.bump()
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ---------- the card ----------

// linkView is a machine's link for its card (GET /hosts/{id}/link).
type linkView struct {
	HostID       int64               `json:"host_id"`
	Mode         string              `json:"mode"`
	State        string              `json:"state"`
	Connected    bool                `json:"connected"`
	Supported    bool                `json:"supported"`
	Unsupported  string              `json:"unsupported_why,omitempty"`
	Agents       int                 `json:"agents"`
	Commands     int64               `json:"commands"`
	RTTMs        float64             `json:"rtt_ms,omitempty"`
	LastError    string              `json:"last_error,omitempty"`
	LastErrorAt  string              `json:"last_error_at,omitempty"`
	Fallback     *store.LinkFallback `json:"fallback,omitempty"`
	ProbePasses  int                 `json:"probe_passes,omitempty"`
	TakenOver    string              `json:"taken_over,omitempty"`
	EmergencyOff bool                `json:"emergency_off,omitempty"`
	Busy         string              `json:"busy,omitempty"`
	Flow         *linkFlow           `json:"flow,omitempty"`
	// MoveAgents: "Move agents too" may be offered (a machine in status mode).
	MoveAgents bool  `json:"move_agents"`
	Version    int64 `json:"version"`
}

func (l *machineLink) view(ctx context.Context, h *store.Host) linkView {
	ok, why := l.m.support(h)
	agents := 0
	if routes, err := l.m.st.ListAgentRoutes(ctx, l.id); err == nil {
		for _, r := range routes {
			if r.Route == store.RouteLink {
				agents++
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := linkView{HostID: l.id, Mode: l.mode, State: l.state, Connected: l.conn != nil && l.state == linkConnected,
		Supported: ok, Unsupported: why, Agents: agents, Commands: l.commands, LastError: l.lastErr,
		Fallback: l.fallback, ProbePasses: l.probeOK, TakenOver: l.takenOver, EmergencyOff: l.m.off,
		Busy: l.busy, Flow: l.flow.copy(), Version: l.version,
		MoveAgents: ok && l.mode == store.LinkStatus && l.fallback == nil}
	if !l.lastErrAt.IsZero() && l.lastErr != "" {
		v.LastErrorAt = store.FormatTime(l.lastErrAt)
	}
	rtt := l.rtt
	if v.Connected {
		if r := l.conn.RTT(); r > 0 {
			rtt = r
		}
	}
	if rtt > 0 {
		v.RTTMs = float64(rtt.Microseconds()) / 1000
	}
	return v
}

// waitChange waits until the link's version passes since (or 25 s).
func (l *machineLink) waitChange(ctx context.Context, since int64) {
	l.mu.Lock()
	if l.version > since {
		l.mu.Unlock()
		return
	}
	ch := l.changed
	l.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	case <-time.After(25 * time.Second):
	}
}

// ---------- helpers ----------

// linkSupport: the button can be pressed for h (a pocket-hold build for its
// platform, holding on, this build able to start the relay).
func linkSupport(h *store.Host) (bool, string) {
	switch {
	case !relayAvailable:
		return false, "This Pocket AI build cannot start the link yet."
	case os.Getenv("POCKET_LINK") == "off":
		return false, "The link is switched off on this server (POCKET_LINK=off)."
	case os.Getenv("POCKET_HOLD") == "off":
		return false, "Agents are not held on this server (POCKET_HOLD=off), and the link is part of the holder."
	case h.Info.OS == "":
		return false, "Check the machine first: Pocket AI does not know its system yet."
	}
	goos, goarch := platform(h.Info.OS)
	if _, _, ok := hold.Binary(goos, goarch); !ok {
		return false, "There is no pocket-hold for " + h.Info.OS + " in this build."
	}
	return true, ""
}

// linkInstall is pocket-hold on the machine (uploaded over plain ssh when it
// is not there yet: the content-hashed file, written aside and renamed).
func linkInstall(ctx context.Context, h *store.Host) (string, error) {
	holders.mu.Lock()
	c, ok := holders.at[h.ID]
	holders.mu.Unlock()
	if ok && c.path != "" {
		return c.path, nil
	}
	path, err := installHolder(agent.PlainSSH(ctx), h)
	if err != nil {
		return "", err
	}
	holders.mu.Lock()
	holders.at[h.ID] = holderPath{path, time.Now()}
	holders.mu.Unlock()
	return path, nil
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func fmtRTT(d time.Duration) string {
	if d < 10*time.Millisecond {
		return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) + " ms"
	}
	return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
}

// envExpected: variables that differ between two ssh sessions by nature.
var envExpected = map[string]bool{
	"SSH_CLIENT": true, "SSH_CONNECTION": true, "SSH_TTY": true, "SSH_AUTH_SOCK": true,
	"SHLVL": true, "_": true, "PWD": true, "OLDPWD": true,
	"XDG_SESSION_ID": true, "MOTD_SHOWN": true,
}

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnv reads env's output; a line that does not start a variable
// belongs to the value before it.
func parseEnv(s string) map[string]string {
	out := map[string]string{}
	last := ""
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && envVarRe.MatchString(k) {
			out[k], last = v, k
			continue
		}
		if last != "" && line != "" {
			out[last] += "\n" + line
		}
	}
	return out
}

// envDiff names the variables (never their values) that differ between the
// two paths, leaving out the expected ones.
func envDiff(viaLink, viaSSH string) []string {
	a, b := parseEnv(viaLink), parseEnv(viaSSH)
	var out []string
	for k, v := range a {
		if envExpected[k] || strings.HasPrefix(k, "POCKET_RELAY") {
			continue
		}
		w, ok := b[k]
		switch {
		case !ok:
			out = append(out, k+" (only over the link)")
		case v != w:
			out = append(out, k+" (differs)")
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok && !envExpected[k] && !strings.HasPrefix(k, "POCKET_RELAY") {
			out = append(out, k+" (only over ssh)")
		}
	}
	sort.Strings(out)
	return out
}
