package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) historyRoutes(r chi.Router) {
	r.Get("/history", s.handleHistory)
	r.Get("/runs", s.handleRuns)
	r.Post("/history/import", s.handleImport)
}

// handleHistory lists the sessions both CLIs keep on the server (read from
// their session files: origin, runs, time worked, running now), newest first.
// ?provider=claude|codex, ?origin=paseo|terminal|pocket-ai|schedule|app,
// ?q= (title, folder, last prompt), ?limit= (default 300).
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	provider, origin := r.URL.Query().Get("provider"), r.URL.Query().Get("origin")
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	known := s.store.SessionThreadsAll(ctx, me(r))
	out := []agent.ServerSession{}
	total := 0
	sessions := []agent.ServerSession{}
	visible := s.sessionFilter(ctx, userFrom(ctx))
	for _, x := range agent.Sessions(s.homesFor(ctx, userFrom(ctx)), s.cfg.PaseoHome) {
		if visible(x.Provider, x.SessionID, x.Account) {
			sessions = append(sessions, x)
		}
	}
	sessions = append(sessions, agent.Sessions(s.askHomes(me(r)), "")...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt > sessions[j].UpdatedAt })
	for _, x := range sessions {
		if th, ok := known[x.Provider+":"+x.SessionID]; ok {
			x.ThreadID = th.ID
			x.Origin = threadOrigin(th.Source, x.Origin)
			x.SharedWith = s.rt.sharedNames(th.ID)
		}
		if (provider != "" && provider != "all" && x.Provider != provider) || (origin != "" && x.Origin != origin) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(x.Title+" "+x.Cwd+" "+x.LastPrompt), q) {
			continue
		}
		total++
		if len(out) < limit {
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "total": total})
}

// sessionFilter says which of the server's CLI sessions a person may see. A
// session one of Pocket AI's threads used is that thread's person's alone
// (admins included: nobody sees anyone else's chats). The rest (terminal,
// Paseo, other apps) are the server user's own work: admins see them, and so
// does whoever owns the login they ran on.
func (s *Server) sessionFilter(ctx context.Context, u *store.User) func(provider, sessionID string, account int64) bool {
	if u == nil {
		return func(string, string, int64) bool { return false }
	}
	owners := s.store.SessionOwners(ctx)
	own := map[string]bool{}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.UserID != nil && *a.UserID == u.ID {
			own[acctKey(a.Provider, a.ID)] = true
		}
	}
	admin := u.Role == "admin"
	return func(provider, sessionID string, account int64) bool {
		if uid, ok := owners[provider+":"+sessionID]; ok {
			return uid == u.ID
		}
		return admin || own[acctKey(provider, account)]
	}
}

// threadOrigin: a session Pocket AI started itself is "pocket-ai" (or
// "schedule"), whatever the CLI wrote; imported ones keep where they came from.
func threadOrigin(source, origin string) string {
	switch source {
	case "import", "":
		return origin
	case "schedule":
		return "schedule"
	}
	return "pocket-ai"
}

// handleRuns lists every prompt the agents worked on between ?from and ?to
// (RFC 3339; default: the server's today), across Pocket AI, Paseo, the
// terminal and other apps.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	y, m, d := time.Now().Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 1)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "from must be an RFC 3339 time")
			return
		}
		from, to = t, t.AddDate(0, 0, 1)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil || !t.After(from) || t.Sub(from) > 32*24*time.Hour {
			writeErr(w, http.StatusBadRequest, "to must be an RFC 3339 time after from, at most a month later")
			return
		}
		to = t
	}
	known := s.store.SessionThreadsAll(r.Context(), me(r))
	runs := []agent.Run{}
	visible := s.sessionFilter(r.Context(), userFrom(r.Context()))
	for _, x := range agent.Runs(s.homesFor(r.Context(), userFrom(r.Context())), s.cfg.PaseoHome, from, to) {
		if visible(x.Provider, x.SessionID, x.Account) {
			runs = append(runs, x)
		}
	}
	runs = append(runs, agent.Runs(s.askHomes(me(r)), "", from, to)...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
	for i := range runs {
		if th, ok := known[runs[i].Provider+":"+runs[i].SessionID]; ok {
			runs[i].ThreadID = th.ID
			runs[i].Origin = threadOrigin(th.Source, runs[i].Origin)
			runs[i].SharedWith = s.rt.sharedNames(th.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "runs": runs})
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
	if account, _ := s.sessionAccount(ctx, userFrom(ctx), in.Provider, in.SessionID); !s.sessionFilter(ctx, userFrom(ctx))(in.Provider, in.SessionID, account) {
		writeErr(w, http.StatusNotFound, "not found")
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
	account, dir := s.sessionAccount(ctx, u, provider, sessionID)
	switch provider {
	case "claude":
		path, err := agent.FindClaude(dir, sessionID)
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
		items, cwd, title, err = agent.ReadCodex(ctx, s.cfg.CodexBin, sessionID, s.accountEnv(ctx, provider, account))
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
	if clean, err := s.checkDir(ctx, cwd); err == nil {
		cwd = clean
	} else {
		cwd = s.homeOf(ctx, u)
	}
	t := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: provider, AccountID: account, ProviderSessionID: sessionID, Cwd: cwd,
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
