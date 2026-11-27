package server

import (
	"context"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// POCKET_SHARE_LIVE=1: real Claude Code through the proxy to Anthropic on
// the real server login (read only).
func TestSharedLoginLive(t *testing.T) {
	if os.Getenv("POCKET_SHARE_LIVE") == "" {
		t.Skip("live")
	}
	real := os.Getenv("REAL_CLAUDE_HOME")
	s, h, _ := testServer(t)
	admin, _ := person(t, s, h, "owner@example.com", "admin", false)
	_, mu := person(t, s, h, "kid@example.com", "member", false)
	_ = os.MkdirAll(s.cfg.ClaudeHome, 0o700)
	if err := os.Symlink(filepath.Join(real, ".credentials.json"), filepath.Join(s.cfg.ClaudeHome, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	var row shareRow
	admin.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "from": 0, "minutes_day": 60}, 201, &row)
	a, _ := s.store.GetAccount(context.Background(), row.AccountID)
	_, env := s.accountHome(context.Background(), "claude", a.ID)
	cmd := exec.Command(os.Getenv("CLAUDE"), "-p", "Reply with just the word pong", "--model", "haiku")
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	t.Logf("claude: %s (err %v)", out, err)
	if !strings.Contains(strings.ToLower(string(out)), "pong") {
		t.Fatal("no answer through the proxy")
	}
	time.Sleep(200 * time.Millisecond)
	u, _ := s.store.ShareUsageSince(context.Background(), a.ID, time.Now().Add(-time.Hour))
	five, week, reset, known := s.lentPlan("claude", 0)
	t.Logf("usage %+v plan five=%v week=%v reset=%v known=%v", u, five, week, reset, known)
	if u.Requests == 0 || u.OutputTokens == 0 {
		t.Fatal("nothing counted")
	}
}

// POCKET_SHARE_LIVE=1 REAL_CODEX_HOME=~/.codex CODEX=…: real Codex through the proxy.
func TestSharedCodexLive(t *testing.T) {
	if os.Getenv("POCKET_SHARE_LIVE") == "" || os.Getenv("REAL_CODEX_HOME") == "" {
		t.Skip("live")
	}
	s, h, _ := testServer(t)
	admin, _ := person(t, s, h, "owner@example.com", "admin", false)
	_, mu := person(t, s, h, "kid@example.com", "member", false)
	_ = os.MkdirAll(s.cfg.CodexHome, 0o700)
	if err := os.Symlink(filepath.Join(os.Getenv("REAL_CODEX_HOME"), "auth.json"), filepath.Join(s.cfg.CodexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	var row shareRow
	admin.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "provider": "codex", "from": 0, "minutes_day": 60}, 201, &row)
	a, _ := s.store.GetAccount(context.Background(), row.AccountID)
	_, env := s.accountHome(context.Background(), "codex", a.ID)
	dir := t.TempDir()
	cmd := exec.Command(os.Getenv("CODEX"), "exec", "--skip-git-repo-check", "-C", dir, "Reply with just the word pong")
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	t.Logf("codex: %s (err %v)", out, err)
	if !strings.Contains(strings.ToLower(string(out)), "pong") {
		t.Fatal("no answer through the proxy")
	}
	u, _ := s.store.ShareUsageSince(context.Background(), a.ID, time.Now().Add(-time.Hour))
	five, week, reset, known := s.lentPlan("codex", 0)
	t.Logf("usage %+v plan five=%v week=%v reset=%v known=%v", u, five, week, reset, known)
	if u.Requests == 0 || u.OutputTokens == 0 {
		t.Fatal("nothing counted")
	}
	// paused: what Codex shows
	admin.want("PATCH", "/api/v1/ai-shares/"+strconv.FormatInt(a.ID, 10), map[string]any{"paused": true}, 200, nil)
	cmd = exec.Command(os.Getenv("CODEX"), "exec", "--skip-git-repo-check", "-C", dir, "Reply with just the word pong")
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	start := time.Now()
	out, _ = cmd.CombinedOutput()
	t.Logf("paused codex (%s): %s", time.Since(start).Round(time.Second), out)
}

// POCKET_SHARE_LIVE=1 REAL_HOME=~ : the other agents for a member, in their
// jail, through the relay, on the owner's real sign-in (Antigravity) and
// OpenCode's free models; Gemini CLI with a made-up key shows the request
// reaching Google with the owner's key, not the share key.
func TestSharedAgentsLive(t *testing.T) {
	if os.Getenv("POCKET_SHARE_LIVE") == "" || os.Getenv("REAL_HOME") == "" {
		t.Skip("live")
	}
	real := os.Getenv("REAL_HOME")
	s, h, root := testServer(t)
	for _, id := range []string{"ANTIGRAVITY", "GEMINI", "OPENCODE", "QWEN"} {
		t.Setenv(id+"_BIN", "") // the real ones
	}
	admin, _ := person(t, s, h, "owner@example.com", "admin", false)
	_, mu := person(t, s, h, "kid@example.com", "member", false)
	ctx := context.Background()
	// the owner's Antigravity sign-in: its default login here is <AGENT_HOME>/.gemini
	if err := os.MkdirAll(filepath.Join(root, ".gemini", "antigravity-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, ".gemini", "antigravity-cli", "antigravity-oauth-token"), filepath.Join(root, ".gemini", "antigravity-cli", "antigravity-oauth-token")); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetACPEnv(ctx, "gemini", map[string]string{"GEMINI_API_KEY": "AIzaFAKE-owner-key"}); err != nil {
		t.Fatal(err)
	}
	run := func(provider string, args ...string) string {
		var row shareRow
		admin.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "provider": provider, "from": 0, "minutes_day": 60}, 201, &row)
		a, _ := s.store.GetAccount(ctx, row.AccountID)
		_, env := s.accountHome(ctx, provider, a.ID)
		if strings.Contains(strings.Join(env, " "), "AIzaFAKE") {
			t.Fatalf("%s: the owner's key is in the member's environment", provider)
		}
		if provider == "antigravity" { // print mode reads ~/.gemini only
			_ = os.Symlink(a.Home, filepath.Join(s.personHome(mu.ID), ".gemini"))
		}
		dir := filepath.Join(s.personProjects(mu.ID), provider)
		_ = os.MkdirAll(dir, 0o700)
		cmd, err := agent.LocalCmd(nil, dir, append(env, "PATH="+os.Getenv("PATH")), args[0], args[1:]...)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-time.After(150 * time.Second):
				_ = cmd.Process.Kill()
			}
		}()
		out, err := cmd.CombinedOutput()
		close(done)
		u, _ := s.store.ShareUsageSince(ctx, a.ID, time.Now().Add(-time.Hour))
		t.Logf("%s (err %v): %s\nusage %+v", provider, err, tailLines(string(out), 6), u)
		return string(out)
	}
	if out := run("antigravity", "agy", "-p", "Reply with just the word pong"); !strings.Contains(strings.ToLower(out), "pong") {
		t.Error("Antigravity did not answer through the relay")
	}
	run("opencode", "opencode", "run", "-m", "opencode/big-pickle", "Reply with just the word pong")
	if out := run("gemini", "gemini", "-p", "Reply with just the word pong", "--skip-trust"); !strings.Contains(out, "API key not valid") && !strings.Contains(out, "API_KEY_INVALID") {
		t.Error("Gemini CLI's request did not reach Google with the owner's key")
	}
}

func tailLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}
