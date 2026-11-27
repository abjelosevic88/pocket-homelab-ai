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
	s.rt.claudeTick(t.Context(), fresh, logPath, &agent.ClaudeLog{}, new(time.Time), false)
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

// With Claude Code's process registry, a quiet log does not end "running
// elsewhere": an agent that only waits for its background sub-agents writes
// nothing for minutes. Clearing it there, for the registry check to set it
// again a moment later, flipped the page between working and idle every
// second. Without the registry the quiet log is still the only sign.
func TestFollowerQuietLogKeepsExternalWithRegistry(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "quiet@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	logPath := filepath.Join(s.defaultHome("claude"), "projects", "-work", th.ProviderSessionID+".jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	if err := os.WriteFile(logPath, []byte(`{"type":"user","uuid":"q1","message":{"content":"go"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	th.LogOffset = agent.FileSize(logPath) // all read: the log is quiet
	if err := s.store.UpdateThread(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.store.GetThreadAny(t.Context(), th.ID)
	quietSince := time.Now().Add(-10 * time.Minute)

	s.rt.setExternal(th.ID, true)
	s.rt.claudeTick(t.Context(), fresh, logPath, &agent.ClaudeLog{}, &quietSince, true)
	if !s.rt.isExternal(th.ID) {
		t.Fatal("a quiet log ended running elsewhere although the registry decides")
	}
	s.rt.claudeTick(t.Context(), fresh, logPath, &agent.ClaudeLog{}, &quietSince, false)
	if s.rt.isExternal(th.ID) {
		t.Fatal("without the registry a long quiet log must end running elsewhere")
	}
}
