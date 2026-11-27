package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
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
    if 'PROPOSE' in text and 'in the panel\'s chat' in text:
        answer = 'I made it shorter.\n--- new version ---\nShort plan.\n--- end of the version ---'
    elif 'You write the summary' in text:
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
	// a rename sticks
	c.want("PATCH", "/api/v1/panels/"+p.ID, map[string]string{"title": "  The rewrite  question "}, 200, nil)
	if np, _ := s.store.GetPanelAny(ctx, p.ID); np.Title != "The rewrite question" {
		t.Fatalf("renamed to %q", np.Title)
	}
	np, _ := s.store.GetPanelAny(ctx, p.ID)
	np.Title = "stale copy the engine still holds"
	_ = s.store.SavePanel(ctx, np)
	if np, _ := s.store.GetPanelAny(ctx, p.ID); np.Title != "The rewrite question" {
		t.Fatalf("a save by the engine put the old name back: %q", np.Title)
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

// Items picked from other Pocket apps join the material: the ones that cannot
// be read whole go in by title and link; nonsense is refused.
func TestPanelItemsBecomeMaterial(t *testing.T) {
	s, h, _ := testServer(t)
	_, u := person(t, s, h, "items@example.test", "admin", true)
	ctx := context.Background()
	got, err := s.panelItemsText(ctx, u, []panelItem{
		{App: "tasks", ID: "82", Title: "Check the\ncontracts", Subtitle: "Athena · due Friday"},
		{App: "projects", ID: "ATH-1", Title: "Panels", URL: "https://projects.test/i/ATH-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Also from the person's apps", "- Pocket Tasks: Check the contracts — Athena · due Friday", "- Pocket Projects: Panels (https://projects.test/i/ATH-1)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if _, err := s.panelItemsText(ctx, u, []panelItem{{App: "No Such/App", ID: "1"}}); err == nil {
		t.Fatal("a made-up app was accepted")
	}
	if _, err := s.panelItemsText(ctx, u, make([]panelItem, panelItemsMax+1)); err == nil || !strings.Contains(err.Error(), "At most") {
		t.Fatalf("too many items: %v", err)
	}
	// a note that cannot be read (no app connection here) stops the start with its name
	if _, err := s.panelItemsText(ctx, u, []panelItem{{App: "notes", ID: "7", Title: "My plan", Attachable: true}}); err == nil || !strings.Contains(err.Error(), "“My plan” from Pocket Notes") {
		t.Fatalf("unreadable note: %v", err)
	}
}

// What a working participant is doing shows its long paths short.
func TestLongPathsShowTheirEnd(t *testing.T) {
	got := longPathRe.ReplaceAllString("Read /home/me/Projects/app/internal/server/panels.go", "…/$1")
	if got != "Read …/server/panels.go" {
		t.Fatal(got)
	}
	if got := longPathRe.ReplaceAllString("Run go test ./internal/...", "…/$1"); got != "Run go test ./internal/..." {
		t.Fatal(got)
	}
}

// What participants may do without asking: each agent's own mode for the
// level picked, a line-up's level when nothing is said, and a panel from
// another app only ever looks.
func TestPanelAccessLevels(t *testing.T) {
	for _, c := range []struct{ provider, want, mode string }{
		{"claude", "auto", "auto"}, {"claude", "full", "bypassPermissions"}, {"claude", "edit", "acceptEdits"},
		{"codex", "auto", "on-request"}, {"codex", "full", "full-access"}, {"claude", "", ""},
	} {
		if got := panelBuildMode(c.provider, c.want); got != c.mode {
			t.Fatalf("%s %q: %q, want %q", c.provider, c.want, got, c.mode)
		}
	}
	fastPanels(t)
	s, h, root := testServer(t)
	c, u := person(t, s, h, "access@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seats := []map[string]any{{"provider": "claude", "model": "default"}, {"provider": "claude", "model": "default"}}
	modeOf := func(id string) (string, string) {
		p := panelDone(t, s, id)
		list, _ := s.store.PanelSeats(ctx, p.ID)
		th, err := s.store.GetThreadAny(ctx, list[0].ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		items, _ := s.store.ListItems(ctx, th.ID)
		return th.Mode, items[0].Body
	}
	var started struct {
		ID string `json:"id"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "ask", "question": "q", "depth": "quick", "cwd": root, "seats": seats, "access": "full"}, 201, &started)
	if mode, first := modeOf(started.ID); mode != "bypassPermissions" || !strings.Contains(first, "without asking") || strings.Contains(first, "You are read-only") {
		t.Fatalf("full access: mode %q, told %q", mode, first)
	}
	// the default line-up's level is used when nothing is said
	c.want("POST", "/api/v1/panels/lineups", map[string]any{"name": "Mine", "default": true, "access": "auto", "seats": seats}, 200, nil)
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "ask", "question": "q", "depth": "quick", "cwd": root}, 201, &started)
	if mode, _ := modeOf(started.ID); mode != "auto" {
		t.Fatalf("line-up's level: mode %q", mode)
	}
	// another app's panel only looks, whatever the default
	p, err := s.startPanel(ctx, u, panelStart{Kind: "ask", Question: "q", Depth: "quick", App: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if mode, first := modeOf(p.ID); mode != "plan" || !strings.Contains(first, "You are read-only") {
		t.Fatalf("another app's panel: mode %q", mode)
	}
	// one an agent starts is no freer than the agent
	level := 1
	p, err = s.startPanel(ctx, u, panelStart{Kind: "ask", Question: "q", Depth: "quick", accessCap: &level})
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := modeOf(p.ID); mode != "plan" {
		t.Fatalf("capped by the starting agent: mode %q", mode)
	}
}

// Refine: the council judges, the editor rewrites, the council judges the
// rewrite (which it is handed), the editor rewrites again: that is the outcome.
func TestRefineRewritesBetweenVerdicts(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "refine@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID string `json:"id"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "refine", "material": "The plan: do it in one go.", "depth": "standard", "cwd": root,
		"seats": []map[string]any{{"provider": "claude", "model": "default"}, {"provider": "claude", "model": "default"}},
		"chair": map[string]any{"provider": "claude", "model": "default"}}, 201, &started)
	p := panelDone(t, s, started.ID)
	if p.Status != "done" || p.Stage != 4 || p.Result != "ANSWER 2 [Second rewrite]" {
		t.Fatalf("panel %+v", p)
	}
	ctx := context.Background()
	answers, _ := s.store.PanelAnswers(ctx, p.ID)
	if len(answers) != 6 {
		t.Fatalf("answers %d", len(answers))
	}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	said := func(threadID string) []string {
		items, _ := s.store.ListItems(ctx, threadID)
		out := []string{}
		for _, it := range items {
			if it.Kind == "user" {
				out = append(out, it.Body)
			}
		}
		return out
	}
	// a participant's second step: the rewrite itself, not the others' verdicts
	second := said(seats[0].ThreadID)[1]
	if !strings.Contains(second, "ANSWER 1 [Rewrite]") || !strings.Contains(second, "The current version") || strings.Contains(second, "## Participant") {
		t.Fatalf("verdict on the rewrite was handed %q", second)
	}
	// the editor: told it is one, no word limit, the verdicts by letter; then only the new ones
	editor := said(seats[2].ThreadID)
	if !strings.Contains(editor[0], "You are the editor") || !strings.Contains(editor[0], "do not cut it to fit") || !strings.Contains(editor[0], "## Participant A") || !strings.Contains(editor[0], "The plan: do it in one go.") {
		t.Fatalf("editor was told %q", editor[0])
	}
	if !strings.Contains(editor[1], "ANSWER 2 [Verdicts on the rewrite]") || strings.Contains(editor[1], "ANSWER 1 [Verdicts]") {
		t.Fatalf("editor's second step %q", editor[1])
	}
}

func TestSplitRewrite(t *testing.T) {
	v, note := splitRewrite("# Plan\n\nDo it.\n\n--- end of the version ---\n\n## What changed\n\n- shorter")
	if v != "# Plan\n\nDo it." || !strings.HasPrefix(note, "## What changed") {
		t.Fatalf("%q / %q", v, note)
	}
	// trailing markdown emphasis must survive, while standalone divider lines are stripped
	v, note = splitRewrite("# Plan\n\nShip this **first**\n\n---\n\n--- end of the version ---\n\n## What changed\n- bolded")
	if v != "# Plan\n\nShip this **first**" || !strings.Contains(note, "bolded") {
		t.Fatalf("bold + divider: %q / %q", v, note)
	}
	v, _ = splitRewrite("# Plan\n\nKeep _italics_\n--- end of the version ---")
	if v != "# Plan\n\nKeep _italics_" {
		t.Fatalf("italics: %q", v)
	}
	// an answer from before the marker was asked for: the heading parts it
	v, note = splitRewrite("# Plan\n\nDo it.\n\n## What changed\n\n- shorter\n\n## Not taken\n\n- x")
	if v != "# Plan\n\nDo it." || !strings.Contains(note, "Not taken") {
		t.Fatalf("%q / %q", v, note)
	}
	if v, note = splitRewrite("Just the plan."); v != "Just the plan." || note != "" {
		t.Fatalf("%q / %q", v, note)
	}
	said, prop := splitProposal("I cut phase 3.\n\n--- new version ---\n# Plan\n1. a\n--- end of the version ---\nAnything else?")
	if said != "I cut phase 3.\n\nAnything else?" || prop != "# Plan\n1. a" {
		t.Fatalf("%q / %q", said, prop)
	}
	if said, prop = splitProposal("It is fine as it is."); prop != "" || said != "It is fine as it is." {
		t.Fatalf("%q / %q", said, prop)
	}
}

func TestRevealNames(t *testing.T) {
	seats := []*store.PanelSeat{
		{Label: "A", PanelSeatSpec: store.PanelSeatSpec{Provider: "claude", Role: "Sceptic"}},
		{Label: "B", PanelSeatSpec: store.PanelSeatSpec{Provider: "codex", Role: "Pragmatist"}},
		{Label: "C", PanelSeatSpec: store.PanelSeatSpec{Provider: "gemini"}},
	}
	for _, tc := range []struct{ in, want string }{
		{"Participant A agrees with Participant B.", "Claude, Sceptic (A) agrees with Codex, Pragmatist (B)."},
		{"Participants A and B agree.", "Claude, Sceptic (A) and Codex, Pragmatist (B) agree."},
		{"Participants A, B, and C raised this.", "Claude, Sceptic (A), Codex, Pragmatist (B), and Gemini CLI (C) raised this."},
		{"Participants A, B & C", "Claude, Sceptic (A), Codex, Pragmatist (B) & Gemini CLI (C)"},
		{"Participant A/B disagreed", "Claude, Sceptic (A)/Codex, Pragmatist (B) disagreed"},
		// words starting with B..H after "and" must not be mistaken for a seat letter
		{"Participant A and Build both pass.", "Claude, Sceptic (A) and Build both pass."},
		{"Participant F is unknown.", "Participant F is unknown."},
	} {
		if got := revealNames(tc.in, seats); got != tc.want {
			t.Errorf("revealNames(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A Refine panel keeps the versions of its plan and goes on from the current
// one: another round, an edit by hand, going back, a word with the editor.
// A conversation that links to the panel is handed the current version.
func TestPanelKeepsVersionsAndGoesOn(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, u := person(t, s, h, "versions@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	type view struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Result   string `json:"result"`
		Versions []struct {
			N      int    `json:"n"`
			Text   string `json:"text"`
			Source string `json:"source"`
			Note   string `json:"note"`
		} `json:"versions"`
		Chat []struct {
			ID       int64  `json:"id"`
			Role     string `json:"role"`
			Text     string `json:"text"`
			Proposal string `json:"proposal"`
			VersionN int    `json:"version_n"`
			State    string `json:"state"`
		} `json:"chat"`
	}
	var v view
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "refine", "material": "The plan: do it in one go.", "depth": "quick", "cwd": root, "title": "One go plan",
		"seats": []map[string]any{{"provider": "claude", "model": "default"}, {"provider": "claude", "model": "default"}},
		"chair": map[string]any{"provider": "claude", "model": "default"}}, 201, &v)
	id := v.ID
	panelDone(t, s, id)
	get := func() view {
		var x view
		c.want("GET", "/api/v1/panels/"+id, nil, 200, &x)
		return x
	}
	v = get()
	if len(v.Versions) != 2 || v.Versions[0].Source != "original" || v.Versions[0].Text != "The plan: do it in one go." || v.Versions[1].Text != "ANSWER 1 [Rewrite]" || v.Result != "ANSWER 1 [Rewrite]" {
		t.Fatalf("versions after the first round: %+v", v.Versions)
	}
	said := func(threadID string) []string {
		items, _ := s.store.ListItems(ctx, threadID)
		out := []string{}
		for _, it := range items {
			if it.Kind == "user" {
				out = append(out, it.Body)
			}
		}
		return out
	}
	seats, _ := s.store.PanelSeats(ctx, id)

	// another round, with a wish: they judge version 2, the editor writes version 3
	c.want("POST", "/api/v1/panels/"+id+"/rounds", map[string]string{"note": "tighten phase two"}, 200, nil)
	panelDone(t, s, id)
	v = get()
	if len(v.Versions) != 3 || v.Versions[2].Text != "ANSWER 2 [Version 3]" || v.Status != "done" {
		t.Fatalf("after another round: %+v %s", v.Versions, v.Status)
	}
	if judge := said(seats[0].ThreadID)[1]; !strings.Contains(judge, "ANSWER 1 [Rewrite]") || !strings.Contains(judge, "tighten phase two") || !strings.Contains(judge, "Verdicts on version 2") {
		t.Fatalf("the round's judges were told %q", judge)
	}

	// an edit by hand is version 4; the editor is told about it in the next round
	c.want("POST", "/api/v1/panels/"+id+"/versions", map[string]string{"text": "My own edit of the plan.", "note": "cut phase three"}, 200, nil)
	c.want("POST", "/api/v1/panels/"+id+"/rounds", map[string]string{}, 200, nil)
	panelDone(t, s, id)
	v = get()
	if len(v.Versions) != 5 || v.Versions[3].Source != "edit" || v.Versions[4].Source != "rewrite" {
		t.Fatalf("after an edit and a round: %+v", v.Versions)
	}
	editor := said(seats[2].ThreadID)
	if last := editor[len(editor)-1]; !strings.Contains(last, "It is not the last one you wrote") || !strings.Contains(last, "My own edit of the plan.") {
		t.Fatalf("the editor was not told of the edit: %q", last)
	}

	// back to the original: a new version with its text
	c.want("POST", "/api/v1/panels/"+id+"/versions/1/restore", nil, 200, nil)
	if v = get(); len(v.Versions) != 6 || v.Versions[5].Text != "The plan: do it in one go." || v.Versions[5].Source != "restore" {
		t.Fatalf("after going back: %+v", v.Versions)
	}

	// a word with the editor: it proposes a version, which is taken
	c.want("POST", "/api/v1/panels/"+id+"/chat", map[string]string{"text": "PROPOSE make it shorter"}, 200, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		v = get()
		if n := len(v.Chat); n == 2 && v.Chat[1].State != "working" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the editor never answered: %+v", v.Chat)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Chat[1].Text != "I made it shorter." || v.Chat[1].Proposal != "Short plan." || len(v.Versions) != 6 {
		t.Fatalf("chat %+v, versions %d", v.Chat, len(v.Versions))
	}
	if told := said(seats[2].ThreadID); !strings.Contains(told[len(told)-1], "The current version is version 6") || !strings.Contains(told[len(told)-1], "The plan: do it in one go.") {
		t.Fatalf("the editor was asked %q", told[len(told)-1])
	}
	c.want("POST", "/api/v1/panels/"+id+"/chat/"+itoa(v.Chat[1].ID)+"/accept", nil, 200, nil)
	if v = get(); len(v.Versions) != 7 || v.Versions[6].Text != "Short plan." || v.Versions[6].Source != "chat" || v.Chat[1].VersionN != 7 || v.Result != "Short plan." {
		t.Fatalf("after taking the proposal: %+v / %+v", v.Versions[len(v.Versions)-1], v.Chat[1])
	}

	// a conversation that links to the panel gets the current version; someone else's link gets nothing
	note := s.panelRefNote(ctx, u.ID, "implement phase one of [One go plan](https://ai.example/panels/"+id+")")
	if !strings.Contains(note, "Short plan.") || !strings.Contains(note, "version 7 of 7") || !strings.Contains(note, "One go plan") {
		t.Fatalf("link note %q", note)
	}
	// a panel report's own link must not duplicate the outcome, while a user link after the report still expands
	reportMsg := "<pocket-panel-report kind=\"Refine\" panel-id=\"" + id + "\">\nOutcome\n(see /panels/" + id + ")\n</pocket-panel-report>"
	if got := s.panelRefNote(ctx, u.ID, reportMsg); got != "" {
		t.Fatalf("report link was re-injected: %q", got)
	}
	if got := s.panelRefNote(ctx, u.ID, reportMsg+"\n\nAlso check /panels/"+id); !strings.Contains(got, "Short plan.") {
		t.Fatalf("user link after report was missed: %q", got)
	}
	if s.panelRefNote(ctx, u.ID+99, "see /panels/"+id) != "" || s.panelRefNote(ctx, u.ID, "no link here") != "" {
		t.Fatal("a panel was handed to someone it does not belong to, or without a link")
	}
	// another round is rejected with 409 while the editor is busy answering in chat
	busyMsg := &store.PanelChatMessage{PanelID: id, Role: "editor", State: "working"}
	if err := s.store.AddPanelChat(ctx, busyMsg); err != nil {
		t.Fatal(err)
	}
	if code, body := c.call("POST", "/api/v1/panels/"+id+"/rounds", map[string]string{}); code != 409 || !strings.Contains(string(body), "chat") {
		t.Fatalf("round while chat working: %d %s", code, body)
	}
	busyMsg.State = "done"
	_ = s.store.SavePanelChat(ctx, busyMsg)

	// export includes current version and version history
	code, exportBody := c.call("GET", "/api/v1/panels/"+id+"/export.md", nil)
	if code != 200 || !strings.Contains(string(exportBody), "## Current version (v7)") || !strings.Contains(string(exportBody), "## Version history") {
		t.Fatalf("export: %d %s", code, exportBody)
	}
	// list view returns batched seats cleanly
	var listed []struct {
		ID    string `json:"id"`
		Seats []any  `json:"seats"`
	}
	c.want("GET", "/api/v1/panels", nil, 200, &listed)
	if len(listed) != 1 || listed[0].ID != id || len(listed[0].Seats) != 3 {
		t.Fatalf("list panels: %+v", listed)
	}
	// found by name for the @ picker, and saved as a file
	if items := s.panelItems(ctx, u.ID, "one go", 5); len(items) != 1 || items[0].Subtitle != "Refine · version 7" || !strings.HasSuffix(items[0].URL, "/panels/"+id) {
		t.Fatalf("picker items %+v", items)
	}
	var saved struct {
		Path string `json:"path"`
	}
	c.want("POST", "/api/v1/panels/"+id+"/save", map[string]string{}, 200, &saved)
	if got, err := os.ReadFile(saved.Path); err != nil || string(got) != "Short plan.\n" || !strings.HasSuffix(saved.Path, "/one-go-plan.md") {
		t.Fatalf("saved %q %q %v", saved.Path, got, err)
	}
}

// A model-scoped limit window (like Claude's "7 days · Fable" at 100%) only
// blocks seats running that model — never other models on the same login.
func TestPanelScopedLimits(t *testing.T) {
	wins := []agent.Window{
		{ID: "five_hour", Name: "5 hours", Used: 0.20, ResetsAt: "2026-11-28T00:00:00Z"},
		{ID: "seven_day", Name: "7 days", Used: 0.90, ResetsAt: "2026-11-29T17:00:00Z"},
		{ID: "weekly_scoped:fable", Name: "7 days · Fable", Used: 1.0, ResetsAt: "2026-11-29T17:00:00Z"},
	}
	if u, _, ok := windowsUsed(wins, "opus", "Opus 5.5"); !ok || u != 0.90 {
		t.Fatalf("opus should see general 90%%, got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(wins, "default", "Default (recommended)"); !ok || u != 0.90 {
		t.Fatalf("default should see general 90%%, got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(wins); !ok || u != 0.90 {
		t.Fatalf("unscoped login summary should see general 90%%, got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(wins, "fable", "Fable"); !ok || u != 1.0 {
		t.Fatalf("fable should see scoped 100%%, got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(wins, "claude-fable-1"); !ok || u != 1.0 {
		t.Fatalf("claude-fable-1 should see scoped 100%%, got %v %v", u, ok)
	}
	// Codex and Antigravity scoped windows behave the same way.
	codexWins := []agent.Window{
		{ID: "codex:10080", Name: "7 days", Used: 0.10},
		{ID: "codex_bengalfox:10080", Name: "7 days · GPT-5.3-Codex-Spark", Used: 1.0},
	}
	if u, _, ok := windowsUsed(codexWins, "gpt-6.2-sol"); !ok || u != 0.10 {
		t.Fatalf("codex sol got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(codexWins, "gpt-5.3-codex-spark"); !ok || u != 1.0 {
		t.Fatalf("codex spark got %v %v", u, ok)
	}
	agyWins := []agent.Window{
		{ID: "g0-w", Name: "7 days", Used: 0.12},
		{ID: "g1-w", Name: "7 days · Claude and GPT", Used: 1.0},
	}
	if u, _, ok := windowsUsed(agyWins, "gemini-4-neon"); !ok || u != 0.12 {
		t.Fatalf("antigravity gemini got %v %v", u, ok)
	}
	if u, _, ok := windowsUsed(agyWins, "claude-opus-5-5"); !ok || u != 1.0 {
		t.Fatalf("antigravity claude got %v %v", u, ok)
	}

	// End-to-end panel start + options: opus runs, fable is skipped.
	fastPanels(t)
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "limits@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	s.rt.mu.Lock()
	s.rt.limits["claude"] = &provLimits{Windows: wins}
	s.rt.mu.Unlock()

	var opts struct {
		Logins []struct {
			Provider string         `json:"provider"`
			Used     float64        `json:"used"`
			Windows  []agent.Window `json:"windows"`
		} `json:"logins"`
	}
	c.want("GET", "/api/v1/panels/options", nil, 200, &opts)
	var claudeLoginFound bool
	for _, l := range opts.Logins {
		if l.Provider == "claude" {
			claudeLoginFound = true
			if l.Used != 0.90 || len(l.Windows) != 3 {
				t.Fatalf("options login quota = %.2f windows=%d, want 0.90 and 3", l.Used, len(l.Windows))
			}
		}
	}
	if !claudeLoginFound {
		t.Fatalf("no claude login in options: %+v", opts.Logins)
	}

	var started struct {
		ID    string `json:"id"`
		Seats []struct {
			Label string `json:"label"`
			Model string `json:"model"`
			State string `json:"state"`
			Error string `json:"error"`
			Chair bool   `json:"chair"`
		} `json:"seats"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{
		"kind": "ask", "question": "Check scoped limits", "depth": "quick", "cwd": root,
		"seats": []map[string]any{
			{"provider": "claude", "model": "opus"},
			{"provider": "claude", "model": "default"},
			{"provider": "claude", "model": "fable"},
		},
		"chair": map[string]any{"provider": "claude", "model": "default"},
	}, 201, &started)
	if len(started.Seats) != 3 {
		t.Fatalf("seats %+v", started.Seats)
	}
	if started.Seats[0].State == "skipped" || started.Seats[1].State == "skipped" {
		t.Fatalf("opus/default were wrongly skipped: %+v", started.Seats)
	}
	if started.Seats[2].State != "skipped" || started.Seats[2].Error != "Its plan limit is used up" {
		t.Fatalf("fable should be skipped: %+v", started.Seats[2])
	}
	p := panelDone(t, s, started.ID)
	if p.Status != "done" {
		t.Fatalf("panel status %q err %q", p.Status, p.Error)
	}
}
