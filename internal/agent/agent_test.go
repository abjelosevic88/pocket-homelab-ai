package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rec struct {
	mu      sync.Mutex
	items   map[string]Item
	order   []string
	deltas  string
	session string
	reqs    []Request
	turns   []TurnInfo
	exited  chan error
}

func newRec() *rec { return &rec{items: map[string]Item{}, exited: make(chan error, 1)} }

func (r *rec) Item(it Item) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[it.Key]; !ok {
		r.order = append(r.order, it.Key)
	}
	r.items[it.Key] = it
}
func (r *rec) Delta(key, field, text string) { r.mu.Lock(); r.deltas += text; r.mu.Unlock() }
func (r *rec) SessionID(id string)           { r.mu.Lock(); r.session = id; r.mu.Unlock() }
func (r *rec) Context(Context)               {}
func (r *rec) Limits(string, []Window)       {}
func (r *rec) Request(q Request)             { r.mu.Lock(); r.reqs = append(r.reqs, q); r.mu.Unlock() }
func (r *rec) TurnStarted()                  {}
func (r *rec) TurnDone(t TurnInfo)           { r.mu.Lock(); r.turns = append(r.turns, t); r.mu.Unlock() }
func (r *rec) Exited(err error)              { r.exited <- err }

func (r *rec) wait(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// fakeClaude replays what Claude Code 2.1 prints for "write a file": a
// streamed text block, a Write permission request, the tool result, the result.
const fakeClaude = `#!/bin/sh
read init
read msg
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1","cwd":"/tmp"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Writ"}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ing it."}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Writing it."}],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"output_tokens":3}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Write","input":{"file_path":"/tmp/x.txt","content":"hi\n"}}]},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Write","input":{"file_path":"/tmp/x.txt","content":"hi\n"},"permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}],"tool_use_id":"tu1"}}'
read answer
printf "%s\n" "$answer" > "$ANSWER_FILE"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"File created","is_error":false}]},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":1200,"total_cost_usd":0.02,"usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":0},"modelUsage":{"m":{"contextWindow":200000}}}'
read more
`

func TestClaudeDriver(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	answer := filepath.Join(dir, "answer.json")
	t.Setenv("ANSWER_FILE", answer)
	r := newRec()
	s, err := StartClaude(context.Background(), Options{Bin: bin, Cwd: dir, Mode: "default"}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Input{Text: "write it"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.reqs) == 1 })
	if q := r.reqs[0]; q.Type != "permission" || q.Title != "Write x.txt" || !q.CanAllowSession {
		t.Fatalf("request: %+v", q)
	}
	if err := s.Respond("r1", Response{Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 1 })
	_ = s.Close()
	<-r.exited

	if r.session != "sess-1" || r.deltas != "Writing it." {
		t.Fatalf("session %q deltas %q", r.session, r.deltas)
	}
	// the streamed block and the final message share one key
	if n := len(r.order); n != 2 {
		t.Fatalf("items %v", r.order)
	}
	if it := r.items[r.order[0]]; it.Kind != "assistant" || it.Text != "Writing it." {
		t.Fatalf("assistant item %+v", it)
	}
	tool := r.items["tu1"].Tool
	if tool == nil || tool.Status != "done" || tool.Output != "File created" || !strings.Contains(tool.Diff, "+hi") {
		t.Fatalf("tool %+v", tool)
	}
	if tt := r.turns[0]; !tt.OK || tt.CostUSD != 0.02 || tt.DurationMS != 1200 {
		t.Fatalf("turn %+v", tt)
	}
	b, _ := os.ReadFile(answer)
	var resp struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior           string          `json:"behavior"`
				UpdatedPermissions json.RawMessage `json:"updatedPermissions"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("answer %s: %v", b, err)
	}
	if resp.Response.RequestID != "r1" || resp.Response.Response.Behavior != "allow" || len(resp.Response.Response.UpdatedPermissions) == 0 {
		t.Fatalf("answer %s", b)
	}
}

func TestCodexItems(t *testing.T) {
	cases := []struct {
		raw   string
		kind  string
		title string
		stat  string
	}{
		{`{"type":"commandExecution","id":"c1","command":"/bin/zsh -lc 'ls'","commandActions":[{"type":"listFiles","command":"ls","path":"."}],"status":"completed","exitCode":0,"aggregatedOutput":"a\nb"}`, "tool", "List .", "done"},
		{`{"type":"commandExecution","id":"c2","command":"false","commandActions":[{"type":"unknown","command":"false"}],"status":"completed","exitCode":1}`, "tool", "false", "error"},
		{`{"type":"fileChange","id":"f1","status":"declined","changes":[{"path":"/x/a.go","kind":{"type":"update"},"diff":"@@ -1 +1 @@\n-a\n+b"}]}`, "tool", "Edit a.go", "denied"},
		{`{"type":"agentMessage","id":"m1","text":"done"}`, "assistant", "", ""},
		{`{"type":"reasoning","id":"r1","summary":["thinking"]}`, "thinking", "", ""},
	}
	for _, c := range cases {
		it, ok := CodexItem(json.RawMessage(c.raw))
		if !ok || it.Kind != c.kind {
			t.Fatalf("%s: %+v", c.raw, it)
		}
		if it.Tool != nil && (it.Tool.Title != c.title || it.Tool.Status != c.stat) {
			t.Errorf("%s: title %q status %q", c.raw, it.Tool.Title, it.Tool.Status)
		}
	}
}

func TestReadClaudeHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"type":"queue-operation","operation":"enqueue"}`,
		`{"type":"user","uuid":"u0","isMeta":true,"cwd":"/home/me","message":{"role":"user","content":"<local-command-caveat>…"}}`,
		`{"type":"user","uuid":"u1","cwd":"/home/me","message":{"role":"user","content":[{"type":"text","text":"is jellyfin up?"}]}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"m","content":[{"type":"thinking","thinking":"…"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"systemctl --user status jellyfin"}}]}}`,
		`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"active (running)"}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"m2","content":[{"type":"text","text":"Yes, it is running."}]}}`,
		`{"type":"assistant","uuid":"a3","isSidechain":true,"message":{"id":"m3","content":[{"type":"text","text":"sub-agent chatter"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	items, cwd, _, err := ReadClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	if cwd != "/home/me" || len(items) != 3 {
		t.Fatalf("cwd %q items %+v", cwd, items)
	}
	if items[0].Text != "is jellyfin up?" || items[1].Tool.Output != "active (running)" || items[2].Text != "Yes, it is running." {
		t.Fatalf("items %+v", items)
	}
}

func TestClaudeTailFollowsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(s string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		f.Close()
	}
	write(`{"type":"user","uuid":"u1","message":{"role":"user","content":"check the disks"}}` + "\n")
	_, _, off, err := ReadClaude(path)
	if err != nil || off != FileSize(path) {
		t.Fatalf("offset %d size %d err %v", off, FileSize(path), err)
	}
	// another process appends a reply and half of the next line
	write(`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"All disks are fine."}],"stop_reason":"end_turn"}}` + "\n" + `{"type":"user","uu`)
	lines, next, err := ClaudeTail(path, off)
	if err != nil || len(lines) != 1 {
		t.Fatalf("lines %d err %v", len(lines), err)
	}
	var log ClaudeLog
	ops := log.Feed(lines[0])
	if len(ops) != 2 || ops[0].Item == nil || ops[0].Item.Text != "All disks are fine." || !ops[1].TurnEnded {
		t.Fatalf("ops %+v", ops)
	}
	// the partial line is read once it is complete
	write(`id":"u2","message":{"role":"user","content":"thanks"}}` + "\n")
	lines, next2, _ := ClaudeTail(path, next)
	if len(lines) != 1 || next2 != FileSize(path) {
		t.Fatalf("second read: %d lines, offset %d of %d", len(lines), next2, FileSize(path))
	}
	if ops := log.Feed(lines[0]); len(ops) != 1 || !ops[0].TurnStarted || ops[0].Item.Text != "thanks" {
		t.Fatalf("ops %+v", ops)
	}
}
