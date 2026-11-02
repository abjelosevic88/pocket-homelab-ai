package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestDeployScripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	origin, repo := filepath.Join(home, "origin.git"), filepath.Join(home, "repo")
	gitT(t, home, "init", "-q", "--bare", "-b", "main", origin)
	gitT(t, home, "clone", "-q", origin, repo)
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	gitT(t, repo, "push", "-q", "origin", "main")
	gitT(t, repo, "checkout", "-q", "-b", "as-8-memory")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "memory for all users")
	ctx := context.Background()

	// the item's branch: new on origin, one uncommitted file where it is checked out
	_ = os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("x"), 0o644)
	out, errOut, err := shOn(ctx, nil, 20*time.Second, wtDeployBranches, repo, "d:"+repo, "b:nope")
	if err != nil {
		t.Fatal(err, errOut)
	}
	c := splitLines(kv(out)["c"])
	if len(c) != 1 || !strings.HasPrefix(c[0], "as-8-memory|") || !strings.Contains(c[0], "|-1|1|") || !strings.HasSuffix(c[0], "|memory for all users") {
		t.Fatalf("branches: %q", out)
	}

	// pushed when origin lacks it, then nothing to push
	out, errOut, err = shOn(ctx, nil, 20*time.Second, wtDeployPush, repo, "as-8-memory")
	if m := kv(out); err != nil || m["pushed"] != "new" || len(m["sha"]) != 40 {
		t.Fatalf("push: %q %q %v", out, errOut, err)
	}
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "second")
	if m := kv(mustSh(t, wtDeployPush, repo, "as-8-memory")); m["pushed"] != "1" {
		t.Fatalf("push 1: %v", m)
	}
	if m := kv(mustSh(t, wtDeployPush, repo, "as-8-memory")); m["pushed"] != "" || m["sha"] == "" {
		t.Fatalf("nothing to push: %v", m)
	}
	if m := kv(mustSh(t, wtDeployPush, repo, "nope")); !strings.Contains(m["err"], "no branch nope") {
		t.Fatalf("missing branch: %v", m)
	}

	// run detached, poll until it ends; a second start while it runs is refused
	script := "export POCKET_BRANCH=" + deployQuote("as-8-memory") + "\n" + deployCdScript("~/repo") +
		"set -e\nprintf '\\033[32mbuilding\\033[0m %s\\n' \"$POCKET_BRANCH\"\nsleep 1\necho https://app.example.org\n"
	m := kv(mustSh(t, wtDeployStart, "github.com-o-r", "7", script))
	if m["started"] == "" {
		t.Fatalf("start: %v", m)
	}
	time.Sleep(300 * time.Millisecond)
	if m := kv(mustSh(t, wtDeployStart, "github.com-o-r", "8", script)); m["busy"] != "7" {
		t.Fatalf("second start: %v", m)
	}
	var meta map[string]string
	var logText string
	for i := 0; i < 50; i++ {
		out := mustSh(t, wtDeployPoll, "github.com-o-r", "7")
		head, body, _ := strings.Cut(out, "@@log\n")
		meta, logText = kv(head), body
		if meta["exit"] != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if meta["exit"] != "0" {
		t.Fatalf("poll: %v %q", meta, logText)
	}
	if got := cleanLog(logText); !strings.HasPrefix(got, "building as-8-memory\n") {
		t.Fatalf("log: %q", got)
	}
	if u := lastURL(logText); u != "https://app.example.org" {
		t.Fatalf("url: %q", u)
	}
	if m := kv(mustSh(t, wtDeployPoll, "github.com-o-r", "6")); m["gone"] != "1" {
		t.Fatalf("other id: %v", m)
	}
}

func mustSh(t *testing.T, script string, args ...string) string {
	t.Helper()
	out, errOut, err := shOn(context.Background(), nil, 20*time.Second, script, args...)
	if err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	return out
}

func TestDeployHelpers(t *testing.T) {
	if got := cleanLog("a\x1b[1mb\x1b[0m\n10%\r50%\r100%\n"); got != "ab\n100%\n" {
		t.Fatalf("cleanLog: %q", got)
	}
	if deployDir("github.com/abjelosevic-athenastudio/athena") != "github.com-abjelosevic-athenastudio-athena" {
		t.Fatal(deployDir("github.com/abjelosevic-athenastudio/athena"))
	}
	if deployCdScript("/work/a b") != "cd -- '/work/a b' || exit 3\n" || deployQuote("it's") != `'it'\''s'` {
		t.Fatal("quoting")
	}
	if lastURL("done\nnot a url at the end") != "" {
		t.Fatal("lastURL")
	}
}

func TestDeployMergeBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	origin, repo := filepath.Join(home, "origin.git"), filepath.Join(home, "repo")
	gitT(t, home, "init", "-q", "--bare", "-b", "development", origin)
	gitT(t, home, "clone", "-q", origin, repo)
	write := func(name, body string) { _ = os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644) }
	write("a.txt", "a\n")
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "first")
	gitT(t, repo, "push", "-q", "origin", "development")
	gitT(t, repo, "remote", "set-head", "origin", "development")
	gitT(t, repo, "checkout", "-q", "-b", "feat/x")
	write("b.txt", "b\n")
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "feature")
	gitT(t, repo, "push", "-q", "origin", "feat/x")
	// development moves on without touching the feature's file
	gitT(t, repo, "checkout", "-q", "development")
	write("c.txt", "c\n")
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "dev work")
	gitT(t, repo, "push", "-q", "origin", "development")
	gitT(t, repo, "checkout", "-q", "feat/x") // someone's checkout: the merge must not touch it
	ctx := context.Background()

	out, _, err := shOn(ctx, nil, 30*time.Second, wtDeployBehind, repo, "feat/x")
	if err != nil || !strings.Contains(out, "base=development") || !strings.Contains(out, "behind=1") || !strings.Contains(out, "clean=1") {
		t.Fatalf("behind: %q %v", out, err)
	}
	if out, _, _ = shOn(ctx, nil, 30*time.Second, wtDeployBehind, repo, "development"); strings.TrimSpace(out) != "" {
		t.Fatalf("the base itself is never behind: %q", out)
	}

	out, _, err = shOn(ctx, nil, time.Minute, wtDeployMergeBase, repo, "feat/x")
	m := kv(out)
	if err != nil || !strings.HasSuffix(m["merged"], " development") {
		t.Fatalf("merge: %q %v", out, err)
	}
	if sha, _, _ := strings.Cut(m["merged"], " "); gitT(t, origin, "rev-parse", "refs/heads/feat/x") != sha {
		t.Fatal("the merge was not pushed")
	}
	if gitT(t, repo, "status", "--porcelain") != "" || gitT(t, repo, "log", "-1", "--format=%s") != "feature" {
		t.Fatal("the checkout changed")
	}
	if out, _, _ = shOn(ctx, nil, 30*time.Second, wtDeployBehind, repo, "feat/x"); !strings.Contains(out, "behind=0") {
		t.Fatalf("still behind after the merge: %q", out)
	}
	if out, _, _ = shOn(ctx, nil, time.Minute, wtDeployMergeBase, repo, "feat/x"); strings.TrimSpace(out) != "" {
		t.Fatalf("nothing to merge: %q", out)
	}

	// both change the same file: conflicts named, nothing pushed
	gitT(t, repo, "pull", "-q", "--ff-only", "origin", "feat/x")
	write("a.txt", "feature\n")
	gitT(t, repo, "commit", "-q", "-am", "feature edits a")
	gitT(t, repo, "push", "-q", "origin", "feat/x")
	before := gitT(t, origin, "rev-parse", "refs/heads/feat/x")
	gitT(t, repo, "checkout", "-q", "development")
	write("a.txt", "development\n")
	gitT(t, repo, "commit", "-q", "-am", "dev edits a")
	gitT(t, repo, "push", "-q", "origin", "development")
	gitT(t, repo, "checkout", "-q", "feat/x")
	out, _, _ = shOn(ctx, nil, 30*time.Second, wtDeployBehind, repo, "feat/x")
	if !strings.Contains(out, "clean=0") || !strings.Contains(out, "conflict=a.txt") {
		t.Fatalf("conflict check: %q", out)
	}
	out, _, _ = shOn(ctx, nil, time.Minute, wtDeployMergeBase, repo, "feat/x")
	if strings.TrimSpace(kv(out)["conflict"]) != "a.txt" || gitT(t, origin, "rev-parse", "refs/heads/feat/x") != before {
		t.Fatalf("conflict merge: %q", out)
	}
	if strings.Count(gitT(t, repo, "worktree", "list"), "\n") != 0 {
		t.Fatal("a merge worktree was left behind")
	}
}

func TestFailureReason(t *testing.T) {
	migrator := `error [2026-11-02 09:37:09.877] [DbMigratorJob] Database migration job failed
{
  "data": {
    "errorMessage": "Error: Refusing to migrate: 13 applied migration created_at values do not exist in the current journal: 1793349881772. Restore the matching journal history before migrating.",
    "stack": "Error: Error: Refusing to migrate: 13 applied\n    at handleSafeCallError (index.ts:138:42)"
  }
}
/work/athena-instance/apps/db-migrator-job:
[ERR_PNPM_RECURSIVE_RUN_FIRST_FAIL] db-migrator-job@0.0.0 start: ` + "`tsx src/main.ts migrate`" + `
Exit status 1
[ELIFECYCLE] Command failed with exit code 1.
`
	for log, want := range map[string]string{
		migrator: "Refusing to migrate: 13 applied migration created_at values do not exist in the current journal: 1793349881772. Restore the matching journal history before migrating.",
		"building\n! 2 migration(s) of x were not applied: regenerate them\n": "2 migration(s) of x were not applied: regenerate them",
		"fatal: couldn't find remote ref feat/x\n":                            "fatal: couldn't find remote ref feat/x",
		"all good\n": "",
	} {
		if got := failureReason(log); got != want {
			t.Errorf("failureReason:\n got %q\nwant %q", got, want)
		}
	}
}

func TestDeployCheck(t *testing.T) {
	s, _, _ := testServer(t)
	cfg := deployConfig{Run: []string{"true"}, Check: `echo "! Resets the database: $POCKET_BRANCH"; echo; echo "Adds 1 migration(s): 0042_x"`}
	v := s.deployCheck(context.Background(), "github.com/o/r", cfg, "as-8")
	if v.Error != "" || len(v.Lines) != 2 || !v.Lines[0].Warn || v.Lines[0].Text != "Resets the database: as-8" || v.Lines[1].Warn || v.Lines[1].Text != "Adds 1 migration(s): 0042_x" {
		t.Fatalf("check: %+v", v)
	}
	if v := s.deployCheck(context.Background(), "github.com/o/r", deployConfig{Run: []string{"true"}}, "as-8"); len(v.Lines) != 0 || v.Error != "" {
		t.Fatalf("no check: %+v", v)
	}
}

// A person's own deploy wins for them (its own slot); everyone else keeps the
// shared one, which runs on its machine for anyone who may run agents.
func TestDeployPerPerson(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	s.cfg.PeopleDir = t.TempDir()
	_, owner := person(t, s, h, "owner@x", "admin", true)
	_, ana := person(t, s, h, "ana@x", "member", true)
	_, bo := person(t, s, h, "bo@x", "member", true)
	_, cy := person(t, s, h, "cy@x", "member", false)
	staging := &store.Host{Name: "staging", Target: "deploy@staging"} // the owner's machine
	box := &store.Host{Name: "ana-box", Target: "ana@box", UserID: ana.ID}
	for _, x := range []*store.Host{staging, box} {
		if err := s.store.CreateHost(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	save := func(slot string, c deployConfig) {
		b, _ := json.Marshal(c)
		if err := s.store.SaveProjectSettings(ctx, deployKey(slot), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	const project = "github.com/o/r"
	save(project, deployConfig{HostID: staging.ID, Run: []string{"deploy"}})
	save(mineSlot(project, ana.ID), deployConfig{HostID: box.ID, Run: []string{"mine"}})

	if d := s.deployFor(ctx, project, ana); !d.ok || !d.mine || d.slot != project+"@"+strconv.FormatInt(ana.ID, 10) || d.cfg.Run[0] != "mine" {
		t.Fatalf("ana: %+v", d)
	}
	if th, err := s.deployTarget(ctx, ana, s.deployFor(ctx, project, ana)); err != nil || th.ID != box.ID {
		t.Fatalf("ana's target: %v %v", th, err)
	}
	d := s.deployFor(ctx, project, bo)
	if !d.ok || d.mine || d.slot != project || d.cfg.Run[0] != "deploy" {
		t.Fatalf("bo: %+v", d)
	}
	if !s.jailed(ctx, bo.ID) || s.mayUseHost(ctx, bo.ID, staging) {
		t.Fatal("bo should not run agents on the owner's machine")
	}
	if th, err := s.deployTarget(ctx, bo, d); err != nil || th.ID != staging.ID {
		t.Fatalf("bo deploys with the shared one: %v %v", th, err)
	}
	if _, err := s.deployTarget(ctx, cy, s.deployFor(ctx, project, cy)); err == nil {
		t.Fatal("cy may not run agents")
	}
	// someone's own deploy on a machine they no longer may use is refused
	if _, err := s.deployTarget(ctx, bo, deploySetup{cfg: deployConfig{HostID: box.ID}, ok: true, mine: true}); err == nil {
		t.Fatal("bo on ana's box")
	}
	// Pocket AI's own server runs deploys unsandboxed: only its owner sets them up
	if _, err := s.mayDeployOn(ctx, bo, 0); err == nil {
		t.Fatal("sandboxed person set up a deploy on Pocket AI's server")
	}
	if _, err := s.mayDeployOn(ctx, owner, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deployTarget(ctx, bo, deploySetup{ok: true, mine: true}); err == nil {
		t.Fatal("bo's own deploy on Pocket AI's server")
	}
	if deployDir(mineSlot(project, ana.ID)) == deployDir(project) {
		t.Fatal("own slot shares the target's state folder")
	}
}
