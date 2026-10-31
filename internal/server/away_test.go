package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Away mode end to end against an offline Claude: a permission asked while
// away is answered "not now" (the turn finishes instead of waiting), the
// summary lists it, the decision reaches the agent as one message, and the
// repeated call runs without asking.
func TestAwayDefersThenApprovedCallRunsWithoutAsking(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "away@example.test", "admin", true)
	fakeClaude(t, s, root)
	outPath := filepath.Join(root, "out.jsonl")
	outJSON, _ := json.Marshal(outPath)
	// each user message: ask to run the same command, report the answer, finish
	script := `#!/usr/bin/env python3
import sys, json
if sys.argv[1:2] == ['auth']:
    print('{}')
    sys.exit(0)
n = 0
def rec(x):
    with open(` + string(outJSON) + `, 'a') as f:
        f.write(json.dumps(x) + '\n')
for line in sys.stdin:
    msg = json.loads(line)
    t = msg.get('type')
    if t == 'control_response':
        rec(msg)
        print(json.dumps({'type':'result','subtype':'success','is_error':False}), flush=True)
        continue
    if t != 'user':
        continue
    n += 1
    rec({'user': msg['message']['content']})
    print(json.dumps({'type':'system','subtype':'init','session_id':'away-sess'}), flush=True)
    print(json.dumps({'type':'control_request','request_id':'r%d' % n,'request':{'subtype':'can_use_tool','tool_name':'Bash','input':{'command':'git push','description':'Push try %d' % n},'tool_use_id':'tu%d' % n}}), flush=True)
`
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	read := func(want int) []map[string]any {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(outPath)
			var out []map[string]any
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(l), &m) == nil {
					out = append(out, m)
				}
			}
			if len(out) >= want {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the agent did not get %d lines", want)
		return nil
	}
	behavior := func(m map[string]any) (string, string) {
		r, _ := m["response"].(map[string]any)
		inner, _ := r["response"].(map[string]any)
		b, _ := inner["behavior"].(string)
		msg, _ := inner["message"].(string)
		return b, msg
	}

	var st awaySum
	owner.want("PUT", "/api/v1/away", map[string]any{"on": true}, 200, &st)
	if !st.On || st.Since == "" {
		t.Fatal("away mode did not turn on", st)
	}
	th := thread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "ship it"}, 200, nil)
	lines := read(2)
	if text, _ := lines[0]["user"].(string); !strings.Contains(text, "away from the keyboard") {
		t.Fatal("a message sent while away does not tell the agent", text)
	}
	if b, msg := behavior(lines[1]); b != "deny" || !strings.Contains(msg, "away mode") {
		t.Fatal("the request was not deferred", lines[1])
	}
	// not waiting: the conversation goes on and ends its turn
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := s.store.GetThreadAny(t.Context(), th.ID)
		if got.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thread stuck in", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner.want("GET", "/api/v1/away", nil, 200, &st)
	if len(st.Items) != 1 || st.Items[0].Request.Status != "deferred" || st.Items[0].ThreadID != th.ID {
		t.Fatal("the summary does not list the saved request", st.Items)
	}
	// someone else may not decide it
	other, _ := person(t, s, h, "other@example.test", "admin", true)
	other.want("POST", "/api/v1/threads/"+th.ID+"/requests/r1", map[string]string{"decision": "allow"}, 404, nil)

	owner.want("PUT", "/api/v1/away", map[string]any{"on": false}, 200, &st)
	owner.want("POST", "/api/v1/threads/"+th.ID+"/requests/r1", map[string]string{"decision": "allow"}, 204, nil)
	owner.want("POST", "/api/v1/threads/"+th.ID+"/requests/r1", map[string]string{"decision": "allow"}, 400, nil)
	lines = read(4)
	if text, _ := lines[2]["user"].(string); !strings.Contains(text, "Approved:") || !strings.Contains(text, "git push") || strings.Contains(text, "away from the keyboard") {
		t.Fatal("the decision did not reach the agent", text)
	}
	if b, _ := behavior(lines[3]); b != "allow" {
		t.Fatal("the approved call asked again", lines[3])
	}
	owner.want("GET", "/api/v1/away", nil, 200, &st)
	if len(st.Items) != 0 {
		t.Fatal("a decided request is still listed", st.Items)
	}
	// the approval is used up: a third try asks as usual
	if s.takeAwayApproval(t.Context(), th.ID, awayMatch("Bash", json.RawMessage(`{"command":"git push"}`))) {
		t.Fatal("an approval ran twice")
	}
}

func TestAwayMatchIgnoresWording(t *testing.T) {
	a := awayMatch("Bash", json.RawMessage(`{"command":"ls","description":"List"}`))
	b := awayMatch("Bash", json.RawMessage(`{"description":"Look around","command":"ls"}`))
	c := awayMatch("Bash", json.RawMessage(`{"command":"ls -la"}`))
	if a != b || a == c || a == awayMatch("Shell", json.RawMessage(`{"command":"ls"}`)) {
		t.Fatal("match is not the exact call")
	}
}

// A background sub-agent's request after the main turn ended is deferred too,
// and the summary waits until the sub-agent is done.
func TestAwayCoversBackgroundSubagents(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "subs@example.test", "admin", true)
	fakeClaude(t, s, root)
	outPath := filepath.Join(root, "out.jsonl")
	outJSON, _ := json.Marshal(outPath)
	script := `#!/usr/bin/env python3
import sys, json, time
if sys.argv[1:2] == ['auth']:
    print('{}')
    sys.exit(0)
def rec(x):
    with open(` + string(outJSON) + `, 'a') as f:
        f.write(json.dumps(x) + '\n')
def say(x):
    print(json.dumps(x), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    t = msg.get('type')
    if t == 'user':
        say({'type':'system','subtype':'init','session_id':'subs-sess'})
        say({'type':'system','subtype':'task_started','task_id':'a1','description':'Check the docs','task_type':'local_agent'})
        say({'type':'result','subtype':'success','is_error':False})
        # the sub-agent, still at work in the background, asks
        say({'type':'control_request','request_id':'s1','request':{'subtype':'can_use_tool','tool_name':'Bash','input':{'command':'rm -rf build'},'tool_use_id':'tu-s1','agent_id':'a1'}})
    elif t == 'control_response':
        rec(msg)
        time.sleep(2)  # the sub-agent carries on, then finishes
        say({'type':'system','subtype':'task_notification','task_id':'a1','status':'completed'})
`
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	old := awayDoneDelay
	awayDoneDelay = 200 * time.Millisecond
	sent := make(chan string, 4)
	awaySummarySent = func(uid int64, title string) {
		if uid == u.ID {
			sent <- title
		}
	}
	t.Cleanup(func() { awayDoneDelay, awaySummarySent = old, nil })

	owner.want("PUT", "/api/v1/away", map[string]any{"on": true}, 200, nil)
	th := thread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "tidy up"}, 200, nil)
	deadline := time.Now().Add(8 * time.Second)
	var resp map[string]any
	for resp == nil && time.Now().Before(deadline) {
		data, _ := os.ReadFile(outPath)
		_ = json.Unmarshal([]byte(strings.TrimSpace(string(data))), &resp)
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := resp["response"].(map[string]any)
	inner, _ := r["response"].(map[string]any)
	if inner["behavior"] != "deny" || !strings.Contains(inner["message"].(string), "away mode") {
		t.Fatal("the sub-agent's request was not deferred", resp)
	}
	var st awaySum
	owner.want("GET", "/api/v1/away", nil, 200, &st)
	if len(st.Items) != 1 || st.Items[0].Request.ID != "s1" {
		t.Fatal("the sub-agent's request is not saved", st.Items)
	}
	select {
	case title := <-sent:
		t.Fatal("summary sent while a sub-agent still works:", title)
	case <-time.After(time.Second):
	}
	// the sub-agent finishes: now everything is done
	select {
	case title := <-sent:
		if !strings.Contains(title, "1 decision") {
			t.Fatal("summary does not count the saved request:", title)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no summary once the sub-agent was done")
	}
}
