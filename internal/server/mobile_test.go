package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/go-chi/chi/v5"
)

func receiptCall(h http.Handler, token, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestMessageReceiptReplayConflictAndRestart(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "receipts@example.test", "admin", true)
	other, _ := person(t, s, h, "other@example.test", "member", true)
	th := thread(t, s, u.ID, root)
	var calls atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 200, map[string]bool{"queued": false})
	}
	router := func(server *Server) http.Handler {
		r := chi.NewRouter()
		r.Use(server.requireAuth)
		r.Post("/threads/{id}/messages", server.messageOnce(handler))
		return r
	}
	route := router(s)
	path := "/threads/" + th.ID + "/messages"
	first := receiptCall(route, owner.token, path, "request-1", `{"text":"hello","upload_ids":[],"send_now":false}`)
	replay := receiptCall(route, owner.token, path, "request-1", ` { "send_now":false, "upload_ids":[], "text":"hello" } `)
	if first.Code != 200 || replay.Code != 200 || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) || calls.Load() != 1 {
		t.Fatalf("replayed message: %d %d calls %d", first.Code, replay.Code, calls.Load())
	}
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("missing replay marker")
	}
	conflict := receiptCall(route, owner.token, path, "request-1", `{"text":"different"}`)
	if conflict.Code != 409 || calls.Load() != 1 {
		t.Fatal("reused ID accepted different content")
	}
	denied := receiptCall(route, other.token, path, "request-1", `{"text":"hello"}`)
	if denied.Code != 404 {
		t.Fatalf("another account read receipt: %d", denied.Code)
	}
	// New runtime, same durable store: no second dispatch.
	restarted := New(s.cfg, s.store, s.log, nil)
	replay = receiptCall(router(restarted), owner.token, path, "request-1", `{"text":"hello","upload_ids":[],"send_now":false}`)
	if replay.Code != 200 || calls.Load() != 1 {
		t.Fatal("restart dispatched a duplicate")
	}
	// A process that stopped between reserve and response must remain uncertain.
	_, err := s.store.DB.Exec(`INSERT INTO message_receipts (user_id,thread_id,request_key,request_hash) SELECT user_id,thread_id,'interrupted',request_hash FROM message_receipts WHERE request_key='request-1'`)
	if err != nil {
		t.Fatal(err)
	}
	unknown := receiptCall(route, owner.token, path, "interrupted", `{"text":"hello","upload_ids":[],"send_now":false}`)
	if unknown.Code != 409 || calls.Load() != 1 {
		t.Fatal("uncertain message was dispatched")
	}
}

func TestConcurrentMessageReceiptOnlyDispatchesOnce(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "concurrent@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := chi.NewRouter()
	r.Use(s.requireAuth)
	r.Post("/threads/{id}/messages", s.messageOnce(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		writeJSON(w, 200, map[string]bool{"queued": true})
	}))
	path := "/threads/" + th.ID + "/messages"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- receiptCall(r, owner.token, path, "concurrent", `{"text":"hello"}`) }()
	<-entered
	second := receiptCall(r, owner.token, path, "concurrent", `{"text":"hello"}`)
	close(release)
	first := <-done
	if first.Code != 200 || second.Code != 409 || calls.Load() != 1 {
		t.Fatalf("duplicate dispatch: %d %d %d", first.Code, second.Code, calls.Load())
	}
}

func TestMobileRegistrationOwnershipRotationAndRevocation(t *testing.T) {
	s, h, _ := testServer(t)
	owner, u := person(t, s, h, "devices@example.test", "admin", true)
	other, v := person(t, s, h, "other-devices@example.test", "member", false)
	ctx := context.Background()
	var caps map[string]bool
	owner.want("GET", "/api/v1/mobile", nil, 200, &caps)
	if !caps["idempotency"] || caps["push"] {
		t.Fatal("wrong capabilities")
	}
	device := map[string]string{"device_id": "device-one", "token": strings.Repeat("ab", 32), "environment": "sandbox"}
	owner.want("PUT", "/api/v1/mobile/devices", device, 204, nil)
	var count int
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM mobile_devices WHERE user_id=?`, u.ID).Scan(&count)
	if count != 1 {
		t.Fatal("registration missing")
	}
	// A different account cannot delete this registration by its device ID.
	other.want("DELETE", "/api/v1/mobile/devices/device-one", nil, 204, nil)
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM mobile_devices WHERE user_id=?`, u.ID).Scan(&count)
	if count != 1 {
		t.Fatal("cross-account delete")
	}
	device["token"] = strings.Repeat("cd", 32)
	owner.want("PUT", "/api/v1/mobile/devices", device, 204, nil)
	var token string
	_ = s.store.DB.QueryRow(`SELECT token FROM mobile_devices WHERE user_id=?`, u.ID).Scan(&token)
	if token != device["token"] {
		t.Fatal("token rotation lost")
	}
	// The same physical phone signs into a different account.
	other.want("PUT", "/api/v1/mobile/devices", device, 204, nil)
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM mobile_devices WHERE user_id=?`, u.ID).Scan(&count)
	if count != 0 {
		t.Fatal("previous account still receives pushes")
	}
	// Revoking the associated API token removes push registrations.
	var id int64
	_ = s.store.DB.QueryRow(`SELECT id FROM api_tokens WHERE token_hash=?`, store.HashToken(other.token)).Scan(&id)
	if err := s.store.DeleteAPIToken(ctx, v.ID, id); err != nil {
		t.Fatal(err)
	}
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM mobile_devices WHERE user_id=?`, v.ID).Scan(&count)
	if count != 0 {
		t.Fatal("revoked token still registered")
	}
	device["environment"] = "wrong"
	owner.want("PUT", "/api/v1/mobile/devices", device, 400, nil)
	owner.want("POST", "/api/v1/mobile/push/test", map[string]string{"device_id": "device-one"}, 503, nil)
}

func TestGitReviewAndAttentionStayPrivate(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "review@example.test", "admin", true)
	viewer, v := person(t, s, h, "review-viewer@example.test", "member", true)
	outsider, _ := person(t, s, h, "review-outside@example.test", "member", true)
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0700)
	gitT(t, repo, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("before\n"), 0600)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-qm", "initial")
	_ = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0600)
	_ = os.WriteFile(filepath.Join(repo, "new file.txt"), []byte("new\n"), 0600)
	th := thread(t, s, u.ID, repo)
	var review struct {
		Branch, Diff string
		Files        []reviewFile
	}
	owner.want("GET", "/api/v1/threads/"+th.ID+"/review", nil, 200, &review)
	if review.Branch != "main" || len(review.Files) != 2 || !strings.Contains(review.Diff, "+after") {
		t.Fatalf("wrong review: %+v", review)
	}
	if err := s.store.SetShares(context.Background(), th.ID, []store.ThreadShare{{UserID: v.ID, Role: "chat"}}); err != nil {
		t.Fatal(err)
	}
	s.rt.loadShares()
	viewer.want("GET", "/api/v1/threads/"+th.ID+"/review", nil, 403, nil)
	outsider.want("GET", "/api/v1/threads/"+th.ID+"/review", nil, 404, nil)
	th.Status = "waiting"
	_ = s.store.UpdateThread(context.Background(), th)
	request := agent.Item{Key: "req", Kind: "request", Request: &agent.Request{ID: "approval", Type: "permission", Title: "Write file", Status: "pending"}}
	data, _ := json.Marshal(request)
	_, _ = s.store.UpsertItem(context.Background(), th.ID, request.Key, request.Kind, data, "")
	var inbox []struct {
		Thread   store.Thread
		Requests []agent.Request
	}
	owner.want("GET", "/api/v1/mobile/attention", nil, 200, &inbox)
	if len(inbox) != 1 || len(inbox[0].Requests) != 1 || inbox[0].Requests[0].Title != "Write file" {
		t.Fatalf("incomplete inbox: %+v", inbox)
	}
	outsider.want("GET", "/api/v1/mobile/attention", nil, 200, &inbox)
	if len(inbox) != 0 {
		t.Fatal("inbox leaked another person's request")
	}
	files := parseReviewStatus("R  new name\x00old name\x00?? a\nstrange name\x00")
	if len(files) != 2 || files[1].Path != "a\nstrange name" {
		t.Fatal("rename or newline path lost")
	}
}
