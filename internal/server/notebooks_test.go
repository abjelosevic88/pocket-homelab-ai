package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestNotebookSourcesAndFolder(t *testing.T) {
	s, h, _ := testServer(t)
	alice, au := person(t, s, h, "alice@example.com", "admin", true)
	bob, _ := person(t, s, h, "bob@example.com", "admin", true)

	var nb notebookOut
	alice.want("POST", "/api/v1/notebooks", map[string]any{"name": "  Session notes  ", "instructions": "Answer in Serbian"}, 201, &nb)
	if nb.Name != "Session notes" || nb.Folder != s.notebookDir(context.Background(), au.ID, nb.ID) {
		t.Fatalf("bad notebook: %#v", nb)
	}
	base := fmt.Sprintf("/api/v1/notebooks/%d", nb.ID)

	var x store.NotebookSource
	alice.want("POST", base+"/sources/text", map[string]any{"title": "Retro", "text": "We agreed to ship on Friday."}, 201, &x)
	if x.Kind != "text" || !strings.HasPrefix(x.File, "sources/") || x.Chars == 0 {
		t.Fatalf("bad source: %#v", x)
	}
	data, err := os.ReadFile(filepath.Join(nb.Folder, x.File))
	if err != nil || !strings.Contains(string(data), "ship on Friday") || !strings.Contains(string(data), "# Retro") {
		t.Fatalf("source file: %v %s", err, data)
	}

	// an upload keeps the original next to its text
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "plan.md")
	part.Write([]byte("# Plan\n\nMove search to its own table.")) //nolint:errcheck
	mw.Close()
	code, body := alice.do("POST", base+"/sources/upload", mw.FormDataContentType(), buf.Bytes())
	if code != 201 || !strings.Contains(string(body), "plan.md") {
		t.Fatalf("upload: %d %s", code, body)
	}

	index, _ := os.ReadFile(filepath.Join(nb.Folder, "INDEX.md"))
	for _, want := range []string{"Session notes", "Answer in Serbian", "Retro", "plan.md", "originals/"} {
		if !strings.Contains(string(index), want) {
			t.Fatalf("INDEX.md lacks %q:\n%s", want, index)
		}
	}

	var text struct{ Text string }
	alice.want("GET", fmt.Sprintf("%s/sources/%d/text", base, x.ID), nil, 200, &text)
	if strings.TrimSpace(text.Text) != "We agreed to ship on Friday." {
		t.Fatalf("text: %q", text.Text)
	}
	alice.want("PUT", fmt.Sprintf("%s/sources/%d", base, x.ID), map[string]any{"title": "Retro 11 Nov"}, 200, &x)

	// someone else's notebook stays invisible
	bob.want("GET", base, nil, 404, nil)
	bob.want("POST", base+"/sources/text", map[string]any{"text": "x"}, 404, nil)
	var list []store.Notebook
	bob.want("GET", "/api/v1/notebooks", nil, 200, &list)
	if len(list) != 0 {
		t.Fatal("another person's notebooks leaked")
	}

	// a chat in its folder is listed and gets the notebook prompt
	th := thread(t, s, au.ID, nb.Folder)
	var full notebookOut
	alice.want("GET", base, nil, 200, &full)
	if len(full.SourcesList) != 2 || len(full.ChatsList) != 1 || full.ChatsList[0].Notebook != nb.ID {
		t.Fatalf("full: %d sources %d chats", len(full.SourcesList), len(full.ChatsList))
	}
	if p := s.notebookPrompt(context.Background(), th); !strings.Contains(p, "Session notes") || !strings.Contains(p, "Answer in Serbian") {
		t.Fatalf("prompt: %q", p)
	}
	if p := s.notebookPrompt(context.Background(), &store.Thread{UserID: au.ID, Cwd: "/tmp/notebooks/1"}); p != "" {
		t.Fatal("a look-alike folder got the notebook prompt")
	}

	alice.want("DELETE", fmt.Sprintf("%s/sources/%d", base, x.ID), nil, 204, nil)
	if _, err := os.Stat(filepath.Join(nb.Folder, x.File)); !os.IsNotExist(err) {
		t.Fatal("removed source's file is still there")
	}
	alice.want("DELETE", base, nil, 204, nil)
	if _, err := os.Stat(nb.Folder); !os.IsNotExist(err) {
		t.Fatal("deleted notebook's folder is still there")
	}
	if got, _ := s.store.GetThread(context.Background(), au.ID, th.ID); got == nil || !got.Archived {
		t.Fatal("its chat should move to the archive")
	}
}

func TestNotebookLink(t *testing.T) {
	page := `<html><head><title>Halfvec &amp; HNSW</title><script>var x=1</script></head><body><nav>Home | Docs</nav>
		<article><h1>Halfvec</h1><p>Half-precision vectors use <b>2 bytes</b> per dimension.</p><ul><li>smaller</li><li>faster</li></ul></article><footer>©</footer></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page)) //nolint:errcheck
	}))
	defer srv.Close()
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "alice@example.com", "admin", true)
	var nb notebookOut
	alice.want("POST", "/api/v1/notebooks", map[string]any{"name": "Search"}, 201, &nb)
	var x store.NotebookSource
	alice.want("POST", fmt.Sprintf("/api/v1/notebooks/%d/sources/link", nb.ID), map[string]any{"url": srv.URL + "/halfvec"}, 201, &x)
	if x.Title != "Halfvec & HNSW" || x.Kind != "link" {
		t.Fatalf("link: %#v", x)
	}
	data, _ := os.ReadFile(filepath.Join(nb.Folder, x.File))
	got := string(data)
	if !strings.Contains(got, "# Halfvec") || !strings.Contains(got, "2 bytes") || !strings.Contains(got, "- smaller") || strings.Contains(got, "var x") || strings.Contains(got, "Home | Docs") {
		t.Fatalf("page text:\n%s", got)
	}
	alice.want("POST", fmt.Sprintf("/api/v1/notebooks/%d/sources/link", nb.ID), map[string]any{"url": "ftp://x"}, 400, nil)
}

// projectsNotebookCore fakes Pocket Core and a Pocket Projects with one
// issue (comments, a sub-issue, a shared note, a file) and one project.
func projectsNotebookCore(t *testing.T, s *Server, email string, comment *string) {
	t.Helper()
	projects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck
		switch r.URL.Path {
		case "/api/v1/workspaces":
			send([]projectsWorkspace{})
		case "/api/v1/people":
			send([]map[string]any{{"id": 4, "name": "Milan"}})
		case "/api/v1/issues/ATH-340":
			send(map[string]any{"identifier": "ATH-340", "title": "Files in the shared search", "description": "Tracked Sources become a third corpus.",
				"state_type": "started", "priority": 2, "url": "https://projects.example/issue/ATH-340",
				"sub_issues":      []map[string]any{{"identifier": "ATH-341", "title": "Halfvec table", "state_type": "backlog", "description": "Own HNSW index."}},
				"attachment_list": []map[string]any{{"id": 55, "kind": "file", "file_name": "measurements.csv", "size": 30}, {"id": 56, "kind": "link", "title": "PR #5244", "url": "https://git.example/pr/5244"}},
				"shared_notes":    []map[string]any{{"id": 9, "title": "ATH-340 plan"}},
				"comment_list":    []map[string]any{{"user_id": 4, "body": *comment, "created_at": "2026-11-11T09:00:00Z"}}})
		case "/api/v1/issues/ATH-340/shared-notes/9":
			send(map[string]any{"title": "ATH-340 plan", "body": "Embed one sheet profile per sheet."})
		case "/api/v1/attachments/55/file":
			w.Write([]byte("source,rows\nvs-production,36862\n")) //nolint:errcheck
		case "/api/v1/issues/ATH-340/notes":
			send(map[string]any{"status": "off", "items": []any{}})
		case "/api/v1/projects/12":
			send(map[string]any{"id": 12, "name": "Unified search", "summary": "One search for every agent.", "status": "started", "lead_id": 4,
				"docs": []map[string]any{{"id": 3, "title": "Architecture", "body": "Postgres 16 with pgvector."}}, "updates": []map[string]any{{"user_id": 4, "health": "on_track", "body": "Halfway there.", "created_at": "2026-11-10T08:00:00Z"}}})
		case "/api/v1/projects/12/milestones":
			send([]map[string]any{{"name": "Beta", "target_date": "2026-12-01"}})
		case "/api/v1/issues":
			send(map[string]any{"issues": []map[string]any{{"identifier": "ATH-340", "title": "Files in the shared search", "state_type": "started"}}, "total": 1})
		case "/api/v1/projects":
			send([]map[string]any{{"id": 12, "name": "Unified search", "status": "started"}})
		default:
			w.WriteHeader(404)
			send(map[string]string{"error": "not found"})
		}
	}))
	t.Cleanup(projects.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "projects", Scope: projectsScope, On: true, State: "on"}}) //nolint:errcheck
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_projects", URL: projects.URL, App: "projects"}) //nolint:errcheck
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
}

func TestNotebookFromProjects(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "alice@example.com", "admin", true)
	comment := "Milan owns the embedding worker."
	projectsNotebookCore(t, s, "alice@example.com", &comment)

	var projs struct{ Projects []struct{ Ref, Name string } }
	alice.want("GET", "/api/v1/projects-projects", nil, 200, &projs)
	if len(projs.Projects) != 1 || projs.Projects[0].Ref != "12" {
		t.Fatalf("projects: %+v", projs)
	}

	var nb notebookOut
	alice.want("POST", "/api/v1/notebooks", map[string]any{"name": "ATH-340"}, 201, &nb)
	base := fmt.Sprintf("/api/v1/notebooks/%d", nb.ID)
	var got struct {
		Report   importReport
		Notebook notebookOut
	}
	alice.want("POST", base+"/sources/projects", map[string]any{"kind": "issue", "ref": "ATH-340"}, 200, &got)
	if got.Report.Added != 3 || len(got.Report.Failed) != 0 {
		t.Fatalf("issue import: %+v", got.Report)
	}
	if got.Notebook.Link != "projects-issue:ATH-340" || got.Notebook.LinkURL != "https://projects.example/issue/ATH-340" {
		t.Fatalf("link: %+v", got.Notebook.Notebook)
	}
	read := func(kind string) string {
		for _, x := range got.Notebook.SourcesList {
			if x.Kind == kind {
				data, _ := os.ReadFile(filepath.Join(nb.Folder, x.File))
				return string(data)
			}
		}
		t.Fatalf("no %s source in %+v", kind, got.Notebook.SourcesList)
		return ""
	}
	issue := read("issue")
	for _, want := range []string{"# ATH-340 · Files in the shared search", "started, High", "third corpus", "ATH-341 Halfvec table", "Own HNSW index", "PR #5244 https://git.example/pr/5244", "### Milan · 2026-11-11", "Milan owns the embedding worker."} {
		if !strings.Contains(issue, want) {
			t.Fatalf("issue source lacks %q:\n%s", want, issue)
		}
	}
	if !strings.Contains(read("shared-note"), "sheet profile") || !strings.Contains(read("file"), "36862") {
		t.Fatal("shared note or file missing")
	}

	// again later: updated in place, nothing doubled
	comment = "Milan hands the worker to Ana."
	alice.want("POST", base+"/sync", nil, 200, &got)
	if got.Report.Added != 0 || got.Report.Updated != 2 || len(got.Notebook.SourcesList) != 3 {
		t.Fatalf("sync: %+v, %d sources", got.Report, len(got.Notebook.SourcesList))
	}
	if !strings.Contains(read("issue"), "hands the worker to Ana") {
		t.Fatal("sync did not update the issue")
	}

	alice.want("POST", base+"/sources/projects", map[string]any{"kind": "project", "ref": "12"}, 200, &got)
	project := read("project")
	for _, want := range []string{"# Project: Unified search", "Lead: Milan", "One search for every agent", "Beta (target 2026-12-01)", "on_track", "Halfway there", "ATH-340 Files in the shared search"} {
		if !strings.Contains(project, want) {
			t.Fatalf("project source lacks %q:\n%s", want, project)
		}
	}
	if !strings.Contains(read("project-doc"), "pgvector") || got.Notebook.Link != "projects-issue:ATH-340" {
		t.Fatal("project doc missing, or the notebook's link changed")
	}
	alice.want("POST", base+"/sources/projects", map[string]any{"kind": "issue", "ref": "ATH-999"}, 404, nil)
}
