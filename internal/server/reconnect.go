package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Reconnecting after a restart. A restart takes this server's agents down
// with the service, but on another machine only the ssh link goes: Claude
// Code there can keep working on the turn. Sending "continue" then started
// a second process on the same session. Now, for every turn a restart cut
// off (and for any message to a session that was active before the restart):
//
//   - still working: Pocket AI reconnects to it. It reads the session log
//     there and shows the turn as it goes on, and holds what the person
//     sends until the turn is done. Stop ends that process.
//   - gone mid-turn: Pocket AI resumes the session itself with a "continue"
//     message, so nobody has to type one (at most once per half hour per
//     conversation, so a turn that keeps restarting Pocket AI cannot loop).

const (
	reattachStuck   = 2 * time.Hour // working with a silent log this long: stuck (a prompt nobody can answer)
	reattachWindow  = 8 << 20       // log bytes per look (and how much of its end is read to find the cut-off turn)
	reattachFails   = 15            // probes in a row that could not reach the machine
	autoContinueGap = 30 * time.Minute
	orphanRecent    = 24 * time.Hour // sessions active this long before the restart are checked once before a resume
)

// leftoverTexts: how many recent items a catch-up checks for messages it
// would repeat.
const leftoverTexts = 400

// reattachPoll: how often a leftover process is looked at (tests shorten it).
var reattachPoll = 4 * time.Second

const autoContinueText = "Pocket AI restarted while you were working, which cut off your last turn. Continue where you left off."

// reattached: threads whose earlier process Pocket AI is reconnected to.
type reattachSet struct {
	mu      sync.Mutex
	m       map[string]*reattachRun
	checked map[string]bool // threads already checked for a leftover process
	// pending: threads whose check right after the restart has not finished;
	// nothing starts a process for them until it has (Remote Control comes
	// back 5 s after a restart, often before the machine answered)
	pending map[string]chan struct{}
}

// pendingWait caps how long a start waits for a thread's check.
const pendingWait = 90 * time.Second

func (rs *reattachSet) markPending(ids []string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.pending == nil {
		rs.pending = map[string]chan struct{}{}
	}
	for _, id := range ids {
		if rs.pending[id] == nil {
			rs.pending[id] = make(chan struct{})
		}
	}
}

// resolve ends a thread's pending check (safe to call more than once).
func (rs *reattachSet) resolve(id string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if ch := rs.pending[id]; ch != nil {
		close(ch)
		delete(rs.pending, id)
	}
}

// waitPending returns once the thread's check after the restart is done.
func (rs *reattachSet) waitPending(id string) {
	rs.mu.Lock()
	ch := rs.pending[id]
	rs.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(pendingWait):
	}
}

type reattachRun struct {
	host    *store.Host // nil = this server
	pid     int
	own     bool // started by Pocket AI (stream-json over stdin), so it may be ended
	stopped bool // the person pressed Stop
}

func (rs *reattachSet) get(id string) *reattachRun {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.m == nil {
		return nil
	}
	return rs.m[id]
}

// begin registers a reconnection; false when one runs already.
func (rs *reattachSet) begin(id string, r *reattachRun) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.m == nil {
		rs.m = map[string]*reattachRun{}
	}
	if rs.m[id] != nil {
		return false
	}
	rs.m[id] = r
	return true
}

func (rs *reattachSet) end(id string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.m, id)
}

func (rs *reattachSet) wasChecked(id string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.checked[id]
}

// firstCheck is true once per thread and process lifetime.
func (rs *reattachSet) firstCheck(id string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.checked == nil {
		rs.checked = map[string]bool{}
	}
	if rs.checked[id] {
		return false
	}
	rs.checked[id] = true
	return true
}

func (s *Server) isReattached(threadID string) bool { return s.re.get(threadID) != nil }

// ---------- probing the session's process ----------

// claudeProbe is what a look at a session's process and log found.
type claudeProbe struct {
	state string // busy | idle | gone
	pid   int
	own   bool
	size  int64  // log size (-1: no log)
	data  []byte // log bytes from the asked offset on (capped)
}

// probeSh prints "state\t<busy|idle|gone>\t<pid>\t<own>\t<log size>" and,
// when $2 >= 0, "data\t<base64 of the log from $2 on>" ($1 session id,
// $3 most bytes). Claude Code registers each process in sessions/<pid>.json.
const probeSh = `d="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; sid=$1; off=$2; most=$3
st=gone; pid=; own=0
for f in "$d"/sessions/*.json; do
  [ -f "$f" ] || continue
  grep -q "\"sessionId\":\"$sid\"" "$f" 2>/dev/null || continue
  p=$(sed -n 's/.*"pid":\([0-9][0-9]*\).*/\1/p' "$f"); s=$(sed -n 's/.*"status":"\([a-z]*\)".*/\1/p' "$f")
  [ -n "$p" ] && kill -0 "$p" 2>/dev/null || continue
  pid=$p; st=${s:-busy}
  case "$(ps -o args= -p "$p" 2>/dev/null)" in *--input-format\ stream-json*--permission-prompt-tool\ stdio*) own=1 ;; *) own=0 ;; esac
  [ "$st" = idle ] || break
done
log=$(ls -1t "$d"/projects/*/"$sid".jsonl 2>/dev/null | head -n 1)
size=-1; [ -n "$log" ] && size=$(wc -c <"$log" | tr -d ' ')
printf 'state\t%s\t%s\t%s\t%s\n' "$st" "$pid" "$own" "$size"
if [ -n "$log" ] && [ "$off" -ge 0 ] && [ "$size" -gt "$off" ]; then
  printf 'data\t'; tail -c +$((off + 1)) "$log" | head -c "$most" | base64 | tr -d '\n'; printf '\n'
fi
exit 0
`

// probeClaude looks at the process on a Claude session and reads its log
// from off on (off < 0: no log bytes).
func (s *Server) probeClaude(ctx context.Context, t *store.Thread, h *store.Host, off int64) (claudeProbe, error) {
	if h != nil {
		out, errOut, err := remoteOf(h).Run(ctx, 45*time.Second, inLogin("claude", t.HostHome, probeSh),
			t.ProviderSessionID, strconv.FormatInt(off, 10), strconv.Itoa(reattachWindow))
		if err != nil {
			return claudeProbe{}, fmt.Errorf("%s: %s", h.Name, sshError(errOut, err))
		}
		return parseProbe(out)
	}
	p := claudeProbe{state: "gone", size: -1}
	dir, _ := s.accountHome(ctx, "claude", t.AccountID)
	if procs, ok := agent.ClaudeProcs(dir); ok {
		if c, found := procs[t.ProviderSessionID]; found {
			p.pid, p.state = c.PID, "idle"
			if c.Busy() {
				p.state = "busy"
			}
			if b, err := agent.ReadConfined("/proc/" + strconv.Itoa(c.PID) + "/cmdline"); err == nil {
				args := string(bytes.ReplaceAll(b, []byte{0}, []byte{' '}))
				p.own = strings.Contains(args, "--input-format stream-json") && strings.Contains(args, "--permission-prompt-tool stdio")
			}
		}
	}
	path, err := agent.FindClaude(dir, t.ProviderSessionID)
	if err != nil {
		return p, nil
	}
	p.size = agent.FileSize(path)
	if off >= 0 && p.size > off {
		fh, err := agent.OpenConfined(path)
		if err != nil {
			return p, nil
		}
		defer fh.Close()
		if _, err := fh.Seek(off, io.SeekStart); err == nil {
			p.data, _ = io.ReadAll(io.LimitReader(fh, reattachWindow))
		}
	}
	return p, nil
}

func parseProbe(out string) (claudeProbe, error) {
	p := claudeProbe{size: -1}
	seen := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		switch {
		case len(f) >= 5 && f[0] == "state":
			seen = true
			p.state = f[1]
			p.pid, _ = strconv.Atoi(f[2])
			p.own = f[3] == "1"
			if n, err := strconv.ParseInt(f[4], 10, 64); err == nil {
				p.size = n
			}
		case len(f) == 2 && f[0] == "data":
			b, err := base64.StdEncoding.DecodeString(f[1])
			if err != nil {
				return p, err
			}
			p.data = b
		}
	}
	if !seen {
		return p, fmt.Errorf("no answer from the machine")
	}
	if p.state != "busy" && p.state != "idle" {
		p.state = "gone"
	}
	return p, nil
}

// endTreeSh SIGTERMs process $1, its process group and everything below it:
// a leftover's background shells and waiters (a Bash tool's "wait until the
// lane is idle") would otherwise go on and act next to the new process.
const endTreeSh = `p=$1; all=$p; new=$p
while [ -n "$new" ]; do
  new=$(ps -A -o pid= -o ppid= 2>/dev/null | awk -v ps=" $new " 'index(ps, " " $2 " ") { printf "%s ", $1 }')
  all="$all $new"
done
kill -TERM -"$p" 2>/dev/null
kill -TERM $all 2>/dev/null
exit 0
`

// endProcess ends a leftover Claude Code process and what it started.
func (s *Server) endProcess(ctx context.Context, h *store.Host, pid int) {
	if pid <= 0 {
		return
	}
	if h == nil {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "sh", "-c", endTreeSh, "sh", strconv.Itoa(pid)).Run(); err != nil {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		return
	}
	if _, errOut, err := remoteOf(h).Run(ctx, 20*time.Second, endTreeSh, strconv.Itoa(pid)); err != nil {
		s.log.Warn("reconnect: could not end the leftover agent", "host", h.Name, "pid", pid, "err", sshError(errOut, err))
	}
}

// ---------- after a restart ----------

// reconnectCut handles the turns a restart cut off (store.ResetRunning).
func (s *Server) reconnectCut(ids []string) {
	s.re.markPending(ids)
	sweep := s.sweepCandidates(s.rt.ctx, ids)
	s.re.markPending(sweep)
	for _, id := range ids {
		s.re.firstCheck(id)
		go s.reconnect(id)
	}
	go s.sweepLeftovers(s.rt.ctx, sweep)
}

// sweepCandidates: the Claude conversations on other machines active in
// the day before the restart, except the ones cut off mid-turn.
func (s *Server) sweepCandidates(ctx context.Context, cut []string) []string {
	skip := map[string]bool{}
	for _, id := range cut {
		skip[id] = true
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM threads WHERE provider = 'claude' AND host_id != 0 AND archived = 0
		AND provider_session_id != '' AND activity_at >= ? ORDER BY host_id, activity_at DESC`, store.FormatTime(s.started.Add(-orphanRecent)))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !skip[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// sweepLeftovers: a turn that was over at the restart is not cut off, but
// its process on another machine may still run background work (a waiter
// on a lane, a monitor) that acts on its own once it fires. So every Claude
// conversation on another machine active in the day before the restart is
// looked at once, right away rather than on its next message: one still
// working is followed, an idle one of Pocket AI's is ended. A machine that
// does not answer is skipped (its conversations are looked at on their next
// message instead).
func (s *Server) sweepLeftovers(ctx context.Context, ids []string) {
	defer func() {
		for _, id := range ids {
			s.re.resolve(id)
		}
	}()
	down := map[int64]bool{}
	for _, id := range ids {
		t, err := s.store.GetThreadAny(ctx, id)
		if err != nil || down[t.HostID] || s.re.wasChecked(id) || s.rt.get(id) != nil || s.isReattached(id) {
			continue
		}
		h, err := s.host(ctx, t.HostID)
		if err != nil {
			continue
		}
		p, err := s.probeClaude(ctx, t, h, -1)
		if err != nil {
			down[t.HostID] = true
			s.log.Info("leftover sweep: machine not reached", "host", h.Name, "err", err)
			continue
		}
		if !s.re.firstCheck(id) {
			s.re.resolve(id)
			continue // a message got there first
		}
		if p.state == "busy" || (p.state == "idle" && p.own) {
			s.log.Info("leftover sweep", "thread", id, "state", p.state, "pid", p.pid, "own", p.own)
			s.startReattach(t, h, p, whySweep)
		}
		s.re.resolve(id)
	}
}

func (s *Server) cutNotice(threadID string) {
	s.rt.saveItem(threadID, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "warn",
		Text: "Pocket AI restarted while the agent was working, so this turn stopped. Send a message (e.g. “continue”) to pick up where it left off."})
}

func (s *Server) reconnect(id string) {
	defer s.re.resolve(id)
	ctx := s.rt.ctx
	t, err := s.store.GetThreadAny(ctx, id)
	if err != nil {
		return
	}
	if t.Archived || t.ProviderSessionID == "" {
		s.cutNotice(id)
		return
	}
	h, err := s.host(ctx, t.HostID)
	if err != nil {
		s.cutNotice(id)
		return
	}
	if t.Provider == "claude" {
		p, err := s.probeClaude(ctx, t, h, -1)
		if err != nil {
			s.log.Info("reconnect: could not look at the agent", "thread", id, "err", err)
			s.cutNotice(id)
			return
		}
		if p.state != "gone" {
			s.startReattach(t, h, p, whyCut)
			return
		}
	} else if h != nil && s.codexActive(ctx, t, h) {
		s.rt.saveItem(id, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "warn",
			Text: "Pocket AI restarted while Codex was working on " + h.Name + ", and Codex may still be writing there. Look before you send “continue”, so it does not do the work twice."})
		return
	}
	s.re.resolve(id) // its "continue" starts the process
	s.autoContinue(t, "Pocket AI restarted while the agent was working, so this turn stopped. It continues on its own:")
}

// codexActive: the session's rollout on the machine changed in the last minute.
func (s *Server) codexActive(ctx context.Context, t *store.Thread, h *store.Host) bool {
	out, _, err := remoteOf(h).Run(ctx, 30*time.Second, inLogin("codex", t.HostHome,
		`d="${CODEX_HOME:-$HOME/.codex}"; [ -d "$d/sessions" ] && find "$d/sessions" -type f -name "*$1.jsonl" -mmin -1 2>/dev/null | head -n 1; exit 0`), t.ProviderSessionID)
	return err == nil && strings.TrimSpace(out) != ""
}

// autoContinue resumes a cut-off turn with a "continue" message from
// Pocket AI. When it cannot (or did so a moment ago), the person gets the
// notice that asks them to.
func (s *Server) autoContinue(t *store.Thread, why string) {
	ctx := s.rt.ctx
	owner, err := s.store.GetUser(ctx, t.UserID)
	if err != nil || (!owner.MayRun() && t.Source != "ask") || s.recentlyContinued(ctx, t.ID) {
		s.cutNotice(t.ID)
		return
	}
	s.rt.saveItem(t.ID, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "info", Text: why})
	key := "u-auto-" + uuid.NewString()
	in := agent.Input{Text: autoContinueText}
	it := agent.Item{Key: key, Kind: "user", Text: autoContinueText, By: "Pocket AI"}
	unlock := s.lockThreadEdit(t.ID)
	held := s.holds().add(t.ID, key, func() { s.sendHeld(t.ID, key, in) })
	it.Queued = held
	s.rt.saveItem(t.ID, it)
	fresh, err := s.store.GetThreadAny(ctx, t.ID)
	unlock()
	if err != nil || held {
		return
	}
	s.log.Info("continuing a turn cut off by the restart", "thread", t.ID)
	if _, err := s.rt.send(fresh, key, in); err != nil {
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		s.rt.setStatus(t.ID, "error", err.Error())
	}
}

// recentlyContinued: the last message is Pocket AI's own "continue" from
// less than autoContinueGap ago.
func (s *Server) recentlyContinued(ctx context.Context, threadID string) bool {
	var key, at string
	if s.store.DB.QueryRowContext(ctx, `SELECT key, created_at FROM items WHERE thread_id = ? AND kind = 'user' ORDER BY id DESC LIMIT 1`, threadID).Scan(&key, &at) != nil {
		return false
	}
	if !strings.HasPrefix(key, "u-auto-") {
		return false
	}
	when, err := time.Parse(time.RFC3339Nano, at)
	return err != nil || time.Since(when) < autoContinueGap
}

// checkLeftover runs before a message starts a process for a thread that
// was active before this restart: if Claude Code still works on the session
// (on another machine, or anywhere for a turn Pocket AI started), Pocket AI
// reconnects to it and the message waits. Callers hold lockThreadEdit.
func (s *Server) checkLeftover(ctx context.Context, t *store.Thread) {
	s.re.waitPending(t.ID)
	if t.Provider != "claude" || t.ProviderSessionID == "" || s.rt.get(t.ID) != nil || s.isReattached(t.ID) {
		return
	}
	last, err := time.Parse(time.RFC3339Nano, firstNonEmpty(t.ActivityAt, t.UpdatedAt))
	if err != nil || !last.Before(s.started) || s.started.Sub(last) > orphanRecent || !s.re.firstCheck(t.ID) {
		return
	}
	h, err := s.host(ctx, t.HostID)
	if err != nil {
		return
	}
	p, err := s.probeClaude(ctx, t, h, -1)
	if err != nil {
		s.log.Info("leftover check failed", "thread", t.ID, "err", err)
		return
	}
	s.log.Info("leftover check", "thread", t.ID, "state", p.state, "pid", p.pid, "own", p.own)
	switch {
	case p.state == "busy" && (h != nil || p.own): // a terminal here is the follower's (and the 409's) business
	case p.state == "idle" && p.own:
		// its turn is over but it may still run background work (waiters,
		// monitors) that would act next to the new process: catch up, end it
	default:
		return
	}
	s.startReattach(t, h, p, whyMessage)
}

// ---------- following the leftover process ----------

// Why Pocket AI looks at a leftover process (the notice says it).
const (
	whyCut     = "cut"     // its turn was cut off by the restart
	whyMessage = "message" // a message came for the conversation
	whySweep   = "sweep"   // the look right after the restart
)

func (s *Server) startReattach(t *store.Thread, h *store.Host, p claudeProbe, why string) {
	cut := why != whyMessage
	run := &reattachRun{host: h, pid: p.pid, own: p.own}
	if !s.re.begin(t.ID, run) {
		return
	}
	s.holds().hold(t.ID)
	where := "on this server"
	if h != nil {
		where = "on " + h.Name
	}
	text := "Pocket AI restarted, but the agent is still working " + where + ", so Pocket AI reconnected to it. Its progress shows here, and anything you send waits until it is done."
	if !cut {
		text = "The agent is still working " + where + " on a turn from before Pocket AI restarted, so Pocket AI reconnected to it. Your message waits until it is done."
	}
	if p.state == "idle" {
		text = "Pocket AI restarted while the agent was working " + where + ". It has finished since: here is what it did."
		if why != whyCut {
			text = "The agent's process from before Pocket AI restarted was still open " + where + "."
		}
		if p.own {
			text += " Pocket AI ended it, with any background work it still ran (waiters, monitors), so only one process works on this conversation."
		}
		if why == whyMessage {
			text += " Your message goes to a new one."
		}
	}
	s.rt.saveItem(t.ID, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "info", Text: text})
	s.log.Info("reconnected to a running agent", "thread", t.ID, "where", where, "pid", p.pid, "state", p.state)
	s.rt.setExternal(t.ID, p.state == "busy")
	// what the thread had before anything new lands (the held message is
	// saved right after this returns): the catch-up repeats none of it
	ap := s.newLeftoverApplier(s.rt.ctx, t)
	go s.followLeftover(t, run, p, ap)
}

// stopReattached ends the process a thread is reconnected to (Stop).
func (s *Server) stopReattached(threadID string) bool {
	run := s.re.get(threadID)
	if run == nil {
		return false
	}
	s.re.mu.Lock()
	run.stopped = true
	pid := run.pid
	s.re.mu.Unlock()
	s.endProcess(s.rt.ctx, run.host, pid)
	return true
}

func (s *Server) followLeftover(t *store.Thread, run *reattachRun, first claudeProbe, ap *leftoverApplier) {
	ctx := s.rt.ctx
	h := run.host
	// catch up: the cut-off turn from its prompt on
	from := first.size - reattachWindow
	if from < 0 {
		from = 0
	}
	off := first.size
	if first.size >= 0 {
		if p, err := s.probeClaude(ctx, t, h, from); err == nil {
			off = ap.catchUp(p.data, from)
		}
	}
	state, fails, lastGrowth := first.state, 0, time.Now()
	for state == "busy" {
		select {
		case <-ctx.Done():
			return
		case <-time.After(reattachPoll):
		}
		p, err := s.probeClaude(ctx, t, h, off)
		if err != nil {
			if fails++; fails >= reattachFails {
				s.log.Warn("reconnect: machine unreachable, giving up", "thread", t.ID, "err", err)
				state = "lost"
				break
			}
			continue
		}
		fails = 0
		if n := ap.feed(p.data, off); n > off {
			off, lastGrowth = n, time.Now()
		}
		state = p.state
		s.re.mu.Lock()
		if p.pid > 0 {
			run.pid, run.own = p.pid, p.own
		}
		s.re.mu.Unlock()
		if state == "busy" && time.Since(lastGrowth) > reattachStuck && p.own {
			s.log.Warn("reconnect: agent silent too long, ending it", "thread", t.ID)
			s.endProcess(ctx, h, p.pid)
			state = "gone"
		}
	}
	s.re.mu.Lock()
	stopped, pid, own := run.stopped, run.pid, run.own
	s.re.mu.Unlock()
	// an idle leftover would only sit on the session next to the new process
	if state == "idle" && own {
		s.endProcess(ctx, h, pid)
	}
	waiting := s.holds().count(t.ID)
	s.re.end(t.ID)
	s.rt.setExternal(t.ID, false)
	s.rt.setStatus(t.ID, "idle", "")
	switch {
	case stopped: // Stop, or Send now on a waiting message
		text := "Stopped."
		if waiting > 0 {
			text = "Stopped. What you sent meanwhile goes now."
		}
		s.rt.saveItem(t.ID, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "info", Text: text})
		s.holds().release(t.ID)
	case state == "lost":
		s.holds().release(t.ID)
		if waiting == 0 {
			s.cutNotice(t.ID)
		}
	case ap.ended || state == "idle" || waiting > 0:
		s.holds().release(t.ID) // what the person sent meanwhile goes now
	default: // the process went away mid-turn
		s.holds().release(t.ID)
		if fresh, err := s.store.GetThreadAny(ctx, t.ID); err == nil {
			s.autoContinue(fresh, "The agent stopped before it finished this turn. It continues on its own:")
		}
	}
	// Remote Control waited for the leftover (attachRemote): it starts now
	if s.rt.remoteWanted(t.ID) {
		go func() { _ = s.rt.attachRemote(t.ID) }()
	}
}

// errLeftoverWorks: nothing starts a second process next to a leftover one.
var errLeftoverWorks = badRequest("The agent from before Pocket AI restarted is still working on this conversation. Wait until it is done, or press Stop to end it first.")

// leftoverApplier turns log lines of a leftover process into timeline
// items without repeating what Pocket AI already showed live (its own
// items have other keys than the log's for messages; tool calls share
// theirs).
type leftoverApplier struct {
	s       *Server
	ctx     context.Context
	t       *store.Thread
	log     *agent.ClaudeLog
	have    map[string]bool // item keys
	texts   map[string]bool // kind + text of recent messages
	ended   bool
	prompt  bool            // the next prompt line is the cut-off turn's own: already there
	turns   int             // footers of that turn Pocket AI already has (it ended before the restart)
	dupTurn map[string]bool // the log's keys for those footers
}

func (s *Server) newLeftoverApplier(ctx context.Context, t *store.Thread) *leftoverApplier {
	ap := &leftoverApplier{s: s, ctx: ctx, t: t, log: &agent.ClaudeLog{}, have: map[string]bool{}, texts: map[string]bool{}, dupTurn: map[string]bool{}}
	items, _ := s.store.ListItems(ctx, t.ID)
	lastUser := 0
	for i, it := range items {
		ap.have[it.Key] = true
		if it.Kind == "user" {
			lastUser = i
		}
	}
	for _, it := range items[lastUser:] {
		if it.Kind == "turn" {
			ap.turns++
		}
	}
	// The catch-up starts at the log's last prompt, which can be well before
	// the last message here: one sent mid-turn is no prompt in the log (a
	// queued command). So messages are matched over the recent past, not
	// just since the last one.
	from := len(items) - leftoverTexts
	if from < 0 {
		from = 0
	}
	for _, it := range items[from:] {
		if it.Kind == "user" || it.Kind == "assistant" {
			var x agent.Item
			if json.Unmarshal(it.Data, &x) == nil {
				ap.texts[it.Kind+"\x00"+strings.TrimSpace(x.Text)] = true
			}
		}
	}
	return ap
}

// logLines splits data (read from off) into complete lines; next is the
// offset after the last one.
func logLines(data []byte, off int64) (lines [][]byte, next int64) {
	next = off
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		if i > 0 {
			lines = append(lines, data[:i])
		}
		next += int64(i) + 1
		data = data[i+1:]
	}
}

// catchUp applies the log from the last prompt in data (read from off) on.
func (ap *leftoverApplier) catchUp(data []byte, off int64) int64 {
	if off > 0 { // started mid-line
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return off
		}
		data, off = data[i+1:], off+int64(i)+1
	}
	lines, next := logLines(data, off)
	start := len(lines)
	scan := &agent.ClaudeLog{}
	for i, l := range lines {
		for _, op := range scan.Feed(l) {
			if op.TurnStarted {
				start = i
			}
		}
	}
	ap.prompt = true
	for _, l := range lines[start:] {
		ap.apply(l)
	}
	return next
}

// feed applies the complete lines of data (read from off); it returns the
// offset after them.
func (ap *leftoverApplier) feed(data []byte, off int64) int64 {
	lines, next := logLines(data, off)
	for _, l := range lines {
		ap.apply(l)
	}
	if next == off && len(data) >= reattachWindow {
		return off + int64(len(data)) // one line longer than a look (pictures): skip it
	}
	return next
}

func (ap *leftoverApplier) apply(line []byte) {
	unlock := ap.s.lockThreadEdit(ap.t.ID)
	defer unlock()
	rt := ap.s.rt
	for _, op := range ap.log.Feed(line) {
		if op.TurnEnded {
			ap.ended = true
		}
		if op.TurnStarted {
			ap.ended = false
		}
		if op.Result != nil {
			if st, err := ap.s.store.GetItem(ap.ctx, ap.t.ID, op.Result.ToolUseID); err == nil {
				var it agent.Item
				if json.Unmarshal(st.Data, &it) == nil && it.Tool != nil && (it.Tool.Status == "running" || it.Tool.Output == "") {
					op.Result.Apply(it.Tool)
					rt.saveItem(ap.t.ID, it)
				}
			}
		}
		it := op.Item
		if it == nil {
			continue
		}
		switch it.Kind {
		case "user", "assistant":
			if it.Kind == "user" && op.TurnStarted && ap.prompt {
				ap.prompt = false
				continue
			}
			k := it.Kind + "\x00" + strings.TrimSpace(it.Text)
			if ap.texts[k] || ap.have[it.Key] {
				continue
			}
			ap.texts[k] = true
		case "tool", "todos":
			if ap.have[it.Key] {
				continue
			}
		case "turn":
			if ap.dupTurn[it.Key] {
				continue
			}
			if !ap.have[it.Key] && ap.turns > 0 {
				ap.turns-- // the log's footer of a turn Pocket AI closed itself
				ap.dupTurn[it.Key] = true
				continue
			}
		}
		ap.have[it.Key] = true
		rt.saveItem(ap.t.ID, *it)
		if it.Kind == "assistant" {
			rt.setPreview(ap.ctx, ap.t.ID, it.Text)
		}
	}
}
