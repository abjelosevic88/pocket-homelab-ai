package server

import (
	"context"
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
