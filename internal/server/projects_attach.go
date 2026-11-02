package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Attaching a conversation to a Pocket Projects issue after the fact (it
// started anywhere: the start page, a recipe, a fork). It then belongs to the
// issue exactly like one the issue started: the issue's Code panel lists it
// (its PRs are attached to the issue), the sidebar and the tab bar group it
// under the issue. It is not pinned (it wasn't started for the issue) and it
// gets no team instructions; the agent hears about it with the next message.
//
// The issue picker searches the workspace open to Pocket AI in Projects (a
// connected instance's issues are host/ATH-12, as Projects names them).

// projectsAttachIssue is one issue the picker offers.
type projectsAttachIssue struct {
	Ref        string `json:"ref"`        // what the conversation is filed under: AS-7 or host/ATH-12
	Identifier string `json:"identifier"` // AS-7
	Title      string `json:"title"`
	StateType  string `json:"state_type"`
}

// projectsCall GETs a Projects API path as the person; workspace "" = Projects' own.
func (s *Server) projectsCall(ctx context.Context, email, path, workspace string, out any) (int, error) {
	tok, err := s.core.ActAs(ctx, email, "projects", projectsScope)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, onThisHost(strings.TrimRight(tok.URL, "/"))+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	if workspace != "" {
		// Projects answers for a connected workspace the way it answers the
		// agent's own reads (workspace_guard.go there)
		req.Header.Set("X-Pocket-Source", "mcp")
		req.Header.Set("X-Pocket-Workspace", workspace)
	}
	res, err := projectsHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return res.StatusCode, errors.New(e.Error)
	}
	return res.StatusCode, json.NewDecoder(http.MaxBytesReader(nil, res.Body, 4<<20)).Decode(out)
}

// projectsAgentWorkspace: the connected workspace open to Pocket AI, "" when
// that is Projects' own (or there is only one).
func (s *Server) projectsAgentWorkspace(ctx context.Context, email string) string {
	for _, w := range s.projectsWorkspaces(ctx, email) {
		if w.Active && !w.Own {
			return strings.ToLower(w.Address)
		}
	}
	return ""
}

// projectsLinkReady answers for the person when the Core link to Projects is off.
func (s *Server) projectsLinkReady(w http.ResponseWriter, r *http.Request, email string) bool {
	if !s.core.Configured() {
		writeErr(w, http.StatusConflict, "There is no Pocket Core on this box, so Pocket AI can't reach Pocket Projects.")
		return false
	}
	if on, reason := s.core.LinkOn(r.Context(), email, "projects", projectsScope); !on {
		writeErr(w, http.StatusConflict, firstNonEmpty(reason, "Turn on Pocket AI → Pocket Projects in Pocket Core's Links first."))
		return false
	}
	return true
}

// GET /projects-issues?q=: issues to attach a conversation to (open ones,
// latest first, without a query).
func (s *Server) handleProjectsIssues(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !s.projectsLinkReady(w, r, u.Email) {
		return
	}
	ws := s.projectsAgentWorkspace(r.Context(), u.Email)
	q := url.Values{"sort": {"updated"}, "limit": {"15"}}
	if text := strings.TrimSpace(r.URL.Query().Get("q")); text != "" {
		q.Set("q", text)
	} else {
		q.Set("status", "open")
	}
	var got struct {
		Issues []struct {
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
			StateType  string `json:"state_type"`
		} `json:"issues"`
	}
	if _, err := s.projectsCall(r.Context(), u.Email, "/api/v1/issues?"+q.Encode(), ws, &got); err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Projects: "+err.Error())
		return
	}
	out := []projectsAttachIssue{}
	for _, is := range got.Issues {
		ref := is.Identifier
		if ws != "" {
			ref = ws + "/" + ref
		}
		out = append(out, projectsAttachIssue{Ref: ref, Identifier: is.Identifier, Title: is.Title, StateType: is.StateType})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": ws, "issues": out})
}

// POST /threads/{id}/attach {ref}: the person files the conversation under an
// issue (moving it from the one it was under, if any).
func (s *Server) handleAttachThread(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	if t.Source == "ask" {
		writeErr(w, http.StatusBadRequest, "Ask chats can't belong to an issue.")
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ws, ident := "", strings.ToUpper(strings.TrimSpace(in.Ref))
	if i := strings.LastIndex(ident, "/"); i > 0 {
		ws, ident = strings.ToLower(ident[:i]), ident[i+1:]
	}
	if !projectsRefRe.MatchString(ident) {
		writeErr(w, http.StatusBadRequest, "name the issue by its key, like AS-7")
		return
	}
	ref := ident
	if ws != "" {
		ref = ws + "/" + ident
	}
	if !s.projectsLinkReady(w, r, u.Email) {
		return
	}
	if c, err := s.store.GetThreadContext(ctx, t.ID); err == nil && c.Source == "projects" && strings.EqualFold(c.Ref, ref) {
		writeJSON(w, http.StatusOK, c) // already there
		return
	}
	var is struct {
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		URL        string `json:"url"`
	}
	if code, err := s.projectsCall(ctx, u.Email, "/api/v1/issues/"+url.PathEscape(ident), ws, &is); err != nil {
		if code == http.StatusNotFound || code == http.StatusForbidden {
			writeErr(w, http.StatusNotFound, "Pocket Projects has no issue "+ref+" you can see.")
			return
		}
		writeErr(w, http.StatusBadGateway, "Pocket Projects: "+err.Error())
		return
	}
	if is.Identifier != "" { // Projects' spelling, which its Code panel asks for
		ident = is.Identifier
		ref = ident
		if ws != "" {
			ref = ws + "/" + ident
		}
	}

	bg := context.WithoutCancel(ctx)
	if err := s.store.DetachThread(bg, u.ID, t.ID, "projects"); err != nil {
		s.fail(w, err, "attach")
		return
	}
	d := &store.LinkDraft{ID: uuid.NewString(), UserID: u.ID, Provider: t.Provider, Cwd: t.Cwd, Title: t.Title,
		Source: "projects", Ref: ref, RefURL: is.URL, RefLabel: is.Title, HostID: t.HostID, UploadIDs: []string{}}
	if err := s.store.CreateLinkDraft(bg, d); err != nil {
		s.fail(w, err, "attach")
		return
	}
	_, _ = s.store.ClaimLinkDraft(bg, u.ID, d.ID)
	if err := s.store.FinishLinkDraft(bg, d.ID, t.ID); err != nil {
		s.fail(w, err, "attach")
		return
	}
	c := &store.ThreadContext{ThreadID: t.ID, Source: "projects", Ref: ref, RefURL: is.URL, RefLabel: is.Title, Unpinned: true}
	if err := s.store.SaveThreadContext(bg, c); err != nil {
		s.fail(w, err, "attach")
		return
	}
	where := ref
	if is.Title != "" {
		where += fmt.Sprintf(" (%q)", is.Title)
	}
	s.rt.addTurnNote(t.ID, "\n\n[Pocket AI: the person attached this conversation to Pocket Projects issue "+where+
		". It is now listed on that issue, and pull requests you open here are attached to it.]\n")
	s.log.Info("projects: conversation attached", "thread", t.ID, "issue", ref)
	s.touch()
	writeJSON(w, http.StatusOK, c)
}

// DELETE /threads/{id}/attach: the conversation no longer belongs to its issue.
func (s *Server) handleDetachThread(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	ctx := r.Context()
	c, err := s.store.GetThreadContext(ctx, t.ID)
	if err != nil || c.Source != "projects" {
		writeErr(w, http.StatusNotFound, "this conversation doesn't belong to a Pocket Projects issue")
		return
	}
	if err := s.store.DetachThread(context.WithoutCancel(ctx), userFrom(ctx).ID, t.ID, "projects"); err != nil {
		s.fail(w, err, "detach")
		return
	}
	s.rt.addTurnNote(t.ID, "\n\n[Pocket AI: the person detached this conversation from Pocket Projects issue "+c.Ref+". It no longer belongs to that issue.]\n")
	s.log.Info("projects: conversation detached", "thread", t.ID, "issue", c.Ref)
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}
