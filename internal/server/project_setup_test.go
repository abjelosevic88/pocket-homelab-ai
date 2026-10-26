package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestGitProjectID(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:Athena-Studio/athena.git":            "github.com/athena-studio/athena",
		"https://github.com/athena-studio/athena":            "github.com/athena-studio/athena",
		"https://user:token@github.com/athena-studio/athena": "github.com/athena-studio/athena",
		"ssh://git@git.bjelke.org:2222/Homelab/app.git":      "git.bjelke.org:2222/Homelab/app",
		"ssh://git@git.bjelke.org:22/Homelab/app.git":        "git.bjelke.org/Homelab/app",
		"/srv/git/app.git":        "",
		"file:///srv/git/app.git": "",
		"":                        "",
	} {
		if got := gitProjectID(remote); got != want {
			t.Errorf("%q: got %q, want %q", remote, got, want)
		}
	}
}

// One worktree setup per project: saved from one checkout, used by every
// checkout of the same remote; a machine's own setup wins for that machine.
func TestWorktreeSetupPerProject(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "admin@example.test", "admin", true)
	mk := func(name, origin string) string {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(dir, 0o755)
		gitT(t, dir, "init", "-q", "-b", "main")
		_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "init")
		if origin != "" {
			gitT(t, dir, "remote", "add", "origin", origin)
		}
		return dir
	}
	here := mk("athena", "git@github.com:athena-studio/athena.git")
	there := mk("checkout-elsewhere", "https://github.com/Athena-Studio/athena")
	lonely := mk("no-remote", "")

	type view struct {
		ProjectID string `json:"project_id"`
		Config    struct {
			Project string    `json:"project"`
			Scope   string    `json:"scope"`
			Saved   *wtConfig `json:"saved"`
		} `json:"config"`
	}
	get := func(dir string) view {
		t.Helper()
		code, body := c.do("GET", "/api/v1/worktrees/config?path="+url.QueryEscape(dir), "", nil)
		if code != 200 {
			t.Fatalf("get %s: %d %s", dir, code, body)
		}
		var v view
		_ = json.Unmarshal(body, &v)
		return v
	}
	put := func(dir, scope, setup string) (int, []byte) {
		b, _ := json.Marshal(map[string]any{"path": dir, "scope": scope, "saved": map[string]any{"setup": []string{setup}}})
		return c.do("PUT", "/api/v1/worktrees/config", "application/json", b)
	}

	if v := get(here); v.Config.Project != "github.com/athena-studio/athena" || v.ProjectID != v.Config.Project || v.Config.Saved != nil {
		t.Fatalf("fresh repo: %+v", v)
	}
	if code, body := put(here, "", "pnpm install"); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	if v := get(there); v.Config.Scope != "project" || v.Config.Saved == nil || v.Config.Saved.Setup[0] != "pnpm install" {
		t.Fatalf("the other checkout should use the project's setup: %+v", v.Config)
	}
	// a machine of its own wins there, and leaves the project alone
	if code, body := put(there, "machine", "make dev"); code != 200 {
		t.Fatalf("save machine: %d %s", code, body)
	}
	if v := get(there); v.Config.Scope != "machine" || v.Config.Saved.Setup[0] != "make dev" {
		t.Fatalf("machine setup should win: %+v", v.Config)
	}
	if v := get(here); v.Config.Scope != "project" || v.Config.Saved.Setup[0] != "pnpm install" {
		t.Fatalf("the project's setup changed: %+v", v.Config)
	}
	// saving for every machine from there drops its own setup
	if code, body := put(there, "project", "pnpm i"); code != 200 {
		t.Fatalf("save project: %d %s", code, body)
	}
	for _, dir := range []string{here, there} {
		if v := get(dir); v.Config.Scope != "project" || v.Config.Saved.Setup[0] != "pnpm i" {
			t.Fatalf("%s: %+v", dir, v.Config)
		}
	}
	// no remote: this machine only
	if v := get(lonely); v.Config.Project != "" {
		t.Fatalf("no remote has no project: %+v", v)
	}
	if code, _ := put(lonely, "project", "x"); code != 400 {
		t.Fatalf("project setup without a remote: %d", code)
	}
	if code, body := put(lonely, "", "npm ci"); code != 200 {
		t.Fatalf("save without remote: %d %s", code, body)
	}
	if v := get(lonely); v.Config.Scope != "machine" || v.Config.Saved.Setup[0] != "npm ci" {
		t.Fatalf("no remote: %+v", v.Config)
	}
}
