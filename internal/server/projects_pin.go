package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A conversation a Pocket Projects issue started ("Code with Pocket AI") is
// pinned to that issue: its Projects changes run on their own (the person's
// auto-approve rules) only on the issue itself (comment, update, pull
// request, shared notes), new sub-issues of it and its team's AI memory. A
// change anywhere else — a related issue the agent found, another team, a
// project — always waits for the person's Allow, rules or not. Reads stay
// open. Forks and rewinds keep the pin; the person can lift it per
// conversation (Unpin on the thread page).
//
// This is checked here, where the conversation's tool calls pass on their way
// to Pocket Core: the agent can't name another conversation or skip it.

// projectsPin checks a Projects change from conversation threadID against its
// issue. outside says why it is outside the issue (the person is asked, even
// with an auto-approve rule); refuse, a call that doesn't name its workspace
// although the pin is in one (nothing is sent). Both "" = fine, or not a
// pinned conversation.
func (s *Server) projectsPin(ctx context.Context, uid int64, threadID, tool string, args map[string]any) (outside, refuse string) {
	if !strings.HasPrefix(tool, "pocket_projects_") {
		return "", ""
	}
	c, err := s.store.GetThreadContext(ctx, threadID)
	if err != nil || c.Source != "projects" || c.Ref == "" || c.Unpinned {
		return "", ""
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
	out := func(what string) (string, string) {
		return "Outside " + where + ", the issue this conversation is pinned to: " + what, ""
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
	if str(args, "workspace") == "" && strings.Contains(c.Ref, "/") {
		return "", "This conversation is pinned to " + where + ": pass workspace " + addr + " with Pocket Projects changes. Nothing was sent."
	}
	if ws := str(args, "workspace"); addr != "" && ws != "" && !strings.EqualFold(ws, addr) {
		return out("this change is for workspace " + ws + ".")
	}
	data, _ := args["data"].(map[string]any)
	switch strings.TrimPrefix(tool, "pocket_projects_") {
	case "create_issue":
		if p := str(data, "parent_id"); !strings.EqualFold(p, ident) {
			return out("the new issue is not a sub-issue of " + ident + ".")
		}
	case "remember":
		if t := str(args, "team"); !strings.EqualFold(t, key) {
			return out("team " + t + "'s memory.")
		}
	case "create_project", "post_update":
		return out("a project.")
	default:
		if id := str(args, "id"); !strings.EqualFold(id, ident) {
			return out("issue " + id + ".")
		}
	}
	return "", ""
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
