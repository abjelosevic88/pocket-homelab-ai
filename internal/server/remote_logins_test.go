package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func runLoginSh(t *testing.T, home, script string, args ...string) string {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

// A conversation moved to another login on a machine finds its session there.
func TestRemoteEnsureSession(t *testing.T) {
	home := t.TempDir()
	sid := "95f90dfe-903a-432d-8a7c-ffb159144a91"
	src := filepath.Join(home, ".claude", "projects", "-home-arno-Projects-athena", sid+".jsonl")
	_ = os.MkdirAll(filepath.Join(filepath.Dir(src), sid, "subagents"), 0o755)
	_ = os.WriteFile(src, []byte("{}\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(home, ".claude-max", "projects"), 0o755)
	max := filepath.Join(home, ".claude-max")

	runLoginSh(t, home, remoteEnsureSession, "claude", max, sid)
	dst := filepath.Join(max, "projects", "-home-arno-Projects-athena", sid+".jsonl")
	if b, err := os.ReadFile(dst); err != nil || string(b) != "{}\n" {
		t.Fatalf("not copied to the login folder: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(max, "projects", "-home-arno-Projects-athena", sid, "subagents")); err != nil {
		t.Fatal("sub-agents not copied along")
	}
	// back to the default: there already, nothing to do; a new session id: nothing either
	_ = os.WriteFile(dst, []byte("{}\n{}\n"), 0o600)
	runLoginSh(t, home, remoteEnsureSession, "claude", "", sid)
	if b, _ := os.ReadFile(src); string(b) != "{}\n" {
		t.Fatal("the default copy was overwritten although it was there")
	}
	runLoginSh(t, home, remoteEnsureSession, "claude", max, "00000000-0000-0000-0000-000000000000")

	// codex rollouts keep their dated path
	roll := filepath.Join(home, ".codex", "sessions", "2026", "10", "29", "rollout-2026-10-29T13-00-00-"+sid+".jsonl")
	_ = os.MkdirAll(filepath.Dir(roll), 0o755)
	_ = os.WriteFile(roll, []byte("{}\n"), 0o600)
	pro := filepath.Join(home, ".codex-pro")
	_ = os.MkdirAll(pro, 0o755)
	_ = os.WriteFile(filepath.Join(pro, "auth.json"), []byte("{}"), 0o600)
	runLoginSh(t, home, remoteEnsureSession, "codex", pro, sid)
	if _, err := os.Stat(filepath.Join(pro, "sessions", "2026", "10", "29", filepath.Base(roll))); err != nil {
		t.Fatal("codex session not copied:", err)
	}
}

func TestInLoginPointsScriptsAtTheFolder(t *testing.T) {
	home := t.TempDir()
	if got := runLoginSh(t, home, inLogin("claude", "/home/arno/.claude-max", `printf %s "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"`)); got != "/home/arno/.claude-max" {
		t.Fatalf("got %q", got)
	}
	if got := runLoginSh(t, home, inLogin("claude", "", `printf %s "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"`)); got != home+"/.claude" {
		t.Fatalf("default: got %q", got)
	}
	if got := runLoginSh(t, home, inLogin("codex", "/x/.codex-pro", `printf %s "$CODEX_HOME"`)); got != "/x/.codex-pro" {
		t.Fatalf("codex: got %q", got)
	}
}

// A conversation on a machine switches to another login folder there, and
// only to one: no other paths.
func TestSwitchMachineLogin(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	owner, u := person(t, s, h, "switch-login@example.test", "admin", true)
	box := &store.Host{Name: "AB-128box", Target: "box.invalid", Info: store.HostInfo{OK: true, Home: "/home/arno"}}
	if err := s.store.CreateHost(ctx, box); err != nil {
		t.Fatal(err)
	}
	th := &store.Thread{ID: "t-switch-login", UserID: u.ID, Provider: "claude", HostID: box.ID, Cwd: "/home/arno/Projects/athena", LogOffset: -1}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	var got store.Thread
	owner.want("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"host_home": "/home/arno/.claude-max"}, 200, &got)
	if got.HostHome != "/home/arno/.claude-max" {
		t.Fatalf("host_home = %q", got.HostHome)
	}
	for _, bad := range []string{"/home/arno/.ssh", "/etc/.claude-x", "/home/arno/.codex-pro", "/home/arno/.claude-max/../.ssh"} {
		if code, body := owner.call("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"host_home": bad}); code != 400 {
			t.Fatalf("%s accepted: %d %s", bad, code, body)
		}
	}
	owner.want("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"host_home": ""}, 200, &got)
	if got.HostHome != "" {
		t.Fatalf("back to the default: %q", got.HostHome)
	}
	// a conversation on this server has no machine logins
	local := &store.Thread{ID: "t-local", UserID: u.ID, Provider: "claude", Cwd: "/tmp", LogOffset: -1}
	_ = s.store.CreateThread(ctx, local)
	if code, _ := owner.call("PATCH", "/api/v1/threads/"+local.ID, map[string]any{"host_home": "/home/arno/.claude-max"}); code != 400 {
		t.Fatalf("local thread took a machine login: %d", code)
	}
}
