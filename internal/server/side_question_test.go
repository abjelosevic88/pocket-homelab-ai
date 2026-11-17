package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func TestSideQuestionAnswersWithoutTouchingTheChat(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "side@example.test", "admin", true)
	other, _ := person(t, s, h, "other@example.test", "member", true)
	ctx := t.Context()
	parent := thread(t, s, u.ID, root)
	parent.Status = "running"
	if err := s.store.UpdateThread(ctx, parent); err != nil {
		t.Fatal(err)
	}
	for _, it := range []agent.Item{
		{Key: "u1", Kind: "user", Text: "Set the approval threshold to 70"},
		{Key: "a1", Kind: "assistant", Text: "Changing the threshold in the Worker now."},
		{Key: "t1", Kind: "tool", Tool: &agent.Tool{Name: "Read", Title: "catalog.yml", Status: "done", Input: json.RawMessage(`{"file_path":"catalog.yml"}`), Output: "on:\n  schedule:\n    - cron: '0 3 * * *'"}},
		{Key: "t2", Kind: "tool", Tool: &agent.Tool{Name: "Bash", Title: "go test", Status: "running", Input: json.RawMessage(`{"command":"go test ./..."}`)}},
		{Key: "q1", Kind: "user", Text: "queued follow-up", Queued: true},
	} {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(ctx, parent.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.store.ListItems(ctx, parent.ID)
	// a deterministic CLI: echoes whether it saw the history and the question, never a paid model
	bin := filepath.Join(root, "fake-claude-side")
	script := `#!/usr/bin/env python3
import sys,json
args=sys.argv[1:]
if args == ['--version']:
    print('test (Claude Code)'); sys.exit(0)
assert '--restricted' in args and args[args.index('--tools')+1] == ''
assert 'side question' in args[args.index('--system-prompt')+1]
for line in sys.stdin:
    m=json.loads(line)
    if m.get('type')=='control_request' and m.get('request_id')=='probe':
        print(json.dumps({'type':'control_response','response':{'request_id':'probe','response':{'models':[{'value':'sonnet','displayName':'Sonnet','supportedEffortLevels':['low','medium']}]}}}),flush=True)
        continue
    if m.get('type')=='user':
        c=m['message']['content']
        text=c if isinstance(c,str) else ''.join(b.get('text','') for b in c)
        ans='saw-history' if 'threshold to 70' in text and 'Changing the threshold' in text and 'queued follow-up' not in text and 'why 70?' in text and "cron: '0 3 * * *'" in text and 'go test ./...' in text else 'missing'
        print(json.dumps({'type':'assistant','message':{'id':'a','content':[{'type':'text','text':ans}]}}),flush=True)
        print(json.dumps({'type':'result','subtype':'success','usage':{'input_tokens':5,'output_tokens':2}}),flush=True)
        break
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.cfg.ClaudeBin = bin
	path := "/api/v1/threads/" + parent.ID + "/side-question"
	if code, body := other.call("POST", path, map[string]string{"question": "why 70?"}); code != 404 && code != 403 {
		t.Fatalf("someone else asked a side question: %d %s", code, body)
	}
	if code, _ := owner.call("POST", path, map[string]string{"question": "  "}); code != 400 {
		t.Fatalf("empty question accepted: %d", code)
	}
	var out struct {
		Answer string `json:"answer"`
		Model  string `json:"model"`
	}
	owner.want("POST", path, map[string]string{"question": "why 70?"}, 200, &out)
	if out.Answer != "saw-history" || out.Model != "sonnet" {
		t.Fatalf("side answer: %+v", out)
	}
	after, _ := s.store.ListItems(ctx, parent.ID)
	if len(after) != len(before) {
		t.Fatalf("side question changed the chat: %d → %d items", len(before), len(after))
	}
	got, _ := s.store.GetThread(ctx, u.ID, parent.ID)
	if got.Status != "running" || got.ProviderSessionID != parent.ProviderSessionID || strings.Contains(got.Title, "70") {
		t.Fatalf("side question changed the thread: %+v", got)
	}
}

func TestSideHistoryKeepsMessagesAndNewestToolResults(t *testing.T) {
	s, h, root := testServer(t)
	ctx := t.Context()
	_, u := person(t, s, h, "long@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	big := strings.Repeat("x", maxToolOutputBytes)
	var items []agent.Item
	for i := range 60 {
		items = append(items,
			agent.Item{Key: fmt.Sprintf("u%d", i), Kind: "user", Text: fmt.Sprintf("message %d", i)},
			agent.Item{Key: fmt.Sprintf("t%d", i), Kind: "tool", Tool: &agent.Tool{Name: "Read", Title: "f", Status: "done", Output: fmt.Sprintf("result-%d-%s", i, big)}})
	}
	for _, it := range items {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(ctx, th.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := s.store.ListItems(ctx, th.ID)
	hist, err := s.sideHistory(ctx, th, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) > maxSideHistoryBytes {
		t.Fatalf("history over budget: %d", len(hist))
	}
	for i := range 60 {
		if !strings.Contains(hist, fmt.Sprintf("message %d\n", i)) {
			t.Fatalf("message %d dropped", i)
		}
	}
	if !strings.Contains(hist, "result-59-") || strings.Contains(hist, "result-0-") || strings.Contains(hist, "omitted") {
		t.Fatalf("expected newest tool results only, every message kept")
	}
}
