package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// attachCore fakes Pocket Core (introspection for the app's threads:write
// calls, the person's links, link tokens) and Pocket Projects behind it.
func attachCore(t *testing.T, s *Server, email string, linkOn *bool) *[]string {
	t.Helper()
	asked := &[]string{}
	projects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.RequestURI())
		switch {
		case r.URL.Path == "/api/v1/workspaces":
			json.NewEncoder(w).Encode([]projectsWorkspace{})
		case r.URL.Path == "/api/v1/issues":
			json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{
				{"identifier": "AS-7", "title": "Fix login", "state_type": "started"},
				{"identifier": "AS-9", "title": "Dark mode", "state_type": "backlog"}}})
		case r.URL.Path == "/api/v1/issues/AS-7":
			json.NewEncoder(w).Encode(map[string]any{"identifier": "AS-7", "title": "Fix login", "url": "https://projects.example/issue/AS-7"})
		case r.URL.Path == "/api/v1/issues/AS-9":
			json.NewEncoder(w).Encode(map[string]any{"identifier": "AS-9", "title": "Dark mode", "url": "https://projects.example/issue/AS-9"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	t.Cleanup(projects.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/introspect":
			json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "scope": ThreadsScope, "audience": "ai",
				"app":  map[string]any{"id": 3, "slug": "projects", "name": "projects"},
				"user": map[string]any{"id": 7, "sub": "sub-" + email, "email": email, "name": email}})
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "projects", Scope: projectsScope, On: *linkOn, State: map[bool]string{true: "on", false: "off_person"}[*linkOn]}})
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_projects", URL: projects.URL, App: "projects"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	return asked
}

func TestAttachThreadToIssue(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "attach@example.test", "member", true)
	other, _ := person(t, s, h, "other@example.test", "admin", true)
	on := false
	asked := attachCore(t, s, "attach@example.test", &on)
	app := &client{t: t, h: h, token: "pcx_valid"}
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0700)
	ctx := context.Background()
	th := &store.Thread{ID: uuid.NewString(), UserID: user.ID, Title: "Login bug", Provider: "claude", Cwd: repo, Source: "web"}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	attach := "/api/v1/threads/" + th.ID + "/attach"

	// the link off: nothing to pick, nothing attached
	owner.want("GET", "/api/v1/projects-issues", nil, 409, nil)
	owner.want("POST", attach, map[string]any{"ref": "AS-7"}, 409, nil)
	on = true

	var picked struct {
		Issues []projectsAttachIssue `json:"issues"`
	}
	owner.want("GET", "/api/v1/projects-issues", nil, 200, &picked)
	if len(picked.Issues) != 2 || picked.Issues[0].Ref != "AS-7" || picked.Issues[0].Title != "Fix login" {
		t.Fatalf("issues: %+v", picked)
	}
	owner.want("GET", "/api/v1/projects-issues?q=login", nil, 200, &picked)
	if last := (*asked)[len(*asked)-1]; !strings.Contains(last, "q=login") || strings.Contains(last, "status=open") {
		t.Fatalf("search asked %q", last)
	}

	// only the owner attaches; bad and unknown keys are refused
	other.want("POST", attach, map[string]any{"ref": "AS-7"}, 404, nil)
	owner.want("POST", attach, map[string]any{"ref": "not an issue"}, 400, nil)
	owner.want("POST", attach, map[string]any{"ref": "AS-404"}, 404, nil)

	var c store.ThreadContext
	owner.want("POST", attach, map[string]any{"ref": "as-7"}, 200, &c)
	if c.Ref != "AS-7" || c.Source != "projects" || !c.Unpinned || c.RefLabel != "Fix login" || c.RefURL != "https://projects.example/issue/AS-7" {
		t.Fatalf("context: %+v", c)
	}
	if note := s.rt.takeTurnNote(th.ID); !strings.Contains(note, "AS-7") {
		t.Fatalf("agent not told: %q", note)
	}
	var l []linkThread
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-7", nil, 200, &l)
	if len(l) != 1 || l[0].ThreadID == nil || *l[0].ThreadID != th.ID || l[0].Status == "draft" || l[0].Title != "Login bug" {
		t.Fatalf("issue conversations: %+v", l)
	}
	var link struct {
		Threads map[string]string `json:"threads"`
	}
	owner.want("GET", "/api/v1/projects-link", nil, 200, &link)
	if link.Threads[th.ID] != "AS-7" {
		t.Fatalf("sidebar grouping: %+v", link.Threads)
	}
	// attaching again changes nothing
	owner.want("POST", attach, map[string]any{"ref": "AS-7"}, 200, nil)
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-7", nil, 200, &l)
	if len(l) != 1 {
		t.Fatalf("attached twice: %d", len(l))
	}

	// another issue moves it
	owner.want("POST", attach, map[string]any{"ref": "AS-9"}, 200, &c)
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-7", nil, 200, &l)
	if len(l) != 0 {
		t.Fatalf("still on AS-7: %+v", l)
	}
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-9", nil, 200, &l)
	if len(l) != 1 {
		t.Fatalf("not on AS-9: %+v", l)
	}

	// detached: off the issue and out of the group
	other.want("DELETE", attach, nil, 404, nil)
	owner.want("DELETE", attach, nil, 204, nil)
	owner.want("DELETE", attach, nil, 404, nil)
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-9", nil, 200, &l)
	if len(l) != 0 {
		t.Fatalf("still on AS-9: %+v", l)
	}
	link.Threads = nil
	owner.want("GET", "/api/v1/projects-link", nil, 200, &link)
	if _, ok := link.Threads[th.ID]; ok {
		t.Fatal("still grouped")
	}
	if _, err := s.store.GetThreadContext(ctx, th.ID); err == nil {
		t.Fatal("context left behind")
	}
}

// A conversation an issue started keeps nothing of it after a detach: its
// source no longer names the issue (the sidebar's fallback reads it).
func TestDetachProjectsStartedThread(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "started@example.test", "member", true)
	on := true
	attachCore(t, s, "started@example.test", &on)
	ctx := context.Background()
	th := &store.Thread{ID: uuid.NewString(), UserID: user.ID, Title: "AS-7", Provider: "claude", Cwd: root, Source: "link:projects", SourceRef: "AS-7"}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	_ = s.store.SaveThreadContext(ctx, &store.ThreadContext{ThreadID: th.ID, System: "TEAM", Source: "projects", Ref: "AS-7"})
	owner.want("DELETE", "/api/v1/threads/"+th.ID+"/attach", nil, 204, nil)
	got, err := s.store.GetThread(ctx, user.ID, th.ID)
	if err != nil || got.SourceRef != "" {
		t.Fatalf("source ref kept: %+v %v", got, err)
	}
}
