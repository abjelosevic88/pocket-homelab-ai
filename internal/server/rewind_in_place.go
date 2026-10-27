package server

import (
	"fmt"
	"net/http"
	"os"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) lockThreadEdit(id string) func() {
	var hash uint32 = 2166136261
	for i := range id {
		hash = (hash ^ uint32(id[i])) * 16777619
	}
	mu := &s.threadEditMu[hash%uint32(len(s.threadEditMu))]
	mu.Lock()
	return mu.Unlock
}

func (s *Server) rewindInPlace(w http.ResponseWriter, r *http.Request, t *store.Thread, rows []*store.Item, prefix []agent.Item, target *agent.Item, sid, sessionPath string) {
	ctx := r.Context()
	uploads := []uploadOut{}
	for _, image := range append(append([]agent.Image{}, target.Images...), target.Files...) {
		up, err := s.store.GetUploadAny(ctx, image.ID)
		if err != nil || up.ThreadID == nil || *up.ThreadID != t.ID {
			writeErr(w, 409, "an attachment is unavailable")
			return
		}
		uploads = append(uploads, uploadOut{Upload: up, URL: "/api/v1/uploads/" + up.ID})
	}
	// Imported Claude pictures live inside its original log. Preserve draft
	// attachments before changing the provider session, just as Edit did before.
	if target.LogImages > 0 && t.HostID == 0 {
		home, _ := s.accountHome(ctx, t.Provider, t.AccountID)
		logPath, err := agent.FindClaude(home, t.ProviderSessionID)
		if err != nil {
			s.fail(w, err, "read original images")
			return
		}
		for i := 0; i < target.LogImages; i++ {
			data, mime, err := agent.LogImage(logPath, target.Key, i)
			if err != nil {
				s.fail(w, err, "read original image")
				return
			}
			up, err := s.writeRewindUpload(ctx, t, fmt.Sprintf("image-%d%s", i+1, imageExt[mime]), mime, "", func(out *os.File) error { _, err := out.Write(data); return err })
			if err != nil {
				s.fail(w, err, "preserve original image")
				return
			}
			uploads = append(uploads, up)
		}
	}
	previousSession := t.ProviderSessionID
	t.ProviderSessionID, t.LogOffset = sid, -1
	if sessionPath != "" {
		t.LogOffset = agent.FileSize(sessionPath)
	}
	t.Status, t.Error, t.Unread, t.CompletedUnread = "idle", "", false, false
	t.Preview, t.LastPrompt, t.PromptAt = "", "", ""
	t.Usage.ContextTokens = 0 // spend is historical; the next turn reports its smaller context
	t.ActivityAt = store.Now()
	for _, item := range prefix {
		if item.Kind == "assistant" {
			t.Preview = oneLine(item.Text, 200)
		}
		if item.Kind == "user" {
			t.LastPrompt = promptLine(item.Text, len(item.Images))
			for _, row := range rows {
				if row.Key == item.Key {
					t.PromptAt = row.CreatedAt
					break
				}
			}
		}
	}
	// Detach the old idle process before switching context. Its delayed exit or
	// flush must not reinsert discarded messages or restore its session ID.
	s.rt.retireForRewind(t.ID)
	lastID := rows[len(rows)-1].ID
	if err := s.store.RewindThread(ctx, t, target.Key, previousSession, lastID); err != nil {
		if err == store.ErrConflict {
			writeErr(w, 409, "The conversation changed. Reopen it before rewinding.")
		} else {
			s.fail(w, err, "rewind")
		}
		return
	}
	cut := false
	for _, row := range rows {
		cut = cut || row.Key == target.Key
		if cut {
			s.rt.toThread(t.ID, sseEvent{Name: "item_removed", Data: map[string]string{"key": row.Key}})
		}
	}
	fresh, err := s.store.GetThreadAny(ctx, t.ID)
	if err != nil {
		s.fail(w, err, "read rewound thread")
		return
	}
	s.rt.publishThread(fresh)
	retained := make([]map[string]any, 0, len(prefix))
	for _, row := range rows {
		if row.Key == target.Key {
			break
		}
		retained = append(retained, itemJSON(row))
	}
	s.rt.toThread(t.ID, sseEvent{Name: "history_reset", Data: retained})
	writeJSON(w, http.StatusOK, map[string]any{"thread": fresh, "items": retained, "draft": map[string]any{"text": target.Text, "uploads": uploads}})
}

// Verify a follower still describes the session it read before waiting for an edit.
func (rt *engine) sameSession(t *store.Thread) bool {
	fresh, err := rt.s.store.GetThreadAny(rt.ctx, t.ID)
	return err == nil && fresh.ProviderSessionID == t.ProviderSessionID
}

func (k *runSink) beginEvent() bool {
	k.r.eventMu.Lock()
	if k.r.retired {
		k.r.eventMu.Unlock()
		return false
	}
	return true
}

func (rt *engine) retireForRewind(id string) {
	r := rt.get(id)
	if r == nil {
		return
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	rt.stop(id)
	r.mu.Lock()
	r.retired = true
	r.live = map[string]*agent.Item{}
	r.dirty = map[string]bool{}
	r.mu.Unlock()
}
