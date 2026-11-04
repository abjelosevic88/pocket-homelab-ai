package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// agentToolClient calls a thread's own Pocket tool endpoint like its CLI does.
type agentToolClient struct {
	t        *testing.T
	url, key string
}

func (c agentToolClient) rpc(method string, params any) map[string]any {
	c.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", c.url, bytes.NewReader(body))
	req.Header.Set("Authorization", c.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Result map[string]any `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		c.t.Fatal(err)
	}
	return out.Result
}

// call runs a tool and returns its structured result (fails on a tool error
// unless failOK).
func (c agentToolClient) call(name string, args any, failOK ...bool) map[string]any {
	c.t.Helper()
	r := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	data, _ := r["structuredContent"].(map[string]any)
	if r["isError"] == true && len(failOK) == 0 {
		c.t.Fatalf("%s failed: %v", name, data)
	}
	return data
}

// agentParent is a coding thread with a stand-in session and its Pocket tool
// endpoint (no app connection: the agent tools come anyway).
func agentParent(t *testing.T, s *Server, u *store.User, root, mode string) (*store.Thread, *run, *startingSession, agentToolClient) {
	t.Helper()
	th := thread(t, s, u.ID, root)
	th.Mode = mode
	if err := s.store.UpdateThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	r := &run{threadID: th.ID, userID: u.ID, provider: "claude", lastUsed: time.Now(), pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}, cancel: func() {}}
	sess := &startingSession{sink: &runSink{rt: s.rt, r: r}, sent: make(chan agent.Input, 8)}
	r.sess = sess
	s.rt.mu.Lock()
	s.rt.runs[th.ID] = r
	s.rt.mu.Unlock()
	t.Cleanup(func() { s.rt.mu.Lock(); delete(s.rt.runs, th.ID); s.rt.mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := agent.Options{SystemPrompt: "Base"}
	if err := s.preparePocketTools(ctx, th, &runSink{rt: s.rt, r: r}, &opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opts.SystemPrompt, "pocket_agent_start") || !strings.Contains(opts.SystemPrompt, "enable the Ask connection") {
		t.Fatalf("instructions: %q", opts.SystemPrompt)
	}
	raw, _ := os.ReadFile(opts.MCPConfig)
	var conf struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal(raw, &conf)
	p := conf.Servers["pocket"]
	return th, r, sess, agentToolClient{t: t, url: p.URL, key: p.Headers["Authorization"]}
}

// pendingRequest waits for the run to ask the person something.
func pendingRequest(t *testing.T, r *run) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for id := range r.pending {
			r.mu.Unlock()
			return id
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing asked for approval")
	return ""
}

// An agent starts an agent: the person approves, the child runs on its own
// conversation (source agent → the parent), the parent waits for its answer,
// a follow-up's answer comes back to the parent as a folded report, and the
// limits hold (no freer mode, no grandchildren starting more).
func TestAgentStartsAgentWaitsAndGetsReport(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "agents@example.test", "admin", true)
	fakeClaude(t, s, root)
	// every message: answer with what it was asked, end the turn
	script := `#!/usr/bin/env python3
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
    print(json.dumps({'type':'system','subtype':'init','session_id':'child-sess'}), flush=True)
    print(json.dumps({'type':'assistant','message':{'id':'a%d' % n,'content':[{'type':'text','text':'CHILD-ANSWER %d: ' % n + text.split('\n')[0][:40]}]}}), flush=True)
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'usage':{'input_tokens':1,'output_tokens':1}}), flush=True)
`
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	parent, r, sess, tools := agentParent(t, s, u, root, "acceptEdits")

	list := tools.rpc("tools/list", map[string]any{})
	names := []string{}
	for _, x := range list["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if !strings.Contains(strings.Join(names, ","), "pocket_agent_start") || strings.Contains(strings.Join(names, ","), "pocket_search") {
		t.Fatalf("tools without an app connection: %v", names)
	}

	// a freer mode than the parent's is refused before anything is asked
	if out := tools.call("pocket_agent_start", map[string]any{"prompt": "x", "mode": "bypassPermissions"}, true); !strings.Contains(out["error"].(string), "not allowed") {
		t.Fatalf("freer mode: %v", out)
	}

	started := make(chan map[string]any, 1)
	go func() {
		started <- tools.call("pocket_agent_start", map[string]any{"prompt": "Count the files in the repo\nmore context", "title": "File counter"})
	}()
	id := pendingRequest(t, r)
	if !strings.HasPrefix(id, "ask-") {
		t.Fatalf("request %q", id)
	}
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+id, map[string]string{"decision": "allow"}, 204, nil)
	var out map[string]any
	select {
	case out = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not return")
	}
	childID, _ := out["agent_id"].(string)
	child, err := s.store.GetThreadAny(context.Background(), childID)
	if err != nil || child.Source != "agent" || child.SourceRef != parent.ID || child.Mode != "acceptEdits" || child.Title != "File counter" || child.Cwd != parent.Cwd {
		t.Fatalf("child %+v %v", child, err)
	}

	waited := tools.call("pocket_agent_wait", map[string]any{"agent_ids": []string{childID}, "timeout_seconds": 10})
	agents := waited["agents"].([]any)
	first := agents[0].(map[string]any)
	if first["state"] != "done" || !strings.Contains(first["reply"].(string), "CHILD-ANSWER 1: Count the files") {
		t.Fatalf("wait: %v", waited)
	}
	if s.store.OwesReport(context.Background(), childID) {
		t.Fatal("the wait handed the answer over, but a report is still owed")
	}

	// a follow-up nobody waits for: its answer comes to the parent as a message
	tools.call("pocket_agent_send", map[string]any{"agent_id": childID, "message": "Now count the folders"})
	select {
	case in := <-sess.sent:
		if !strings.HasPrefix(in.Text, "<pocket-agent-report") || !strings.Contains(in.Text, "CHILD-ANSWER 2: Now count the folders") || !strings.Contains(in.Text, `from-name="File counter"`) {
			t.Fatalf("report %q", in.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the answer never came back to the parent")
	}

	read := tools.call("pocket_agent_read", map[string]any{"agent_id": childID})
	if msgs := read["messages"].([]any); len(msgs) != 4 {
		t.Fatalf("read: %v", read)
	}
	listed := tools.call("pocket_agent_list", map[string]any{})
	if l := listed["agents"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", listed)
	}

	// another conversation may message it too, but only with the person's OK
	other, otherRun, _, otherTools := agentParent(t, s, u, root, "default")
	res := make(chan map[string]any, 1)
	go func() {
		res <- otherTools.call("pocket_agent_send", map[string]any{"agent_id": childID, "message": "hi"}, true)
	}()
	rid := pendingRequest(t, otherRun)
	c.want("POST", "/api/v1/threads/"+other.ID+"/requests/"+rid, map[string]string{"decision": "deny"}, 204, nil)
	if out := <-res; !strings.Contains(out["error"].(string), "did not approve") {
		t.Fatalf("other conversation: %v", out)
	}
	// someone else's conversations are out of reach
	_, stranger := person(t, s, h, "stranger@example.test", "admin", true)
	theirs := thread(t, s, stranger.ID, root)
	if out := tools.call("pocket_agent_read", map[string]any{"agent_id": theirs.ID}, true); !strings.Contains(out["error"].(string), "No conversation with that id") {
		t.Fatalf("stranger's conversation: %v", out)
	}
	all := tools.call("pocket_agent_list", map[string]any{"scope": "all"})
	if l := all["agents"].([]any); len(l) != 2 { // the child and the other one, not itself, not the stranger's
		t.Fatalf("list all: %v", all)
	}
}

// A heartbeat: approved, sent to the idle conversation when due, skipped
// while it works, removable; too-frequent ones are refused.
func TestAgentHeartbeat(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "beat@example.test", "admin", true)
	parent, r, sess, tools := agentParent(t, s, u, root, "default")
	ctx := context.Background()
	if out := tools.call("pocket_agent_heartbeat_create", map[string]any{"prompt": "check", "spec": "every 1m"}, true); !strings.Contains(out["error"].(string), "5 minutes") {
		t.Fatalf("every minute: %v", out)
	}
	res := make(chan map[string]any, 1)
	go func() {
		res <- tools.call("pocket_agent_heartbeat_create", map[string]any{"prompt": "Check the deploy", "spec": "every 30m", "max_runs": 2})
	}()
	rid := pendingRequest(t, r)
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+rid, map[string]string{"decision": "allow"}, 204, nil)
	out := <-res
	hb := out["heartbeat"].(map[string]any)
	id := int64(hb["id"].(float64))
	due := func() {
		_, _ = s.store.DB.Exec(`UPDATE heartbeats SET next_at = '2000-01-01T00:00:00Z' WHERE id = ?`, id)
	}

	_, _ = s.store.DB.Exec(`UPDATE threads SET status = 'idle' WHERE id = ?`, parent.ID) // the turn that set it is over
	due()
	s.runHeartbeats(ctx)
	select {
	case in := <-sess.sent:
		if !strings.HasPrefix(in.Text, `<pocket-heartbeat id=`) || !strings.Contains(in.Text, "Check the deploy") {
			t.Fatalf("beat %q", in.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeat was not sent")
	}
	// busy (the beat's own turn is still on): skipped, moved on
	due()
	s.runHeartbeats(ctx)
	list, _ := s.store.ThreadHeartbeats(ctx, parent.ID)
	if len(list) != 1 || list[0].Runs != 1 || list[0].NextAt == "2000-01-01T00:00:00Z" {
		t.Fatalf("busy beat: %+v", list)
	}
	select {
	case in := <-sess.sent:
		t.Fatalf("sent while busy: %q", in.Text)
	default:
	}
	c.want("GET", "/api/v1/threads/"+parent.ID+"/heartbeats", nil, 200, nil)
	c.want("DELETE", "/api/v1/threads/"+parent.ID+"/heartbeats/"+strconv.FormatInt(id, 10), nil, 204, nil)
	if list, _ := s.store.ThreadHeartbeats(ctx, parent.ID); len(list) != 0 {
		t.Fatal("not removed")
	}
}

// An agent's agent cannot start more; read-only starts get the read-only mode.
func TestAgentDepthAndModes(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "depth@example.test", "admin", true)
	th := thread(t, s, u.ID, root)
	ctx := context.Background()
	mk := func(parent string) *store.Thread {
		c := &store.Thread{ID: parent + "-c", UserID: u.ID, Provider: "claude", Mode: "default", Cwd: root, Source: "agent", SourceRef: parent}
		if err := s.store.CreateThread(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1 := mk(th.ID)
	c2 := mk(c1.ID)
	if d := s.agentDepth(ctx, th); d != 0 {
		t.Fatal(d)
	}
	if d := s.agentDepth(ctx, c2); d != 2 {
		t.Fatal(d)
	}
	if _, err := s.agentStart(ctx, ctx, u, c2, nil, agentStart{Prompt: "more"}); err == nil || !strings.Contains(err.Error(), "cannot start more") {
		t.Fatalf("grandchild started an agent: %v", err)
	}
	p := &store.Thread{Provider: "claude", Mode: "auto"}
	for _, c := range []struct {
		provider, asked string
		ro              bool
		want            string
	}{
		{"claude", "", false, "auto"},
		{"codex", "", false, "on-request"},
		{"codex", "", true, "read-only"},
		{"claude", "", true, "plan"},
		{"claude", "acceptEdits", false, "acceptEdits"},
	} {
		if got, err := childMode(c.provider, p, c.asked, c.ro); err != nil || got != c.want {
			t.Fatalf("%+v: %q %v", c, got, err)
		}
	}
	if _, err := childMode("codex", p, "full-access", false); err == nil {
		t.Fatal("full access allowed from auto")
	}
}

func TestAgentCommandsExpand(t *testing.T) {
	got := expandAgentCommand("claude", "/advisor is the cache right?")
	if !strings.Contains(got, `provider "codex"`) || !strings.HasSuffix(got, "is the cache right?") {
		t.Fatal(got)
	}
	if got := expandAgentCommand("codex", "/committee\nwhich db?"); !strings.Contains(got, "/committee]") || !strings.HasSuffix(got, "which db?") {
		t.Fatal(got)
	}
	for _, keep := range []string{"/handoffs now", "hello /handoff", "/compact"} {
		if expandAgentCommand("claude", keep) != keep {
			t.Fatal("expanded", keep)
		}
	}
	if l := agentCommandSkills([]agent.Skill{{Name: "handoff"}}); len(l) != 2 {
		t.Fatal(l)
	}
}
