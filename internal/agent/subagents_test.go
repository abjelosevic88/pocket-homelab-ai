package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListSubagents(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "s1.jsonl")
	sub := filepath.Join(dir, "s1", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// finished: it ended its turn with an answer
	write("agent-aaa.meta.json", `{"agentType":"fork","description":"Build the engine","model":"inherit"}`)
	write("agent-aaa.jsonl", `{"type":"user","isSidechain":true,"uuid":"u1","timestamp":"2026-10-25T10:00:00Z","message":{"role":"user","content":"build it"}}
{"type":"assistant","isSidechain":true,"uuid":"a1","timestamp":"2026-10-25T10:01:00Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go build ./..."}}],"usage":{"input_tokens":5,"cache_read_input_tokens":1000,"output_tokens":20}}}
{"type":"user","isSidechain":true,"uuid":"u2","timestamp":"2026-10-25T10:01:30Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"assistant","isSidechain":true,"uuid":"a2","timestamp":"2026-10-25T10:02:00Z","message":{"id":"m2","model":"claude-opus-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"The engine is built."}],"usage":{"input_tokens":2,"cache_read_input_tokens":2000,"output_tokens":30}}}
`)
	// running: a tool call with no result yet, written just now
	write("agent-bbb.meta.json", `{"agentType":"Explore","description":"Find the tests"}`)
	write("agent-bbb.jsonl", `{"type":"user","timestamp":"2026-10-25T10:05:00Z","message":{"role":"user","content":"find"}}
{"type":"assistant","timestamp":"2026-10-25T10:05:10Z","message":{"model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"t2","name":"Grep","input":{"pattern":"func Test"}}]}}
`)
	// stopped: quiet for an hour without finishing
	write("agent-ccc.meta.json", `{"description":"Old work"}`)
	write("agent-ccc.jsonl", `{"type":"user","timestamp":"2026-10-25T09:00:00Z","message":{"role":"user","content":"x"}}
`)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(sub, "agent-ccc.jsonl"), old, old)

	list := ListSubagents(session)
	if len(list) != 3 {
		t.Fatalf("want 3 sub-agents, got %#v", list)
	}
	byID := map[string]SubagentInfo{}
	for _, a := range list {
		byID[a.ID] = a
	}
	a := byID["aaa"]
	if a.Status != "done" || a.Model != "claude-opus-5-5" || a.ToolUses != 1 || a.Tokens != 2032 || a.Result != "The engine is built." || a.Description != "Build the engine" {
		t.Fatalf("finished one: %#v", a)
	}
	if b := byID["bbb"]; b.Status != "running" || b.Activity == "" || b.Result != "" {
		t.Fatalf("running one: %#v", b)
	}
	if c := byID["ccc"]; c.Status != "stopped" {
		t.Fatalf("quiet one: %#v", c)
	}
	if list[0].ID != "ccc" {
		t.Fatalf("not oldest first: %s", list[0].ID)
	}
	p, _ := SubagentPath(session, "aaa")
	items, err := ReadClaudeSubagent(p)
	if err != nil || len(items) < 3 {
		t.Fatalf("sub-agent transcript: %v %#v", err, items)
	}
	if _, err := SubagentPath(session, "../s1"); err == nil {
		t.Fatal("path escape accepted")
	}
	if p, err := SubagentPath(session, "aaa"); err != nil || filepath.Base(p) != "agent-aaa.jsonl" {
		t.Fatalf("SubagentPath: %q %v", p, err)
	}
}
