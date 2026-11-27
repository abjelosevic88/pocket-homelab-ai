package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// panelClaude is a stand-in CLI for panel participants: the summary writer
// names the participants by letter, a "Sceptic" fails when told to, everyone
// else answers with the step it was sent.
const panelClaude = `#!/usr/bin/env python3
import sys, json
if sys.argv[1:2] == ['auth']:
    print('{}')
    sys.exit(0)
n = 0
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get('type') != 'user':
        continue
    n += 1
    content = msg['message']['content']
    text = content if isinstance(content, str) else ' '.join(p.get('text', '') for p in content if isinstance(p, dict))
    print(json.dumps({'type':'system','subtype':'init','session_id':'s'}), flush=True)
    if 'FAIL-SCEPTIC' in text and 'Sceptic' in text and 'You write the summary' not in text:
        print(json.dumps({'type':'result','subtype':'error_during_execution','is_error':True,'result':'boom'}), flush=True)
        continue
    if 'You write the summary' in text:
        seen = 'saw-critique' if 'Step 2: Critique' in text else 'no-critique'
        answer = 'Participant A and Participant B agree. ' + seen
    else:
        step = text.split('# What to do now: ')[-1].split('\n')[0]
        answer = 'ANSWER %d [%s]' % (n, step)
    print(json.dumps({'type':'assistant','message':{'id':'a%d' % n,'content':[{'type':'text','text':answer}]}}), flush=True)
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'usage':{'input_tokens':1,'output_tokens':1}}), flush=True)
`

func panelDone(t *testing.T, s *Server, id string) *store.Panel {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p, err := s.store.GetPanelAny(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != "running" {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the panel never finished")
	return nil
}

func fastPanels(t *testing.T) {
	was := panelPollEvery
	panelPollEvery = 15 * time.Millisecond
	t.Cleanup(func() { panelPollEvery = was })
}

// A council runs its steps in order: every participant answers alone, sees
// the others' answers by letter in the critique, and the summary names them
// by who they were. Their conversations stay out of the conversation list.
func TestCouncilRunsItsSteps(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "panel@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID    string `json:"id"`
		Seats []struct {
			Label string `json:"label"`
			Chair bool   `json:"chair"`
		} `json:"seats"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "council", "question": "Should we rewrite it?", "material": "The plan: rewrite it.", "depth": "standard", "cwd": root,
		"seats": []map[string]any{{"provider": "claude", "model": "default", "role": "Sceptic"}, {"provider": "claude", "model": "default", "role": "Pragmatist"}},
		"chair": map[string]any{"provider": "claude", "model": "default"}}, 201, &started)
	if len(started.Seats) != 3 || started.Seats[0].Label != "A" || !started.Seats[2].Chair {
		t.Fatalf("seats %+v", started.Seats)
	}
	p := panelDone(t, s, started.ID)
	if p.Status != "done" || p.Stage != 3 {
		t.Fatalf("panel %+v", p)
	}
	if !strings.Contains(p.Result, "Claude, Sceptic (A) and Claude, Pragmatist (B) agree") || !strings.Contains(p.Result, "saw-critique") {
		t.Fatalf("result %q", p.Result)
	}
	ctx := context.Background()
	answers, _ := s.store.PanelAnswers(ctx, p.ID)
	if len(answers) != 5 { // 2 verdicts, 2 critiques, the verdict
		t.Fatalf("answers %d", len(answers))
	}
	if answers[0].Text != "ANSWER 1 [Verdicts]" || answers[2].Text != "ANSWER 2 [Critique]" {
		t.Fatalf("answers %q %q", answers[0].Text, answers[2].Text)
	}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	for _, st := range seats {
		th, err := s.store.GetThreadAny(ctx, st.ThreadID)
		if err != nil || th.Source != "panel" || th.SourceRef != p.ID || th.Mode != "plan" || th.CompletedUnread || th.Unread {
			t.Fatalf("participant thread %+v %v", th, err)
		}
	}
	// the critique handed B's verdict to A, by letter, and not A's own
	items, _ := s.store.ListItems(ctx, seats[0].ThreadID)
	second := ""
	for _, it := range items {
		if it.Kind == "user" {
			second = it.Body
		}
	}
	if !strings.Contains(second, "## Participant B (Pragmatist)") || strings.Contains(second, "## Participant A") || !strings.Contains(second, "ANSWER 1 [Verdicts]") {
		t.Fatalf("critique message %q", second)
	}
	var list []map[string]any
	c.want("GET", "/api/v1/threads", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("participants in the conversation list: %d", len(list))
	}
	var got struct {
		Answers []any `json:"answers"`
		Seats   []struct {
			Who string `json:"who"`
		} `json:"seats"`
	}
	c.want("GET", "/api/v1/panels/"+p.ID, nil, 200, &got)
	if len(got.Answers) != 5 || got.Seats[0].Who != "Claude, Sceptic (A)" {
		t.Fatalf("view %+v", got)
	}
	// deleting it takes its participants' conversations along
	c.want("DELETE", "/api/v1/panels/"+p.ID, nil, 204, nil)
	if _, err := s.store.GetThreadAny(ctx, seats[0].ThreadID); err == nil {
		t.Fatal("a participant's conversation outlived its panel")
	}
}

// Too few answers end a panel honestly: it fails, says who dropped out, and
// keeps what was answered.
func TestPanelFailsWithoutQuorum(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "quorum@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID string `json:"id"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "council", "question": "FAIL-SCEPTIC should we?", "depth": "quick", "cwd": root,
		"seats": []map[string]any{{"provider": "claude", "model": "default", "role": "Sceptic"}, {"provider": "claude", "model": "default", "role": "Pragmatist"}}}, 201, &started)
	p := panelDone(t, s, started.ID)
	if p.Status != "failed" || !strings.Contains(p.Error, "Only 1 of 2 answered") || !strings.Contains(p.Error, "Sceptic (A)") {
		t.Fatalf("panel %+v", p)
	}
	answers, _ := s.store.PanelAnswers(context.Background(), p.ID)
	if len(answers) != 1 {
		t.Fatalf("answers kept: %d", len(answers))
	}
	// one participant is not a panel
	code, body := c.call("POST", "/api/v1/panels", map[string]any{"kind": "council", "question": "x", "seats": []map[string]any{{"provider": "claude"}}})
	if code != 400 || !strings.Contains(string(body), "at least two") {
		t.Fatalf("one participant: %d %s", code, body)
	}
}

// An agent starts a panel with pocket_panel_start: the person approves, the
// agent waits and gets the outcome; the "/council" command tells it to.
func TestAgentStartsPanel(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, u := person(t, s, h, "paneltool@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	// a default line-up of two (one agent works here, so the automatic one is too small)
	c.want("POST", "/api/v1/panels/lineups", map[string]any{"name": "Mine", "default": true,
		"seats": []map[string]any{{"provider": "claude", "model": "default"}, {"provider": "claude", "model": "default"}}}, 200, nil)
	parent, r, sess, tools := agentParent(t, s, u, root, "default")

	if got := expandAgentCommand("claude", "/council the plan"); !strings.Contains(got, `pocket_panel_start, kind "council"`) || !strings.Contains(got, "the plan") {
		t.Fatalf("command: %q", got)
	}
	if got := expandAgentCommand("claude", "/committee old habit"); !strings.Contains(got, `kind "ask"`) {
		t.Fatalf("old command: %q", got)
	}

	started := make(chan map[string]any, 1)
	go func() {
		started <- tools.call("pocket_panel_start", map[string]any{"kind": "ask", "question": "What is best?", "depth": "quick", "report": false})
	}()
	id := pendingRequest(t, r)
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+id, map[string]string{"decision": "allow"}, 204, nil)
	var out map[string]any
	select {
	case out = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not return")
	}
	pid, _ := out["panel_id"].(string)
	waited := tools.call("pocket_panel_wait", map[string]any{"panel_id": pid, "timeout_seconds": 20})
	if waited["state"] != "done" || !strings.Contains(waited["outcome"].(string), "ANSWER 1 [Answers]") {
		t.Fatalf("wait: %v", waited)
	}
	select {
	case in := <-sess.sent:
		t.Fatalf("a report came although nobody asked for one: %q", in.Text)
	case <-time.After(150 * time.Millisecond):
	}

	// one nobody waits for reports back as a message
	go func() {
		started <- tools.call("pocket_panel_start", map[string]any{"kind": "ask", "question": "And second best?", "depth": "quick"})
	}()
	id = pendingRequest(t, r)
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+id, map[string]string{"decision": "allow"}, 204, nil)
	<-started
	select {
	case in := <-sess.sent:
		if !strings.HasPrefix(in.Text, "<pocket-panel-report") || !strings.Contains(in.Text, "ANSWER 1 [Answers]") {
			t.Fatalf("report %q", in.Text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the outcome never came back")
	}
	// a participant cannot start a panel of its own
	seats, _ := s.store.PanelSeats(context.Background(), pid)
	th, _ := s.store.GetThreadAny(context.Background(), seats[0].ThreadID)
	if agentToolsOn(th) {
		t.Fatal("a participant has the agent tools")
	}
}
