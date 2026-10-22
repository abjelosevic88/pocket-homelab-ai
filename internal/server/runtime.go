package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	Name string
	Data any
}

// engine owns the live CLI processes (one per active thread), the live
// copies of items that are still streaming, and the SSE subscribers.
type engine struct {
	s   *Server
	ctx context.Context

	mu        sync.Mutex
	runs      map[string]*run                       // thread id → live process
	watchers  map[string]map[chan sseEvent]struct{} // thread id → SSE clients of that thread
	userSubs  map[int64]map[chan sseEvent]struct{}  // user id → SSE clients of /events
	limits    map[string]*provLimits                // limitsKey (provider[:account]) → plan limit windows
	limitsAt  time.Time                             // last full read (both CLIs)
	limitsMu  sync.Mutex                            // one full read at a time
	starting  map[string]chan struct{}              // thread id → start in progress
	followers map[string]*follower                  // thread id → log follower (follow.go)
	external  map[string]bool                       // thread id → running somewhere else
	extSubs   map[string][]agent.Subagent           // thread id → sub-agents of a session run elsewhere
	extSubsAt map[string]time.Time                  // when extSubs last went out
	active    []agent.ActiveSession                 // last scan of the CLIs' session files (running.go)
}

// run is one live CLI process attached to a thread.
type run struct {
	threadID string
	userID   int64
	provider string
	account  int64 // the login it runs on (0 = the server's own)
	sess     agent.Session
	cancel   context.CancelFunc

	mu        sync.Mutex
	running   bool
	pending   map[string]bool
	queue     []queued
	live      map[string]*agent.Item // items still streaming
	dirty     map[string]bool
	lastUsed  time.Time
	stale     bool // settings changed: restart after this turn
	turnCount int
	lastText  string // last assistant text of the current turn
	subs      []agent.Subagent
}

type queued struct {
	key string
	in  agent.Input
}

func newRuntime(s *Server) *engine {
	return &engine{s: s, runs: map[string]*run{}, watchers: map[string]map[chan sseEvent]struct{}{},
		userSubs: map[int64]map[chan sseEvent]struct{}{}, limits: map[string]*provLimits{}, starting: map[string]chan struct{}{},
		followers: map[string]*follower{}, external: map[string]bool{}, extSubs: map[string][]agent.Subagent{}, extSubsAt: map[string]time.Time{}}
}

func (rt *engine) start(ctx context.Context) {
	rt.ctx = ctx
	keys := []string{"claude", "codex"}
	if list, err := rt.s.store.ListAccounts(ctx); err == nil {
		for _, a := range list {
			keys = append(keys, limitsKey(a.Provider, a.ID))
		}
	}
	for _, p := range keys {
		var l provLimits
		if v := rt.s.store.BoxSetting(ctx, "limits."+p); v != "" && json.Unmarshal([]byte(v), &l) == nil {
			rt.limits[p] = &l
		}
	}
	go rt.reaper(ctx)
	go rt.activeLoop(ctx)
	go rt.flusher(ctx)
}

func (rt *engine) liveCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.runs)
}

func (rt *engine) get(threadID string) *run {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.runs[threadID]
}

// decorate fills a thread's live fields.
func (rt *engine) decorate(t *store.Thread) *store.Thread {
	ownSubs := false
	if r := rt.get(t.ID); r != nil {
		r.mu.Lock()
		t.Live = true
		t.Pending = len(r.pending)
		t.Queued = len(r.queue)
		if len(r.subs) > 0 {
			t.Subagents = append([]agent.Subagent(nil), r.subs...)
			ownSubs = true
		}
		r.mu.Unlock()
	}
	t.External = rt.isExternal(t.ID)
	rt.mu.Lock()
	if subs := rt.extSubs[t.ID]; len(subs) > 0 && !ownSubs {
		t.Subagents = append([]agent.Subagent(nil), subs...)
	}
	rt.mu.Unlock()
	return t
}

// viewing reports whether someone has the thread open right now.
func (rt *engine) viewing(threadID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.watchers[threadID]) > 0
}

// ---------- SSE fan-out ----------

func (rt *engine) subscribeThread(threadID string) chan sseEvent {
	ch := make(chan sseEvent, 512)
	rt.mu.Lock()
	if rt.watchers[threadID] == nil {
		rt.watchers[threadID] = map[chan sseEvent]struct{}{}
	}
	rt.watchers[threadID][ch] = struct{}{}
	rt.mu.Unlock()
	return ch
}

func (rt *engine) unsubscribeThread(threadID string, ch chan sseEvent) {
	rt.mu.Lock()
	delete(rt.watchers[threadID], ch)
	if len(rt.watchers[threadID]) == 0 {
		delete(rt.watchers, threadID)
	}
	rt.mu.Unlock()
}

func (rt *engine) subscribeUser(uid int64) chan sseEvent {
	ch := make(chan sseEvent, 256)
	rt.mu.Lock()
	if rt.userSubs[uid] == nil {
		rt.userSubs[uid] = map[chan sseEvent]struct{}{}
	}
	rt.userSubs[uid][ch] = struct{}{}
	rt.mu.Unlock()
	return ch
}

func (rt *engine) unsubscribeUser(uid int64, ch chan sseEvent) {
	rt.mu.Lock()
	delete(rt.userSubs[uid], ch)
	if len(rt.userSubs[uid]) == 0 {
		delete(rt.userSubs, uid)
	}
	rt.mu.Unlock()
}

func deliver(ch chan sseEvent, ev sseEvent) {
	select {
	case ch <- ev:
	default: // a stuck client misses events; it reloads on reconnect
	}
}

func (rt *engine) toThread(threadID string, ev sseEvent) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for ch := range rt.watchers[threadID] {
		deliver(ch, ev)
	}
}

func (rt *engine) toUser(uid int64, ev sseEvent) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for ch := range rt.userSubs[uid] {
		deliver(ch, ev)
	}
}

// publishThread sends the thread's current state to its viewers and to its owner's lists.
func (rt *engine) publishThread(t *store.Thread) {
	rt.decorate(t)
	ev := sseEvent{Name: "thread", Data: t}
	rt.toThread(t.ID, ev)
	rt.toUser(t.UserID, ev)
}

// ---------- items ----------

// itemJSON is what the API returns for one timeline item.
func itemJSON(it *store.Item) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(it.Data, &out)
	out["id"] = it.ID
	out["key"] = it.Key
	out["kind"] = it.Kind
	out["created_at"] = it.CreatedAt
	out["updated_at"] = it.UpdatedAt
	return out
}

func itemBody(it agent.Item) string {
	switch it.Kind {
	case "user", "assistant":
		return it.Text
	}
	return ""
}

// saveItem persists an item and tells the thread's viewers.
func (rt *engine) saveItem(threadID string, it agent.Item) *store.Item {
	data, _ := json.Marshal(it)
	st, err := rt.s.store.UpsertItem(rt.ctx, threadID, it.Key, it.Kind, data, itemBody(it))
	if err != nil {
		rt.s.log.Error("save item", "thread", threadID, "err", err)
		return nil
	}
	rt.toThread(threadID, sseEvent{Name: "item", Data: itemJSON(st)})
	return st
}

// liveItems returns the streaming items of a thread (for GET and SSE snapshots).
func (rt *engine) liveItems(threadID string) map[string]agent.Item {
	r := rt.get(threadID)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]agent.Item{}
	for k, it := range r.live {
		out[k] = *it
	}
	return out
}

// flusher writes streaming items to the database every two seconds so a
// reload mid-turn shows close to what is on screen.
func (rt *engine) flusher(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rt.mu.Lock()
		runs := make([]*run, 0, len(rt.runs))
		for _, r := range rt.runs {
			runs = append(runs, r)
		}
		rt.mu.Unlock()
		for _, r := range runs {
			rt.flush(r)
		}
	}
}

func (rt *engine) flush(r *run) {
	r.mu.Lock()
	var items []agent.Item
	for k := range r.dirty {
		if it := r.live[k]; it != nil {
			items = append(items, *it)
		}
		delete(r.dirty, k)
	}
	r.mu.Unlock()
	for _, it := range items {
		data, _ := json.Marshal(it)
		_, _ = rt.s.store.UpsertItem(rt.ctx, r.threadID, it.Key, it.Kind, data, itemBody(it))
	}
}

// ---------- starting and sending ----------

func (rt *engine) systemPrompt(ctx context.Context, uid int64) string {
	st, _ := rt.s.store.GetSettings(ctx, uid)
	base := "You are being used through Pocket AI, a web and phone interface to the coding-agent CLIs on this server. " +
		"The person often reads on a phone: keep answers short, put the conclusion first, and use compact Markdown " +
		"(short lists, small tables, code blocks only for commands or code)."
	if strings.TrimSpace(st.SystemPrompt) != "" {
		return base + "\n\n" + st.SystemPrompt
	}
	return base
}

// ensure returns the live process of a thread, starting (or resuming) it if needed.
func (rt *engine) ensure(t *store.Thread) (*run, error) {
	for {
		rt.mu.Lock()
		if r := rt.runs[t.ID]; r != nil {
			rt.mu.Unlock()
			return r, nil
		}
		if wait := rt.starting[t.ID]; wait != nil {
			rt.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		rt.starting[t.ID] = done
		rt.mu.Unlock()
		r, err := rt.startRun(t)
		rt.mu.Lock()
		delete(rt.starting, t.ID)
		if r != nil {
			rt.runs[t.ID] = r
		}
		rt.mu.Unlock()
		close(done)
		if err == nil {
			rt.enforceMax()
		}
		return r, err
	}
}

func (rt *engine) startRun(t *store.Thread) (*run, error) {
	if fi, err := os.Stat(t.Cwd); err != nil || !fi.IsDir() {
		return nil, badRequest("the folder " + t.Cwd + " does not exist any more")
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	r := &run{threadID: t.ID, userID: t.UserID, provider: t.Provider, account: t.AccountID, cancel: cancel, pending: map[string]bool{},
		live: map[string]*agent.Item{}, dirty: map[string]bool{}, lastUsed: time.Now()}
	o := agent.Options{Cwd: t.Cwd, Model: t.Model, Effort: t.Effort, Mode: t.Mode, Speed: t.Speed, ResumeID: t.ProviderSessionID,
		SystemPrompt: rt.systemPrompt(ctx, t.UserID), Env: []string{"POCKET_AI=1"}, Log: rt.s.log}
	if o.ResumeID == "" && strings.HasPrefix(t.SourceRef, "fork:") {
		o.ResumeID, o.Fork = strings.TrimPrefix(t.SourceRef, "fork:"), true
	}
	// The account's folder, with the session in it (it may have been
	// started on another login).
	o.Env = append(o.Env, rt.s.accountEnv(ctx, t.Provider, t.AccountID)...)
	if err := rt.s.ensureSession(ctx, t, o.ResumeID); err != nil {
		cancel()
		return nil, err
	}
	sink := &runSink{rt: rt, r: r}
	if t.Source == "ask" {
		if err := rt.s.preparePocketTools(ctx, t, sink, &o); err != nil {
			cancel()
			return nil, err
		}
	} else if _, err := rt.s.askToken(ctx, t.UserID); err == nil {
		// An existing per-person connection is shared with ordinary coding
		// threads. Core still checks access and the proxy still gates writes.
		if err := rt.s.preparePocketTools(ctx, t, sink, &o); err != nil {
			rt.s.log.Warn("Pocket app tools unavailable", "thread", t.ID, "err", err)
			o.SystemPrompt += "\nPocket app tools are currently unavailable. For app-data questions, explain that the user should reconnect Pocket Ask; do not search credentials or databases as a workaround."
		}
	} else {
		o.SystemPrompt += "\nFor questions about Pocket app data, the user can enable the Ask connection in an application. Do not search credentials or databases to substitute for that connection."
	}
	var err error
	switch t.Provider {
	case "claude":
		o.Bin = rt.s.cfg.ClaudeBin
		r.sess, err = agent.StartClaude(ctx, o, sink)
	case "codex":
		o.Bin = rt.s.cfg.CodexBin
		r.sess, err = agent.StartCodex(ctx, o, sink)
	default:
		err = badRequest("unknown provider " + t.Provider)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	rt.s.log.Info("agent started", "thread", t.ID, "provider", t.Provider, "account", t.AccountID, "model", t.Model, "mode", t.Mode, "resume", o.ResumeID != "", "cwd", t.Cwd)
	return r, nil
}

// stop ends a thread's process (it resumes on the next message).
func (rt *engine) stop(threadID string) {
	rt.mu.Lock()
	r := rt.runs[threadID]
	delete(rt.runs, threadID)
	rt.mu.Unlock()
	if r != nil {
		rt.flush(r)
		_ = r.sess.Close()
		r.cancel()
	}
}

// reaper stops processes that sat idle longer than IDLE_TIMEOUT.
func (rt *engine) reaper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			rt.mu.Lock()
			ids := make([]string, 0, len(rt.runs))
			for id := range rt.runs {
				ids = append(ids, id)
			}
			rt.mu.Unlock()
			for _, id := range ids {
				rt.stop(id)
			}
			return
		case <-t.C:
		}
		for _, id := range rt.idle(rt.s.cfg.IdleTimeout) {
			rt.s.log.Info("agent idle, stopping", "thread", id)
			rt.stop(id)
		}
	}
}

// idle lists threads whose process has done nothing for at least d, oldest first.
func (rt *engine) idle(d time.Duration) []string {
	type cand struct {
		id   string
		last time.Time
	}
	var c []cand
	rt.mu.Lock()
	for id, r := range rt.runs {
		r.mu.Lock()
		if !r.running && len(r.pending) == 0 && len(r.queue) == 0 && len(r.subs) == 0 && time.Since(r.lastUsed) >= d {
			c = append(c, cand{id, r.lastUsed})
		}
		r.mu.Unlock()
	}
	rt.mu.Unlock()
	sort.Slice(c, func(i, j int) bool { return c[i].last.Before(c[j].last) })
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.id
	}
	return out
}

func (rt *engine) enforceMax() {
	max := rt.s.cfg.MaxLive
	if max <= 0 {
		return
	}
	over := rt.liveCount() - max
	if over <= 0 {
		return
	}
	for _, id := range rt.idle(0) {
		if over <= 0 {
			break
		}
		rt.stop(id)
		over--
	}
}

// send hands a message to the thread's agent, or queues it while a turn runs.
// The user item is written by the caller.
func (rt *engine) send(t *store.Thread, key string, in agent.Input) (bool, error) {
	r, err := rt.ensure(t)
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	if r.running || len(r.queue) > 0 {
		r.queue = append(r.queue, queued{key: key, in: in})
		r.mu.Unlock()
		rt.publishThread(t)
		return true, nil
	}
	r.running = true
	r.lastUsed = time.Now()
	r.lastText = ""
	r.mu.Unlock()
	in = rt.withMemory(r, in)
	if err := r.sess.Send(in); err != nil {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
		rt.stop(t.ID)
		return false, err
	}
	return false, nil
}

// next sends the first queued message after a turn ended.
func (rt *engine) next(r *run) {
	r.mu.Lock()
	if len(r.queue) == 0 || r.running {
		r.mu.Unlock()
		return
	}
	q := r.queue[0]
	r.queue = r.queue[1:]
	r.running = true
	r.lastUsed = time.Now()
	r.lastText = ""
	r.mu.Unlock()
	if st, err := rt.s.store.GetItem(rt.ctx, r.threadID, q.key); err == nil {
		// it goes out now: move it from where it was typed (mid-turn) to the
		// end of the conversation
		var it agent.Item
		_ = json.Unmarshal(st.Data, &it)
		it.Queued = false
		_ = rt.s.store.DeleteItem(rt.ctx, r.threadID, q.key)
		rt.toThread(r.threadID, sseEvent{Name: "item_removed", Data: map[string]string{"key": q.key}})
		rt.saveItem(r.threadID, it)
	}
	q.in = rt.withMemory(r, q.in)
	if err := r.sess.Send(q.in); err != nil {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
		rt.s.log.Warn("send queued message", "thread", r.threadID, "err", err)
	}
}

// busyCount is how many threads have a turn running or messages waiting.
func (rt *engine) busyCount() int {
	rt.mu.Lock()
	runs := make([]*run, 0, len(rt.runs))
	for _, r := range rt.runs {
		runs = append(runs, r)
	}
	rt.mu.Unlock()
	n := 0
	for _, r := range runs {
		r.mu.Lock()
		if r.running || len(r.queue) > 0 {
			n++
		}
		r.mu.Unlock()
	}
	return n
}

// sendNow puts a queued message first and stops the current turn, so it goes
// out right away (next() sends it when the interrupted turn ends).
func (rt *engine) sendNow(threadID, key string) error {
	r := rt.get(threadID)
	if r == nil {
		return badRequest("that message is no longer queued")
	}
	r.mu.Lock()
	i := -1
	for j, q := range r.queue {
		if q.key == key {
			i = j
		}
	}
	if i < 0 {
		r.mu.Unlock()
		return badRequest("that message is no longer queued")
	}
	q := r.queue[i]
	r.queue = append([]queued{q}, append(r.queue[:i:i], r.queue[i+1:]...)...)
	running := r.running
	r.mu.Unlock()
	if !running {
		rt.next(r)
		return nil
	}
	return r.sess.Interrupt()
}

// dropQueued removes one queued (or unsent) message.
func (rt *engine) dropQueued(threadID, key string) {
	if r := rt.get(threadID); r != nil {
		r.mu.Lock()
		for j, q := range r.queue {
			if q.key == key {
				r.queue = append(r.queue[:j:j], r.queue[j+1:]...)
				break
			}
		}
		r.mu.Unlock()
	}
	_ = rt.s.store.DeleteItem(rt.ctx, threadID, key)
	rt.toThread(threadID, sseEvent{Name: "item_removed", Data: map[string]string{"key": key}})
}

// clearQueue drops queued messages (and their timeline entries).
func (rt *engine) clearQueue(threadID string) {
	r := rt.get(threadID)
	if r == nil {
		return
	}
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	r.mu.Unlock()
	for _, x := range q {
		_ = rt.s.store.DeleteItem(rt.ctx, threadID, x.key)
		rt.toThread(threadID, sseEvent{Name: "item_removed", Data: map[string]string{"key": x.key}})
	}
}

// respond answers a pending request.
func (rt *engine) respond(t *store.Thread, reqID string, resp agent.Response) error {
	r := rt.get(t.ID)
	if r == nil {
		return badRequest("the agent is no longer running; send a new message instead")
	}
	r.mu.Lock()
	ok := r.pending[reqID]
	r.mu.Unlock()
	if !ok {
		return badRequest("that request is no longer waiting")
	}
	var answerErr error
	if strings.HasPrefix(reqID, "ask-") {
		answerErr = rt.s.answerAsk(t, reqID, resp)
	} else {
		answerErr = r.sess.Respond(reqID, resp)
	}
	if answerErr != nil {
		return answerErr
	}
	r.mu.Lock()
	delete(r.pending, reqID)
	r.lastUsed = time.Now()
	left := len(r.pending)
	r.mu.Unlock()
	if st, err := rt.s.store.GetItem(rt.ctx, t.ID, "req-"+reqID); err == nil {
		var it agent.Item
		_ = json.Unmarshal(st.Data, &it)
		if it.Request != nil {
			switch {
			case resp.Decision == "deny":
				it.Request.Status = "denied"
			case it.Request.Type == "question":
				it.Request.Status = "answered"
			default:
				it.Request.Status = "allowed"
			}
			it.Request.Answer = resp
			rt.saveItem(t.ID, it)
		}
	}
	if left == 0 {
		rt.setStatus(t.ID, "running", "")
	} else {
		if th, err := rt.s.store.GetThreadAny(rt.ctx, t.ID); err == nil {
			rt.publishThread(th)
		}
	}
	return nil
}

// setMode switches the permission mode of a live thread and answers the
// pending permission requests the new mode would have allowed.
func (rt *engine) setMode(t *store.Thread, mode string) {
	r := rt.get(t.ID)
	if r == nil {
		return // the next process starts with the new mode
	}
	if err := r.sess.SetMode(mode); err != nil {
		rt.s.log.Warn("set mode", "thread", t.ID, "err", err)
		r.mu.Lock()
		r.stale = true // restart with the new flags after this turn
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.pending))
	for id := range r.pending {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		// App writes always require a decision on this exact operation.
		if strings.HasPrefix(id, "ask-") {
			continue
		}
		st, err := rt.s.store.GetItem(rt.ctx, t.ID, "req-"+id)
		if err != nil {
			continue
		}
		var it agent.Item
		_ = json.Unmarshal(st.Data, &it)
		if it.Request == nil || it.Request.Type != "permission" || !modeAllows(mode, it.Request.ToolName) {
			continue
		}
		if err := rt.respond(t, id, agent.Response{Decision: "allow"}); err != nil {
			rt.s.log.Warn("auto-allow after mode change", "thread", t.ID, "err", err)
		}
	}
}

// modeAllows reports whether a permission mode would not have asked for this tool.
func modeAllows(mode, tool string) bool {
	switch mode {
	case "bypassPermissions", "full-access":
		return true
	case "acceptEdits":
		switch tool {
		case "Edit", "MultiEdit", "Write", "NotebookEdit", "Edit files":
			return true
		}
	}
	return false
}

func (rt *engine) interrupt(threadID string) error {
	r := rt.get(threadID)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	running := r.running
	r.mu.Unlock()
	if !running {
		return nil
	}
	return r.sess.Interrupt()
}

// setStatus updates a thread's status and tells everyone.
func (rt *engine) setStatus(threadID, status, errMsg string) *store.Thread {
	t, err := rt.s.store.GetThreadAny(rt.ctx, threadID)
	if err != nil {
		return nil
	}
	t.Status, t.Error = status, errMsg
	t.ActivityAt = store.Now()
	_ = rt.s.store.UpdateThread(rt.ctx, t)
	rt.publishThread(t)
	return t
}

// ---------- the sink: what the CLI does ----------

type runSink struct {
	rt *engine
	r  *run
}

func (k *runSink) Item(it agent.Item) {
	r := k.r
	r.mu.Lock()
	r.lastUsed = time.Now()
	streaming := (it.Kind == "assistant" || it.Kind == "thinking") && it.Text == "" ||
		it.Kind == "tool" && it.Tool != nil && it.Tool.Status == "running"
	if streaming {
		cp := it
		if prev := r.live[it.Key]; prev != nil && prev.Text != "" && cp.Text == "" {
			cp.Text = prev.Text
		}
		r.live[it.Key] = &cp
	} else {
		delete(r.live, it.Key)
		delete(r.dirty, it.Key)
	}
	if it.Kind == "assistant" && it.Text != "" {
		r.lastText = it.Text
	}
	r.mu.Unlock()
	k.rt.saveItem(r.threadID, it)
}

func (k *runSink) Delta(key, field, text string) {
	r := k.r
	r.mu.Lock()
	it := r.live[key]
	if it == nil {
		it = &agent.Item{Key: key, Kind: "assistant"}
		r.live[key] = it
	}
	switch field {
	case "output":
		if it.Tool != nil {
			it.Tool.Output += text
			if len(it.Tool.Output) > 200000 {
				it.Tool.Output = it.Tool.Output[len(it.Tool.Output)-200000:]
			}
		}
	default:
		it.Text += text
	}
	r.dirty[key] = true
	r.lastUsed = time.Now()
	r.mu.Unlock()
	k.rt.toThread(r.threadID, sseEvent{Name: "delta", Data: map[string]string{"key": key, "field": field, "text": text}})
}

func (k *runSink) SessionID(id string) {
	t, err := k.rt.s.store.GetThreadAny(k.rt.ctx, k.r.threadID)
	if err != nil || t.ProviderSessionID == id {
		return
	}
	t.ProviderSessionID = id
	_ = k.rt.s.store.UpdateThread(k.rt.ctx, t)
	k.rt.publishThread(t)
}

func (k *runSink) Context(c agent.Context) {
	t, err := k.rt.s.store.GetThreadAny(k.rt.ctx, k.r.threadID)
	if err != nil {
		return
	}
	if c.Tokens > 0 {
		t.Usage.ContextTokens = c.Tokens
	}
	if c.Window > 0 {
		t.Usage.ContextWindow = c.Window
	}
	_ = k.rt.s.store.UpdateThread(k.rt.ctx, t)
	k.rt.publishThread(t)
}

// provLimits are one provider's plan limit windows.
type provLimits struct {
	Provider  string         `json:"provider,omitempty"`
	AccountID int64          `json:"account_id"`
	Label     string         `json:"label,omitempty"` // the account's name (filled in per snapshot)
	Windows   []agent.Window `json:"windows"`
	Note      string         `json:"note,omitempty"`
	Resets    *agent.Resets  `json:"resets,omitempty"` // Codex free resets
	UpdatedAt string         `json:"updated_at"`
	Error     string         `json:"error,omitempty"`
}

// Limits merges what a running CLI reports (it only reports the windows of
// the model in use) into the full picture by window id.
func (k *runSink) Limits(provider string, w []agent.Window) {
	k.rt.setLimits(limitsKey(provider, k.r.account), w, nil, false, "")
}

// limitsKey names the limits of one login: "claude", "codex" for the
// server's own, "claude:3" for account 3.
func limitsKey(provider string, account int64) string {
	if account == 0 {
		return provider
	}
	return provider + ":" + strconv.FormatInt(account, 10)
}

// setLimits stores windows: replace=true for a full read, otherwise merge.
func (rt *engine) setLimits(provider string, w []agent.Window, note *string, replace bool, errMsg string) {
	rt.mu.Lock()
	cur := rt.limits[provider]
	if cur == nil {
		cur = &provLimits{}
		rt.limits[provider] = cur
	}
	switch {
	case errMsg != "":
		cur.Error = errMsg
	case replace:
		cur.Windows, cur.Error = w, ""
	default:
		for _, nw := range w {
			found := false
			for i := range cur.Windows {
				if cur.Windows[i].ID == nw.ID || cur.Windows[i].ID == "" && cur.Windows[i].Name == nw.Name {
					cur.Windows[i], found = nw, true
					break
				}
			}
			if !found {
				cur.Windows = append(cur.Windows, nw)
			}
		}
	}
	if note != nil {
		cur.Note = *note
	}
	if errMsg == "" {
		cur.UpdatedAt = store.Now()
	}
	b, _ := json.Marshal(cur)
	rt.mu.Unlock()
	_ = rt.s.store.SetBoxSetting(rt.ctx, "limits."+provider, string(b))
	rt.publishLimits()
}

// publishLimits sends every connected person the limits of the logins they
// may use.
func (rt *engine) publishLimits() {
	rt.mu.Lock()
	uids := make([]int64, 0, len(rt.userSubs))
	for uid := range rt.userSubs {
		uids = append(uids, uid)
	}
	rt.mu.Unlock()
	for _, uid := range uids {
		snap := rt.limitsSnapshot(uid)
		rt.mu.Lock()
		for ch := range rt.userSubs[uid] {
			deliver(ch, sseEvent{Name: "limits", Data: snap})
		}
		rt.mu.Unlock()
	}
}

// dropLimits forgets the limits of a removed account.
func (rt *engine) dropLimits(key string) {
	rt.mu.Lock()
	delete(rt.limits, key)
	rt.mu.Unlock()
	_ = rt.s.store.SetBoxSetting(rt.ctx, "limits."+key, "")
	rt.publishLimits()
}

// refreshLimitsNow reads the limits again right away (a new login).
func (rt *engine) refreshLimitsNow() {
	rt.mu.Lock()
	rt.limitsAt = time.Time{}
	rt.mu.Unlock()
	rt.refreshLimits(false)
}

// publishAccounts tells the web apps that the list of logins changed.
func (rt *engine) publishAccounts() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, subs := range rt.userSubs {
		for ch := range subs {
			deliver(ch, sseEvent{Name: "accounts", Data: map[string]any{}})
		}
	}
}

// stopAccount stops the idle processes of an account (signed out, removed);
// busy ones restart on their next turn.
func (rt *engine) stopAccount(id int64) {
	rt.mu.Lock()
	var ids []string
	for tid, r := range rt.runs {
		if r.account == id {
			r.mu.Lock()
			busy := r.running || len(r.pending) > 0 || len(r.queue) > 0
			r.stale = r.stale || busy
			r.mu.Unlock()
			if !busy {
				ids = append(ids, tid)
			}
		}
	}
	rt.mu.Unlock()
	for _, tid := range ids {
		rt.stop(tid)
	}
}

// refreshLimits reads the full plan limits from both CLIs (at most every
// minute); wait=false runs it in the background.
func (rt *engine) refreshLimits(wait bool) {
	run := func() {
		if !rt.limitsMu.TryLock() {
			return
		}
		defer rt.limitsMu.Unlock()
		rt.mu.Lock()
		fresh := time.Since(rt.limitsAt) < time.Minute
		rt.mu.Unlock()
		if fresh {
			return
		}
		ctx, cancel := context.WithTimeout(rt.ctx, 45*time.Second)
		defer cancel()
		type login struct {
			provider string
			id       int64
			env      []string
		}
		logins := []login{{"claude", 0, nil}, {"codex", 0, nil}}
		if list, err := rt.s.store.ListAccounts(ctx); err == nil {
			for _, a := range list {
				logins = append(logins, login{a.Provider, a.ID, agent.AccountEnv(a.Provider, a.Home)})
			}
		}
		var wg sync.WaitGroup
		for _, l := range logins {
			key := limitsKey(l.provider, l.id)
			if l.id != 0 && !rt.s.signedIn(l.provider, l.id) {
				rt.setLimits(key, nil, nil, false, "Not signed in")
				continue
			}
			wg.Add(1)
			if l.provider == "claude" {
				go func() {
					defer wg.Done()
					w, note, err := agent.ClaudeLimits(ctx, rt.s.cfg.ClaudeBin, rt.s.cfg.Home, l.env)
					if err != nil {
						rt.s.log.Debug("claude limits", "account", l.id, "err", err)
						rt.setLimits(key, nil, nil, false, err.Error())
						return
					}
					rt.setLimits(key, w, &note, true, "")
				}()
				continue
			}
			go func() {
				defer wg.Done()
				w, resets, err := agent.CodexLimits(ctx, rt.s.cfg.CodexBin, l.env)
				if err != nil {
					rt.s.log.Debug("codex limits", "account", l.id, "err", err)
					rt.setLimits(key, nil, nil, false, err.Error())
					return
				}
				rt.mu.Lock() // before setLimits, which sends the update
				if rt.limits[key] == nil {
					rt.limits[key] = &provLimits{}
				}
				rt.limits[key].Resets = resets
				rt.mu.Unlock()
				none := ""
				rt.setLimits(key, w, &none, true, "")
			}()
		}
		wg.Wait()
		rt.mu.Lock()
		rt.limitsAt = time.Now()
		rt.mu.Unlock()
	}
	if wait {
		run()
	} else {
		go run()
	}
}

// limitsStale reports whether the last full read is older than d.
func (rt *engine) limitsStale(d time.Duration) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return time.Since(rt.limitsAt) > d
}

func (rt *engine) limitsSnapshot(uid int64) map[string]any {
	u, _ := rt.s.store.GetUser(rt.ctx, uid)
	labels := map[string]string{"claude": "", "codex": ""}
	if list, err := rt.s.store.ListAccounts(rt.ctx); err == nil {
		for _, a := range list {
			if mayUseAccount(u, a) {
				labels[limitsKey(a.Provider, a.ID)] = a.Label
			}
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := map[string]any{}
	for k, l := range rt.limits {
		label, ok := labels[k]
		if !ok {
			continue
		}
		cp := *l
		cp.Provider, cp.Label = k, label
		if i := strings.IndexByte(k, ':'); i > 0 {
			cp.Provider = k[:i]
			cp.AccountID, _ = strconv.ParseInt(k[i+1:], 10, 64)
		}
		cp.Windows = append([]agent.Window(nil), l.Windows...)
		out[k] = cp
	}
	return out
}

func (k *runSink) Request(req agent.Request) {
	r := k.r
	r.mu.Lock()
	r.pending[req.ID] = true
	r.lastUsed = time.Now()
	r.mu.Unlock()
	k.rt.saveItem(r.threadID, agent.Item{Key: "req-" + req.ID, Kind: "request", Request: &req})
	t := k.rt.setStatus(r.threadID, "waiting", "")
	if t == nil {
		return
	}
	what := "needs your OK"
	switch req.Type {
	case "question":
		what = "has a question"
	case "plan":
		what = "has a plan for you"
	}
	k.rt.s.publish(t.UserID, "ai.needs_input", t.ID, map[string]any{"thread_id": t.ID, "title": t.Title, "type": req.Type, "what": req.Title})
	k.rt.notify(t, "input", fmt.Sprintf("%s %s", providerName(t.Provider), what), oneLine(req.Title+"\n"+t.Title, 200))
}

func (k *runSink) Subagents(list []agent.Subagent) {
	k.r.mu.Lock()
	k.r.subs = list
	k.r.lastUsed = time.Now()
	k.r.mu.Unlock()
	if t, err := k.rt.s.store.GetThreadAny(k.rt.ctx, k.r.threadID); err == nil {
		k.rt.publishThread(t)
	}
}

func (k *runSink) TurnStarted() {
	r := k.r
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	k.rt.setStatus(r.threadID, "running", "")
}

func (k *runSink) TurnDone(ti agent.TurnInfo) {
	r := k.r
	rt := k.rt
	rt.flush(r)
	r.mu.Lock()
	r.running = false
	r.turnCount++
	expired := make([]string, 0, len(r.pending))
	for id := range r.pending {
		expired = append(expired, id)
	}
	r.pending = map[string]bool{}
	// tools still "running" when the turn ends were cut off
	var cut []agent.Item
	for key, it := range r.live {
		if it.Kind == "tool" && it.Tool != nil && it.Tool.Status == "running" {
			c := *it
			tl := *it.Tool
			tl.Status = "error"
			if ti.Interrupted {
				tl.Status = "denied"
			}
			c.Tool = &tl
			cut = append(cut, c)
		} else if it.Text != "" {
			cut = append(cut, *it)
		}
		delete(r.live, key)
	}
	r.dirty = map[string]bool{}
	lastText := r.lastText
	stale := r.stale
	queuedN := len(r.queue)
	r.mu.Unlock()
	for _, it := range cut {
		rt.saveItem(r.threadID, it)
	}
	for _, id := range expired {
		if st, err := rt.s.store.GetItem(rt.ctx, r.threadID, "req-"+id); err == nil {
			var it agent.Item
			_ = json.Unmarshal(st.Data, &it)
			if it.Request != nil && it.Request.Status == "pending" {
				it.Request.Status = "expired"
				rt.saveItem(r.threadID, it)
			}
		}
	}
	// Keyed by the CLI's turn id where there is one, so re-reading the session
	// later (follow.go) finds this footer instead of adding a second one.
	turnKey := "turn-" + uuid.NewString()[:8]
	if ti.ID != "" {
		turnKey = "turn-" + ti.ID
	}
	rt.saveItem(r.threadID, agent.Item{Key: turnKey, Kind: "turn", Turn: &ti})

	t, err := rt.s.store.GetThreadAny(rt.ctx, r.threadID)
	if err != nil {
		return
	}
	t.Usage.InputTokens += ti.InputTokens
	t.Usage.OutputTokens += ti.OutputTokens
	t.Usage.CachedTokens += ti.CachedTokens
	t.Usage.CostUSD += ti.CostUSD
	t.Usage.Turns++
	if lastText != "" {
		t.Preview = oneLine(lastText, 200)
	}
	t.Status, t.Error = "idle", ""
	if !ti.OK && !ti.Interrupted {
		t.Status, t.Error = "error", ti.Error
	}
	viewing := rt.viewing(t.ID)
	if !viewing {
		t.Unread = true
	}
	t.ActivityAt = store.Now()
	_ = rt.s.store.UpdateThread(rt.ctx, t)
	_ = rt.s.store.AddTurn(rt.ctx, store.Turn{UserID: t.UserID, ThreadID: t.ID, Provider: t.Provider, Model: t.Model, OK: ti.OK,
		DurationMS: ti.DurationMS, InputTokens: ti.InputTokens, OutputTokens: ti.OutputTokens, CachedTokens: ti.CachedTokens, CostUSD: ti.CostUSD})
	rt.publishThread(t)

	scheduled := (t.Source == "schedule" && rt.s.scheduleFinished(t, ti, lastText)) || rt.s.workflowFinished(t, ti, lastText)
	if !scheduled && queuedN == 0 {
		switch {
		case !ti.OK && !ti.Interrupted:
			rt.notify(t, "failed", providerName(t.Provider)+" stopped with an error", oneLine(t.Title+"\n"+ti.Error, 200))
		case ti.OK:
			rt.notify(t, "finished", providerName(t.Provider)+" finished", oneLine(firstNonEmpty(t.Preview, t.Title), 240))
		}
	}
	rt.s.publish(t.UserID, "ai.turn_finished", t.ID, map[string]any{"thread_id": t.ID, "title": t.Title, "ok": ti.OK,
		"provider": t.Provider, "cost_usd": ti.CostUSD, "duration_ms": ti.DurationMS})

	if queuedN > 0 {
		rt.next(r)
		return
	}
	if stale {
		rt.stop(r.threadID)
	}
}

func (k *runSink) Exited(err error) {
	rt := k.rt
	r := k.r
	// what our process wrote to the CLI's log is on the timeline already
	if t, e := rt.s.store.GetThreadAny(rt.ctx, r.threadID); e == nil {
		rt.endOffset(t)
	}
	rt.mu.Lock()
	if rt.runs[r.threadID] == r {
		delete(rt.runs, r.threadID)
	}
	rt.mu.Unlock()
	r.cancel()
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	r.mu.Unlock()
	if err != nil {
		rt.s.log.Warn("agent exited", "thread", r.threadID, "err", err)
		rt.saveItem(r.threadID, agent.Item{Key: "exit-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		rt.setStatus(r.threadID, "error", err.Error())
	} else if t, e := rt.s.store.GetThreadAny(rt.ctx, r.threadID); e == nil && (t.Status == "running" || t.Status == "waiting") {
		rt.setStatus(r.threadID, "idle", "")
	} else if e == nil {
		rt.publishThread(t)
	}
	// Messages queued behind a process that died start a new one.
	if len(q) > 0 {
		if t, e := rt.s.store.GetThreadAny(rt.ctx, r.threadID); e == nil {
			for _, x := range q {
				if _, err := rt.send(t, x.key, x.in); err != nil {
					rt.s.log.Warn("resend queued message", "thread", t.ID, "err", err)
					break
				}
			}
		}
	}
}

// notify pushes a message to the thread's owner when they asked for that
// kind ("finished" | "input" | "failed") and are not looking at the thread.
func (rt *engine) notify(t *store.Thread, kind, title, body string) {
	st, err := rt.s.store.GetSettings(rt.ctx, t.UserID)
	if err != nil {
		return
	}
	switch kind {
	case "finished":
		if !st.NotifyFinished {
			return
		}
	case "input":
		if !st.NotifyInput {
			return
		}
	case "failed":
		if !st.NotifyFailed {
			return
		}
	}
	if st.NotifyOnlyAway && rt.viewing(t.ID) {
		return
	}
	rt.s.notifyUser(rt.ctx, t.UserID, kind, title, body, rt.s.threadURL(t.ID))
}

func providerName(p string) string {
	if p == "codex" {
		return "Codex"
	}
	return "Claude"
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// threadURL links to a thread in the web app.
func (s *Server) threadURL(id string) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	return s.cfg.BaseURL + "/t/" + id
}

// publish sends an event to Pocket Core (if configured) with the owner's email.
func (s *Server) publish(uid int64, topic, subject string, payload map[string]any) {
	if !s.core.Configured() {
		return
	}
	if u, err := s.store.GetUser(s.rt.ctx, uid); err == nil {
		payload["user_email"] = u.Email
	}
	if err := s.core.Publish(s.rt.ctx, topic, subject, payload); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Debug("pocket core: publish", "topic", topic, "err", err)
	}
}

// Resolve preferences at dispatch time, including queued turns, so a forgotten
// preference is never injected from an old in-memory queue snapshot.
func (rt *engine) withMemory(r *run, in agent.Input) agent.Input {
	t, err := rt.s.store.GetThread(rt.ctx, r.userID, r.threadID)
	if err != nil {
		return in
	}
	in.Text += rt.s.memoryContext(rt.ctx, r.userID, t.NoMemory)
	return in
}
