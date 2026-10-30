package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ThreadsReadScope lets an app see which files a person's coding
// conversations edited, in order, with the sentence the agent said just
// before — Pocket PR Review shows a pull request "as it was built". Never
// the rest of the conversation.
const ThreadsReadScope = "threads:read"

// linkReader accepts a link token with threads:read (or threads:write, which
// includes it) and returns the person it acts for.
func (s *Server) linkReader(w http.ResponseWriter, r *http.Request) *linkReaderUser {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer pcx_") || !s.core.Configured() {
		writeErr(w, http.StatusUnauthorized, "a Pocket Core app-link token is required")
		return nil
	}
	in, err := s.core.Introspect(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil || in == nil || !in.Active || !in.Delegated() || in.Audience != "ai" || in.User == nil || in.User.Email == "" ||
		!(in.HasScope(ThreadsReadScope) || in.HasScope(ThreadsScope)) {
		writeErr(w, http.StatusForbidden, "Pocket AI conversation link rejected")
		return nil
	}
	u, err := s.coreIdentityUser(r.Context(), in)
	if err != nil {
		s.fail(w, err, "resolve AI identity")
		return nil
	}
	return &linkReaderUser{ID: u.ID}
}

type linkReaderUser struct{ ID int64 }

type linkEdit struct {
	At       string `json:"at"`
	Path     string `json:"path"` // relative to the conversation's folder when inside it
	Tool     string `json:"tool"`
	Note     string `json:"note,omitempty"`
	ThreadID string `json:"thread_id"`
}

// maxLinkEdits caps one answer.
const maxLinkEdits = 3000

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

// handleLinkEdits lists the file edits of the person's conversations that
// made a pull request (?pr=<its address>: the conversations that printed it)
// or worked on a branch (?branch=<name>: their worktree), oldest first.
func (s *Server) handleLinkEdits(w http.ResponseWriter, r *http.Request) {
	u := s.linkReader(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	pr := strings.TrimSpace(r.URL.Query().Get("pr"))
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))
	if pr == "" && branch == "" {
		writeErr(w, http.StatusBadRequest, "say which pull request (pr) or branch")
		return
	}
	threads := map[string]string{} // id → cwd
	if pr != "" {
		pr = strings.TrimRight(pr, "/")
		rows, err := s.store.DB.QueryContext(ctx, `SELECT DISTINCT t.id, t.cwd FROM items i JOIN threads t ON t.id = i.thread_id
			WHERE t.user_id = ? AND i.kind = 'tool' AND instr(i.data, ?) > 0`, u.ID, pr)
		if err != nil {
			s.fail(w, err, "edits")
			return
		}
		for rows.Next() {
			var id, cwd string
			if rows.Scan(&id, &cwd) == nil {
				threads[id] = cwd
			}
		}
		rows.Close()
	}
	if branch != "" {
		rows, err := s.store.DB.QueryContext(ctx, `SELECT t.id, t.cwd FROM threads t JOIN worktrees wt ON wt.id = t.worktree_id
			WHERE t.user_id = ? AND wt.user_id = ? AND wt.branch = ?`, u.ID, u.ID, branch)
		if err != nil {
			s.fail(w, err, "edits")
			return
		}
		for rows.Next() {
			var id, cwd string
			if rows.Scan(&id, &cwd) == nil {
				threads[id] = cwd
			}
		}
		rows.Close()
	}
	out := []linkEdit{}
	for id, cwd := range threads {
		rows, err := s.store.DB.QueryContext(ctx, `SELECT kind, data, created_at FROM items WHERE thread_id = ? AND kind IN ('assistant', 'tool') ORDER BY id`, id)
		if err != nil {
			s.fail(w, err, "edits")
			return
		}
		note := ""
		for rows.Next() && len(out) < maxLinkEdits {
			var kind, data, at string
			if rows.Scan(&kind, &data, &at) != nil {
				continue
			}
			var it struct {
				Text string `json:"text"`
				Tool *struct {
					Name  string          `json:"name"`
					Kind  string          `json:"kind"`
					Input json.RawMessage `json:"input"`
				} `json:"tool"`
			}
			if json.Unmarshal([]byte(data), &it) != nil {
				continue
			}
			if kind == "assistant" {
				if t := strings.TrimSpace(it.Text); t != "" {
					note = oneLine(t, 300)
				}
				continue
			}
			if it.Tool == nil || (it.Tool.Kind != "edit" && it.Tool.Kind != "write") {
				continue
			}
			for _, p := range editedPaths(it.Tool.Input) {
				out = append(out, linkEdit{At: at, Path: relTo(cwd, p), Tool: it.Tool.Name, Note: note, ThreadID: id})
			}
		}
		rows.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	writeJSON(w, http.StatusOK, map[string]any{"edits": out, "threads": len(threads)})
}

// editedPaths reads the file(s) an edit tool call changed: Claude's
// file_path, a path field, or the files named in an apply_patch patch.
func editedPaths(raw json.RawMessage) []string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	for _, k := range []string{"file_path", "path", "notebook_path"} {
		if p, ok := in[k].(string); ok && p != "" {
			return []string{p}
		}
	}
	var out []string
	if ch, ok := in["changes"].(map[string]any); ok {
		for p := range ch {
			out = append(out, p)
		}
		sort.Strings(out)
		return out
	}
	for _, k := range []string{"patch", "input", "command"} {
		if s, ok := in[k].(string); ok {
			for _, m := range patchFile.FindAllStringSubmatch(s, -1) {
				out = append(out, strings.TrimSpace(m[1]))
			}
		}
	}
	return out
}

// relTo makes an absolute path relative to the conversation's folder when it is inside it.
func relTo(cwd, p string) string {
	if cwd == "" || !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}
