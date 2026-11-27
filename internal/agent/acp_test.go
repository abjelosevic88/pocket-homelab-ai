package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeACP plays an ACP agent for one turn: thinking, text, a shell command
// that asks first, a plan, usage, the end.
const fakeACP = `#!/bin/sh
read init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"mcpCapabilities":{"http":true}},"authMethods":[]}}'
read new
printf '%s\n' "$new" > "$NEW_FILE"
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s-1","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"},{"id":"yolo","name":"YOLO"}]}}}'
read prompt
printf '%s\n' "$prompt" > "$PROMPT_FILE"
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Let me "}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"look."}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"ls","kind":"execute","status":"pending","rawInput":{"command":"ls -la"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"s-1","toolCall":{"toolCallId":"t1"},"options":[{"optionId":"a1","name":"Allow","kind":"allow_once"},{"optionId":"a2","name":"Always","kind":"allow_always"},{"optionId":"r1","name":"Reject","kind":"reject_once"}]}}'
read answer
printf '%s\n' "$answer" > "$ANSWER_FILE"
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"a.txt\nb.txt"}}]}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call","toolCallId":"t2","title":"Edit a.txt","kind":"edit","status":"completed","content":[{"type":"diff","path":"/w/a.txt","oldText":"x","newText":"y"}]}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"plan","entries":[{"content":"Look","status":"completed","priority":"high"},{"content":"Tell","status":"in_progress","priority":"high"}]}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Two "}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"files."}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"usage_update","used":1200,"size":100000,"cost":{"amount":0.25,"currency":"USD"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5,"cachedReadTokens":90,"totalTokens":105}}}'
read more
`

// TestACPTurnPlain runs the fake's turn without a mode switch.
func TestACPTurnPlain(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-acp")
	if err := os.WriteFile(bin, []byte(fakeACP), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func(k string) string { b, _ := os.ReadFile(filepath.Join(dir, k)); return string(b) }
	env := []string{"NEW_FILE=" + filepath.Join(dir, "new"), "PROMPT_FILE=" + filepath.Join(dir, "prompt"), "ANSWER_FILE=" + filepath.Join(dir, "answer")}
	mcp := filepath.Join(dir, "mcp.json")
	_ = os.WriteFile(mcp, []byte(`{"mcpServers":{"pocket":{"type":"http","url":"http://127.0.0.1:9/mcp","headers":{"Authorization":"Bearer k"}}}}`), 0o600)
	r := newRec()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ag := ACPAgent{ID: "fake", Name: "Fake", Bin: bin}
	s, err := StartACP(ctx, ag, Options{Cwd: dir, Env: env, SystemPrompt: "Be brief.", MCPConfig: mcp}, r)
	if err != nil {
		t.Fatal(err)
	}
	if r.session != "s-1" {
		t.Fatalf("session %q", r.session)
	}
	var nw struct {
		Params struct {
			MCPServers []map[string]any `json:"mcpServers"`
		} `json:"params"`
	}
	_ = json.Unmarshal([]byte(read("new")), &nw)
	if len(nw.Params.MCPServers) != 1 || nw.Params.MCPServers[0]["type"] != "http" || nw.Params.MCPServers[0]["name"] != "pocket" {
		t.Fatalf("mcp servers: %s", read("new"))
	}
	if err := s.Send(Input{Text: "list files"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Input{Text: "again"}); err != ErrACPBusy {
		t.Fatalf("second send while running: %v", err)
	}
	r.wait(t, func() bool { return len(r.reqs) == 1 })
	q := r.reqs[0]
	if q.ToolName != "Shell" || q.Title != "Run ls -la" || !q.CanAllowSession {
		t.Fatalf("request %+v", q)
	}
	if err := s.Respond(q.ID, Response{Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 1 })
	if !strings.Contains(read("answer"), `"optionId":"a2"`) {
		t.Fatalf("answer %s", read("answer"))
	}
	p := read("prompt")
	if !strings.Contains(p, "Be brief.") || !strings.Contains(p, "list files") {
		t.Fatalf("prompt %s", p)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[0]
	if !turn.OK || turn.OutputTokens != 5 || turn.InputTokens != 100 || turn.CostUSD != 0.25 {
		t.Fatalf("turn %+v", turn)
	}
	var kinds []string
	for _, k := range r.order {
		kinds = append(kinds, r.items[k].Kind)
	}
	if got := strings.Join(kinds, ","); got != "thinking,tool,tool,todos,assistant" {
		t.Fatalf("items %s %v", got, r.order)
	}
	th := r.items[r.order[0]]
	if th.Text != "Let me look." {
		t.Fatalf("thinking %q", th.Text)
	}
	sh := r.items["tc-t1"].Tool
	if sh.Kind != "bash" || sh.Status != "done" || sh.Output != "a.txt\nb.txt" || sh.Title != "ls -la" {
		t.Fatalf("shell %+v", sh)
	}
	ed := r.items["tc-t2"].Tool
	if ed.Kind != "edit" || !strings.Contains(ed.Diff, "-x\n+y") {
		t.Fatalf("edit %+v", ed)
	}
	if a := r.items[r.order[4]]; a.Text != "Two files." {
		t.Fatalf("answer %q", a.Text)
	}
	if len(r.ctx) == 0 || r.ctx[len(r.ctx)-1].Tokens != 1200 {
		t.Fatalf("context %+v", r.ctx)
	}
}

// TestACPLive drives a real agent (ACP_LIVE=opencode, or gemini / qwen when
// signed in): a turn that runs a command, then the session continued in a
// new process, then copied.
func TestACPLive(t *testing.T) {
	id := os.Getenv("ACP_LIVE")
	if id == "" {
		t.Skip("ACP_LIVE=<agent id> runs a real agent")
	}
	ag, ok := ACPAgentByID(id)
	if !ok {
		t.Fatal("unknown agent " + id)
	}
	if _, err := exec.LookPath(ag.Bin); err != nil {
		t.Skip(ag.Bin + " not installed")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	env := strings.Fields(os.Getenv("ACP_ENV"))
	p := ProbeACP(ctx, ag, "", filepath.Join(dir, "probe"), env)
	t.Logf("probe: available=%v version=%s models=%d default=%s modes=%v err=%s", p.Available, p.Version, len(p.Models), p.DefaultModel, p.Modes, p.Error)
	if !p.Available {
		t.Fatal(p.Error)
	}
	mode := os.Getenv("ACP_MODE")
	acpForceLoad = os.Getenv("ACP_FORCE_LOAD") != ""
	defer func() { acpForceLoad = false }()
	turn := func(o Options, text string) (*rec, Session) {
		r := newRec()
		s, err := StartACP(ctx, ag, o, r)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send(Input{Text: text}); err != nil {
			t.Fatal(err)
		}
		for {
			r.mu.Lock()
			n := len(r.turns)
			reqs := append([]Request(nil), r.reqs...)
			r.mu.Unlock()
			for _, q := range reqs {
				if s.Respond(q.ID, Response{Decision: "allow"}) == nil {
					t.Logf("  allowed: %s (%s)", q.Title, q.ToolName)
				}
			}
			if n > 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out")
			case <-time.After(200 * time.Millisecond):
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, k := range r.order {
			it := r.items[k]
			if it.Tool != nil {
				t.Logf("  %s tool %s [%s] %q → %q", k, it.Tool.Name, it.Tool.Status, it.Tool.Title, oneLine(it.Tool.Output, 80))
			} else {
				t.Logf("  %s %s %q", k, it.Kind, oneLine(it.Text, 100))
			}
		}
		t.Logf("  turn %+v", r.turns[0])
		if !r.turns[0].OK {
			t.Fatalf("turn failed: %+v", r.turns[0])
		}
		return r, s
	}
	r1, s1 := turn(Options{Cwd: dir, Env: env, Mode: mode, SystemPrompt: "The secret word is PELICAN."}, "Create a file named hello.txt containing hi, then run `ls` and tell me what it printed. Keep it short.")
	sid := r1.session
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err != nil {
		t.Errorf("hello.txt not written: %v", err)
	}
	_ = s1.Close()
	<-s1.Done()
	r2, s2 := turn(Options{Cwd: dir, Env: env, Mode: mode, ResumeID: sid}, "What is the secret word? Answer with the word only.")
	_ = s2.Close()
	<-s2.Done()
	var last string
	for _, k := range r2.order {
		if r2.items[k].Kind == "assistant" {
			last = r2.items[k].Text
		}
	}
	if !strings.Contains(strings.ToUpper(last), "PELICAN") {
		t.Errorf("resumed session forgot: %q", last)
	}
	if len(r2.order) > 0 && r2.items[r2.order[0]].Kind == "user" {
		t.Errorf("the load replayed the conversation into the timeline")
	}
	t.Logf("resumed answer: %q", last)
}

// TestGeminiPrepare: Gemini CLI is pointed at the sign-in that is there, so
// a Google sign-in it lacks never leaves it waiting for a browser.
func TestGeminiPrepare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ag, _ := ACPAgentByID("gemini")
	gh := ag.ACPHome()
	_ = os.MkdirAll(gh, 0o700)
	_ = os.WriteFile(filepath.Join(gh, "settings.json"), []byte(`{"ui":{"theme":"x"},"security":{"auth":{"selectedType":"oauth-personal"}}}`), 0o600)
	ag.prepare(nil) // no Google sign-in, no key: fail fast on the key
	if got := geminiAuth(gh); got != "gemini-api-key" {
		t.Fatalf("nothing set up: %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(gh, "settings.json"))
	if !strings.Contains(string(b), `"theme": "x"`) {
		t.Fatalf("other settings lost: %s", b)
	}
	_ = os.WriteFile(filepath.Join(gh, "oauth_creds.json"), []byte(`{"refresh_token":"r"}`), 0o600)
	ag.prepare([]string{"GEMINI_API_KEY=k"})
	if got := geminiAuth(gh); got != "oauth-personal" {
		t.Fatalf("signed in with Google: %q", got)
	}
	if id := ag.authMethod(nil, []acpAuthMethod{{ID: "oauth-personal"}, {ID: "gemini-api-key"}}); id != "oauth-personal" {
		t.Fatalf("auth method %q", id)
	}
}

func TestAntigravityAuthMethod(t *testing.T) {
	ag, _ := ACPAgentByID("antigravity")
	if id := ag.authMethod(nil, []acpAuthMethod{{ID: "oauth-personal"}, {ID: "gemini-api-key"}}); id != "oauth-personal" {
		t.Fatalf("auth method %q", id)
	}
}
