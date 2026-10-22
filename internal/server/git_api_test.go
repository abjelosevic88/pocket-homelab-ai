package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestGitBranchSwitch(t *testing.T) {
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "a@example.com", "admin", false)
	ctx := context.Background()
	repo := filepath.Join(root, "repo")
	plain := filepath.Join(root, "plain")
	for _, d := range []string{repo, plain} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if _, err := runGit(ctx, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "first")
	git(repo, "branch", "feature")
	// a fake remote branch with no local twin
	git(repo, "remote", "add", "origin", plain)
	git(repo, "update-ref", "refs/remotes/origin/hotfix", "HEAD")
	git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")

	var info gitInfo
	c.want("GET", "/api/v1/fs/git?path="+url.QueryEscape(plain), nil, 200, &info)
	if info.Repo {
		t.Fatal("plain folder reported as a repo")
	}
	c.want("GET", "/api/v1/fs/git?path="+url.QueryEscape(repo), nil, 200, &info)
	if !info.Repo || info.Branch != "main" || info.Detached {
		t.Fatalf("info: %+v", info)
	}
	names := map[string]gitBranch{}
	for _, b := range info.Branches {
		names[b.Name] = b
	}
	if !names["main"].Current || names["feature"].Name == "" || !names["origin/hotfix"].Remote {
		t.Fatalf("branches: %+v", info.Branches)
	}
	if _, dup := names["origin/main"]; dup {
		t.Error("origin/main listed although local main exists")
	}

	c.want("POST", "/api/v1/fs/git/switch", map[string]any{"path": repo, "branch": "feature"}, 200, &info)
	if info.Branch != "feature" {
		t.Fatalf("after switch: %+v", info)
	}
	c.want("POST", "/api/v1/fs/git/switch", map[string]any{"path": repo, "branch": "new-thing", "create": true}, 200, &info)
	if info.Branch != "new-thing" {
		t.Fatalf("after create: %+v", info)
	}
	c.want("POST", "/api/v1/fs/git/switch", map[string]any{"path": repo, "branch": "origin/hotfix", "remote": true}, 200, &info)
	if info.Branch != "hotfix" {
		t.Fatalf("after remote: %+v", info)
	}
	for _, body := range []map[string]any{
		{"path": repo, "branch": "nope"},
		{"path": repo, "branch": "--orphan"},
		{"path": repo, "branch": "bad..name", "create": true},
		{"path": "/etc", "branch": "main"},
	} {
		if code, b := c.call("POST", "/api/v1/fs/git/switch", body); code != http.StatusBadRequest {
			t.Errorf("%v: got %d (%s)", body, code, b)
		}
	}
}
