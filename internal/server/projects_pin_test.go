package server

import (
	"context"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A conversation a Projects issue started changes only that issue.
func TestProjectsPin(t *testing.T) {
	s, h, _ := testServer(t)
	alex, u := person(t, s, h, "alex@example.com", "admin", true)
	ctx := context.Background()
	mk := func(id, ref string) {
		t.Helper()
		if err := s.store.CreateThread(ctx, &store.Thread{ID: id, UserID: u.ID, Provider: "claude", Cwd: "/tmp", Source: "link:projects", SourceRef: ref}); err != nil {
			t.Fatal(err)
		}
		if err := s.store.SaveThreadContext(ctx, &store.ThreadContext{ThreadID: id, Source: "projects", Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	mk("t-remote", "projects.athena.example/ATH-12")
	ws := "projects.athena.example"
	pin := func(thread, tool string, args map[string]any) string {
		return s.projectsPin(ctx, u.ID, thread, tool, args)
	}
	ok := []struct {
		tool string
		args map[string]any
	}{
		{"pocket_projects_comment", map[string]any{"id": "ATH-12", "workspace": ws}},
		{"pocket_projects_update_issue", map[string]any{"id": "ath-12", "workspace": ws}},
		{"pocket_projects_link_pr", map[string]any{"id": "ATH-12", "workspace": ws}},
		{"pocket_projects_share_note", map[string]any{"id": "ATH-12", "note": 4, "workspace": ws}},
		{"pocket_projects_create_issue", map[string]any{"workspace": ws, "data": map[string]any{"title": "Follow-up", "team": "ATH", "parent_id": "ATH-12"}}},
		{"pocket_projects_remember", map[string]any{"team": "ATH", "workspace": ws}},
		{"pocket_notes_create", map[string]any{"data": map[string]any{"title": "Plan"}}}, // other apps: not about the pin
	}
	for _, c := range ok {
		if msg := pin("t-remote", c.tool, c.args); msg != "" {
			t.Fatalf("%s refused: %s", c.tool, msg)
		}
	}
	no := []struct {
		tool string
		args map[string]any
	}{
		{"pocket_projects_comment", map[string]any{"id": "ATH-13", "workspace": ws}},
		{"pocket_projects_comment", map[string]any{"id": 12, "workspace": ws}},
		{"pocket_projects_comment", map[string]any{"id": "ATH-12", "workspace": "projects.home.example"}},
		{"pocket_projects_comment", map[string]any{"id": "ATH-12"}},
		{"pocket_projects_create_issue", map[string]any{"workspace": ws, "data": map[string]any{"title": "Loose", "team": "ATH"}}},
		{"pocket_projects_create_issue", map[string]any{"workspace": ws, "data": map[string]any{"title": "Elsewhere", "team": "ATH", "parent_id": "ATH-1"}}},
		{"pocket_projects_remember", map[string]any{"team": "OPS", "workspace": ws}},
		{"pocket_projects_create_project", map[string]any{"workspace": ws, "data": map[string]any{"name": "P"}}},
		{"pocket_projects_post_update", map[string]any{"id": 3, "workspace": ws}},
		{"pocket_projects_update_shared_note", map[string]any{"id": "ATH-2", "note": 9, "workspace": ws}},
	}
	for _, c := range no {
		if msg := pin("t-remote", c.tool, c.args); !strings.Contains(msg, "pinned to ATH-12 (projects.athena.example)") {
			t.Fatalf("%s %v went through: %q", c.tool, c.args, msg)
		}
	}
	// a conversation no issue started is not pinned
	if err := s.store.CreateThread(ctx, &store.Thread{ID: "t-free", UserID: u.ID, Provider: "claude", Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if msg := pin("t-free", "pocket_projects_comment", map[string]any{"id": "ATH-13", "workspace": ws}); msg != "" {
		t.Fatalf("free conversation pinned: %s", msg)
	}
	// the person unpins (and pins again); forks keep it
	var c store.ThreadContext
	alex.want("POST", "/api/v1/threads/t-remote/pin", map[string]any{"pinned": false}, 200, &c)
	if !c.Unpinned || pin("t-remote", "pocket_projects_comment", map[string]any{"id": "ATH-13", "workspace": ws}) != "" {
		t.Fatal("unpinned conversation still pinned")
	}
	alex.want("POST", "/api/v1/threads/t-remote/pin", map[string]any{"pinned": true}, 200, &c)
	if c.Unpinned || pin("t-remote", "pocket_projects_comment", map[string]any{"id": "ATH-13", "workspace": ws}) == "" {
		t.Fatal("pinning again didn't hold")
	}
	if err := s.store.CreateThread(ctx, &store.Thread{ID: "t-fork", UserID: u.ID, Provider: "claude", Cwd: "/tmp", Source: "fork"}); err != nil {
		t.Fatal(err)
	}
	from, _ := s.store.GetThread(ctx, u.ID, "t-remote")
	to, _ := s.store.GetThread(ctx, u.ID, "t-fork")
	s.inheritLink(ctx, from, to)
	if pin("t-fork", "pocket_projects_comment", map[string]any{"id": "ATH-13", "workspace": ws}) == "" {
		t.Fatal("a fork lost the pin")
	}
	// someone else can't unpin it
	bob, _ := person(t, s, h, "bob@example.com", "member", true)
	if code, _ := bob.call("POST", "/api/v1/threads/t-remote/pin", map[string]any{"pinned": false}); code < 400 {
		t.Fatalf("someone else unpinned it: %d", code)
	}
}
