package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

type queueSession struct {
	agent.Session
	sent        []agent.Input
	interrupted int
	onInterrupt func() error
}

func (s *queueSession) Send(in agent.Input) error { s.sent = append(s.sent, in); return nil }
func (s *queueSession) Interrupt() error {
	s.interrupted++
	if s.onInterrupt != nil {
		return s.onInterrupt()
	}
	return nil
}
func queueFixture(t *testing.T) (*Server, *client, *store.Thread, *run, *queueSession) {
	t.Helper()
	s, h, root := testServer(t)
	c, u := person(t, s, h, "queue@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	sess := &queueSession{}
	r := &run{threadID: th.ID, userID: u.ID, sess: sess, running: true, lastUsed: time.Now(), pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}}
	s.rt.mu.Lock()
	s.rt.runs[th.ID] = r
	s.rt.mu.Unlock()
	t.Cleanup(func() { s.rt.mu.Lock(); delete(s.rt.runs, th.ID); s.rt.mu.Unlock() })
	return s, c, th, r, sess
}
func queuedTexts(r *run) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, q := range r.queue {
		out = append(out, q.in.Text)
	}
	return out
}
func TestSendNowPrioritizesWithoutDroppingQueue(t *testing.T) {
	s, c, th, r, sess := queueFixture(t)
	endpoint := "/api/v1/threads/" + th.ID + "/messages"
	for _, text := range []string{"first", "second"} {
		c.want("POST", endpoint, map[string]any{"text": text}, 200, nil)
	}
	if sess.interrupted != 0 || len(sess.sent) != 0 {
		t.Fatal("ordinary sends must wait")
	}
	sess.onInterrupt = func() error { r.mu.Lock(); r.running = false; r.mu.Unlock(); s.rt.next(r); return nil }
	c.want("POST", endpoint, map[string]any{"text": "urgent", "send_now": true}, 200, nil)
	if sess.interrupted != 1 || len(sess.sent) != 1 || !strings.HasPrefix(sess.sent[0].Text, "urgent\n") {
		t.Fatalf("wrong immediate dispatch: %+v", sess)
	}
	if strings.Join(queuedTexts(r), ",") != "first,second" {
		t.Fatal("older queue was changed")
	}
	rows, _ := s.store.ListItems(context.Background(), th.ID)
	found := false
	for _, row := range rows {
		var it agent.Item
		_ = json.Unmarshal(row.Data, &it)
		if it.Text == "urgent" {
			found = true
			if it.Queued {
				t.Fatal("sent item was incorrectly marked queued again")
			}
		}
	}
	if !found {
		t.Fatal("missing sent item")
	}
}
func TestSendNowIdleAndInterruptFailure(t *testing.T) {
	s, c, th, r, sess := queueFixture(t)
	endpoint := "/api/v1/threads/" + th.ID + "/messages"
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	c.want("POST", endpoint, map[string]any{"text": "idle", "send_now": true}, 200, nil)
	if len(sess.sent) != 1 || sess.interrupted != 0 {
		t.Fatal("idle send must start normally")
	}
	sess.onInterrupt = func() error { return errors.New("provider rejected interrupt") }
	c.want("POST", endpoint, map[string]any{"text": "kept", "send_now": true}, 200, nil)
	if strings.Join(queuedTexts(r), ",") != "kept" {
		t.Fatal("failed interrupt lost the message")
	}
	rows, _ := s.store.ListItems(context.Background(), th.ID)
	found := false
	for _, row := range rows {
		var it agent.Item
		_ = json.Unmarshal(row.Data, &it)
		if it.Kind == "notice" && strings.Contains(it.Text, "your message is queued") {
			found = true
		}
	}
	if !found {
		t.Fatal("failed interruption was not surfaced")
	}
}
func TestEditQueuedRestoresAttachmentsAndRejectsSentMessage(t *testing.T) {
	s, c, th, r, sess := queueFixture(t)
	ctx := context.Background()
	up := &store.Upload{ID: "queue-doc", UserID: th.UserID, Name: "notes.txt", Mime: "text/plain", Path: "unused", ExtractedText: "attachment text"}
	if err := s.store.CreateUpload(ctx, up); err != nil {
		t.Fatal(err)
	}
	c.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "edit me", "upload_ids": []string{up.ID}}, 200, nil)
	r.mu.Lock()
	key := r.queue[0].key
	r.mu.Unlock()
	var draft struct {
		Text    string      `json:"text"`
		Uploads []uploadOut `json:"uploads"`
	}
	edit := "/api/v1/threads/" + th.ID + "/queue/" + key + "/edit"
	c.want("POST", edit, nil, 200, &draft)
	if draft.Text != "edit me" || len(draft.Uploads) != 1 || draft.Uploads[0].ID != up.ID {
		t.Fatalf("incomplete draft: %+v", draft)
	}
	if len(queuedTexts(r)) != 0 || len(sess.sent) != 0 || sess.interrupted != 0 {
		t.Fatal("editing must only dequeue")
	}
	if _, err := s.store.GetUpload(ctx, th.UserID, up.ID); err != nil {
		t.Fatal("attachment removed")
	}
	c.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "updated", "upload_ids": []string{up.ID}}, 200, nil)
	r.mu.Lock()
	key = r.queue[0].key
	r.running = false
	r.mu.Unlock()
	s.rt.next(r)
	c.want("POST", "/api/v1/threads/"+th.ID+"/queue/"+key+"/edit", nil, 409, nil)
	if _, err := s.store.GetItem(ctx, th.ID, key); err != nil {
		t.Fatal("stale edit deleted the sent message")
	}
	if len(sess.sent) != 1 || !strings.Contains(sess.sent[0].Text, "attachment text") {
		t.Fatal("edited attachment not dispatched")
	}
	// Model next()'s short interval after dequeue but before the item flag clears.
	s.rt.saveItem(th.ID, agent.Item{Key: "stale", Kind: "user", Text: "already dispatched", Queued: true})
	c.want("POST", "/api/v1/threads/"+th.ID+"/queue/stale/edit", nil, 409, nil)
	if _, err := s.store.GetItem(ctx, th.ID, "stale"); err != nil {
		t.Fatal("stale queued flag allowed deletion")
	}
}
func TestSendBehaviorSettingIsPersonal(t *testing.T) {
	s, c, _, _, _ := queueFixture(t)
	other, _ := person(t, s, c.h, "other-setting@example.test", "member", true)
	var settings store.Settings
	c.want("GET", "/api/v1/settings", nil, 200, &settings)
	if settings.EnterSendNow {
		t.Fatal("default must queue")
	}
	c.want("PUT", "/api/v1/settings", map[string]any{"enter_send_now": true}, 200, nil)
	c.want("GET", "/api/v1/settings", nil, 200, &settings)
	if !settings.EnterSendNow {
		t.Fatal("preference did not persist")
	}
	other.want("GET", "/api/v1/settings", nil, 200, &settings)
	if settings.EnterSendNow {
		t.Fatal("preference leaked to another user")
	}
	c.want("PUT", "/api/v1/settings", map[string]any{"enter_send_now": false}, 200, nil)
	c.want("GET", "/api/v1/settings", nil, 200, &settings)
	if settings.EnterSendNow {
		t.Fatal("preference could not be switched back")
	}
}
