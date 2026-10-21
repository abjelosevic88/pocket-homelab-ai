package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) threadRoutes(r chi.Router) {
	r.Get("/providers", s.handleProviders)
	r.Get("/threads", s.handleListThreads)
	r.Post("/threads", s.handleCreateThread)
	r.Get("/threads/{id}", s.handleGetThread)
	r.Patch("/threads/{id}", s.handlePatchThread)
	r.Delete("/threads/{id}", s.handleDeleteThread)
	r.Post("/threads/{id}/messages", s.handleSendMessage)
	r.Delete("/threads/{id}/queue", s.handleClearQueue)
	r.Post("/threads/{id}/interrupt", s.handleInterrupt)
	r.Post("/threads/{id}/requests/{rid}", s.handleRespond)
	r.Post("/threads/{id}/read", s.handleMarkRead)
	r.Post("/threads/{id}/fork", s.handleFork)
	r.Get("/threads/{id}/export.md", s.handleExport)
	r.Get("/search", s.handleSearch)
	r.Get("/usage", s.handleUsage)
}

// requireRun answers 403 unless the person may start agents.
func (s *Server) requireRun(w http.ResponseWriter, r *http.Request) bool {
	if !userFrom(r.Context()).MayRun() {
		writeErr(w, http.StatusForbidden, "an admin has not allowed you to run agents on this server")
		return false
	}
	return true
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) *store.Thread {
	t, err := s.store.GetThread(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "thread")
		return nil
	}
	return s.rt.decorate(t)
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.providers(r.Context(), queryBool(r, "refresh")))
}

// providers probes both CLIs (cached for 30 minutes).
func (s *Server) providers(ctx context.Context, refresh bool) []agent.Provider {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	if !refresh && s.provList != nil && time.Since(s.provAt) < 30*time.Minute {
		return s.provList
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	out := make([]agent.Provider, 2)
	done := make(chan struct{}, 2)
	go func() { out[0] = agent.ProbeClaude(ctx, s.cfg.ClaudeBin, s.cfg.Home); done <- struct{}{} }()
	go func() { out[1] = agent.ProbeCodex(ctx, s.cfg.CodexBin, s.cfg.Home); done <- struct{}{} }()
	<-done
	<-done
	s.provList, s.provAt = out, time.Now()
	return out
}

func (s *Server) agentProvider(ctx context.Context, id string) (agent.Provider, bool) {
	for _, p := range s.providers(ctx, false) {
		if p.ID == id {
			return p, p.Available
		}
	}
	return agent.Provider{}, false
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListThreads(r.Context(), me(r), store.ThreadFilter{Archived: queryBool(r, "archived"),
		WorkspaceID: queryInt64(r, "workspace"), Q: r.URL.Query().Get("q"), Limit: queryInt(r, "limit")})
	if err != nil {
		s.fail(w, err, "list threads")
		return
	}
	for _, t := range list {
		s.rt.decorate(t)
	}
	writeJSON(w, http.StatusOK, list)
}

type newThread struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Effort      string   `json:"effort"`
	Mode        string   `json:"mode"`
	Cwd         string   `json:"cwd"`
	WorkspaceID *int64   `json:"workspace_id"`
	Title       string   `json:"title"`
	Text        string   `json:"text"`
	UploadIDs   []string `json:"upload_ids"`
}

// createThread validates, stores and starts a thread with its first message.
func (s *Server) createThread(ctx context.Context, u *store.User, in newThread, source, ref string) (*store.Thread, error) {
	st, _ := s.store.GetSettings(ctx, u.ID)
	if in.Provider == "" {
		in.Provider = st.DefaultProvider
	}
	if in.Provider != "claude" && in.Provider != "codex" {
		return nil, badRequest("provider must be claude or codex")
	}
	prov, ok := s.agentProvider(ctx, in.Provider)
	if !ok {
		return nil, badRequest(firstNonEmpty(prov.Error, in.Provider+" is not available on this server"))
	}
	if strings.TrimSpace(in.Text) == "" && len(in.UploadIDs) == 0 {
		return nil, badRequest("write something to start with")
	}
	def := st.Claude
	if in.Provider == "codex" {
		def = st.Codex
	}
	t := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: in.Provider, Source: source, SourceRef: ref,
		Model: firstNonEmpty(in.Model, def.Model, prov.DefaultModel), Effort: firstNonEmpty(in.Effort, def.Effort), Mode: firstNonEmpty(in.Mode, def.Mode),
		Title: strings.TrimSpace(in.Title)}
	if !validMode(t.Provider, t.Mode) {
		return nil, badRequest("unknown mode " + t.Mode)
	}
	if in.WorkspaceID != nil && *in.WorkspaceID > 0 {
		ws, err := s.store.GetWorkspace(ctx, u.ID, *in.WorkspaceID)
		if err != nil {
			return nil, badRequest("that workspace does not exist")
		}
		t.WorkspaceID = &ws.ID
		if in.Cwd == "" {
			in.Cwd = ws.Path
		}
	}
	cwd := firstNonEmpty(in.Cwd, st.DefaultCwd, s.cfg.Home)
	clean, err := s.checkDir(cwd)
	if err != nil {
		return nil, err
	}
	t.Cwd = clean
	if t.WorkspaceID == nil {
		t.WorkspaceID = s.store.WorkspaceForPath(ctx, u.ID, clean)
	}
	if t.Title == "" {
		t.Title = titleFrom(in.Text)
	}
	if err := s.store.CreateThread(ctx, t); err != nil {
		return nil, err
	}
	if _, err := s.postMessage(ctx, u, t, in.Text, in.UploadIDs); err != nil {
		return t, err
	}
	return s.rt.decorate(t), nil
}

func validMode(provider, mode string) bool {
	modes := agent.ClaudeModes
	if provider == "codex" {
		modes = agent.CodexModes
	}
	for _, m := range modes {
		if m.ID == mode {
			return true
		}
	}
	return false
}

func titleFrom(text string) string {
	t := oneLine(text, 80)
	if t == "" {
		return "Image"
	}
	return t
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in newThread
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	t, err := s.createThread(r.Context(), userFrom(r.Context()), in, "web", "")
	if err != nil && t == nil {
		s.fail(w, err, "create thread")
		return
	}
	if err != nil {
		// The thread exists; the agent did not start. Show it with the error.
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		t = s.rt.setStatus(t.ID, "error", err.Error())
	}
	writeJSON(w, http.StatusCreated, t)
}

// postMessage writes the user item and hands it to the agent (or the queue).
func (s *Server) postMessage(ctx context.Context, u *store.User, t *store.Thread, text string, uploadIDs []string) (bool, error) {
	in := agent.Input{Text: text}
	it := agent.Item{Key: "u-" + uuid.NewString(), Kind: "user", Text: text}
	for _, id := range uploadIDs {
		up, err := s.store.GetUpload(ctx, u.ID, id)
		if err != nil {
			return false, badRequest("an attached image is gone; attach it again")
		}
		in.Images = append(in.Images, agent.ImageFile{Path: up.Path, Mime: up.Mime})
		it.Images = append(it.Images, agent.Image{ID: up.ID, Name: up.Name, URL: "/api/v1/uploads/" + up.ID})
		_ = s.store.AttachUpload(ctx, up.ID, t.ID)
	}
	if r := s.rt.get(t.ID); r != nil {
		r.mu.Lock()
		it.Queued = r.running || len(r.queue) > 0
		r.mu.Unlock()
	}
	s.rt.saveItem(t.ID, it)
	t.ActivityAt = store.Now()
	if t.Title == "" {
		t.Title = titleFrom(text)
	}
	_ = s.store.UpdateThread(ctx, t)
	queued, err := s.rt.send(t, it.Key, in)
	if err != nil {
		return false, err
	}
	if queued && !it.Queued {
		it.Queued = true
		s.rt.saveItem(t.ID, it)
	}
	return queued, nil
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	items, err := s.store.ListItems(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "items")
		return
	}
	live := s.rt.liveItems(t.ID)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if l, ok := live[it.Key]; ok {
			data, _ := json.Marshal(l)
			it.Data = data
		}
		out = append(out, itemJSON(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": t, "items": out})
}

func (s *Server) handlePatchThread(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	var in struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
		Model    *string `json:"model"`
		Effort   *string `json:"effort"`
		Mode     *string `json:"mode"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	restart := false
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		t.Title = oneLine(*in.Title, 200)
	}
	if in.Pinned != nil {
		t.Pinned = *in.Pinned
	}
	if in.Archived != nil {
		t.Archived = *in.Archived
	}
	if in.Model != nil && *in.Model != t.Model {
		t.Model, restart = *in.Model, true
	}
	if in.Effort != nil && *in.Effort != t.Effort {
		t.Effort, restart = *in.Effort, true
	}
	if in.Mode != nil && *in.Mode != t.Mode {
		if !validMode(t.Provider, *in.Mode) {
			writeErr(w, http.StatusBadRequest, "unknown mode")
			return
		}
		t.Mode, restart = *in.Mode, true
	}
	if err := s.store.UpdateThread(r.Context(), t); err != nil {
		s.fail(w, err, "update thread")
		return
	}
	if restart {
		// New flags need a new process; it resumes the same session.
		if run := s.rt.get(t.ID); run != nil {
			run.mu.Lock()
			busy := run.running || len(run.pending) > 0 || len(run.queue) > 0
			run.stale = busy
			run.mu.Unlock()
			if !busy {
				s.rt.stop(t.ID)
			}
		}
	}
	if t.Archived {
		s.rt.stop(t.ID)
	}
	s.rt.publishThread(t)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	s.rt.stop(t.ID)
	ups := s.store.UploadsOfThread(r.Context(), t.ID)
	if err := s.store.DeleteThread(r.Context(), t.UserID, t.ID); err != nil {
		s.fail(w, err, "delete thread")
		return
	}
	for _, u := range ups {
		s.removeUploadFile(u.Path)
		_ = s.store.DeleteUpload(r.Context(), u.UserID, u.ID)
	}
	s.rt.toUser(t.UserID, sseEvent{Name: "thread_removed", Data: map[string]string{"id": t.ID}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	var in struct {
		Text      string   `json:"text"`
		UploadIDs []string `json:"upload_ids"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(in.Text) == "" && len(in.UploadIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "empty message")
		return
	}
	if t.Archived {
		t.Archived = false
	}
	if t.Status == "error" {
		t.Status, t.Error = "idle", ""
	}
	queued, err := s.postMessage(r.Context(), userFrom(r.Context()), t, in.Text, in.UploadIDs)
	if err != nil {
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		s.rt.setStatus(t.ID, "error", err.Error())
		s.fail(w, err, "send")
		return
	}
	t, _ = s.store.GetThread(r.Context(), me(r), t.ID)
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "thread": s.rt.decorate(t)})
}

func (s *Server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	if t := s.thread(w, r); t != nil {
		s.rt.clearQueue(t.ID)
		if t2, err := s.store.GetThread(r.Context(), me(r), t.ID); err == nil {
			s.rt.publishThread(t2)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if t := s.thread(w, r); t != nil {
		if err := s.rt.interrupt(t.ID); err != nil {
			s.fail(w, err, "interrupt")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleRespond(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	var in agent.Response
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	switch in.Decision {
	case "allow", "allow_session", "deny":
	default:
		writeErr(w, http.StatusBadRequest, "decision must be allow, allow_session or deny")
		return
	}
	if err := s.rt.respond(t, chi.URLParam(r, "rid"), in); err != nil {
		s.fail(w, err, "respond")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	if t.Unread {
		t.Unread = false
		_ = s.store.UpdateThread(r.Context(), t)
		s.rt.publishThread(t)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFork starts a new thread that continues the same agent session
// under a new id; the timeline so far is copied.
func (s *Server) handleFork(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	if t.ProviderSessionID == "" {
		writeErr(w, http.StatusBadRequest, "nothing to fork yet")
		return
	}
	ctx := r.Context()
	n := &store.Thread{ID: uuid.NewString(), UserID: t.UserID, Title: "Fork of " + t.Title, Provider: t.Provider, Model: t.Model,
		Effort: t.Effort, Mode: t.Mode, Cwd: t.Cwd, WorkspaceID: t.WorkspaceID, Source: "fork", SourceRef: "fork:" + t.ProviderSessionID}
	if err := s.store.CreateThread(ctx, n); err != nil {
		s.fail(w, err, "fork")
		return
	}
	items, _ := s.store.ListItems(ctx, t.ID)
	for _, it := range items {
		if it.Kind == "request" || it.Kind == "turn" {
			continue
		}
		_, _ = s.store.UpsertItem(ctx, n.ID, it.Key, it.Kind, it.Data, it.Body)
	}
	n.Preview = t.Preview
	_ = s.store.UpdateThread(ctx, n)
	s.rt.publishThread(n)
	writeJSON(w, http.StatusCreated, n)
}

// handleExport writes the thread as Markdown.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	items, err := s.store.ListItems(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s · %s · `%s` · %s\n\n", t.Title, providerName(t.Provider), firstNonEmpty(t.Model, "default"), t.Cwd, t.CreatedAt)
	for _, st := range items {
		var it agent.Item
		_ = json.Unmarshal(st.Data, &it)
		switch it.Kind {
		case "user":
			fmt.Fprintf(&b, "---\n\n**You:**\n\n%s\n\n", it.Text)
			for _, im := range it.Images {
				fmt.Fprintf(&b, "_[image: %s]_\n\n", im.Name)
			}
		case "assistant":
			fmt.Fprintf(&b, "%s\n\n", it.Text)
		case "tool":
			if it.Tool != nil {
				fmt.Fprintf(&b, "> 🔧 %s (%s)\n\n", it.Tool.Title, it.Tool.Status)
			}
		case "todos":
			for _, td := range it.Todos {
				mark := " "
				if td.Status == "completed" {
					mark = "x"
				}
				fmt.Fprintf(&b, "- [%s] %s\n", mark, td.Text)
			}
			b.WriteString("\n")
		case "request":
			if it.Request != nil {
				fmt.Fprintf(&b, "> ❓ %s — %s\n\n", it.Request.Title, it.Request.Status)
				if it.Request.Plan != "" {
					fmt.Fprintf(&b, "%s\n\n", it.Request.Plan)
				}
			}
		case "notice":
			fmt.Fprintf(&b, "> ℹ️ %s\n\n", it.Text)
		}
	}
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '"' || r < 32 {
			return '-'
		}
		return r
	}, oneLine(t.Title, 60))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, name))
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []store.SearchHit{})
		return
	}
	hits, err := s.store.Search(r.Context(), me(r), q, 50)
	if err != nil {
		s.fail(w, err, "search")
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := queryInt(r, "days")
	if days <= 0 || days > 366 {
		days = 30
	}
	loc := s.loc(ctx, me(r))
	start := time.Now().In(loc).AddDate(0, 0, -days+1)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	_, off := time.Now().In(loc).Zone()
	offset := fmt.Sprintf("%+d minutes", off/60)
	byDay, err := s.store.UsageByDay(ctx, me(r), store.FormatTime(start), offset)
	if err != nil {
		s.fail(w, err, "usage")
		return
	}
	// fill in empty days so the chart has every bar
	have := map[string]store.UsageDay{}
	for _, d := range byDay {
		have[d.Date] = d
	}
	all := make([]store.UsageDay, 0, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		if v, ok := have[d]; ok {
			all = append(all, v)
		} else {
			all = append(all, store.UsageDay{Date: d})
		}
	}
	prov, err := s.store.UsageByProvider(ctx, me(r), store.FormatTime(start))
	if err != nil {
		s.fail(w, err, "usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": all, "providers": prov, "limits": s.rt.limitsSnapshot()})
}
