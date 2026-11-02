package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func TestSentFilesAreServedFromACopy(t *testing.T) {
	s, h, root := testServer(t)
	alice, au := person(t, s, h, "alice@example.com", "admin", false)
	bob, _ := person(t, s, h, "bob@example.com", "admin", false)
	th := thread(t, s, au.ID, root)

	page := filepath.Join(root, "out", "decisions.html")
	_ = os.MkdirAll(filepath.Dir(page), 0o755)
	if err := os.WriteFile(page, []byte("<h1>Decisions</h1><script>fetch('/api/v1/me')</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := agent.Item{Key: "toolu_send", Kind: "tool", Tool: &agent.Tool{Name: "SendUserFile", Kind: "file", Status: "done",
		Files: []agent.SentFile{{Name: "decisions.html", Path: page, Size: 55, Mime: "text/html"}, {Name: "notes.md", Path: "notes.md"}}}}
	data, _ := json.Marshal(it)
	if _, err := s.store.UpsertItem(context.Background(), th.ID, it.Key, it.Kind, data, ""); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "notes.md"), []byte("# notes"), 0o644) // relative to the thread's folder

	base := "/api/v1/threads/" + th.ID + "/sent-file?key=toolu_send"
	get := func(c *client, q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", base+q, nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := get(alice, "&n=0")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Decisions") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("a sent page must run fenced off: %q", csp)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("type %q", ct)
	}
	// the agent deletes it afterwards: Pocket AI still has its copy
	_ = os.Remove(page)
	if w = get(alice, "&n=0&download=1"); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("copy: %d %v", w.Code, w.Header())
	}
	if w = get(alice, "&n=1"); w.Code != 200 || w.Body.String() != "# notes" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("relative markdown: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w = get(alice, "&n=2"); w.Code != 404 {
		t.Fatalf("no such file: %d", w.Code)
	}
	if w = get(bob, "&n=0"); w.Code != 404 {
		t.Fatalf("someone else's thread: %d", w.Code)
	}
	// deleting the thread deletes the copies
	alice.want("DELETE", "/api/v1/threads/"+th.ID, nil, 204, nil)
	if _, err := os.Stat(s.sentDir(th.ID)); !os.IsNotExist(err) {
		t.Fatalf("copies left behind: %v", err)
	}
}

func TestSentTypes(t *testing.T) {
	for _, c := range []struct {
		f      agent.SentFile
		ctype  string
		active bool
	}{
		{agent.SentFile{Name: "a.svg"}, "image/svg+xml", true},
		{agent.SentFile{Name: "a.png", Mime: "image/png"}, "image/png", false},
		{agent.SentFile{Name: "a.pdf"}, "application/pdf", false},
		{agent.SentFile{Name: "a.js", Mime: "text/javascript"}, "text/plain; charset=utf-8", false},
		{agent.SentFile{Name: "a.xhtml"}, "text/plain; charset=utf-8", false},
		{agent.SentFile{Name: "a.go"}, "text/plain; charset=utf-8", false},
		{agent.SentFile{Name: "blob"}, "application/octet-stream", false},
	} {
		if ct, active := sentType(c.f); ct != c.ctype || active != c.active {
			t.Errorf("%s: %q %v", c.f.Name, ct, active)
		}
	}
}
