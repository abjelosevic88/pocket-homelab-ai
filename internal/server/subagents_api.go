package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// handleSubagents lists every sub-agent the thread's Claude Code session
// started, working or finished. GET /threads/{id}/subagents
func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessView)
	if t == nil {
		return
	}
	list := []agent.SubagentInfo{}
	if path := s.threadSessionPath(r, t); path != "" {
		list = agent.ListSubagents(path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"subagents": list})
}

// handleSubagent is one sub-agent: its state and its whole transcript as
// timeline items. GET /threads/{id}/subagents/{sid}
func (s *Server) handleSubagent(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessView)
	if t == nil {
		return
	}
	sid := chi.URLParam(r, "sid")
	path := s.threadSessionPath(r, t)
	if path == "" {
		writeErr(w, http.StatusNotFound, "no such sub-agent")
		return
	}
	var info *agent.SubagentInfo
	for _, a := range agent.ListSubagents(path) {
		if a.ID == sid {
			info = &a
			break
		}
	}
	tp, err := agent.SubagentPath(path, sid)
	if info == nil || err != nil {
		writeErr(w, http.StatusNotFound, "no such sub-agent")
		return
	}
	items, err := agent.ReadClaudeSubagent(tp)
	if err != nil {
		s.fail(w, err, "read sub-agent")
		return
	}
	if info.Status == "running" { // tools still waiting for their result are at work
		for i := range items {
			if tl := items[i].Tool; tl != nil && tl.Output == "" && tl.ExitCode == nil && tl.Diff == "" && tl.Images == 0 {
				c := *tl
				c.Status = "running"
				items[i].Tool = &c
			}
		}
	}
	if items == nil {
		items = []agent.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subagent": info, "items": items})
}

// claudeSessionPath is the session file of a Claude thread, or "" for Codex
// threads and sessions that are gone.
func (s *Server) claudeSessionPath(r *http.Request, provider string, accountID int64, sessionID string) string {
	if provider != "claude" || sessionID == "" {
		return ""
	}
	dir, _ := s.accountHome(r.Context(), provider, accountID)
	path, err := agent.FindClaude(dir, sessionID)
	if err != nil {
		return ""
	}
	return path
}
