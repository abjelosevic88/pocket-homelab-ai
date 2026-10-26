package server

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The packing script finds a session's sub-agents in the Claude folder and
// the copy keeps only their files, readable like local ones.
func TestRemoteSubagentCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	sid := "348f774e-a64a-4e70-b08f-7a1b5e1f34ea"
	src := filepath.Join(home, ".claude", "projects", "-work-app", sid, "subagents")
	_ = os.MkdirAll(src, 0o700)
	_ = os.WriteFile(filepath.Join(src, "agent-a1.meta.json"), []byte(`{"agentType":"Explore","description":"Trace feedback loops"}`), 0o600)
	_ = os.WriteFile(filepath.Join(src, "agent-a1.jsonl"), []byte(`{"type":"user","isSidechain":true,"message":{"role":"user","content":"look"}}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(src, "notes.txt"), []byte("not a sub-agent"), 0o600)

	out, errOut, err := shOn(context.Background(), nil, 10*time.Second, remoteSubagentFiles, sid)
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("pack: %v %s", err, errOut)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, sid, "subagents")
	if err := untarSubagents(raw, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err == nil {
		t.Fatal("copied a file that is not a sub-agent's")
	}
	list := agent.ListSubagents(filepath.Join(base, sid+".jsonl"))
	if len(list) != 1 || list[0].ID != "a1" || list[0].Description != "Trace feedback loops" {
		t.Fatalf("list: %+v", list)
	}
	if p, err := agent.SubagentPath(filepath.Join(base, sid+".jsonl"), "a1"); err != nil || p == "" {
		t.Fatalf("path: %q %v", p, err)
	}
	// nothing there: no output, no error
	if out, _, err := shOn(context.Background(), nil, 10*time.Second, remoteSubagentFiles, "00000000-none"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("missing session: %q %v", out, err)
	}
}

// A turn that ended with background sub-agents still at work counts as
// working for other apps (Pocket Projects marks the issue).
func TestLinkStatusSubagents(t *testing.T) {
	th := &store.Thread{Status: "idle"}
	if linkStatus(th) != "idle" {
		t.Fatal("idle thread not idle")
	}
	th.Subagents = []agent.Subagent{{ID: "a1"}}
	if linkStatus(th) != "running" {
		t.Fatal("sub-agents at work, but not running")
	}
	th.Pending = 1
	if linkStatus(th) != "waiting" {
		t.Fatal("a question waiting, but not waiting")
	}
}
