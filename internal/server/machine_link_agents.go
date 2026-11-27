package server

// Phase 2 of the machine link (docs/machine-link-plan.md, "Pocket tools
// through the link", "Port ownership and fallback"): agents and their tools
// on the link, switched on together.
//
// Every held agent on another machine asks its machine's link, before each
// of its connections (agent.Hold.Route), which way that connection goes:
// over the link (a hold stream; a start runs pocket-hold attach -start-only
// as an exec there first) or its own ssh connection with the -R forward of
// its tools port (the per-agent path). An agent moves between the two by
// ending its connection on purpose (agent.Reroutable) and asking again.
//
// Each agent's tools port has exactly one recorded owner (agent_routes):
//
//	ssh      its own ssh connection forwards it
//	to-link  moving onto the link: written before the old connection is
//	         confirmed gone, the port freed (fenced) and bound by the relay
//	link     the relay listens on it (bind acknowledged) and the agent's hold
//	         stream worked
//	to-ssh   moving back: written before the unbind; ssh again once the
//	         forward is up
//
// The move state is written before the old route is closed and the final
// one only after the new route works. After every (re)connect the link
// reconciles the move states against what the relay holds, then sync lists
// only the ports recorded as the relay's, then the agents still on ssh are
// moved one at a time (converge). A port is never handed to the per-agent
// path while the relay may still hold it: a waiting agent waits until the
// link is back, or a fallback or switch back has ended the relay (fenced)
// and freed its ports.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
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

var (
	// linkAgentWait: an agent whose port the relay may still hold waits this
	// long for the link (or a fallback) before its connection attempt fails
	// and is tried again like a lost one.
	linkAgentWait = 2 * time.Minute
	// linkMoveWait: one agent's move (cutover, switch back) may take this long.
	linkMoveWait = time.Minute
)

// errLinkDown: an agent's connection over the link was asked for while the
// link is not up.
var errLinkDown = errors.New("the machine's link is not up")

// linkAgent is one held agent on a machine: what its link needs to carry it.
type linkAgent struct {
	thread string
	slot   string
	bin    string // pocket-hold there
	port   int    // its tools port there (0: it has no tools)
	local  string // its tools endpoint here ("127.0.0.1:N")

	// under the machine link's mu:
	sess    agent.Reroutable
	prio    func() relay.Priority
	notify  func(text string)
	stay    string // why it stays on the per-agent path for now (a bind failed)
	noTools string // why its tools port cannot be had (an unrelated program has it)
	told    string // what its thread is to be told once it can be (before attachAgent)
}

// wireAgent sets o.Hold up for the machine's link. Every held agent on a
// machine asks its way at each connection, whatever the machine's mode is
// now: a machine moved onto the link later takes its running agents along,
// and one switched back gives them their own connections again.
func (m *linkManager) wireAgent(ctx context.Context, h *store.Host, thread string, o *agent.Options, port int, local string) *linkAgent {
	if h == nil || o.Hold == nil || o.Remote == nil {
		return nil
	}
	l, err := m.get(ctx, h.ID)
	if err != nil {
		return nil
	}
	a := &linkAgent{thread: thread, slot: o.Hold.Slot, bin: o.Hold.Bin, port: port, local: local}
	l.mu.Lock()
	if l.agents == nil {
		l.agents = map[string]*linkAgent{}
	}
	l.agents[thread] = a
	l.mu.Unlock()
	o.Hold.Route = func(ctx context.Context, start bool) (agent.HoldPath, error) { return l.agentPath(ctx, a, start) }
	o.Hold.Dial = l.dialHold
	o.Hold.StartOnly = func(ctx context.Context, s agent.HoldStart) (hold.Header, error) { return l.startOnly(ctx, a, s) }
	o.Hold.CatchUp = func(ctx context.Context) (func(), error) { return l.catchUp(ctx, a) }
	return a
}

// attachAgent: a's session runs (Reroute moves it), with how to rank its
// catch-up and how to tell its thread something.
func (m *linkManager) attachAgent(hostID int64, a *linkAgent, s agent.Session, prio func() relay.Priority, notify func(string)) {
	if a == nil {
		return
	}
	l := m.existing(hostID)
	if l == nil {
		return
	}
	l.mu.Lock()
	if r, ok := s.(agent.Reroutable); ok {
		a.sess = r
	}
	a.prio, a.notify = prio, notify
	want := l.wantLinkLocked() && l.upLocked()
	told := a.told
	a.told = ""
	l.mu.Unlock()
	if told != "" && notify != nil {
		notify(told)
	}
	if want {
		// it came up on its own connection (the link was not up yet when it
		// started): it moves over now
		go l.converge(m.baseCtx())
	}
}

// untrackAgent: a's session ended. ended: its CLI is gone too (not Pocket AI
// stopping, which leaves it running for the next process): its port is
// let go of and its route forgotten.
func (m *linkManager) untrackAgent(hostID int64, a *linkAgent, ended bool) {
	if a == nil {
		return
	}
	l := m.existing(hostID)
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.agents[a.thread] == a {
		delete(l.agents, a.thread)
	}
	c, bound := l.conn, l.bound[a.port] && a.port > 0
	if ended && bound {
		delete(l.bound, a.port)
	}
	l.mu.Unlock()
	if !ended {
		return
	}
	ctx, cancel := context.WithTimeout(m.baseCtx(), 30*time.Second)
	defer cancel()
	if bound && c != nil {
		_ = c.Unbind(ctx, a.port)
	}
	_ = m.st.DeleteAgentRoute(ctx, a.thread)
}

// keepsStream: thread's agent on hostID is carried by its machine's link,
// which keeps a stream per tracked agent: it is not parked (phase 3).
func (m *linkManager) keepsStream(hostID int64, thread string) bool {
	l := m.existing(hostID)
	if l == nil {
		return false
	}
	l.mu.Lock()
	want := l.wantLinkLocked()
	a := l.agents[thread]
	stay := a != nil && a.stay != ""
	l.mu.Unlock()
	if !want || stay {
		return false
	}
	route, _ := l.routeOf(context.Background(), thread)
	return route == store.RouteLink || route == store.RouteToLink
}

// existing is a machine's link if one was made in this process.
func (m *linkManager) existing(id int64) *machineLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links[id]
}

// wantLinkLocked: the machine's agents belong on its link now. Callers hold mu.
func (l *machineLink) wantLinkLocked() bool {
	return l.mode == store.LinkAgents && !l.m.off && l.fallback == nil && l.takenOver == "" &&
		l.state != linkStopped && l.state != linkEmergencyOff && !l.removed
}

// upLocked: the link is up and synced. Callers hold mu.
func (l *machineLink) upLocked() bool { return l.conn != nil && l.state == linkConnected }

func (l *machineLink) routeOf(ctx context.Context, thread string) (string, error) {
	r, err := l.m.st.GetAgentRoute(ctx, thread)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.Route, nil
}

// setRoute records a's route and tells whoever waits on the link.
func (l *machineLink) setRoute(a *linkAgent, route string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(l.m.baseCtx()), 10*time.Second)
	defer cancel()
	if err := l.m.st.SetAgentRoute(ctx, &store.AgentRoute{ThreadID: a.thread, HostID: l.id, Port: a.port, Route: route}); err != nil && l.m.log != nil {
		l.m.log.Warn("machine link: could not record an agent's route", "thread", a.thread, "route", route, "err", err)
	}
	l.mu.Lock()
	l.bump()
	l.mu.Unlock()
}

// agentPath is a's Hold.Route: which way its next connection goes.
func (l *machineLink) agentPath(ctx context.Context, a *linkAgent, start bool) (agent.HoldPath, error) {
	waited := time.NewTimer(linkAgentWait)
	defer waited.Stop()
	for {
		l.mu.Lock()
		want, up, conn, changed, stay := l.wantLinkLocked(), l.upLocked(), l.conn, l.changed, a.stay
		l.mu.Unlock()
		route, err := l.routeOf(ctx, a.thread)
		if err != nil {
			return agent.HoldPath{}, err
		}
		switch {
		case want && stay == "" && up:
			return l.toLink(ctx, a, conn, route)
		case route == store.RouteLink || route == store.RouteToLink || route == store.RouteToSSH:
			if up {
				// switch back (or it stays on the per-agent path): unbind first
				return l.toSSH(ctx, a, conn, route)
			}
			// the relay may still hold its port: wait until the link is back,
			// or a fallback or switch back has ended the relay and freed it
		default:
			return l.sshPath(a, route), nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return agent.HoldPath{}, ctx.Err()
		case <-waited.C:
			return agent.HoldPath{}, fmt.Errorf("waiting for the link to %s", l.machineName())
		}
	}
}

func (l *machineLink) machineName() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return firstNonEmpty(l.name, "the machine")
}

// toLink: a's next connection goes over the link. Moving it there: to-link,
// the port freed of the old ssh connection (fenced), bind acknowledged, then
// the hold stream; link once that works. Callers know the link is up.
func (l *machineLink) toLink(ctx context.Context, a *linkAgent, conn linkConn, route string) (agent.HoldPath, error) {
	onLink := func(err error) {
		if err == nil && route != store.RouteLink {
			l.setRoute(a, store.RouteLink)
			route = store.RouteLink
		}
	}
	l.mu.Lock()
	bound := l.boundOn(conn, a.port)
	l.mu.Unlock()
	if a.port == 0 || bound {
		return agent.HoldPath{Link: true, Done: onLink}, nil
	}
	if route != store.RouteToLink {
		l.setRoute(a, store.RouteToLink)
	}
	// its own ssh connection is gone (this is its next connection): nothing
	// of it may still hold the port. What cannot be freed is not ours to
	// end (another program, or nothing Pocket AI can tell is its own): the
	// agent goes on over the link without its tools (its own ssh connection
	// could not forward that port either), and each of its later
	// connections tries again.
	if err := l.freeAgentPort(ctx, a); err != nil {
		if ctx.Err() != nil {
			return agent.HoldPath{}, ctx.Err()
		}
		l.toolsUnavailable(a, err)
		return agent.HoldPath{Link: true, Done: onLink}, nil
	}
	if err := conn.Bind(ctx, a.port); err != nil {
		if ctx.Err() != nil {
			return agent.HoldPath{}, ctx.Err()
		}
		if strings.Contains(err.Error(), "in use") {
			// taken between the check and the bind: not by an agent of ours
			l.toolsUnavailable(a, fmt.Errorf("port %d is in use by another program, %s", a.port, relay.NotAnAgent))
			return agent.HoldPath{Link: true, Done: onLink}, nil
		}
		return l.putBack(a, "the link could not bind its tools port: "+oneLine(err.Error(), 200)), nil
	}
	l.mu.Lock()
	if l.conn == conn {
		l.bound[a.port] = true
	}
	a.noTools = ""
	l.mu.Unlock()
	return agent.HoldPath{Link: true, Done: onLink}, nil
}

// boundOn: conn (the current link) listens on port. Callers hold mu.
func (l *machineLink) boundOn(conn linkConn, port int) bool {
	return port > 0 && conn != nil && l.conn == conn && l.bound[port]
}

// putBack: the bind failed; a stays on the per-agent path (mixed routes are
// fine), and the cutover goes on with the next agent.
func (l *machineLink) putBack(a *linkAgent, why string) agent.HoldPath {
	l.mu.Lock()
	a.stay = why
	l.setErr("an agent stays on its own connection: " + why)
	l.bump()
	l.mu.Unlock()
	if l.m.log != nil {
		l.m.log.Warn("machine link: agent stays on ssh", "thread", a.thread, "why", why)
	}
	l.setRoute(a, store.RouteSSH)
	return l.sshPath(a, store.RouteSSH)
}

// toolsUnavailable: an unrelated program has a's stored port. It is left
// alone; a runs without its tools, and its thread says why.
func (l *machineLink) toolsUnavailable(a *linkAgent, err error) {
	why := oneLine(err.Error(), 200)
	l.mu.Lock()
	first := a.noTools == ""
	a.noTools = why
	notify := a.notify
	l.mu.Unlock()
	if l.m.log != nil {
		l.m.log.Warn("machine link: agent's tools port is taken", "thread", a.thread, "port", a.port, "why", why)
	}
	if !first {
		return
	}
	text := "Pocket tools are unavailable to this agent for now: " + why + ". Pocket AI leaves that program alone."
	if notify != nil {
		notify(text)
		return
	}
	l.mu.Lock()
	a.told = text // its session is still starting
	l.mu.Unlock()
}

// toSSH: a's next connection goes over its own ssh connection again, while
// the link is up: to-ssh, unbind acknowledged, the port confirmed free, then
// the forward; ssh once that works.
func (l *machineLink) toSSH(ctx context.Context, a *linkAgent, conn linkConn, route string) (agent.HoldPath, error) {
	if route != store.RouteToSSH {
		l.setRoute(a, store.RouteToSSH)
	}
	if a.port > 0 {
		if err := conn.Unbind(ctx, a.port); err != nil {
			return agent.HoldPath{}, fmt.Errorf("unbinding its tools port: %w", err)
		}
		l.mu.Lock()
		if l.conn == conn {
			delete(l.bound, a.port)
		}
		l.mu.Unlock()
		if err := l.freeAgentPort(ctx, a); err != nil {
			if ctx.Err() != nil {
				return agent.HoldPath{}, ctx.Err()
			}
			// not the relay's any more (unbound), and not ours to end: its
			// own connection goes without the forward
			l.toolsUnavailable(a, err)
		} else {
			l.mu.Lock()
			a.noTools = ""
			l.mu.Unlock()
		}
	}
	return l.sshPath(a, store.RouteToSSH), nil
}

// sshPath is the per-agent path: on a machine with a fence, behind the
// fence's check (a delayed restore from before a newer link is refused
// there), and with the fenced cleanup when ssh says the port is held.
func (l *machineLink) sshPath(a *linkAgent, route string) agent.HoldPath {
	p := agent.HoldPath{}
	l.mu.Lock()
	gen, noTools := l.gen, a.noTools
	l.mu.Unlock()
	if gen > 0 && a.bin != "" {
		p.Guard = l.guardScript(a.bin, gen)
		p.Free = func(ctx context.Context) error { return l.freeAgentPort(ctx, a) }
	}
	p.NoForwards = noTools != "" && a.port > 0
	if route != "" && route != store.RouteSSH {
		p.Done = func(err error) {
			if err == nil {
				l.setRoute(a, store.RouteSSH)
				route = store.RouteSSH
			}
		}
	}
	return p
}

// guardScript is the fence's check before a per-agent connection: refused
// (newer, off) ends it; another machine entry owning the account does not
// (its agents never were on this entry's link).
func (l *machineLink) guardScript(bin string, gen int64) string {
	return fmt.Sprintf(`o=$(%s fence -installation %s -entry %d -generation %d check 2>/dev/null); [ $? -ne 3 ] || case "$o" in *other-entry*) ;; *) false;; esac`,
		agent.ShellQuote(bin), agent.ShellQuote(l.m.installationID()), l.id, gen)
}

// freeAgentPort frees a's tools port of this account's stale ssh forwards
// (its slot's recorded sshd), fenced with this process's generation; an
// unrelated listener is left alone and named (relay.NotAnAgent). Over plain
// ssh: it is the per-agent path's cleanup.
func (l *machineLink) freeAgentPort(ctx context.Context, a *linkAgent) error {
	if a.port == 0 {
		return nil
	}
	l.mu.Lock()
	gen := l.gen
	l.mu.Unlock()
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil {
		return err
	}
	if gen <= 0 {
		r := plainRemoteOf(h)
		r.Slot, r.Forwards = a.slot, []string{fmt.Sprintf("127.0.0.1:%d:%s", a.port, a.local)}
		return r.FreeForwards(ctx)
	}
	return l.fence(ctx, h, a.bin, gen, "free-ports", "-slot", a.slot, strconv.Itoa(a.port))
}

// dialHold is a hold stream to slot's holder over the link (Hold.Dial).
func (l *machineLink) dialHold(ctx context.Context, slot string) (net.Conn, error) {
	l.mu.Lock()
	c, up := l.conn, l.upLocked()
	l.mu.Unlock()
	if !up {
		return nil, errLinkDown
	}
	return c.OpenHold(ctx, slot)
}

// catchUp waits for a place among the streams catching up (Hold.CatchUp):
// viewed conversations first, then busy ones, then idle ones.
func (l *machineLink) catchUp(ctx context.Context, a *linkAgent) (func(), error) {
	l.mu.Lock()
	g, prio := l.gate, a.prio
	l.mu.Unlock()
	p := relay.Idle
	if prio != nil {
		p = prio()
	}
	return g.Acquire(ctx, p)
}

// startOnly starts a's holder over the link (Hold.StartOnly): today's start
// script with attach -start-only, as an exec. It is atomic per slot (the
// holder's flock); after an unknown result or "slot busy" the slot is asked
// what runs there before any new launch, so a thread never gets two CLIs
// and is never left stuck.
func (l *machineLink) startOnly(ctx context.Context, a *linkAgent, s agent.HoldStart) (hold.Header, error) {
	if s.Script == "" {
		return hold.Header{}, errors.New("no start script for the link")
	}
	for try := 0; ; try++ {
		hd, err := l.execStart(ctx, s.Script)
		if err == nil && !hd.SlotBusy {
			return hd, nil
		}
		if err != nil && !errors.Is(err, agent.ErrLinkUnknown) {
			return hd, err
		}
		if try >= 3 {
			if err == nil {
				err = errors.New("the agent's slot there stayed busy")
			}
			return hd, err
		}
		why := "slot busy"
		if err != nil {
			why = "result unknown"
		}
		if l.m.log != nil {
			l.m.log.Info("machine link: agent start not confirmed; asking its slot", "thread", a.thread, "why", why)
		}
		// what runs there decides before any new launch
		deadline := time.Now().Add(30 * time.Second)
		for {
			st, ok := l.slotStatus(ctx, a)
			if ok && !st.Gone && !st.Exited && !st.Stopping {
				st.Type, st.Started, st.SlotBusy = hold.HeaderType, false, false
				return st, nil // it runs: attach to it
			}
			if ok && st.Gone && time.Until(deadline) < 27*time.Second {
				break // nobody listens: starting again is safe (the slot's flock)
			}
			if time.Now().After(deadline) {
				return hd, fmt.Errorf("the agent's start there was not confirmed (%s)", why)
			}
			select {
			case <-ctx.Done():
				return hd, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

// execStart runs a start-only script over the link and reads its header.
func (l *machineLink) execStart(ctx context.Context, script string) (hold.Header, error) {
	var hd hold.Header
	var out, errb bytes.Buffer
	code, err := l.Exec(ctx, script, nil, nil, &out, &errb)
	if err != nil {
		return hd, err
	}
	found := false
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if json.Unmarshal(sc.Bytes(), &hd) == nil && hd.Type == hold.HeaderType {
			found = true
			break
		}
	}
	switch {
	case found && hd.SlotBusy && code == hold.ExitSlotBusy:
		return hd, nil
	case code != 0:
		return hd, fmt.Errorf("pocket-hold there exited %d: %s", code, oneLine(firstNonEmpty(strings.TrimSpace(errb.String()), out.String()), 300))
	case !found:
		return hd, fmt.Errorf("unexpected output from pocket-hold: %s", oneLine(out.String(), 200))
	}
	return hd, nil
}

// slotStatus asks pocket-hold there about a's slot: over the link when it is
// up, else over plain ssh. It only reads, so either is safe.
func (l *machineLink) slotStatus(ctx context.Context, a *linkAgent) (hold.Header, bool) {
	var hd hold.Header
	var out, errb bytes.Buffer
	script := agent.RemoteScriptText(`"$1" status "$2"`, a.bin, a.slot)
	code, err := l.Exec(ctx, script, nil, nil, &out, &errb)
	if err != nil || code != 0 {
		h, herr := l.m.hostOf(ctx, l.id)
		if herr != nil {
			return hd, false
		}
		o, _, perr := l.m.plain(ctx, h, `"$1" status "$2"`, a.bin, a.slot)
		if perr != nil {
			return hd, false
		}
		out.Reset()
		out.WriteString(o)
	}
	if json.Unmarshal([]byte(lastLine(strings.TrimSpace(out.String()))), &hd) != nil {
		return hd, false
	}
	return hd, true
}

// toolsDial is the relay's tools streams' target (DialOptions.Tools): only
// the endpoint registered for that port, never any other address.
func (l *machineLink) toolsDial(port int) (net.Conn, error) {
	l.mu.Lock()
	addr := ""
	for _, a := range l.agents {
		if a.port == port {
			addr = a.local
			break
		}
	}
	l.mu.Unlock()
	if addr == "" {
		return nil, fmt.Errorf("no agent's tools on port %d here", port)
	}
	return net.DialTimeout("tcp", addr, 5*time.Second)
}

// ---------- after every (re)connect: reconcile, sync, converge ----------

// afterConnect runs once a run-mode relay answered, before the link counts
// as up: routes in a move state are settled against what the relay holds,
// routes of agents whose holder is gone are forgotten, and sync makes the
// relay listen on exactly the ports recorded as its own.
func (l *machineLink) afterConnect(ctx context.Context, conn linkConn, bin string) error {
	routes, err := l.m.st.ListAgentRoutes(ctx, l.id)
	if err != nil {
		return err
	}
	l.mu.Lock()
	want := l.mode == store.LinkAgents && l.fallback == nil
	l.mu.Unlock()
	if len(routes) == 0 && !want {
		return nil
	}
	// agents that ended while nobody watched: their routes go
	if len(routes) > 0 && bin != "" {
		gone := l.holdersGone(ctx, conn, bin, routes)
		kept := routes[:0]
		for _, r := range routes {
			if gone[r.ThreadID] && !l.tracked(r.ThreadID) {
				_ = l.m.st.DeleteAgentRoute(ctx, r.ThreadID)
				continue
			}
			kept = append(kept, r)
		}
		routes = kept
	}
	st, err := conn.Status(ctx)
	if err != nil {
		return fmt.Errorf("the relay's status: %w", err)
	}
	for _, r := range routes {
		holds := r.Port > 0 && slices.Contains(st.Ports, r.Port)
		to := r.Route
		switch r.Route {
		case store.RouteToLink:
			// finished when the relay has its port, reversed when not: its
			// next connection moves it again
			to = store.RouteSSH
			if holds {
				to = store.RouteLink
			}
		case store.RouteToSSH:
			to = store.RouteSSH
			if holds && want {
				to = store.RouteLink
			} else if holds {
				if err := conn.Unbind(ctx, r.Port); err != nil {
					return err
				}
			}
		}
		if to != r.Route {
			if l.m.log != nil {
				l.m.log.Info("machine link: settled an agent's move", "thread", r.ThreadID, "was", r.Route, "now", to)
			}
			r.Route = to
			_ = l.m.st.SetAgentRoute(ctx, r)
		}
	}
	var ports []int
	l.mu.Lock()
	for _, r := range routes {
		if r.Route == store.RouteLink && r.Port > 0 {
			if a := l.agents[r.ThreadID]; a != nil && a.noTools != "" {
				continue
			}
			ports = append(ports, r.Port)
		}
	}
	l.mu.Unlock()
	sort.Ints(ports)
	bound, failed, err := conn.Sync(ctx, ports)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	l.mu.Lock()
	l.bound = map[int]bool{}
	for _, p := range bound {
		l.bound[p] = true
	}
	l.mu.Unlock()
	if len(failed) > 0 && l.m.log != nil {
		// those agents' next connection frees and binds their port again
		l.m.log.Warn("machine link: some tools ports could not be bound again", "machine", l.machineName(), "failed", failed)
	}
	return nil
}

func (l *machineLink) tracked(thread string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agents[thread] != nil
}

// holdersGone asks pocket-hold there, in one exec, which of the routes'
// holders are gone.
func (l *machineLink) holdersGone(ctx context.Context, conn linkConn, bin string, routes []*store.AgentRoute) map[string]bool {
	args := []string{bin}
	for _, r := range routes {
		args = append(args, r.ThreadID)
	}
	var out bytes.Buffer
	res, err := conn.Exec(ctx, linkExecRequest{Script: agent.RemoteScriptText(`b=$1; shift; for s; do printf '%s ' "$s"; "$b" status "$s" 2>/dev/null | sed -e 's/,"meta":.*$/}/' -e 's/,"stderr":.*$/}/' | tr -d '\n'; echo; done`, args...), Stdout: &out})
	gone := map[string]bool{}
	if err != nil || res.Code != 0 {
		return gone
	}
	for _, line := range strings.Split(out.String(), "\n") {
		id, status, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		var hd hold.Header
		if json.Unmarshal([]byte(status), &hd) == nil && hd.Gone {
			gone[id] = true
		}
	}
	return gone
}

// converge moves the agents still on ssh onto the link, one at a time,
// while the machine wants them there and the link is up (the cutover after
// "Move agents too", and after each reconnect for agents started meanwhile).
func (l *machineLink) converge(ctx context.Context) {
	l.mu.Lock()
	if l.converging {
		l.convergeAgain = true
		l.mu.Unlock()
		return
	}
	l.converging = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.converging = false
		again := l.convergeAgain
		l.convergeAgain = false
		l.mu.Unlock()
		if again && ctx.Err() == nil {
			go l.converge(ctx)
		}
	}()
	tried := map[*linkAgent]bool{}
	for ctx.Err() == nil {
		l.mu.Lock()
		if !l.wantLinkLocked() || !l.upLocked() {
			l.mu.Unlock()
			return
		}
		var list []*linkAgent
		for _, a := range l.agents {
			if a.sess != nil && a.stay == "" && !tried[a] {
				list = append(list, a)
			}
		}
		l.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].thread < list[j].thread })
		var next *linkAgent
		for _, a := range list {
			if r, err := l.routeOf(ctx, a.thread); err == nil && r != store.RouteLink {
				next = a
				break
			}
		}
		if next == nil {
			return
		}
		tried[next] = true
		l.moveOne(ctx, next, store.RouteLink)
	}
}

// moveAll moves every tracked agent to route, one at a time (switch back:
// RouteSSH, while the relay serves the rest).
func (l *machineLink) moveAll(ctx context.Context, to string) {
	l.mu.Lock()
	var list []*linkAgent
	for _, a := range l.agents {
		list = append(list, a)
	}
	l.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].thread < list[j].thread })
	for _, a := range list {
		l.mu.Lock()
		up := l.upLocked()
		l.mu.Unlock()
		if !up {
			return // the rest go the fallback way: relay ended first
		}
		if r, err := l.routeOf(ctx, a.thread); err == nil && r != "" && r != to {
			l.moveOne(ctx, a, to)
		}
	}
}

// moveOne makes a's connection again (its route decides where it goes) and
// waits until it got there, it stayed where it was (a failed bind), or a
// minute passed.
func (l *machineLink) moveOne(ctx context.Context, a *linkAgent, to string) bool {
	l.mu.Lock()
	sess := a.sess
	l.mu.Unlock()
	if sess == nil || !sess.Reroute() {
		return false
	}
	deadline := time.NewTimer(linkMoveWait)
	defer deadline.Stop()
	for {
		l.mu.Lock()
		changed, stay := l.changed, a.stay
		gone := l.agents[a.thread] != a
		l.mu.Unlock()
		r, _ := l.routeOf(ctx, a.thread)
		switch {
		case r == to:
			return true
		case gone, to == store.RouteLink && stay != "":
			return false
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		case <-deadline.C:
			if l.m.log != nil {
				l.m.log.Warn("machine link: an agent did not move in time", "thread", a.thread, "to", to, "route", r)
			}
			return false
		}
	}
}

// release ends the link of a machine entry that is being deleted, and
// releases its fence there (fenced), so another entry may take the account:
// a deleted entry does not lock it out for good.
func (l *machineLink) release(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.stopLoop()
	l.mu.Lock()
	l.removed = true
	c, gen := l.conn, l.gen
	l.conn = nil
	l.bump()
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	h, err := l.m.hostOf(ctx, l.id)
	if err != nil || (gen <= 0 && h.LinkGeneration <= 0) {
		return err
	}
	bin, err := l.m.install(ctx, h)
	if err != nil {
		return err
	}
	g, err := l.m.lease.allocate(ctx, l.id)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.gen = max(l.gen, g)
	l.mu.Unlock()
	return l.fence(ctx, h, bin, g, "release")
}

// ---------- new machines (phase 5) ----------

// setUpNew: a new machine's first check passed. The link is the default for
// new machines: the same Install, Connect and Test as the button (with the
// agents' test), and when they pass it is set up on the link with its agents
// (there are none yet, so nothing needs confirming). When a step fails it
// stays on ssh per agent and its card shows why and the button. Existing
// machines keep the button; POCKET_LINK_DEFAULT=off turns this off.
func (m *linkManager) setUpNew(h *store.Host) {
	if os.Getenv("POCKET_LINK_DEFAULT") == "off" || m.off || firstNonEmpty(h.LinkMode, store.LinkOff) != store.LinkOff || h.LinkGeneration > 0 {
		return
	}
	if ok, _ := m.support(h); !ok {
		return
	}
	if m.st != nil && m.st.HostHasThreads(m.baseCtx(), h.ID) {
		return // conversations run there: moving them needs the person's Confirm
	}
	l, err := m.get(m.baseCtx(), h.ID)
	if err != nil {
		return
	}
	f := newLinkFlow(store.LinkAgents)
	ctx, cancel := context.WithTimeout(m.baseCtx(), 5*time.Minute)
	f.cancel = cancel
	l.mu.Lock()
	if l.flow != nil && (l.flow.State == "running" || l.flow.State == "confirm") {
		l.mu.Unlock()
		cancel()
		return
	}
	l.flow = f
	l.bump()
	l.mu.Unlock()
	go func() {
		defer cancel()
		l.op.Lock()
		defer l.op.Unlock()
		l.prepareLocked(ctx, f)
		l.mu.Lock()
		ready := f.State == "confirm"
		l.mu.Unlock()
		if !ready {
			if m.log != nil {
				m.log.Info("machine link: a new machine stays on ssh per agent", "machine", h.Name, "why", f.Error)
			}
			return
		}
		if err := l.switchLocked(ctx, f, store.LinkAgents); err == nil && m.log != nil {
			m.log.Info("machine link: a new machine uses one connection", "machine", h.Name)
		}
	}()
}

// ---------- the button's phase 2 test ----------

// testAgents is the Test step for "agents": a disposable holder in a
// throwaway slot (started with attach -start-only as an exec, checked over a
// hold stream with the in-process client, never a live holder), and an
// ephemeral loopback port bound through control with a tools round trip.
// Everything it made is gone when it returns.
func (l *machineLink) testAgents(ctx context.Context, cand linkConn, bin string, probe *linkProbe) error {
	slot := "linktest-" + randomToken()[:10]
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = cand.Exec(cctx, linkExecRequest{Script: agent.RemoteScriptText(`"$1" stop "$2" >/dev/null 2>&1; d="$HOME/.pocket-ai/hold/$2"; i=0; while [ -S "$d/s" ] && [ $i -lt 25 ]; do sleep 0.2; i=$((i+1)); done; rm -rf -- "$d" "$HOME/.pocket-ai/run/$2" "$HOME/.pocket-ai/run/$2.sshd"`, bin, slot)})
	}
	defer cleanup()
	var out, errb bytes.Buffer
	res, err := cand.Exec(ctx, linkExecRequest{Script: agent.RemoteScriptText(`exec "$1" attach -start-only -idle 2m "$2" -- /bin/sh -c 'while IFS= read -r l; do printf "%s\n" "$l"; done'`, bin, slot), Stdout: &out, Stderr: &errb})
	if err != nil {
		return fmt.Errorf("starting a test holder: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("starting a test holder exited %d: %s", res.Code, oneLine(errb.String(), 200))
	}
	nc, err := cand.OpenHold(ctx, slot)
	if err != nil {
		return fmt.Errorf("a hold stream to the test holder: %w", err)
	}
	hc, _, err := hold.Open(nc, hold.FromReplay)
	if err != nil {
		return fmt.Errorf("the holder protocol over the link: %w", err)
	}
	defer hc.Close()
	stop := context.AfterFunc(ctx, func() { _ = hc.Close() })
	defer stop()
	const word = "pocket-link-test"
	if err := hc.WriteIn([]byte(word + "\n")); err != nil {
		return fmt.Errorf("input to the test holder: %w", err)
	}
	timer := time.AfterFunc(20*time.Second, func() { _ = hc.Close() })
	defer timer.Stop()
	var got []byte
	for !bytes.Contains(got, []byte(word)) {
		typ, p, err := hc.Read()
		if err != nil {
			return fmt.Errorf("output of the test holder: %w", err)
		}
		if typ == hold.FrameOut {
			got = append(got, p...)
		}
	}
	if err := hc.Stop(); err != nil {
		return fmt.Errorf("stopping the test holder: %w", err)
	}
	for {
		typ, _, err := hc.Read()
		if err != nil {
			return fmt.Errorf("the test holder's end: %w", err)
		}
		if typ == hold.FrameExit {
			_ = hc.Ack()
			break
		}
	}

	// tools: an ephemeral loopback port there, bound through control, with
	// a round trip to this server's end
	port := 0
	for i := 0; i < 5 && port == 0; i++ {
		p := 20000 + rand.IntN(40000)
		probe.set(p)
		err = cand.Bind(ctx, p)
		if err == nil {
			port = p
		} else if !strings.Contains(err.Error(), "in use") {
			return fmt.Errorf("binding a test port there: %w", err)
		}
	}
	if port == 0 {
		return fmt.Errorf("binding a test port there: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = cand.Unbind(cctx, port)
	}()
	out.Reset()
	errb.Reset()
	res, err = cand.Exec(ctx, linkExecRequest{Script: agent.RemoteScriptText(`exec "$1" probe "$2"`, bin, strconv.Itoa(port)), Stdout: &out, Stderr: &errb})
	if err != nil {
		return fmt.Errorf("a tools round trip: %w", err)
	}
	if res.Code != 0 || strings.TrimSpace(out.String()) != relay.ProbeAnswer {
		return fmt.Errorf("a tools round trip did not come back (%d): %s", res.Code, oneLine(firstNonEmpty(errb.String(), out.String()), 200))
	}
	return nil
}

// linkProbe is the server's end of the test step's tools round trip.
type linkProbe struct {
	mu   sync.Mutex
	port int
}

func newLinkProbe() *linkProbe { return &linkProbe{} }

func (p *linkProbe) set(port int) {
	p.mu.Lock()
	p.port = port
	p.mu.Unlock()
}

// dial answers the probe port (and only it) with relay.ProbeAnswer.
func (p *linkProbe) dial(port int) (net.Conn, error) {
	p.mu.Lock()
	want := p.port
	p.mu.Unlock()
	if want == 0 || port != want {
		return nil, fmt.Errorf("no test target on port %d", port)
	}
	a, b := net.Pipe()
	go func() {
		defer a.Close()
		_ = a.SetDeadline(time.Now().Add(20 * time.Second))
		line, err := bufio.NewReader(a).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != relay.ProbeLine {
			return
		}
		_, _ = io.WriteString(a, relay.ProbeAnswer+"\n")
	}()
	return b, nil
}
