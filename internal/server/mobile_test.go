package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/notify"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestCompletionBadgeCountsChatsAndClearsOnReadArchiveDelete(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "badge@example.test", "admin", true)
	other, v := person(t, s, h, "badge-other@example.test", "member", true)
	ctx := context.Background()
	completed := func(uid int64, status string, archived, unread, success bool) *store.Thread {
		th := thread(t, s, uid, root)
		th.Status, th.Archived, th.Unread, th.CompletedUnread = status, archived, unread, success
		if err := s.store.UpdateThread(ctx, th); err != nil {
			t.Fatal(err)
		}
		return th
	}
	first := completed(u.ID, "idle", false, true, true)
	second := completed(u.ID, "idle", false, true, true)
	third := completed(u.ID, "idle", false, true, true)
	completed(u.ID, "error", false, true, false)
	completed(u.ID, "running", false, true, true)
	completed(u.ID, "waiting", false, true, true)
	completed(u.ID, "idle", true, true, true)
	completed(u.ID, "idle", false, false, true)
	completed(u.ID, "idle", false, true, false) // interrupted, not a completion
	completed(v.ID, "idle", false, true, true)
	check := func(c *client, expected int) {
		t.Helper()
		var out struct {
			Count int `json:"count"`
		}
		c.want("GET", "/api/v1/mobile/badge", nil, 200, &out)
		if out.Count != expected {
			t.Fatalf("badge=%d, want %d", out.Count, expected)
		}
	}
	check(owner, 3)
	check(other, 1)
	other.want("POST", "/api/v1/threads/"+first.ID+"/read", nil, 404, nil)
	check(owner, 3)
	owner.want("POST", "/api/v1/threads/"+first.ID+"/read", nil, 204, nil)
	owner.want("POST", "/api/v1/threads/"+first.ID+"/read", nil, 204, nil)
	check(owner, 2)
	owner.want("PATCH", "/api/v1/threads/"+second.ID, map[string]bool{"archived": true}, 200, nil)
	check(owner, 1)
	owner.want("DELETE", "/api/v1/threads/"+third.ID, nil, 204, nil)
	check(owner, 0)
	check(other, 1)
}

func TestTurnCompletionCreatesOneBadgeUnlessViewedFailedOrInterrupted(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "turn-badge@example.test", "admin", true)
	ctx := context.Background()
	th := thread(t, s, u.ID, root)
	th.ProviderSessionID = "" // no real session files in this integration test
	if err := s.store.UpdateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	sink := &runSink{rt: s.rt, r: &run{threadID: th.ID, userID: u.ID, provider: "claude"}}
	check := func(expected int) {
		t.Helper()
		actual, err := s.store.CompletionBadge(ctx, u.ID)
		if err != nil || actual != expected {
			t.Fatalf("badge=%d err=%v, want %d", actual, err, expected)
		}
	}
	sink.TurnDone(agent.TurnInfo{ID: "one", OK: true})
	check(1)
	sink.TurnDone(agent.TurnInfo{ID: "two", OK: true})
	check(1) // two replies in a single conversation still count as one
	sink.TurnDone(agent.TurnInfo{ID: "failed", Error: "fixture failure"})
	check(0)
	sink.TurnDone(agent.TurnInfo{ID: "stopped", Interrupted: true})
	check(0)
	subscription := s.rt.subscribeThread(th.ID, u.ID)
	defer s.rt.unsubscribeThread(th.ID, subscription)
	sink.TurnDone(agent.TurnInfo{ID: "viewed", OK: true})
	check(0)
}

func TestMobileNotificationPreferencesAreDeviceScopedAndSurviveRotation(t *testing.T) {
	s, h, _ := testServer(t)
	owner, u := person(t, s, h, "device-preferences@example.test", "admin", true)
	other, _ := person(t, s, h, "other-preferences@example.test", "member", true)
	var caps map[string]bool
	owner.want("GET", "/api/v1/mobile", nil, 200, &caps)
	if !caps["notification_preferences"] {
		t.Fatal("missing separate-preferences capability")
	}
	web, err := s.store.GetSettings(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"device-one", "device-two"} {
		owner.want("PUT", "/api/v1/mobile/devices", map[string]any{"device_id": id, "token": strings.Repeat(map[string]string{"device-one": "ab", "device-two": "cd"}[id], 32), "environment": "sandbox"}, 204, nil)
	}
	prefs := mobileNotificationPreferences{Finished: false, Input: true, Failed: false, OnlyAway: false}
	path := "/api/v1/mobile/devices/device-one/notifications"
	owner.want("PUT", path, prefs, 204, nil)
	var got mobileNotificationPreferences
	owner.want("GET", path, nil, 200, &got)
	if got != prefs {
		t.Fatalf("saved device policy differs: %+v", got)
	}
	other.want("GET", path, nil, 404, nil)
	other.want("PUT", path, prefs, 404, nil)
	owner.want("PUT", path, map[string]bool{"finished": true}, 400, nil)
	owner.want("GET", "/api/v1/mobile/devices/device-two/notifications", nil, 200, &got)
	if !got.Finished || !got.Input || !got.Failed || !got.OnlyAway {
		t.Fatalf("another phone changed: %+v", got)
	}
	after, _ := s.store.GetSettings(context.Background(), u.ID)
	if web.NotifyFinished != after.NotifyFinished || web.NotifyInput != after.NotifyInput || web.NotifyFailed != after.NotifyFailed || web.NotifyOnlyAway != after.NotifyOnlyAway {
		t.Fatal("phone policy changed web settings")
	}
	// Token refresh, including an older client's registration, preserves the policy.
	owner.want("PUT", "/api/v1/mobile/devices", map[string]any{"device_id": "device-one", "token": strings.Repeat("ef", 32), "environment": "sandbox", "notifications": mobileNotificationPreferences{true, true, true, true}}, 204, nil)
	owner.want("GET", path, nil, 200, &got)
	if got != prefs {
		t.Fatal("registration reset device preferences")
	}
	web.NotifyFinished = false
	web.NotifyInput = false
	web.NotifyFailed = false
	if err := s.store.SaveSettings(context.Background(), u.ID, web); err != nil {
		t.Fatal(err)
	}
	owner.want("GET", path, nil, 200, &got)
	if got != prefs {
		t.Fatal("web settings changed phone preferences")
	}
	if !got.allows("input") || got.allows("finished") || got.allows("failed") || got.allows("unknown") {
		t.Fatal("wrong native event selection")
	}
}

// The real APNs sender uses this transport with a generated test-only signing key.
type mobileTestTransport func(*http.Request) (*http.Response, error)

func (f mobileTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeAlertsIgnoreWebEventAndViewingSwitches(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "native-routing@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	web, _ := s.store.GetSettings(context.Background(), u.ID)
	web.NotifyFinished = false
	if err := s.store.SaveSettings(context.Background(), u.ID, web); err != nil {
		t.Fatal(err)
	}
	s.rt.mu.Lock()
	s.rt.watchers[th.ID] = map[chan sseEvent]int64{make(chan sseEvent): u.ID}
	s.rt.mu.Unlock()
	tokenA, tokenB := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	for token, prefs := range map[string]mobileNotificationPreferences{tokenA: {true, true, true, true}, tokenB: {false, true, true, true}} {
		owner.want("PUT", "/api/v1/mobile/devices", map[string]any{"device_id": "phone-" + token[:8], "token": token, "environment": "sandbox", "notifications": prefs}, 204, nil)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	path := filepath.Join(t.TempDir(), "test-signing-key.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	s.apns = &notify.APNS{TeamID: "TEST", KeyID: "TEST", Topic: "org.bjelke.pocketai", KeyFile: path}
	type push struct {
		token string
		alert bool
	}
	sent := make(chan push, 4)
	original := http.DefaultTransport
	http.DefaultTransport = mobileTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.sandbox.push.apple.com" {
			return original.RoundTrip(r)
		}
		var payload struct {
			APS struct {
				Alert json.RawMessage `json:"alert"`
			} `json:"aps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		sent <- push{strings.TrimPrefix(r.URL.Path, "/3/device/"), len(payload.APS.Alert) > 0}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})
	defer func() { s.mobilePushMu.Lock(); http.DefaultTransport = original; s.mobilePushMu.Unlock() }()
	s.rt.notify(th, "finished", "Finished", "A useful answer")
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case p := <-sent:
			got[p.token] = p.alert
		case <-time.After(5 * time.Second):
			t.Fatal("native delivery was blocked by web settings")
		}
	}
	if !got[tokenA] || got[tokenB] {
		t.Fatalf("wrong per-device alerts: %+v", got)
	}
}
