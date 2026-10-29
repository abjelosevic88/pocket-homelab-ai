package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// localRun runs the remote scripts with sh here, HOME being a fake machine.
func localRun(home string) runFn {
	return func(ctx context.Context, _ time.Duration, script string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", script, "sh"}, args...)...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
}

func TestMirrorSessions(t *testing.T) {
	remote, dir := t.TempDir(), t.TempDir()
	sid := "95f90dfe-903a-432d-8a7c-ffb159144a91"
	claude := filepath.Join(remote, ".claude", "projects", "-home-x-Projects-core", sid+".jsonl")
	sub := filepath.Join(remote, ".claude", "projects", "-home-x-Projects-core", sid, "subagents", "agent-a1.jsonl")
	codex := filepath.Join(remote, ".codex", "sessions", "2026", "10", "29", "rollout-2026-10-29T13-00-00-abc.jsonl")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line := `{"type":"user","uuid":"u1","timestamp":"2026-10-29T12:00:00Z","cwd":"/home/x/Projects/core","entrypoint":"cli","message":{"content":"pull the latest"}}` + "\n"
	write(claude, line)
	write(sub, "{}\n")
	write(codex, "{\"type\":\"session_meta\"}\n")
	old := time.Date(2026, 10, 29, 13, 3, 0, 0, time.UTC)
	_ = os.Chtimes(claude, old, old)
	run := localRun(remote)
	mirrored := func(p string) string {
		rel, _ := filepath.Rel(remote, p) // .claude/… or .codex/…
		return filepath.Join(dir, rel[1:])
	}
	check := func(p string) {
		t.Helper()
		want, _ := os.ReadFile(p)
		got, err := os.ReadFile(mirrored(p))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q (%v), want %q", p, got, err, want)
		}
	}
	if err := mirrorSessions(context.Background(), run, dir, ""); err != nil {
		t.Fatal(err)
	}
	check(claude)
	check(codex)
	if info, _ := os.Stat(mirrored(claude)); !info.ModTime().Equal(old) {
		t.Fatalf("mtime %v, want the remote's %v", info.ModTime(), old)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude", "projects", "-home-x-Projects-core", sid, "subagents")); err == nil {
		t.Fatal("sub-agent transcripts are not sessions")
	}
	got := Sessions(MirrorHomes(dir), "")
	if len(got) != 1 || got[0].SessionID != sid || got[0].Origin != "terminal" || got[0].Cwd != "/home/x/Projects/core" {
		t.Fatalf("sessions = %+v", got)
	}

	// grows: only the new bytes come over; rewritten shorter: copied anew; gone: removed
	f, _ := os.OpenFile(claude, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a1","timestamp":"2026-10-29T12:00:05Z","message":{"content":[{"type":"text","text":"done"}]}}` + "\n")
	f.Close()
	write(codex, "{}\n")
	if err := mirrorSessions(context.Background(), run, dir, ""); err != nil {
		t.Fatal(err)
	}
	check(claude)
	check(codex)
	_ = os.Remove(codex)
	if err := mirrorSessions(context.Background(), run, dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mirrored(codex)); err == nil {
		t.Fatal("a session gone from the machine stays in the mirror")
	}
	check(claude)
}

func TestParseSessionListRefusesOddPaths(t *testing.T) {
	out := "claude/projects/../../etc/x12345678.jsonl\t10\t1\n" +
		"claude/projects/a/../b12345678.jsonl\t10\t1\n" +
		"codex/sessions/../../.ssh/id.jsonl\t10\t1\n" +
		"claude/projects/-home-x/abcdef12-3456.jsonl\t10\t1700000000\n"
	got := parseSessionList(out)
	if len(got) != 1 || got[0].rel != "claude/projects/-home-x/abcdef12-3456.jsonl" || got[0].size != 10 {
		t.Fatalf("got %+v", got)
	}
}
