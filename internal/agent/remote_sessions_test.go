package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		kind, rest, _ := strings.Cut(rel, "/")
		return filepath.Join(dir, kind[1:], "default", rest)
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
	if _, err := os.Stat(filepath.Join(dir, "claude", "default", "projects", "-home-x-Projects-core", sid, "subagents")); err == nil {
		t.Fatal("sub-agent transcripts are not sessions")
	}
	homes, _ := MirrorHomes(dir, remote)
	got := Sessions(homes, "")
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
	out := "claude/default/projects/../../etc/x12345678.jsonl\t10\t1\n" +
		"claude/default/projects/a/../b12345678.jsonl\t10\t1\n" +
		"codex/default/sessions/../../.ssh/id.jsonl\t10\t1\n" +
		"claude/.ssh/projects/a/abcdef12-3456.jsonl\t10\t1\n" +
		"claude/.claude-..x/projects/a/abcdef12-3456.jsonl\t10\t1\n" +
		"codex/.claude-max/sessions/a.jsonl\t10\t1\n" +
		"claude/.claude-max/projects/-home-x/abcdef12-3456.jsonl\t10\t1700000000\n"
	got := parseSessionList(out)
	if len(got) != 1 || got[0].rel != "claude/.claude-max/projects/-home-x/abcdef12-3456.jsonl" || got[0].size != 10 {
		t.Fatalf("got %+v", got)
	}
}

// Arno's box: claude-max etc. are folders a script starts claude on.
func TestMirrorSessionsEveryLogin(t *testing.T) {
	remote, dir := t.TempDir(), t.TempDir()
	put := func(rel, s string) {
		t.Helper()
		p := filepath.Join(remote, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line := func(cwd string) string {
		return `{"type":"user","uuid":"u1","timestamp":"2026-10-29T12:00:00Z","cwd":"` + cwd + `","entrypoint":"cli","message":{"content":"hi"}}` + "\n"
	}
	put(".claude/projects/-a/11111111-aaaa.jsonl", line("/a"))
	put(".claude-max/projects/-b/22222222-bbbb.jsonl", line("/b"))
	put(".claude-team-2/projects/-c/33333333-cccc.jsonl", line("/c"))
	put(".claude-team-2/.credentials.json", "{}")
	put(".claude-notes/readme.txt", "not a login: no config in it")
	put(".claude.json", "{}")
	if err := mirrorSessions(context.Background(), localRun(remote), dir, ""); err != nil {
		t.Fatal(err)
	}
	homes, ids := MirrorHomes(dir, "/home/arno")
	got := map[string]string{}
	for _, x := range Sessions(homes, "") {
		got[x.SessionID] = ids[x.Account]
	}
	want := map[string]string{"11111111-aaaa": "", "22222222-bbbb": "/home/arno/.claude-max", "33333333-cccc": "/home/arno/.claude-team-2"}
	if len(got) != len(want) {
		t.Fatalf("sessions %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("session %s in %q, want %q (all %v)", k, got[k], v, got)
		}
	}
}

func TestRemoteHomes(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude", ".claude-max/projects", ".claude_work", ".claude-empty", ".codex-pro", ".codex"} {
		_ = os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(home, ".claude_work", ".claude.json"), []byte("{}"), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".codex-pro", "auth.json"), []byte("{}"), 0o600)
	_ = os.Symlink(filepath.Join(home, ".claude-max"), filepath.Join(home, ".claude-link"))
	out, errOut, err := localRun(home)(context.Background(), time.Minute, RemoteHomesSh+"homes claude; echo --; homes codex")
	if err != nil {
		t.Fatal(err, errOut)
	}
	want := home + "/.claude\n" + home + "/.claude-max\n" + home + "/.claude_work\n--\n" + home + "/.codex\n" + home + "/.codex-pro\n"
	if out != want {
		t.Fatalf("homes:\n%s\nwant:\n%s", out, want)
	}
}

func TestValidHostHome(t *testing.T) {
	for dir, ok := range map[string]bool{
		"/home/arno/.claude-max": true, "/home/arno/.claude_team-2": true, "/home/arno/.claude": false,
		"/home/arno/x/.claude-max": false, "/home/other/.claude-max": false, "/home/arno/.claude-..": false,
		"/home/arno/.codex-pro": false, "": false,
	} {
		if got := ValidHostHome("claude", "/home/arno", dir); got != ok {
			t.Errorf("%q: %v, want %v", dir, got, ok)
		}
	}
	if !ValidHostHome("codex", "/home/arno/", "/home/arno/.codex-pro") {
		t.Error("codex folder refused")
	}
}
