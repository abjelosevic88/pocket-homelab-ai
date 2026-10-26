package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// A conversation whose folder is gone: messages are kept as not sent under
// ONE notice that offers the move; moving carries the Claude history along.
func TestFolderGoneAndMove(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "admin@example.test", "admin", true)
	old := filepath.Join(root, "athena", "instance")
	th := thread(t, s, u.ID, old) // the folder never existed: as if cleaned up
	sid := th.ProviderSessionID
	// the session as Claude Code filed it, under the old folder's name
	oldDir := filepath.Join(s.cfg.ClaudeHome, "projects", claudeProjectName(old))
	_ = os.MkdirAll(filepath.Join(oldDir, sid, "subagents"), 0o700)
	_ = os.WriteFile(filepath.Join(oldDir, sid+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(oldDir, sid, "subagents", "agent-1.jsonl"), []byte("{}\n"), 0o600)

	items := func() []agent.Item {
		rows, _ := s.store.ListItems(t.Context(), th.ID)
		var out []agent.Item
		for _, r := range rows {
			var it agent.Item
			_ = json.Unmarshal(r.Data, &it)
			out = append(out, it)
		}
		return out
	}
	for _, text := range []string{"do I need it?", "hey"} {
		code, body := c.do("POST", "/api/v1/threads/"+th.ID+"/messages", "application/json", []byte(`{"text":"`+text+`"}`))
		if code != 200 {
			t.Fatalf("send %q: %d %s", text, code, body)
		}
	}
	var notices, unsent int
	for _, it := range items() {
		if it.Kind == "notice" && it.Fix == "folder" {
			notices++
		}
		if it.Kind == "user" && it.Unsent && strings.Contains(it.UnsentWhy, "folder is gone") {
			unsent++
		}
	}
	last := items()[len(items())-1]
	if notices != 1 || unsent != 2 || last.Fix != "folder" || !strings.Contains(last.Text, old) {
		t.Fatalf("want one folder notice last and two unsent messages: notices=%d unsent=%d last=%+v", notices, unsent, last)
	}

	// a folder that is not there cannot be the new one; outside the roots neither
	for _, bad := range []string{filepath.Join(root, "nope"), "/etc"} {
		if code, _ := c.do("POST", "/api/v1/threads/"+th.ID+"/folder", "application/json", []byte(`{"cwd":"`+bad+`"}`)); code != 400 {
			t.Fatalf("moved to %s: %d", bad, code)
		}
	}
	dest := filepath.Join(root, "work", "athena-instance")
	_ = os.MkdirAll(dest, 0o755)
	code, body := c.do("POST", "/api/v1/threads/"+th.ID+"/folder", "application/json", []byte(`{"cwd":"`+dest+`"}`))
	if code != 200 {
		t.Fatalf("move: %d %s", code, body)
	}
	if got, _ := s.store.GetThreadAny(t.Context(), th.ID); got.Cwd != dest {
		t.Fatalf("cwd not moved: %q", got.Cwd)
	}
	newDir := filepath.Join(s.cfg.ClaudeHome, "projects", claudeProjectName(dest))
	for _, f := range []string{sid + ".jsonl", filepath.Join(sid, "subagents", "agent-1.jsonl")} {
		if _, err := os.Stat(filepath.Join(newDir, f)); err != nil {
			t.Fatalf("history not moved (%s): %v", f, err)
		}
	}
	for _, it := range items() {
		if it.Fix == "folder" {
			t.Fatal("the folder notice is still there after the move")
		}
	}
	if last := items()[len(items())-1]; last.Kind != "notice" || !strings.Contains(last.Text, "athena-instance") {
		t.Fatalf("no moved notice: %+v", last)
	}
	// someone else cannot move it
	o, _ := person(t, s, h, "other@example.test", "member", true)
	if code, _ := o.do("POST", "/api/v1/threads/"+th.ID+"/folder", "application/json", []byte(`{"cwd":"`+dest+`"}`)); code != 404 {
		t.Fatalf("another person moved the conversation: %d", code)
	}
}

func TestClaudeProjectName(t *testing.T) {
	for in, want := range map[string]string{
		"/home/ubuntu/athena/instance":                         "-home-ubuntu-athena-instance",
		"/home/ubuntu/.pocket-ai/worktrees/athena-fee2cf/test": "-home-ubuntu--pocket-ai-worktrees-athena-fee2cf-test",
		"/work/Projects/athena":                                "-work-Projects-athena",
	} {
		if got := claudeProjectName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
