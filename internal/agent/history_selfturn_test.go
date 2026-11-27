package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selfTurnLog is a Claude Code session as written when background sub-agents
// hand back after the turn that started them ended: Claude Code carries on by
// itself, with no prompt line of its own.
var selfTurnLog = []string{
	// a normal turn: thinking and text of the same message both say end_turn
	`{"type":"user","uuid":"p1","timestamp":"2026-11-27T10:00:00Z","message":{"role":"user","content":"start two agents"}}`,
	`{"type":"assistant","uuid":"a1","timestamp":"2026-11-27T10:00:05Z","message":{"id":"m1","content":[{"type":"tool_use","id":"toolu_1","name":"Agent","input":{"description":"one"}}],"stop_reason":"tool_use"}}`,
	`{"type":"user","uuid":"r1","timestamp":"2026-11-27T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"started"}]}}`,
	// a hand-back while the turn still runs belongs to that turn
	`{"type":"attachment","uuid":"h0","timestamp":"2026-11-27T10:00:07Z","attachment":{"type":"queued_command","prompt":"<agent-message from=\"x\">early hand-back</agent-message>","commandMode":"prompt"}}`,
	`{"type":"assistant","uuid":"a2","timestamp":"2026-11-27T10:00:10Z","message":{"id":"m2","content":[{"type":"thinking","thinking":"…"}],"stop_reason":"end_turn"}}`,
	`{"type":"assistant","uuid":"a3","timestamp":"2026-11-27T10:00:10Z","message":{"id":"m2","content":[{"type":"text","text":"Both agents are running."}],"stop_reason":"end_turn"}}`,
	`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-27T10:01:00Z"}`,
	// self-started after a task notification (a user entry nobody typed)
	`{"type":"user","uuid":"tn1","timestamp":"2026-11-27T10:01:00Z","message":{"role":"user","content":"<task-notification>\n<task-id>a1</task-id>\n</task-notification>"}}`,
	`{"type":"attachment","uuid":"tn1b","timestamp":"2026-11-27T10:01:00Z","attachment":{"type":"queued_command","prompt":"<task-notification>…</task-notification>","commandMode":"task-notification"}}`,
	`{"type":"assistant","uuid":"a4","timestamp":"2026-11-27T10:01:02Z","message":{"id":"m3","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"make deploy"}}],"stop_reason":"tool_use"}}`,
	`{"type":"user","uuid":"r2","timestamp":"2026-11-27T10:01:20Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"deployed"}]}}`,
	`{"type":"assistant","uuid":"a5","timestamp":"2026-11-27T10:01:30Z","message":{"id":"m4","content":[{"type":"text","text":"Agent one is deployed."}],"stop_reason":"end_turn"}}`,
	// self-started after a sub-agent hand-back (shown as its message)
	`{"type":"attachment","uuid":"hb2","timestamp":"2026-11-27T10:02:00Z","attachment":{"type":"queued_command","prompt":"<agent-message from=\"y\">[Subagent hand-back] done</agent-message>","commandMode":"prompt"}}`,
	`{"type":"assistant","uuid":"a6","timestamp":"2026-11-27T10:02:15Z","message":{"id":"m5","content":[{"type":"text","text":"Agent two is deployed."}],"stop_reason":"end_turn"}}`,
	// another session's message (a meta row), then a reply that ends at once
	`{"type":"user","uuid":"peer3","isMeta":true,"timestamp":"2026-11-27T10:03:00Z","message":{"role":"user","content":"Another Claude session sent a message: …"}}`,
	`{"type":"assistant","uuid":"a7","timestamp":"2026-11-27T10:03:00Z","message":{"id":"7f0e","model":"<synthetic>","content":[{"type":"text","text":"You've hit your limit."}],"stop_reason":"end_turn"}}`,
	// compaction between turns changes nothing
	`{"type":"system","subtype":"compact_boundary","uuid":"cb","timestamp":"2026-11-27T10:04:00Z","content":"Conversation compacted"}`,
	`{"type":"user","uuid":"sum","isCompactSummary":true,"timestamp":"2026-11-27T10:04:00Z","message":{"role":"user","content":"This session is being continued…"}}`,
	// a normal turn again
	`{"type":"user","uuid":"p2","timestamp":"2026-11-27T10:05:00Z","message":{"role":"user","content":"thanks, what next?"}}`,
	`{"type":"assistant","uuid":"a8","timestamp":"2026-11-27T10:05:04Z","message":{"id":"m6","content":[{"type":"text","text":"Nothing, all done."}],"stop_reason":"end_turn"}}`,
}

func writeSelfTurnLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(selfTurnLog, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every finished reply gets a footer (and with it a fork point), also the
// ones of turns the agent started by itself; a normal turn keeps its key.
func TestClaudeLogFootsSelfStartedTurns(t *testing.T) {
	items, _, _, err := ReadClaude(writeSelfTurnLog(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	durations := map[string]int64{}
	for _, it := range items {
		switch it.Kind {
		case "turn":
			got = append(got, it.Key)
			durations[it.Key] = it.Turn.DurationMS
		case "assistant", "user":
			got = append(got, it.Text)
		}
	}
	want := []string{
		"start two agents", `<agent-message from="x">early hand-back</agent-message>`, "Both agents are running.", "turn-p1",
		"Agent one is deployed.", "turn-tn1",
		`<agent-message from="y">[Subagent hand-back] done</agent-message>`, "Agent two is deployed.", "turn-hb2",
		"You've hit your limit.", "turn-peer3",
		"thanks, what next?", "Nothing, all done.", "turn-p2",
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("timeline:\n got %q\nwant %q", got, want)
	}
	for key, ms := range map[string]int64{"turn-p1": 10000, "turn-tn1": 30000, "turn-hb2": 15000, "turn-peer3": 0, "turn-p2": 4000} {
		if durations[key] != ms {
			t.Errorf("%s worked %d ms, want %d", key, durations[key], ms)
		}
	}
}

// Followed line by line (a live session), the same footers come out, once
// per turn end, and a turn's start is not a prompt (no TurnStarted).
func TestClaudeLogSelfStartedTurnsLineByLine(t *testing.T) {
	var log ClaudeLog
	var keys []string
	started := 0
	for _, l := range selfTurnLog {
		for _, op := range log.Feed([]byte(l)) {
			if op.Item != nil && op.Item.Kind == "turn" {
				keys = append(keys, op.Item.Key)
			}
			if op.TurnStarted {
				started++
			}
		}
	}
	if strings.Join(keys, ",") != "turn-p1,turn-tn1,turn-hb2,turn-peer3,turn-p2" || started != 2 {
		t.Fatalf("footers %v, prompts %d", keys, started)
	}
}

// A file that stops right after the thinking of its last reply still shows
// that reply's footer.
func TestClaudeLogFooterAfterThinkingAtTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := selfTurnLog[:5] // up to the thinking of the first reply, which ends the turn
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _, _, err := ReadClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	if last := items[len(items)-1]; last.Key != "turn-p1" || last.Turn.DurationMS != 10000 {
		t.Fatalf("last item %+v", last)
	}
}
