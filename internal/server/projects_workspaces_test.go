package server

import (
	"context"
	"testing"
	"time"
)

// Several Projects windows can be open on several workspaces: all of them are
// open to Pocket AI, and the one looked at last is where it searches.
func TestProjectsAgentWorkspaceSeveralOpen(t *testing.T) {
	s, ctx := &Server{}, context.Background()
	set := func(email string, list ...projectsWorkspace) {
		projectsWS.Store(email, projectsWSCached{at: time.Now(), list: list})
		t.Cleanup(func() { projectsWS.Delete(email) })
	}
	set("a@example.com",
		projectsWorkspace{Address: "projects.home.example", Own: true, Active: true},
		projectsWorkspace{Address: "Projects.Work.example", Active: true, Open: true})
	if got := s.projectsAgentWorkspace(ctx, "a@example.com"); got != "projects.work.example" {
		t.Fatalf("looked at last is the connection: %q", got)
	}
	set("b@example.com",
		projectsWorkspace{Address: "projects.home.example", Own: true, Active: true, Open: true},
		projectsWorkspace{Address: "projects.work.example", Active: true})
	if got := s.projectsAgentWorkspace(ctx, "b@example.com"); got != "" {
		t.Fatalf("looked at last is the own instance: %q", got)
	}
	set("c@example.com",
		projectsWorkspace{Address: "projects.home.example", Own: true},
		projectsWorkspace{Address: "projects.work.example", Active: true})
	if got := s.projectsAgentWorkspace(ctx, "c@example.com"); got != "projects.work.example" {
		t.Fatalf("only the connection is open: %q", got)
	}
}
