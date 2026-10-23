package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runSh(t *testing.T, script string, args ...string) map[string]string {
	t.Helper()
	out, errOut, err := shOn(context.Background(), nil, time.Minute, script, args...)
	if err != nil {
		t.Fatalf("script failed: %v: %s\n%s", err, errOut, out)
	}
	return kv(out)
}

// The whole life of a worktree against a real repository: create, copy
// ignored files, work, commit, merge (fast-forward, merge, squash, conflict),
// archive with uncommitted work, restore it, forget it.
func TestWorktreeLifecycle(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, "web"), 0o755)
	gitT(t, repo, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n.env.*\nnode_modules/\n.claude/\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "web", "b.txt"), []byte("b\n"), 0o644)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "init")
	_ = os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o644)
	// ignored folders that hold a .env must never be copied whole (or at all)
	for _, d := range []string{"node_modules/pkg", ".claude/worktrees/other"} {
		_ = os.MkdirAll(filepath.Join(repo, d), 0o755)
		_ = os.WriteFile(filepath.Join(repo, d, ".env"), []byte("NO=1\n"), 0o644)
		_ = os.WriteFile(filepath.Join(repo, d, "big.bin"), []byte("x"), 0o644)
	}
	gitT(t, filepath.Join(repo, ".claude/worktrees/other"), "init", "-q") // a nested checkout, like Claude Code's worktrees
	_ = os.WriteFile(filepath.Join(repo, "web", ".env.local"), []byte("X=2\n"), 0o644)

	info, err := inspectRepo(context.Background(), nil, filepath.Join(repo, "web"))
	if err != nil || !info.Repo || info.Main != repo || info.Rel != "web/" || info.Branch != "main" {
		t.Fatalf("inspect: %+v %v", info, err)
	}

	wt := filepath.Join(root, "wts", "repo", "fix-a")
	m := runSh(t, wtAdd, repo, filepath.Join(root, "wts", "repo"), "fix-a", "main", "new", "")
	if m["path"] != wt || m["sha"] == "" || m["branch"] != "fix-a" {
		t.Fatalf("add: %v", m)
	}
	// the name is taken now: -2; the branch is checked out: not forked
	if m2 := runSh(t, wtAdd, repo, filepath.Join(root, "wts", "repo"), "fix-a", "main", "new", ""); m2["branch"] != "fix-a-2" {
		t.Fatalf("second add: %v", m2)
	} else {
		runSh(t, wtArchive, m2["path"], repo, "refs/pocket-ai/archive/x", "fix-a-2", "1")
		runSh(t, wtForget, repo, "refs/pocket-ai/archive/x", "fix-a-2", "1")
	}
	if m2 := runSh(t, wtAdd, repo, filepath.Join(root, "wts", "repo"), "other", "main", "branch", "fix-a"); m2["inuse"] != wt {
		t.Fatalf("branch in use: %v", m2)
	}
	m = runSh(t, wtCopy, append([]string{repo, wt}, defaultCopy...)...)
	if m["copied"] != ".env\nweb/.env.local" {
		t.Fatalf("copy: %q", m["copied"])
	}
	for _, d := range []string{"node_modules", ".claude"} {
		if _, err := os.Stat(filepath.Join(wt, d)); !os.IsNotExist(err) {
			t.Fatalf("%s was copied into the worktree", d)
		}
	}
	if again, _, _ := shOn(context.Background(), nil, time.Minute, wtInspect, wt); kv(again)["main"] != repo {
		t.Fatalf("inspect from the worktree: %s", again)
	}
	w := &store.Worktree{Path: wt, Repo: repo, Branch: "fix-a", Base: "main", BaseSHA: m["sha"], Status: "active"}
	w.BaseSHA = gitT(t, repo, "rev-parse", "main")

	// work: an edit and a new file, uncommitted
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644)
	_ = os.WriteFile(filepath.Join(wt, "new.txt"), []byte("new\n"), 0o644)
	st := worktreeStatus(context.Background(), nil, w)
	if st.Dirty != 2 || st.Ahead != 0 || st.Merged || st.Branch != "fix-a" {
		t.Fatalf("status dirty: %+v", st)
	}
	diff, err := worktreeDiff(context.Background(), nil, w)
	if err != nil || !strings.Contains(diff, "+two") || !strings.Contains(diff, "new.txt") || strings.Contains(diff, "SECRET") {
		t.Fatalf("diff: %v\n%s", err, diff)
	}
	runSh(t, wtCommitAll, wt, "Add two")
	st = worktreeStatus(context.Background(), nil, w)
	if st.Dirty != 0 || st.Ahead != 1 || len(st.Commits) != 1 || st.Commits[0].Subject != "Add two" {
		t.Fatalf("status committed: %+v", st)
	}

	// fast-forward into main while main is checked out (and dirty elsewhere)
	_ = os.WriteFile(filepath.Join(repo, "web", "b.txt"), []byte("b local\n"), 0o644)
	m = runSh(t, wtMerge, repo, "fix-a", "main", "merge", "Merge fix-a")
	if m["how"] != "fast-forward" {
		t.Fatalf("ff: %v", m)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "one\ntwo\n" {
		t.Fatalf("main checkout not updated: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "web", "b.txt")); string(got) != "b local\n" {
		t.Fatalf("local change in main lost: %q", got)
	}
	if st = worktreeStatus(context.Background(), nil, w); !st.Merged {
		t.Fatalf("should be merged: %+v", st)
	}
	_ = os.WriteFile(filepath.Join(repo, "web", "b.txt"), []byte("b\n"), 0o644)

	// diverged: real merge commit, computed without touching the worktree
	_ = os.WriteFile(filepath.Join(repo, "main.txt"), []byte("m\n"), 0o644)
	gitT(t, repo, "add", "main.txt")
	gitT(t, repo, "commit", "-q", "-m", "main moves on")
	_ = os.WriteFile(filepath.Join(wt, "c.txt"), []byte("c\n"), 0o644)
	runSh(t, wtCommitAll, wt, "Add c")
	m = runSh(t, wtMerge, repo, "fix-a", "main", "merge", "Merge fix-a again")
	if m["how"] != "merge" {
		t.Fatalf("merge: %v", m)
	}
	if _, err := os.Stat(filepath.Join(repo, "c.txt")); err != nil {
		t.Fatalf("merge did not reach the main checkout: %v", err)
	}
	if parents := strings.Fields(gitT(t, repo, "log", "-1", "--format=%P", "main")); len(parents) != 2 {
		t.Fatalf("expected a merge commit, parents %v", parents)
	}

	// conflict: reported, nothing changes
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("main side\n"), 0o644)
	gitT(t, repo, "commit", "-q", "-am", "main edits a")
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("branch side\n"), 0o644)
	runSh(t, wtCommitAll, wt, "branch edits a")
	before := gitT(t, repo, "rev-parse", "main")
	m = runSh(t, wtMerge, repo, "fix-a", "main", "squash", "Squash")
	if m["conflict"] != "a.txt" || gitT(t, repo, "rev-parse", "main") != before {
		t.Fatalf("conflict: %v", m)
	}
	// bring main in: conflicts left for the agent, then aborted
	m = runSh(t, wtUpdate, wt, "main", "merge")
	if m["conflict"] != "a.txt" || !worktreeStatus(context.Background(), nil, w).Merging {
		t.Fatalf("update conflict: %v", m)
	}
	runSh(t, wtUpdate, wt, "main", "abort")
	if worktreeStatus(context.Background(), nil, w).Merging {
		t.Fatal("abort left a merge in progress")
	}

	// a rebased copy of the branch's commit lands on main: equivalent, so merged
	gitT(t, wt, "reset", "-q", "--hard", "main")
	_ = os.WriteFile(filepath.Join(wt, "d.txt"), []byte("d\n"), 0o644)
	runSh(t, wtCommitAll, wt, "Add d")
	_ = os.WriteFile(filepath.Join(repo, "e.txt"), []byte("e\n"), 0o644)
	gitT(t, repo, "add", "e.txt")
	gitT(t, repo, "commit", "-q", "-m", "main again")
	gitT(t, repo, "cherry-pick", gitT(t, wt, "rev-parse", "HEAD"))
	if st := worktreeStatus(context.Background(), nil, w); !st.Merged || st.Ahead != 1 {
		t.Fatalf("cherry-picked should count as merged: %+v", st)
	}

	// archive with uncommitted work (an edit and a new file), then restore
	_ = os.WriteFile(filepath.Join(wt, "c.txt"), []byte("c edited\n"), 0o644)
	_ = os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(wt, "node_modules", "x"), 0o755)
	m = runSh(t, wtArchive, wt, repo, "refs/pocket-ai/archive/1", "fix-a", "0")
	if m["saved"] == "" {
		t.Fatalf("archive: %v", m)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
	if strings.Contains(gitT(t, repo, "ls-tree", "-r", "--name-only", "refs/pocket-ai/archive/1"), "node_modules") {
		t.Fatal("ignored files were archived")
	}
	runSh(t, wtRestore, repo, wt, "fix-a", "refs/pocket-ai/archive/1")
	if got, _ := os.ReadFile(filepath.Join(wt, "wip.txt")); string(got) != "wip\n" {
		t.Fatalf("new file not restored: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(wt, "c.txt")); string(got) != "c edited\n" {
		t.Fatalf("edit not restored: %q", got)
	}
	if st := worktreeStatus(context.Background(), nil, w); st.Dirty != 2 {
		t.Fatalf("restored work should be uncommitted again: %+v", st)
	}

	// archive deleting the branch, restore from the archive alone, forget
	runSh(t, wtArchive, wt, repo, "refs/pocket-ai/archive/1", "fix-a", "1")
	runSh(t, wtRestore, repo, wt, "fix-a", "refs/pocket-ai/archive/1")
	if got, _ := os.ReadFile(filepath.Join(wt, "wip.txt")); string(got) != "wip\n" {
		t.Fatalf("restore without branch: %q", got)
	}
	runSh(t, wtArchive, wt, repo, "refs/pocket-ai/archive/1", "fix-a", "1")
	runSh(t, wtForget, repo, "refs/pocket-ai/archive/1", "fix-a", "1")
	if out := gitT(t, repo, "for-each-ref", "refs/pocket-ai/"); out != "" {
		t.Fatalf("archive ref left: %s", out)
	}
}

func TestCompareURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:me/app.git":            "https://github.com/me/app/compare/main...x?expand=1",
		"https://github.com/me/app.git":        "https://github.com/me/app/compare/main...x?expand=1",
		"https://tok@github.com/me/app":        "https://github.com/me/app/compare/main...x?expand=1",
		"ssh://git@git.bjelke.org:2222/me/app": "https://git.bjelke.org/me/app/compare/main...x?expand=1",
		"/srv/git/app.git":                     "",
	} {
		if got := compareURL(in, "main", "x"); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
