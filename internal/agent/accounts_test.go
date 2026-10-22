package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopySessionClaude(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	sid := "0b6c1f7e-1111-2222-3333-444455556666"
	writeFile(t, filepath.Join(a, "projects", "-tmp-x", sid+".jsonl"), "line1\nline2\n")
	writeFile(t, filepath.Join(a, "projects", "-tmp-x", sid, "subagents", "agent-1.jsonl"), "sub\n")
	dst, err := CopySession("claude", sid, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(b, "projects", "-tmp-x", sid+".jsonl"); dst != want {
		t.Fatalf("dst %s, want %s", dst, want)
	}
	if _, err := os.Stat(filepath.Join(b, "projects", "-tmp-x", sid, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatal("sub-agent log not copied:", err)
	}
	// A longer copy at the destination (the session went on there) is kept.
	writeFile(t, dst, "line1\nline2\nline3\n")
	if _, err := CopySession("claude", sid, a, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "line1\nline2\nline3\n" {
		t.Fatalf("longer copy was overwritten: %q", got)
	}
}

func TestCopySessionCodex(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	sid := "01a162f3-3bda-7590-8917-eaf26cdc6fce"
	rel := filepath.Join("sessions", "2026", "10", "22", "rollout-2026-10-22T11-31-10-"+sid+".jsonl")
	writeFile(t, filepath.Join(a, rel), "{}\n")
	dst, err := CopySession("codex", sid, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if dst != filepath.Join(b, rel) {
		t.Fatalf("dst %s", dst)
	}
	if _, err := SessionPath("codex", b, "../etc"); err == nil {
		t.Fatal("a session id with .. was accepted")
	}
}

func TestAccountFilesNewestCopyWins(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	sid := "0b6c1f7e-aaaa-bbbb-cccc-444455556666"
	line := `{"type":"user","sessionId":"` + sid + `","uuid":"u1","timestamp":"2026-10-22T09:00:00Z","cwd":"/tmp","message":{"content":"hi"}}` + "\n"
	pa := filepath.Join(a, "projects", "-tmp", sid+".jsonl")
	pb := filepath.Join(b, "projects", "-tmp", sid+".jsonl")
	writeFile(t, pa, line)
	writeFile(t, pb, line)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(pa, old, old)
	files := accountFiles(Homes{Claude: []AccountHome{{ID: 0, Dir: a}, {ID: 7, Dir: b}}}, time.Time{})
	if len(files) != 1 {
		t.Fatalf("got %d files, want the session once", len(files))
	}
	if files[0].account != 7 {
		t.Fatalf("account %d, want 7 (the newer copy)", files[0].account)
	}
}
