package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Away from keyboard. While a person is away, what an agent would stop and
// wait for them on (a permission, a question, a Pocket app change) is not left
// waiting: the agent hears "not now, carry on with the rest" and the request
// is kept. When none of their conversations works any more they get one
// summary with everything to decide. Decisions go back to each conversation
// as one message, and an approved call runs without asking again when the
// agent repeats it. Plans still wait: there is nothing else to do in plan mode.

const awayKey = "away"

// how long an approval given after being away waits for the agent to repeat the call
const awayApprovalTTL = 24 * time.Hour

// the agent repeats an approved call soon after hearing the decisions
const awayDecisionDelay = 2500 * time.Millisecond

const awayDeferText = "The user is away from the keyboard (Pocket AI away mode), so this was NOT run and nobody can approve it right now. " +
	"It is saved for them to decide when they are back: do not ask for it again and do not wait for it. " +
	"Carry on with every part of the task that does not depend on it; if nothing is left, end your turn. " +
	"In your final message, list what is still waiting for their approval and what you will do once they decide."

const awayQuestionText = "The user is away from the keyboard (Pocket AI away mode) and cannot answer now; the question is saved for them. " +
	"Do not wait: if a choice is safe and easy to undo, make the most sensible one and say which you picked; " +
	"otherwise carry on with the work that does not depend on the answer. In your final message, list the questions still open."

const awayNote = "\n\n[Pocket AI: the user is away from the keyboard. Don't stop to ask them in chat: decide what is safe and easy to undo yourself. " +
	"If an approval or a question is declined because they are away, keep working on everything that does not depend on it, " +
	"and end with a short list of what is waiting for them. Sub-agents you start get the same answers: tell them to carry on past " +
	"anything declined as away and to report it back to you, then include what they report in your list.]"

// how long none of a person's conversations must stay quiet before the summary
// goes out: a background sub-agent that finishes wakes its agent for another turn.
var awayDoneDelay = 8 * time.Second

// awaySummarySent sees each summary (tests).
var awaySummarySent func(uid int64, title string)

type awayState struct {
	On    bool   `json:"on"`
	Since string `json:"since,omitempty"`
}

// awayBook is what away mode keeps in memory: decisions on their way to a
// conversation, and per person what finished while they were away.
type awayBook struct {
	mu        sync.Mutex
	decisions map[string]*awayBatch     // thread id → decisions not sent yet
	finished  map[int64]map[string]bool // person → threads that finished a turn while away
	dirty     map[int64]bool            // person → something happened since the last summary
	timers    map[int64]*time.Timer     // person → summary check waiting for quiet
}

type awayBatch struct {
	by    int64
	lines []string
	timer *time.Timer
}

func (s *Server) awayState(ctx context.Context, uid int64) awayState {
	var st awayState
	_ = json.Unmarshal([]byte(s.store.UserSetting(ctx, uid, awayKey)), &st)
	return st
}

func (s *Server) isAway(ctx context.Context, uid int64) bool {
	return uid > 0 && s.awayState(ctx, uid).On
}

// awayMatch names an exact call: the tool and its input, without the parts
// an agent words differently when it repeats the same call.
func awayMatch(tool string, input json.RawMessage) string {
	var v any
	_ = json.Unmarshal(input, &v)
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"description", "request_id", "reason", "justification"} {
			delete(m, k)
		}
	}
	b, _ := json.Marshal(v) // map keys come out sorted
	sum := sha256.Sum256(append([]byte(tool+"\x00"), b...))
	return hex.EncodeToString(sum[:])
}

// takeAwayApproval reports whether the person approved exactly this call
// after being away, and uses that approval up.
func (s *Server) takeAwayApproval(ctx context.Context, threadID, match string) bool {
	since := time.Now().Add(-awayApprovalTTL).UTC().Format(time.RFC3339Nano)
	res, err := s.store.DB.ExecContext(ctx, `UPDATE deferred_requests SET used=1 WHERE rowid = (
		SELECT rowid FROM deferred_requests WHERE thread_id=? AND match=? AND status='allowed' AND used=0 AND decided_at > ? LIMIT 1)`, threadID, match, since)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// saveDeferred keeps a request that was answered "not now" for its person.
func (s *Server) saveDeferred(threadID string, req agent.Request, uid int64, match string) {
	req.Status = "deferred"
	s.rt.saveItem(threadID, agent.Item{Key: "req-" + req.ID, Kind: "request", Request: &req})
	_, err := s.store.DB.ExecContext(s.rt.ctxOrBackground(), `INSERT OR REPLACE INTO deferred_requests (thread_id, id, user_id, match, status, created_at) VALUES (?,?,?,?, 'deferred', ?)`,
		threadID, req.ID, uid, match, store.Now())
	if err != nil {
		s.log.Warn("away: save deferred request", "thread", threadID, "err", err)
	}
	s.awayMark(uid)
	s.rt.toUser(uid, sseEvent{Name: "away", Data: map[string]any{}})
	s.log.Info("away: deferred a request", "thread", threadID, "type", req.Type, "tool", req.ToolName)
}

// deferAgentRequest answers a CLI's request "not now" because its person is
// away. false = it could not (the request stays waiting as usual).
func (k *runSink) deferAgentRequest(req agent.Request, uid int64) bool {
	msg := awayDeferText
	if req.Type == "question" {
		msg = awayQuestionText
	}
	if err := k.r.sess.Respond(req.ID, agent.Response{Decision: "defer", Message: msg}); err != nil {
		k.rt.s.log.Warn("away: defer request", "thread", k.r.threadID, "err", err)
		return false
	}
	k.r.mu.Lock()
	k.r.lastUsed = time.Now()
	k.r.mu.Unlock()
	k.rt.s.saveDeferred(k.r.threadID, req, uid, awayMatch(req.ToolName, req.Input))
	return true
}

// approvedAgentRequest lets a call through that its person approved after
// being away (the agent repeats it). It reports whether it did.
func (k *runSink) approvedAgentRequest(req agent.Request) bool {
	if req.Type != "permission" || !k.rt.s.takeAwayApproval(k.rt.ctxOrBackground(), k.r.threadID, awayMatch(req.ToolName, req.Input)) {
		return false
	}
	if err := k.r.sess.Respond(req.ID, agent.Response{Decision: "allow"}); err != nil {
		return false
	}
	req.Status, req.Auto = "allowed", "away"
	k.rt.saveItem(k.r.threadID, agent.Item{Key: "req-" + req.ID, Kind: "request", Request: &req})
	return true
}

// deferWaiting answers "not now" what already waits on a person who just
// went away (their plans keep waiting).
func (s *Server) deferWaiting(uid int64) {
	s.rt.mu.Lock()
	runs := make([]*run, 0, len(s.rt.runs))
	for _, r := range s.rt.runs {
		runs = append(runs, r)
	}
	s.rt.mu.Unlock()
	ctx := s.rt.ctxOrBackground()
	for _, r := range runs {
		r.mu.Lock()
		ids := make([]string, 0, len(r.pending))
		for id := range r.pending {
			ids = append(ids, id)
		}
		r.mu.Unlock()
		for _, id := range ids {
			st, err := s.store.GetItem(ctx, r.threadID, "req-"+id)
			if err != nil {
				continue
			}
			var it agent.Item
			if json.Unmarshal(st.Data, &it) != nil || it.Request == nil || it.Request.Status != "pending" || it.Request.Type == "plan" {
				continue
			}
			who := r.userID
			if it.Request.ForID != 0 {
				who = it.Request.ForID
			}
			if who != uid {
				continue
			}
			t, err := s.store.GetThreadAny(ctx, r.threadID)
			if err != nil {
				continue
			}
			if err := s.rt.respond(t, id, agent.Response{Decision: "defer"}); err != nil {
				s.log.Warn("away: defer waiting request", "thread", r.threadID, "err", err)
			}
		}
	}
}

// awayMark: something for the summary happened to an away person.
func (s *Server) awayMark(uid int64) {
	s.aw.mu.Lock()
	defer s.aw.mu.Unlock()
	if s.aw.dirty == nil {
		s.aw.dirty = map[int64]bool{}
	}
	s.aw.dirty[uid] = true
}

// awayTurnDone notes a finished turn of someone away: no push for it, it
// is in the summary.
func (s *Server) awayTurnDone(uid int64, threadID string) {
	s.aw.mu.Lock()
	defer s.aw.mu.Unlock()
	if s.aw.finished == nil {
		s.aw.finished, s.aw.dirty = map[int64]map[string]bool{}, map[int64]bool{}
	}
	if s.aw.finished[uid] == nil {
		s.aw.finished[uid] = map[string]bool{}
	}
	s.aw.finished[uid][threadID] = true
	s.aw.dirty[uid] = true
}

// awayCheckDone sends the summary once none of an away person's
// conversations (sub-agents included) works any more and stays so for
// awayDoneDelay (waiting on a plan counts as done).
func (s *Server) awayCheckDone(uid int64) {
	s.aw.mu.Lock()
	if s.aw.timers == nil {
		s.aw.timers = map[int64]*time.Timer{}
	}
	if t := s.aw.timers[uid]; t != nil {
		t.Reset(awayDoneDelay)
	} else {
		s.aw.timers[uid] = time.AfterFunc(awayDoneDelay, func() {
			s.aw.mu.Lock()
			delete(s.aw.timers, uid)
			s.aw.mu.Unlock()
			s.awaySummaryIfDone(uid)
		})
	}
	s.aw.mu.Unlock()
}

func (s *Server) awaySummaryIfDone(uid int64) {
	ctx := s.rt.ctxOrBackground()
	if !s.isAway(ctx, uid) {
		return
	}
	s.rt.mu.Lock()
	runs := make([]*run, 0, len(s.rt.runs))
	for _, r := range s.rt.runs {
		if r.userID == uid {
			runs = append(runs, r)
		}
	}
	s.rt.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		busy := r.working() && !(r.running && len(r.pending) > 0 && len(r.queue) == 0)
		r.mu.Unlock()
		if busy {
			return
		}
	}
	s.aw.mu.Lock()
	if !s.aw.dirty[uid] {
		s.aw.mu.Unlock()
		return
	}
	delete(s.aw.dirty, uid)
	finished := len(s.aw.finished[uid])
	delete(s.aw.finished, uid)
	s.aw.mu.Unlock()
	sum := s.awaySummary(ctx, uid)
	waiting := len(sum.Items) + len(sum.Waiting)
	title := "Everything is done"
	kind := "finished"
	if waiting > 0 {
		title, kind = fmt.Sprintf("Everything else is done · %s for you", plural(waiting, "decision", "decisions")), "input"
	}
	body := ""
	if finished > 0 {
		body = plural(finished, "conversation", "conversations") + " finished while you were away."
	}
	if waiting > 0 {
		body = strings.TrimSpace(body + " Open Pocket AI to approve or answer what they saved for you.")
	}
	s.log.Info("away: all work done", "user", uid, "finished", finished, "waiting", waiting)
	if awaySummarySent != nil {
		awaySummarySent(uid, title)
	}
	s.notifyMobile(uid, kind, title, body, "")
	s.notifyUser(ctx, uid, kind, title, body, strings.TrimRight(s.cfg.BaseURL, "/")+"/away")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

type awayEntry struct {
	ThreadID    string         `json:"thread_id"`
	ThreadTitle string         `json:"thread_title"`
	Key         string         `json:"key"`
	Request     *agent.Request `json:"request"`
	At          string         `json:"at"`
}

type awaySum struct {
	awayState
	Items   []awayEntry `json:"items"`   // answered "not now", waiting for a decision
	Waiting []awayEntry `json:"waiting"` // still waiting in a live conversation (plans, or asked before going away)
	Working int         `json:"working"` // conversations of theirs still at work
}

func (s *Server) awaySummary(ctx context.Context, uid int64) awaySum {
	out := awaySum{awayState: s.awayState(ctx, uid), Items: []awayEntry{}, Waiting: []awayEntry{}}
	titles := map[string]string{}
	title := func(id string) string {
		if v, ok := titles[id]; ok {
			return v
		}
		v := "Conversation"
		if t, err := s.store.GetThreadAny(ctx, id); err == nil && t.Title != "" {
			v = t.Title
		}
		titles[id] = v
		return v
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT thread_id, id, created_at FROM deferred_requests WHERE user_id=? AND status='deferred' ORDER BY created_at`, uid)
	if err == nil {
		type row struct{ thread, id, at string }
		var list []row
		for rows.Next() {
			var x row
			if rows.Scan(&x.thread, &x.id, &x.at) == nil {
				list = append(list, x)
			}
		}
		rows.Close()
		for _, x := range list {
			st, err := s.store.GetItem(ctx, x.thread, "req-"+x.id)
			if err != nil {
				continue
			}
			var it agent.Item
			if json.Unmarshal(st.Data, &it) != nil || it.Request == nil || it.Request.Status != "deferred" {
				continue
			}
			out.Items = append(out.Items, awayEntry{ThreadID: x.thread, ThreadTitle: title(x.thread), Key: it.Key, Request: it.Request, At: x.at})
		}
	}
	s.rt.mu.Lock()
	runs := make([]*run, 0, len(s.rt.runs))
	for _, r := range s.rt.runs {
		runs = append(runs, r)
	}
	s.rt.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		ids := make([]string, 0, len(r.pending))
		for id := range r.pending {
			ids = append(ids, id)
		}
		busy := r.userID == uid && r.working() && len(r.pending) == 0
		r.mu.Unlock()
		if busy {
			out.Working++
		}
		for _, id := range ids {
			st, err := s.store.GetItem(ctx, r.threadID, "req-"+id)
			if err != nil {
				continue
			}
			var it agent.Item
			if json.Unmarshal(st.Data, &it) != nil || it.Request == nil || it.Request.Status != "pending" {
				continue
			}
			who := r.userID
			if it.Request.ForID != 0 {
				who = it.Request.ForID
			}
			if who == uid {
				out.Waiting = append(out.Waiting, awayEntry{ThreadID: r.threadID, ThreadTitle: title(r.threadID), Key: it.Key, Request: it.Request, At: st.CreatedAt})
			}
		}
	}
	sort.Slice(out.Waiting, func(i, j int) bool { return out.Waiting[i].At < out.Waiting[j].At })
	return out
}

// deferredFor: the request is one that was answered "not now" for this
// person, waiting for their decision.
func (s *Server) deferredFor(ctx context.Context, threadID, reqID string, uid int64) (bool, error) {
	var owner int64
	var status string
	err := s.store.DB.QueryRowContext(ctx, `SELECT user_id, status FROM deferred_requests WHERE thread_id=? AND id=?`, threadID, reqID).Scan(&owner, &status)
	if err != nil || status != "deferred" {
		return false, nil
	}
	if owner != uid {
		return true, errForbidden("this was saved for someone else: only they can decide it")
	}
	return true, nil
}

// decideDeferred records the person's decision on a saved request and tells
// the conversation (decisions arriving together go out as one message).
func (s *Server) decideDeferred(ctx context.Context, uid int64, t *store.Thread, reqID string, resp agent.Response) error {
	st, err := s.store.GetItem(ctx, t.ID, "req-"+reqID)
	if err != nil {
		return badRequest("that request is gone")
	}
	var it agent.Item
	if json.Unmarshal(st.Data, &it) != nil || it.Request == nil || it.Request.Status != "deferred" {
		return badRequest("that was decided already")
	}
	req := it.Request
	status := "allowed"
	switch {
	case resp.Decision == "deny":
		status = "denied"
	case req.Type == "question":
		status = "answered"
	}
	res, err := s.store.DB.ExecContext(ctx, `UPDATE deferred_requests SET status=?, decided_at=? WHERE thread_id=? AND id=? AND status='deferred'`, status, store.Now(), t.ID, reqID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return badRequest("that was decided already")
	}
	req.Status, req.Answer = status, resp
	s.rt.saveItem(t.ID, it)
	s.rt.toUser(uid, sseEvent{Name: "away", Data: map[string]any{}})

	line := awayDecisionLine(req, resp)
	s.aw.mu.Lock()
	if s.aw.decisions == nil {
		s.aw.decisions = map[string]*awayBatch{}
	}
	b := s.aw.decisions[t.ID]
	if b == nil {
		b = &awayBatch{by: uid}
		s.aw.decisions[t.ID] = b
		id := t.ID
		b.timer = time.AfterFunc(awayDecisionDelay, func() { s.sendDecisions(id) })
	} else {
		b.timer.Reset(awayDecisionDelay)
	}
	b.lines = append(b.lines, line)
	s.aw.mu.Unlock()
	return nil
}

func awayDecisionLine(req *agent.Request, resp agent.Response) string {
	what := req.Title
	if what == "" {
		what = req.ToolName
	}
	var in map[string]any
	_ = json.Unmarshal(req.Input, &in)
	if c, ok := in["command"].(string); ok && c != "" {
		what += ": `" + oneLine(c, 200) + "`"
	} else if p, ok := in["file_path"].(string); ok && p != "" && !strings.Contains(what, p) {
		what += " (" + p + ")"
	}
	if req.Type == "question" {
		var parts []string
		for _, q := range req.Questions {
			a := strings.Join(resp.Answers[q.ID], ", ")
			if resp.Decision == "deny" || a == "" {
				a = "(no answer — decide yourself)"
			}
			parts = append(parts, fmt.Sprintf("“%s” → %s", oneLine(q.Question, 200), a))
		}
		if len(parts) == 0 {
			return "- Your question: no answer — decide yourself."
		}
		return "- Answer to your question: " + strings.Join(parts, "; ")
	}
	if resp.Decision == "deny" {
		line := "- Denied: " + what + ". Don't do it; finish without it."
		if m := strings.TrimSpace(resp.Message); m != "" {
			line += " My note: " + m
		}
		return line
	}
	return "- Approved: " + what + ". Do it now (it will run without asking)."
}

// sendDecisions posts the decisions on what a conversation saved while its
// person was away, as their message.
func (s *Server) sendDecisions(threadID string) {
	s.aw.mu.Lock()
	b := s.aw.decisions[threadID]
	delete(s.aw.decisions, threadID)
	s.aw.mu.Unlock()
	if b == nil || len(b.lines) == 0 {
		return
	}
	ctx := s.rt.ctxOrBackground()
	t, err := s.store.GetThreadAny(ctx, threadID)
	if err != nil {
		return
	}
	u, err := s.store.GetUser(ctx, b.by)
	if err != nil {
		return
	}
	text := "Here is what I decided on what you saved for me while I was away:\n" + strings.Join(b.lines, "\n") +
		"\nCarry on with these, and with anything that was waiting on them."
	if _, err := s.postMessage(ctx, u, t, text, nil); err != nil {
		s.log.Warn("away: send decisions", "thread", threadID, "err", err)
		s.rt.saveItem(threadID, agent.Item{Key: fmt.Sprintf("away-%d", time.Now().UnixNano()), Kind: "notice", Level: "error", Text: "Could not send your decisions to the agent: " + err.Error()})
	}
}

// ---------- API ----------

func (s *Server) handleGetAway(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.awaySummary(r.Context(), me(r)))
}

func (s *Server) handlePutAway(w http.ResponseWriter, r *http.Request) {
	var in struct {
		On bool `json:"on"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	uid := me(r)
	st := s.awayState(r.Context(), uid)
	if st.On != in.On {
		st = awayState{On: in.On}
		if in.On {
			st.Since = store.Now()
		}
		raw, _ := json.Marshal(st)
		if err := s.store.SetUserSetting(r.Context(), uid, awayKey, string(raw)); err != nil {
			s.fail(w, err, "save away mode")
			return
		}
		s.aw.mu.Lock()
		delete(s.aw.finished, uid)
		delete(s.aw.dirty, uid)
		s.aw.mu.Unlock()
		s.log.Info("away mode", "user", uid, "on", in.On)
		if in.On {
			s.deferWaiting(uid)
		}
		s.rt.toUser(uid, sseEvent{Name: "away", Data: map[string]any{}})
	}
	writeJSON(w, 200, s.awaySummary(r.Context(), uid))
}

// handleAwayAllowAll approves every saved permission and app change (not
// questions: those need an answer).
func (s *Server) handleAwayAllowAll(w http.ResponseWriter, r *http.Request) {
	uid := me(r)
	n := 0
	for _, e := range s.awaySummary(r.Context(), uid).Items {
		if e.Request.Type != "permission" {
			continue
		}
		t, err := s.store.GetThreadAny(r.Context(), e.ThreadID)
		if err != nil {
			continue
		}
		if err := s.decideDeferred(r.Context(), uid, t, e.Request.ID, agent.Response{Decision: "allow"}); err == nil {
			n++
		}
	}
	writeJSON(w, 200, map[string]int{"allowed": n})
}
