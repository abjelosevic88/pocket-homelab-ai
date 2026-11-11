package server

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
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
