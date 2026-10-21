package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) historyRoutes(r chi.Router) {
	r.Get("/history", s.handleHistory)
	r.Post("/history/import", s.handleImport)
}

// handleHistory lists the sessions the CLIs keep on the server.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	q := r.URL.Query().Get("q")
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	var list []agent.Past
	var err error
	provider := r.URL.Query().Get("provider")
	switch provider {
	case "codex":
		list, err = agent.ListCodex(ctx, s.cfg.CodexBin, q, limit)
	default:
		provider = "claude"
		list, err = agent.ListClaude(s.cfg.ClaudeHome, q, limit)
	}
	if err != nil {
		s.fail(w, err, "history")
		return
	}
	known := s.store.SessionThreads(ctx, me(r), provider)
	for i := range list {
		list[i].ThreadID = known[list[i].SessionID]
	}
	writeJSON(w, http.StatusOK, list)
}

// handleImport creates a thread that shows a CLI session's transcript and
// resumes that session on the next message.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	var in struct {
		Provider  string `json:"provider"`
		SessionID string `json:"session_id"`
	}
	if err := decode(r, &in); err != nil || in.SessionID == "" {
		writeErr(w, http.StatusBadRequest, "provider and session_id are required")
		return
	}
	if t, err := s.store.ThreadBySession(ctx, me(r), in.Provider, in.SessionID); err == nil {
		writeJSON(w, http.StatusOK, s.rt.decorate(t))
		return
	}
	t, err := s.importSession(ctx, userFrom(ctx), in.Provider, in.SessionID)
	if err != nil {
		s.fail(w, err, "import")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) importSession(ctx context.Context, u *store.User, provider, sessionID string) (*store.Thread, error) {
	var items []agent.Item
	var cwd, title string
	logOffset := int64(-1)
	switch provider {
	case "claude":
		path, err := agent.FindClaude(s.cfg.ClaudeHome, sessionID)
		if err != nil {
			return nil, badRequest("that Claude Code session is not on this server")
		}
		var size int64
		items, cwd, size, err = agent.ReadClaude(path)
		logOffset = size
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Kind == "user" {
				title = it.Text
				break
			}
		}
	case "codex":
		var err error
		items, cwd, title, err = agent.ReadCodex(ctx, s.cfg.CodexBin, sessionID)
		if err != nil {
			return nil, err
		}
	default:
		return nil, badRequest("provider must be claude or codex")
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	def := st.Claude
	if provider == "codex" {
		def = st.Codex
	}
	if clean, err := s.checkDir(cwd); err == nil {
		cwd = clean
	} else {
		cwd = s.cfg.Home
	}
	t := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: provider, ProviderSessionID: sessionID, Cwd: cwd,
		Model: def.Model, Effort: def.Effort, Mode: def.Mode, Source: "import", Title: titleFrom(title), LogOffset: logOffset}
	t.WorkspaceID = s.store.WorkspaceForPath(ctx, u.ID, cwd)
	if err := s.store.CreateThread(ctx, t); err != nil {
		return nil, err
	}
	for _, it := range items {
		data, _ := json.Marshal(it)
		body := ""
		if it.Kind == "user" || it.Kind == "assistant" {
			body = it.Text
		}
		if _, err := s.store.UpsertItem(ctx, t.ID, it.Key, it.Kind, data, body); err != nil {
			return nil, err
		}
		if it.Kind == "assistant" && strings.TrimSpace(it.Text) != "" {
			t.Preview = oneLine(it.Text, 200)
		}
	}
	_ = s.store.UpdateThread(ctx, t)
	s.rt.publishThread(t)
	return t, nil
}
