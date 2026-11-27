package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	five, week, reset, known := s.lentPlan(0)
	t.Logf("usage %+v plan five=%v week=%v reset=%v known=%v", u, five, week, reset, known)
	if u.Requests == 0 || u.OutputTokens == 0 {
		t.Fatal("nothing counted")
	}
}
