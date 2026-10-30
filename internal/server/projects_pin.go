package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A conversation a Pocket Projects issue started ("Code with Pocket AI") is
// pinned to that issue: its Projects changes may only touch the issue itself
// (comment, update, pull request, shared notes), new sub-issues of it and its
// team's AI memory. Reads stay open. Forks and rewinds keep the pin; the
// person can lift it per conversation (Unpin on the thread page).
//
// This is checked here, where the conversation's tool calls pass on their way
// to Pocket Core: the agent can't name another conversation or skip it.

// projectsPin says why a Projects change from conversation threadID is outside
// its issue ("" = fine, or not a pinned conversation).
func (s *Server) projectsPin(ctx context.Context, uid int64, threadID, tool string, args map[string]any) string {
	if !strings.HasPrefix(tool, "pocket_projects_") {
		return ""
	}
	c, err := s.store.GetThreadContext(ctx, threadID)
	if err != nil || c.Source != "projects" || c.Ref == "" || c.Unpinned {
		return ""
	}
	addr, ident := "", strings.ToUpper(c.Ref)
	if i := strings.LastIndex(c.Ref, "/"); i > 0 {
		addr, ident = strings.ToLower(c.Ref[:i]), strings.ToUpper(c.Ref[i+1:])
	} else if u, err := s.store.GetUser(ctx, uid); err == nil {
		for _, w := range s.projectsWorkspaces(ctx, u.Email) {
			if w.Own {
				addr = strings.ToLower(w.Address)
			}
		}
	}
	key := ident
	if i := strings.LastIndex(ident, "-"); i > 0 {
		key = ident[:i]
	}
	where := ident
	if addr != "" {
		where += " (" + addr + ")"
	}
	refuse := func(what string) string {
		return "This conversation is pinned to " + where + ": its Pocket Projects changes may only touch that issue, new sub-issues of it (parent_id " + ident + ") and team " + key + "'s memory. " + what +
			" Nothing was sent. If the person wants other issues changed from here, they unpin the conversation in Pocket AI."
	}
	str := func(m map[string]any, k string) string {
		switch v := m[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return fmt.Sprint(int64(v))
		}
		return ""
	}
	if ws := str(args, "workspace"); addr != "" && ws != "" && !strings.EqualFold(ws, addr) {
		return refuse("This call is for " + ws + ".")
	}
	if str(args, "workspace") == "" && strings.Contains(c.Ref, "/") {
		return refuse("Pass workspace " + addr + ".")
	}
	data, _ := args["data"].(map[string]any)
	switch strings.TrimPrefix(tool, "pocket_projects_") {
	case "create_issue":
		if p := str(data, "parent_id"); !strings.EqualFold(p, ident) {
			return refuse("A new issue here must be a sub-issue of " + ident + ".")
		}
	case "remember":
		if t := str(args, "team"); !strings.EqualFold(t, key) {
			return refuse("Team " + t + " is another team.")
		}
	case "create_project", "post_update":
		return refuse("Projects are outside it.")
	default:
		if id := str(args, "id"); !strings.EqualFold(id, ident) {
			return refuse("Issue " + id + " is another one (name it " + ident + ").")
		}
	}
	return ""
}

// POST /threads/{id}/pin {pinned}: the person pins or unpins a Projects
// conversation (owner only; agents have no route here).
func (s *Server) handleThreadPin(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	var in struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	c, err := s.store.GetThreadContext(r.Context(), t.ID)
	if err != nil || c.Source != "projects" {
		writeErr(w, http.StatusNotFound, "this conversation doesn't belong to a Pocket Projects issue")
		return
	}
	if err := s.store.SetThreadPinned(r.Context(), t.ID, in.Pinned); err != nil {
		s.fail(w, err, "pin")
		return
	}
	c.Unpinned = !in.Pinned
	writeJSON(w, http.StatusOK, c)
}
