package server

// Parking: an agent held on another machine (held.go) needs no ssh
// connection while nothing happens on it. After PARK_AFTER without a turn,
// a request waiting, background work or anyone looking, its connection is
// let go; the agent stays under its holder, with Remote Control too. It is
// taken back, at the exact byte where reading stopped, when a message goes
// to it, someone opens the thread, or it starts working on its own (a
// message from the Claude app, a background task ending): one connection per
// machine watches the holders of all agents parked there.
//
// Why: a connection per live agent, kept for days, was 57 to one machine,
// more than the NAT in front of this server allows to one destination (about
// 64): new connections there timed out.
//
// Machines on the link (machine_link_agents.go) need none of this: all their
// agents share one connection, and each tracked agent keeps its stream. Only
// agents on the per-agent path (a machine not on the link, fallen back, or
// an agent whose bind failed) are parked.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

var (
	// parkPoll is how often a machine's parked agents are looked at.
	parkPoll = 10 * time.Second
	// parkTouch: a parked agent is attached to again this often. Its holder
	// ends a CLI nobody was attached to for a day, and Remote Control is
	// meant to stay up.
	parkTouch = 6 * time.Hour
)

// parkWatchScript prints, every $2 seconds, a line per slot (the rest of the
// arguments) with what its holder ($1) says, cut before the two fields that
// can be long (what was stored with it, the CLI's last stderr), then "@".
// It ends when nobody reads it any more.
const parkWatchScript = `b=$1; n=$2; shift 2
while :; do
  for s; do printf '%s ' "$s"; "$b" status "$s" 2>/dev/null | sed -e 's/,"meta":.*$/}/' -e 's/,"stderr":.*$/}/' | tr -d '\n'; echo; done
  echo @ || exit 0
  sleep "$n"
done`

// parkStir: what a parked agent's holder says (status) means its connection
// is wanted back: the CLI said something since (off is what was read of its
// stdout), works, ended, or cannot be asked.
func parkStir(status string, off int64) bool {
	var hd hold.Header
	if json.Unmarshal([]byte(status), &hd) != nil {
		return true
	}
	return hd.Gone || hd.Exited || hd.Stopping || hd.Busy || hd.Offset != off
}

// wake asks a parked agent's connection back; nothing when it has one.
func (rt *engine) wake(r *run, why string) {
	p, ok := r.sess.(agent.Parkable)
	if !ok || !p.Wake() {
		return
	}
	r.mu.Lock()
	r.wokeAt = time.Now()
	away := time.Since(r.parkedAt)
	r.mu.Unlock()
	rt.s.log.Info("agent connection asked back", "thread", r.threadID, "why", why, "parked", away.Round(time.Second).String())
}

// parkIdle lets go of the connections of agents on other machines that
// nothing has happened on for PARK_AFTER, and attaches again for a while to
// those parked for long (parkTouch).
func (rt *engine) parkIdle() {
	after := rt.s.cfg.ParkAfter
	if after <= 0 {
		return
	}
	var list []*run
	rt.mu.Lock()
	for id, r := range rt.runs {
		if r.host != 0 && r.held && len(rt.watchers[id]) == 0 {
			list = append(list, r)
		}
	}
	rt.mu.Unlock()
	// a machine on the link keeps a stream per tracked agent (it costs a
	// socket there and a few kilobytes here, not a connection): only agents
	// on the per-agent path are parked
	list = slices.DeleteFunc(list, func(r *run) bool { return rt.s.links.keepsStream(r.host, r.threadID) })
	hosts := map[int64]bool{}
	for _, r := range list {
		p, ok := r.sess.(agent.Parkable)
		if !ok {
			continue
		}
		if _, parked := p.Parked(); parked {
			r.mu.Lock()
			long := time.Since(r.parkedAt) >= parkTouch
			r.mu.Unlock()
			if long {
				rt.wake(r, "parked for long: its holder must see it")
			} else {
				hosts[r.host] = true
			}
			continue
		}
		r.mu.Lock()
		quiet := !r.working() && len(r.pending) == 0 && !r.linkDown && !r.replaying && !r.retired &&
			time.Since(r.lastUsed) >= after && time.Since(r.wokeAt) >= after
		r.mu.Unlock()
		if !quiet || rt.rcSettling(r.threadID) || !p.Park() {
			continue
		}
		r.mu.Lock()
		r.parkedAt = time.Now()
		r.mu.Unlock()
		hosts[r.host] = true
		rt.s.log.Info("agent idle: connection let go", "thread", r.threadID, "host", rt.machineName(r.host))
	}
	for id := range hosts {
		rt.watchParked(id)
	}
}

// rcSettling: Remote Control is being turned on for the thread (the CLI is
// asked and answers): not the moment to let its connection go.
func (rt *engine) rcSettling(threadID string) bool {
	rt.rcMu.Lock()
	defer rt.rcMu.Unlock()
	st := rt.rc[threadID]
	return st != nil && (st.attaching || st.State == "starting")
}

// parkedOn lists the agents parked on a machine, by thread.
func (rt *engine) parkedOn(hostID int64) map[string]*run {
	out := map[string]*run{}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for id, r := range rt.runs {
		if r.host != hostID {
			continue
		}
		if p, ok := r.sess.(agent.Parkable); ok {
			if _, parked := p.Parked(); parked {
				out[id] = r
			}
		}
	}
	return out
}

// watchParked watches the holders of the agents parked on a machine, until
// none is parked there.
func (rt *engine) watchParked(hostID int64) {
	rt.parkMu.Lock()
	if rt.parkWatch[hostID] {
		rt.parkMu.Unlock()
		return
	}
	rt.parkWatch[hostID] = true
	rt.parkMu.Unlock()
	go func() {
		wait := time.Second
		for {
			// whether to go on is decided with parkMu held: an agent parked
			// right now either finds this watcher or starts the next one
			rt.parkMu.Lock()
			slots := rt.parkedOn(hostID)
			if len(slots) == 0 || rt.ctx.Err() != nil {
				delete(rt.parkWatch, hostID)
				rt.parkMu.Unlock()
				return
			}
			rt.parkMu.Unlock()
			start := time.Now()
			err := rt.watchOnce(hostID, slots)
			switch {
			case err == nil, time.Since(start) > time.Minute:
				wait = time.Second
			default:
				// a watch that did not last (the machine is away): slower,
				// up to a minute apart; the agents stay parked meanwhile,
				// as their connections could not be had either
				wait = min(wait*2, time.Minute)
			}
			if err != nil && rt.ctx.Err() == nil {
				rt.s.log.Warn("watching parked agents", "host", rt.machineName(hostID), "err", err)
			}
			select {
			case <-rt.ctx.Done():
			case <-time.After(wait):
			}
		}
	}()
}

// watchOnce runs one watch connection for the agents in slots. It returns
// when another agent was parked there (the next one asks about it too), none
// is left, or the connection ended.
func (rt *engine) watchOnce(hostID int64, slots map[string]*run) error {
	ctx, cancel := context.WithCancel(rt.ctx)
	defer cancel()
	wakeAll := func(why string) {
		for _, r := range slots {
			rt.wake(r, why)
		}
	}
	h, err := rt.s.host(ctx, hostID)
	if err != nil || h == nil {
		wakeAll("its machine is gone") // their links find out what is left
		return err
	}
	bin := rt.holder(ctx, h)
	if bin == "" {
		wakeAll("no holder to ask")
		return nil
	}
	args := []string{bin, parkSeconds()}
	for id := range slots {
		args = append(args, id)
	}
	// Stream is always its own ssh connection, never the link (phase 3
	// replaces this watch on machines that are on the link)
	cmd, err := remoteOf(h).Stream(ctx, parkWatchScript, args...)
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return err
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		cancel()
		for range lines {
		}
		_ = cmd.Wait()
	}()
	quiet := time.NewTimer(3*parkPoll + 45*time.Second)
	defer quiet.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-quiet.C:
			return errors.New("no word from the machine")
		case line, ok := <-lines:
			if !ok {
				return errors.New(firstNonEmpty(oneLine(errb.String(), 200), "the connection ended"))
			}
			if line != "@" {
				id, status, _ := strings.Cut(line, " ")
				r := slots[id]
				if r == nil {
					continue
				}
				p, _ := r.sess.(agent.Parkable)
				if off, parked := p.Parked(); parked && off >= 0 && parkStir(status, off) {
					rt.wake(r, "it stirred")
				}
				continue
			}
			// a round is through
			quiet.Reset(3*parkPoll + 45*time.Second)
			now := rt.parkedOn(hostID)
			if len(now) == 0 {
				return nil
			}
			for id, r := range now {
				if slots[id] != r {
					return nil // parked since: the next watch asks about it too
				}
			}
		}
	}
}

// parkSeconds is parkPoll for sleep(1).
func parkSeconds() string { return strconv.FormatFloat(parkPoll.Seconds(), 'f', -1, 64) }
