package server

import (
	"encoding/json"
	"net/http"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/go-chi/chi/v5"
)

// Editing takes an unsent message out of the queue before handing its complete
// contents to the composer. next() uses the same lock, so a sent message can
// never be removed by a stale Edit click.
func (s *Server) handleEditQueued(w http.ResponseWriter, req *http.Request) {
	if !s.requireRun(w, req) {
		return
	}
	t := s.threadAs(w, req, accessChat)
	if t == nil {
		return
	}
	key := chi.URLParam(req, "key")
	st, err := s.store.GetItem(req.Context(), t.ID, key)
	if err != nil {
		s.fail(w, err, "message")
		return
	}
	var it agent.Item
	if json.Unmarshal(st.Data, &it) != nil || it.Kind != "user" || !it.Queued {
		writeErr(w, http.StatusConflict, "That message has already been sent")
		return
	}
	sender := it.ByID
	if sender == 0 {
		sender = t.UserID
	}
	if sender != me(req) {
		writeErr(w, http.StatusForbidden, "You can only edit your own queued messages")
		return
	}
	uploads := []uploadOut{}
	for _, im := range append(append([]agent.Image{}, it.Images...), it.Files...) {
		// A fork's history file belongs to the thread owner, including when a
		// shared-chat participant typed the first message carrying it.
		up, err := s.store.GetUploadAny(req.Context(), im.ID)
		if err != nil || up.ThreadID == nil || *up.ThreadID != t.ID {
			writeErr(w, http.StatusConflict, "An attachment is no longer available; the message is still queued")
			return
		}
		uploads = append(uploads, uploadOut{Upload: up, URL: "/api/v1/uploads/" + up.ID})
	}
	if err := s.rt.takeQueued(t.ID, key); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.rt.publishThread(t)
	writeJSON(w, http.StatusOK, map[string]any{"text": it.Text, "uploads": uploads})
}

func (rt *engine) takeQueued(threadID, key string) error {
	r := rt.get(threadID)
	if r == nil {
		return badRequest("That message has already been sent")
	}
	r.mu.Lock()
	found := -1
	for i, q := range r.queue {
		if q.key == key {
			found = i
			break
		}
	}
	if found < 0 {
		r.mu.Unlock()
		return badRequest("That message has already been sent")
	}
	if err := rt.s.store.DeleteItem(rt.ctx, threadID, key); err != nil {
		r.mu.Unlock()
		return err
	}
	r.queue = append(r.queue[:found:found], r.queue[found+1:]...)
	r.mu.Unlock()
	rt.toThread(threadID, sseEvent{Name: "item_removed", Data: map[string]string{"key": key}})
	return nil
}
