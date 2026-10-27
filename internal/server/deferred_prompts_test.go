package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func afterSource(t *testing.T, s *Server, owner int64, root string) *store.Thread {
	t.Helper()
	th := thread(t, s, owner, root)
	th.Status = "running"
	if err := s.store.UpdateThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	return th
}
func finishAfterSource(t *testing.T, s *Server, th *store.Thread, ok, interrupted bool) {
	t.Helper()
	ti := agent.TurnInfo{OK: ok, Interrupted: interrupted}
	data, _ := json.Marshal(agent.Item{Key: "completion", Kind: "turn", Turn: &ti})
	if _, err := s.store.UpsertItem(context.Background(), th.ID, "completion", "turn", data, ""); err != nil {
		t.Fatal(err)
	}
	th.Usage.Turns++
	th.Status = "idle"
	if !ok && !interrupted {
		th.Status = "error"
	}
	if err := s.store.UpdateThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
}
func afterJobs(t *testing.T, c *client, th *store.Thread) []deferredPrompt {
	t.Helper()
	var jobs []deferredPrompt
	c.want("GET", "/api/v1/threads/"+th.ID+"/after", nil, 200, &jobs)
	return jobs
}
func TestDeferredPromptReceiptsAndOwnership(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "after@example.test", "admin", true)
	other, v := person(t, s, h, "other-after@example.test", "member", true)
	dest := thread(t, s, u.ID, root)
	source := afterSource(t, s, u.ID, root)
	foreign := afterSource(t, s, v.ID, root)
	path := "/api/v1/threads/" + dest.ID + "/after"
	body := `{"text":"Review the result","after_thread_id":"` + source.ID + `","upload_ids":[]}`
	for i := 0; i < 2; i++ {
		w := receiptCall(h, c.token, path, "after-once", body)
		if w.Code != 201 {
			t.Fatalf("save/replay: %d %s", w.Code, w.Body.String())
		}
	}
	if len(afterJobs(t, c, dest)) != 1 {
		t.Fatal("retry duplicated waiting prompt")
	}
	w := receiptCall(h, c.token, path, "after-once", `{"text":"different","after_thread_id":"`+source.ID+`"}`)
	if w.Code != 409 {
		t.Fatal("changed content reused receipt")
	}
	other.want("GET", path, nil, 404, nil)
	c.want("POST", path, map[string]any{"text": "no", "after_thread_id": foreign.ID}, 404, nil)
	c.want("POST", path, map[string]any{"text": "no", "after_thread_id": dest.ID}, 400, nil)
	var jobs = afterJobs(t, c, dest)
	other.want("DELETE", path+"/"+jobs[0].ID, nil, 404, nil)
	source.Status = "idle"
	_ = s.store.UpdateThread(context.Background(), source)
	c.want("POST", path, map[string]any{"text": "too late", "after_thread_id": source.ID}, 400, nil)
}
func TestDeferredPromptSurvivesRestartAndJoinsDestinationQueueExactlyOnce(t *testing.T) {
	s, c, dest, r, _ := queueFixture(t)
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	source := afterSource(t, s, dest.UserID, dest.Cwd)
	c.want("POST", "/api/v1/threads/"+dest.ID+"/after", map[string]any{"text": "Use the finished report", "after_thread_id": source.ID}, 201, nil)
	restarted := New(s.cfg, s.store, s.log, nil)
	restarted.rt.ctx = context.Background()
	restarted.rt.runs[dest.ID] = r
	restarted.deferredTick(context.Background())
	if len(queuedTexts(r)) != 0 {
		t.Fatal("sent before source finished")
	}
	finishAfterSource(t, s, source, true, false)
	restarted.deferredTick(context.Background())
	restarted.deferredTick(context.Background())
	if got := queuedTexts(r); len(got) != 1 || got[0] != "Use the finished report" {
		t.Fatalf("dispatch: %v", got)
	}
	if len(afterJobs(t, c, dest)) != 0 {
		t.Fatal("accepted prompt still waiting")
	}
}
func TestDeferredPromptBlocksFailureAndCancellationWins(t *testing.T) {
	s, c, dest, r, _ := queueFixture(t)
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	source := afterSource(t, s, dest.UserID, dest.Cwd)
	path := "/api/v1/threads/" + dest.ID + "/after"
	c.want("POST", path, map[string]any{"text": "cancel this", "after_thread_id": source.ID}, 201, nil)
	job := afterJobs(t, c, dest)[0]
	c.want("DELETE", path+"/"+job.ID, nil, 204, nil)
	c.want("POST", path, map[string]any{"text": "review first", "after_thread_id": source.ID}, 201, nil)
	finishAfterSource(t, s, source, false, true)
	restarted := New(s.cfg, s.store, s.log, nil)
	restarted.rt.ctx = context.Background()
	restarted.rt.runs[dest.ID] = r
	restarted.deferredTick(context.Background())
	jobs := afterJobs(t, c, dest)
	if len(jobs) != 1 || jobs[0].Status != "blocked" || len(queuedTexts(r)) != 0 {
		t.Fatalf("blocked/cancelled: %+v", jobs)
	}
	c.want("POST", path+"/"+jobs[0].ID+"/now", nil, 204, nil)
	c.want("POST", path+"/"+jobs[0].ID+"/now", nil, 409, nil)
	if got := queuedTexts(r); len(got) != 1 || got[0] != "review first" {
		t.Fatalf("manual send: %v", got)
	}
}

type failingDeferredSession struct {
	agent.Session
	calls atomic.Int32
}

func (s *failingDeferredSession) Send(agent.Input) error {
	s.calls.Add(1)
	return errors.New("connection failed after acceptance")
}
func (s *failingDeferredSession) Close() error { return nil }
func TestDeferredPromptAmbiguousDispatchIsNeverReplayed(t *testing.T) {
	s, c, dest, r, _ := queueFixture(t)
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	source := afterSource(t, s, dest.UserID, dest.Cwd)
	path := "/api/v1/threads/" + dest.ID + "/after"
	c.want("POST", path, map[string]any{"text": "one attempt", "after_thread_id": source.ID}, 201, nil)
	failing := &failingDeferredSession{}
	r.sess = failing
	r.cancel = func() {}
	r.running = false
	finishAfterSource(t, s, source, true, false)
	restarted := New(s.cfg, s.store, s.log, nil)
	restarted.rt.ctx = context.Background()
	restarted.rt.runs[dest.ID] = r
	restarted.deferredTick(context.Background())
	restarted.deferredTick(context.Background())
	jobs := afterJobs(t, c, dest)
	if len(jobs) != 1 || jobs[0].Status != "uncertain" || failing.calls.Load() != 1 {
		t.Fatalf("uncertain: %+v calls %d", jobs, failing.calls.Load())
	}
	c.want("POST", path+"/"+jobs[0].ID+"/now", nil, http.StatusConflict, nil)
	if failing.calls.Load() != 1 {
		t.Fatal("ambiguous delivery retried")
	}
}
func TestDeferredPromptRechecksRunPermission(t *testing.T) {
	s, c, dest, r, _ := queueFixture(t)
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	source := afterSource(t, s, dest.UserID, dest.Cwd)
	path := "/api/v1/threads/" + dest.ID + "/after"
	c.want("POST", path, map[string]any{"text": "not after revocation", "after_thread_id": source.ID}, 201, nil)
	finishAfterSource(t, s, source, true, false)
	_, _ = s.store.DB.Exec(`UPDATE users SET role='member',can_run=0 WHERE id=?`, dest.UserID)
	restarted := New(s.cfg, s.store, s.log, nil)
	restarted.rt.ctx = context.Background()
	restarted.rt.runs[dest.ID] = r
	restarted.deferredTick(context.Background())
	jobs := afterJobs(t, c, dest)
	if len(jobs) != 1 || jobs[0].Status != "blocked" || len(queuedTexts(r)) != 0 {
		t.Fatalf("permission bypass: %+v", jobs)
	}
}

func TestDeferredPromptFailureStaysPausedWhenAnotherTurnStartsImmediately(t *testing.T) {
	s, c, dest, r, _ := queueFixture(t)
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	source := afterSource(t, s, dest.UserID, dest.Cwd)
	c.want("POST", "/api/v1/threads/"+dest.ID+"/after", map[string]any{"text": "needs review", "after_thread_id": source.ID}, 201, nil)
	sourceRun := &run{threadID: source.ID, userID: source.UserID, provider: "claude", running: true, pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}}
	sink := &runSink{rt: s.rt, r: sourceRun}
	sink.TurnDone(agent.TurnInfo{OK: false})
	sink.TurnStarted()
	sink.TurnDone(agent.TurnInfo{OK: true})
	restarted := New(s.cfg, s.store, s.log, nil)
	restarted.rt.ctx = context.Background()
	restarted.rt.runs[dest.ID] = r
	restarted.deferredTick(context.Background())
	jobs := afterJobs(t, c, dest)
	if len(jobs) != 1 || jobs[0].Status != "blocked" || len(queuedTexts(r)) != 0 {
		t.Fatalf("failed turn was skipped: %+v", jobs)
	}
}
