package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// A message sent right as Pocket AI restarts reaches the log only after the
// new process took over: the follower must not show it a second time, but a
// prompt repeated after a reply is a new message.
func TestFollowerDoesNotRepeatAPromptSentDuringRestart(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "restart@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	sent := agent.Item{Key: "u-pocket", Kind: "user", Text: "open the playlist"}
	data, _ := json.Marshal(sent)
	if _, err := s.store.UpsertItem(t.Context(), th.ID, sent.Key, sent.Kind, data, sent.Text); err != nil {
		t.Fatal(err)
	}
	th.LogOffset = 0
	if err := s.store.UpdateThread(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.defaultHome("claude"), "projects", "-work", th.ProviderSessionID+".jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	var b strings.Builder
	for _, row := range []map[string]any{
		{"type": "user", "uuid": "log-1", "message": map[string]any{"content": "open the playlist\nNo saved preferences are currently enabled. Earlier preferences may have been forgotten; do not rely on them.\n"}},
		{"type": "assistant", "uuid": "log-2", "message": map[string]any{"id": "m1", "content": []map[string]any{{"type": "text", "text": "Opened."}}, "stop_reason": "end_turn"}},
		{"type": "user", "uuid": "log-3", "message": map[string]any{"content": "open the playlist\nNo saved preferences are currently enabled. Earlier preferences may have been forgotten; do not rely on them.\n"}},
	} {
		line, _ := json.Marshal(row)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.store.GetThreadAny(t.Context(), th.ID)
	s.rt.claudeTick(t.Context(), fresh, logPath, &agent.ClaudeLog{}, new(time.Time))
	items, _ := s.store.ListItems(t.Context(), th.ID)
	var prompts []string
	for _, it := range items {
		if it.Kind == "user" {
			prompts = append(prompts, it.Key)
		}
	}
	if strings.Join(prompts, ",") != "u1,u-pocket,u-log-3" {
		t.Fatalf("prompts: %v", prompts)
	}
}
