package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClaudeRemote answers a remote_control request like Claude Code 2.1.310,
// then a message arrives from the Claude app (and our own one is echoed), a
// permission request is withdrawn because it was answered there, and Remote
// Control is turned off again.
const fakeClaudeRemote = `#!/bin/sh
read init
read rc
printf '%s\n' '{"type":"system","subtype":"bridge_state","state":"ready"}'
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"rc-1","response":{"session_url":"https://claude.ai/code/session_x","bridge_session_id":"cse_x"}}}'
read msg
printf '%s\n' '{"type":"system","subtype":"bridge_state","state":"connected"}'
printf '%s\n' '{"type":"user","message":{"role":"user","content":"from here"}}'
printf '%s\n' '{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"from the phone"}]}}'
printf '%s\n' '{"type":"user","message":{"role":"user","content":"<task-notification>done</task-notification>"}}'
printf '%s\n' '{"type":"control_request","request_id":"p1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"t1"}}'
printf '%s\n' '{"type":"control_cancel_request","request_id":"p1"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'
read off
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"rc-2"}}'
read more
`

type rcRec struct {
	*rec
	bridge    []string
	cancelled []string
}

func (r *rcRec) Bridge(state string) { r.mu.Lock(); r.bridge = append(r.bridge, state); r.mu.Unlock() }
func (r *rcRec) RequestCancelled(id string) {
	r.mu.Lock()
	r.cancelled = append(r.cancelled, id)
	r.mu.Unlock()
}

func TestClaudeRemoteControl(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaudeRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &rcRec{rec: newRec()}
	s, err := StartClaude(context.Background(), Options{Bin: bin, Cwd: dir, Mode: "default"}, r)
	if err != nil {
		t.Fatal(err)
	}
	ctl := s.(RemoteControllable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := ctl.RemoteControl(ctx, true, "Fix the build", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionURL != "https://claude.ai/code/session_x" || info.BridgeSessionID != "cse_x" {
		t.Fatalf("info = %+v", info)
	}
	if err := s.Send(Input{Text: "from here"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 1 })
	r.mu.Lock()
	var users []Item
	for _, k := range r.order {
		if it := r.items[k]; it.Kind == "user" {
			users = append(users, it)
		}
	}
	if len(users) != 1 || users[0].Text != "from the phone" || users[0].Key != "rc-u1" {
		t.Fatalf("user items = %+v", users)
	}
	if len(r.reqs) != 1 || len(r.cancelled) != 1 || r.cancelled[0] != "p1" {
		t.Fatalf("reqs %d, cancelled %v", len(r.reqs), r.cancelled)
	}
	if len(r.bridge) != 2 || r.bridge[1] != "connected" {
		t.Fatalf("bridge = %v", r.bridge)
	}
	r.mu.Unlock()
	if err := s.Respond("p1", Response{Decision: "allow"}); err == nil {
		t.Fatal("a withdrawn request can still be answered")
	}
	if _, err := ctl.RemoteControl(ctx, false, "", ""); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	<-r.exited
}
