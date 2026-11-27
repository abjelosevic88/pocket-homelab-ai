package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A session where the agent carried on by itself twice after its turn ended
// (a task notification, then a sub-agent hand-back), then a new prompt.
var selfTurnSession = []string{
	`{"type":"user","uuid":"p1","timestamp":"2026-11-27T10:00:00Z","message":{"role":"user","content":"start the agents"}}`,
	`{"type":"assistant","uuid":"a1","timestamp":"2026-11-27T10:00:10Z","message":{"id":"m1","content":[{"type":"thinking","thinking":"…"}],"stop_reason":"end_turn"}}`,
	`{"type":"assistant","uuid":"a2","timestamp":"2026-11-27T10:00:10Z","message":{"id":"m1","content":[{"type":"text","text":"REPLY_ONE agents running."}],"stop_reason":"end_turn"}}`,
	`{"type":"user","uuid":"tn1","timestamp":"2026-11-27T10:01:00Z","message":{"role":"user","content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
	`{"type":"assistant","uuid":"a3","timestamp":"2026-11-27T10:01:30Z","message":{"id":"m2","content":[{"type":"text","text":"REPLY_TWO first agent done."}],"stop_reason":"end_turn"}}`,
	`{"type":"attachment","uuid":"hb2","timestamp":"2026-11-27T10:02:00Z","attachment":{"type":"queued_command","prompt":"<agent-message from=\"y\">[Subagent hand-back] done</agent-message>","commandMode":"prompt"}}`,
	`{"type":"assistant","uuid":"a4","timestamp":"2026-11-27T10:02:15Z","message":{"id":"m3","content":[{"type":"text","text":"REPLY_THREE second agent done."}],"stop_reason":"end_turn"}}`,
	`{"type":"user","uuid":"p2","timestamp":"2026-11-27T10:05:00Z","message":{"role":"user","content":"LATER_PROMPT"}}`,
	`{"type":"assistant","uuid":"a5","timestamp":"2026-11-27T10:05:04Z","message":{"id":"m4","content":[{"type":"text","text":"LATER_REPLY"}],"stop_reason":"end_turn"}}`,
}

// "Fork from here" on the footer of a turn the agent started by itself takes
// the conversation up to that reply, and nothing after it.
func TestForkFromSelfStartedTurn(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "selfturn@example.test", "admin", true)
	ctx := t.Context()
	parent := thread(t, s, u.ID, root)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(selfTurnSession, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _, _, err := agent.ReadClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(ctx, parent.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		after    string
		has, not []string
	}{
		{"turn-p1", []string{"start the agents", "REPLY_ONE"}, []string{"REPLY_TWO", "REPLY_THREE", "LATER_"}},
		{"turn-tn1", []string{"REPLY_ONE", "REPLY_TWO"}, []string{"REPLY_THREE", "LATER_"}},
		{"turn-hb2", []string{"REPLY_TWO", "[Subagent hand-back] done", "REPLY_THREE"}, []string{"LATER_"}},
		{"turn-p2", []string{"REPLY_THREE", "LATER_PROMPT", "LATER_REPLY"}, nil},
	} {
		var fork store.Thread
		owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", map[string]any{"after": c.after}, 201, &fork)
		history := historyOfFork(t, s, fork).ExtractedText
		for _, want := range c.has {
			if !strings.Contains(history, want) {
				t.Errorf("fork after %s: missing %q", c.after, want)
			}
		}
		for _, bad := range c.not {
			if strings.Contains(history, bad) {
				t.Errorf("fork after %s: has %q from later", c.after, bad)
			}
		}
	}
}

// A restart in the middle of such a stretch: the catch-up from the last
// prompt does not repeat the footer Pocket AI wrote for that turn itself, and
// each turn the agent started by itself afterwards gets its own.
func TestLeftoverCatchUpFootsSelfStartedTurns(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "selfcatchup@example.test", "admin", true)
	th := cutThread(t, s, u.ID, root)
	// the turn of "start the agents" ended live before the restart
	for _, it := range []agent.Item{{Key: "u-sent", Kind: "user", Text: "start the agents"}, {Key: "c-msg_1-0", Kind: "assistant", Text: "REPLY_ONE agents running."},
		{Key: "turn-live", Kind: "turn", Turn: &agent.TurnInfo{OK: true}}} {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(context.Background(), th.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	ap := s.newLeftoverApplier(context.Background(), th)
	log := strings.Join(selfTurnSession[:7], "\n") + "\n"
	ap.catchUp([]byte(log), 0)
	var got []string
	for _, it := range threadItems(t, s, th.ID) {
		if it.Kind == "turn" {
			got = append(got, it.Key)
		}
	}
	if strings.Join(got, ",") != "turn-live,turn-tn1,turn-hb2" {
		t.Fatalf("footers %v", got)
	}
	if n := countText(threadItems(t, s, th.ID), "assistant", "REPLY_ONE agents running."); n != 1 {
		t.Fatalf("first reply %d times", n)
	}
}
