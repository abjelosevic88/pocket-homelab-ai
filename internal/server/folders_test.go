package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The folder picker: a missing folder lists its nearest parent; new folders
// are made inside the roots only.
func TestFolderPickerMissingAndMkdir(t *testing.T) {
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "admin@example.test", "admin", true)
	listing := func(code int, body []byte) dirListing {
		t.Helper()
		if code != 200 {
			t.Fatalf("got %d: %s", code, body)
		}
		var l dirListing
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}

	want := filepath.Join(root, "Projects", "app")
	l := listing(c.do("GET", "/api/v1/fs/dirs?path="+url.QueryEscape(want), "", nil))
	if l.Missing != want || l.Path != root {
		t.Fatalf("missing folder: want nearest %s with missing %s, got %+v", root, want, l)
	}
	l = listing(c.do("POST", "/api/v1/fs/mkdir", "application/json", []byte(`{"path":"`+want+`"}`)))
	if l.Path != want || l.Missing != "" {
		t.Fatalf("Create it: %+v", l)
	}
	l = listing(c.do("POST", "/api/v1/fs/mkdir", "application/json", []byte(`{"path":"`+want+`","name":"docs"}`)))
	if fi, err := os.Stat(filepath.Join(want, "docs")); err != nil || !fi.IsDir() || l.Path != filepath.Join(want, "docs") {
		t.Fatalf("new folder inside: %v %+v", err, l)
	}

	for _, body := range []string{
		`{"path":"` + want + `","name":"../../escape"}`,
		`{"path":"` + want + `","name":".."}`,
		`{"path":"/definitely-outside-the-roots/x"}`,
		`{"path":"` + root + `/../outside-sibling"}`,
	} {
		if code, out := c.do("POST", "/api/v1/fs/mkdir", "application/json", []byte(body)); code != 400 {
			t.Fatalf("%s: want 400, got %d %s", body, code, out)
		}
	}
	// a symlink out of the roots is outside too
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("POST", "/api/v1/fs/mkdir", "application/json", []byte(`{"path":"`+filepath.Join(root, "link-out", "x")+`"}`)); code != 400 {
		t.Fatalf("made a folder through a symlink out of the roots: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Fatal("folder appeared outside the roots")
	}

	// people who may not run agents may not make folders
	m, _ := person(t, s, h, "viewer@example.test", "member", false)
	if code, _ := m.do("POST", "/api/v1/fs/mkdir", "application/json", []byte(`{"path":"`+want+`","name":"nope"}`)); code != 403 {
		t.Fatalf("member without can_run made a folder: %d", code)
	}
}

func TestCloneInputs(t *testing.T) {
	for url, name := range map[string]string{
		"https://github.com/abjelosevic88/pocket-homelab-ai.git": "pocket-homelab-ai",
		"git@github.com:owner/repo.git":                          "repo",
		"ssh://git@git.example:2222/team/thing/":                 "thing",
	} {
		if !cloneURL.MatchString(url) || repoName(url) != name {
			t.Fatalf("%s: match=%v name=%q", url, cloneURL.MatchString(url), repoName(url))
		}
	}
	for _, url := range []string{"--upload-pack=touch /tmp/x", "/etc", "file:///etc", "https://x/y z", "ext::sh -c id"} {
		if cloneURL.MatchString(url) {
			t.Fatalf("accepted %q", url)
		}
	}
}
