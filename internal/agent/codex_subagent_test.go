package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Codex sub-agent's log starts with its parent's prompt: it must not show
// as a second copy of the chat in Running or in the session list.
func TestCodexSubagentsAreNotListedOnTheirOwn(t *testing.T) {
	codex := t.TempDir()
	now := time.Now().UTC()
	dir := filepath.Join(codex, "sessions", now.Local().Format("2006"), now.Local().Format("01"), now.Local().Format("02"))
	_ = os.MkdirAll(dir, 0o700)
	at := now.Format(time.RFC3339)
	write := func(name, meta string) {
		lines := []string{
			`{"timestamp":"` + at + `","type":"session_meta","payload":` + meta + `}`,
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"task_started"}}`,
			`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"user_message","message":"Fix the list"}}`,
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("rollout-parent.jsonl", `{"id":"parent","source":"vscode","originator":"pocket-ai","cwd":"/x"}`)
	write("rollout-child.jsonl", `{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}},"forked_from_id":"parent","originator":"pocket-ai","cwd":"/x"}`)
	h := Homes{Codex: []AccountHome{{Dir: codex}}}

	active := ActiveSessions(h, "")
	if len(active) != 1 || active[0].SessionID != "parent" {
		t.Fatalf("running %+v, want only the parent", active)
	}
	list := Sessions(h, "")
	if len(list) != 1 || list[0].SessionID != "parent" {
		t.Fatalf("sessions %+v, want only the parent", list)
	}
	runs := Runs(h, "", now.Add(-time.Hour), now.Add(time.Hour))
	for _, r := range runs {
		if r.SessionID == "child" {
			t.Fatalf("runs list the sub-agent: %+v", runs)
		}
	}
}
