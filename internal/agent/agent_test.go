package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	shells  []int
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
func (r *rec) Subagents([]Subagent)          {}
func (r *rec) Shells(n int)                  { r.mu.Lock(); r.shells = append(r.shells, n); r.mu.Unlock() }
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

func TestClaudeLogTurnFooter(t *testing.T) {
	var log ClaudeLog
	var turns []Item
	for _, l := range []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-10-21T10:00:00Z","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-21T10:00:05Z","message":{"content":[{"type":"text","text":"working"}],"stop_reason":"tool_use"}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-10-21T10:01:30Z","message":{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}`,
	} {
		for _, op := range log.Feed([]byte(l)) {
			if op.Item != nil && op.Item.Kind == "turn" {
				turns = append(turns, *op.Item)
			}
		}
	}
	if len(turns) != 1 || turns[0].Key != "turn-p1" || turns[0].Turn.DurationMS != 90000 {
		t.Fatalf("turn footer: %+v", turns)
	}
}

func TestRealPromptSkipsPaseoSummary(t *testing.T) {
	if got := realPrompt("<chat-history-summary>\nlong story\n</chat-history-summary>where is my fork?"); got != "where is my fork?" {
		t.Fatalf("got %q", got)
	}
	if got := realPrompt("<chat-history-summary>never closed"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestQueuedCommandAndLogImages(t *testing.T) {
	png := "iVBORw0KGgo="
	lines := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-10-21T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-21T10:00:01Z","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/x.png"}}],"stop_reason":"tool_use"}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-10-21T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}]}}`,
		`{"type":"attachment","uuid":"q1","timestamp":"2026-10-21T10:00:03Z","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"also this"}],"commandMode":"prompt"}}`,
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _, _, err := ReadClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		switch {
		case it.Kind == "user":
			got = append(got, it.Key+":"+it.Text+":"+strconv.Itoa(it.LogImages))
		case it.Tool != nil:
			got = append(got, it.Key+":"+strconv.Itoa(it.Tool.Images))
		}
	}
	if strings.Join(got, ",") != "u-p1:look:1,toolu_1:1,u-q1:also this:0" {
		t.Fatalf("items: %v", got)
	}
	for _, key := range []string{"u-p1", "toolu_1"} {
		data, mime, err := LogImage(path, key, 0)
		if err != nil || mime != "image/png" || len(data) == 0 {
			t.Fatalf("%s: %v %q %d", key, err, mime, len(data))
		}
	}
	if _, _, err := LogImage(path, "u-p1", 1); err != ErrNoImage {
		t.Fatalf("missing image: %v", err)
	}
}

func TestSessionScanRuns(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, "c.jsonl")
	lines := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-10-21T10:00:00Z","entrypoint":"cli","cwd":"/w","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-21T10:00:05Z","message":{"id":"m1","model":"claude-x","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"stop_reason":"tool_use","usage":{"output_tokens":10}}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-10-21T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"grep found [Request interrupted in a file"}]}}`,
		`{"type":"attachment","uuid":"q1","timestamp":"2026-10-21T10:00:07Z","attachment":{"type":"queued_command","prompt":"and this"}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-10-21T10:01:00Z","message":{"id":"m2","model":"claude-x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"output_tokens":5}}}`,
		`{"type":"user","uuid":"p2","timestamp":"2026-10-21T11:00:00Z","message":{"role":"user","content":"second"}}`,
		`{"type":"user","uuid":"r2","timestamp":"2026-10-21T11:00:09Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t9","content":"[Request interrupted by user for tool use]"}]}}`,
	}
	if err := os.WriteFile(claude, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(claude)
	sc := scanSession("claude", claude, info)
	if len(sc.runs) != 2 || sc.entry != "cli" {
		t.Fatalf("runs: %d entry %q", len(sc.runs), sc.entry)
	}
	r := sc.runs[0]
	if r.Status != "done" || r.DurationMS != 60000 || r.Tools != 1 || r.FollowUps != 1 || r.OutputTokens != 15 || r.Model != "claude-x" || r.Key != "u-p1" {
		t.Fatalf("run 1: %+v", *r)
	}
	if sc.runs[1].Status != "interrupted" || sc.runs[1].DurationMS != 9000 {
		t.Fatalf("run 2: %+v", *sc.runs[1])
	}

	codex := filepath.Join(dir, "x.jsonl")
	lines = []string{
		`{"timestamp":"2026-10-21T10:00:00Z","type":"session_meta","payload":{"id":"cx1","cwd":"/w","originator":"codex-tui"}}`,
		`{"timestamp":"2026-10-21T10:00:01Z","type":"turn_context","payload":{"model":"gpt-x"}}`,
		`{"timestamp":"2026-10-21T10:00:02Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-10-21T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"timestamp":"2026-10-21T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check it"}]}}`,
		`{"timestamp":"2026-10-21T10:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell"}}`,
		`{"timestamp":"2026-10-21T10:00:22Z","type":"event_msg","payload":{"type":"task_complete","duration_ms":20000}}`,
	}
	if err := os.WriteFile(codex, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(codex)
	cx := scanSession("codex", codex, info)
	if len(cx.runs) != 1 || cx.sessionID != "cx1" || cx.originName(nil) != "terminal" {
		t.Fatalf("codex: %d %q %q", len(cx.runs), cx.sessionID, cx.originName(nil))
	}
	if r := cx.runs[0]; r.Prompt != "check it" || r.Status != "done" || r.DurationMS != 20000 || r.Tools != 1 || r.Model != "gpt-x" {
		t.Fatalf("codex run: %+v", *r)
	}
	// appending continues from where the scan stopped
	fh, _ := os.OpenFile(codex, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"timestamp":"2026-10-21T11:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n")
	fh.Close()
	info, _ = os.Stat(codex)
	if cx2 := scanSession("codex", codex, info); len(cx2.runs) != 2 || len(cx.runs) != 1 {
		t.Fatalf("incremental: %d (old copy %d)", len(cx2.runs), len(cx.runs))
	}
}

// After background sub-agents finish, Claude Code starts a turn by itself:
// no message from us, then a reply and a result. It is a turn like any other.
const fakeClaudeSelfStart = `#!/bin/sh
read init
read msg
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-2","cwd":"/tmp"}'
printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Started the sub-agents."}]},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":900,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'
printf '%s\n' '{"type":"system","subtype":"task_notification","task_id":"t1","status":"completed"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m2"}},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"All done."}]},"parent_tool_use_id":null}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":4000,"total_cost_usd":0.03,"usage":{"input_tokens":2,"output_tokens":2}}'
read more
`

func TestClaudeSelfStartedTurn(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaudeSelfStart), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRec()
	s, err := StartClaude(context.Background(), Options{Bin: bin, Cwd: dir, Mode: "default"}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Input{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 2 })
	_ = s.Close()
	<-r.exited
	if tt := r.turns[1]; tt.DurationMS != 4000 || tt.CostUSD < 0.0199 || tt.CostUSD > 0.0201 { // cost is this turn's share of the running total
		t.Fatalf("self-started turn: %+v", r.turns[1])
	}
}

// A shell command put in the background reports as a task; its end comes as a
// notification without a task type. Both must reach the sink as a count.
const fakeClaudeBackgroundShell = `#!/bin/sh
read init
read msg
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-3","cwd":"/tmp"}'
printf '%s\n' '{"type":"system","subtype":"task_started","task_id":"b1","task_type":"local_bash","description":"build the ISO","is_backgrounded":true}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":900,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'
printf '%s\n' '{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed"}'
read more
`

func TestClaudeBackgroundShellsCounted(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaudeBackgroundShell), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRec()
	s, err := StartClaude(context.Background(), Options{Bin: bin, Cwd: dir, Mode: "default"}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Input{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.shells) == 2 })
	_ = s.Close()
	<-r.exited
	if r.shells[0] != 1 || r.shells[1] != 0 {
		t.Fatalf("shell counts: %v", r.shells)
	}
}
