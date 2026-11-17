package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func cleanupRepo(t *testing.T) (*store.Worktree, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "Cleanup test")
	t.Setenv("GIT_AUTHOR_EMAIL", "cleanup@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "Cleanup test")
	t.Setenv("GIT_COMMITTER_EMAIL", "cleanup@example.test")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-qm", "base")
	base := gitT(t, repo, "rev-parse", "HEAD")
	wt := filepath.Join(root, "topic")
	gitT(t, repo, "worktree", "add", "-qb", "topic", wt)
	if err := os.WriteFile(filepath.Join(wt, "file"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, wt, "commit", "-qam", "change")
	return &store.Worktree{Repo: repo, Path: wt, Branch: "topic", Base: "main", BaseSHA: base, Status: "active"}, root
}

func cleanupInspect(t *testing.T, w *store.Worktree) map[string]string {
	t.Helper()
	return runSh(t, wtCleanupInspect, w.Path, w.Repo, "", w.Base, w.BaseSHA, "test")
}

func TestCleanupGitGuards(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*testing.T, *store.Worktree)
	}{
		{"base branch", "base branch", func(t *testing.T, w *store.Worktree) { w.Base = w.Branch }},
		{"default branch", "default branch", func(t *testing.T, w *store.Worktree) {
			gitT(t, w.Repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/topic")
		}},
		{"untracked", "Uncommitted", func(t *testing.T, w *store.Worktree) {
			_ = os.WriteFile(filepath.Join(w.Path, "notes"), []byte("keep"), 0644)
		}},
		{"dirty", "Uncommitted", func(t *testing.T, w *store.Worktree) {
			_ = os.WriteFile(filepath.Join(w.Path, "file"), []byte("keep"), 0644)
		}},
		{"locked", "locked", func(t *testing.T, w *store.Worktree) { gitT(t, w.Repo, "worktree", "lock", w.Path) }},
		{"switched to unmerged", "", func(t *testing.T, w *store.Worktree) {
			gitT(t, w.Path, "checkout", "-qb", "other")
			gitT(t, w.Path, "commit", "--allow-empty", "-qm", "unmerged")
		}},
		{"detached", "No branch", func(t *testing.T, w *store.Worktree) { gitT(t, w.Path, "checkout", "--detach") }},
		{"assume unchanged", "Index flags", func(t *testing.T, w *store.Worktree) {
			gitT(t, w.Path, "update-index", "--assume-unchanged", "file")
			_ = os.WriteFile(filepath.Join(w.Path, "file"), []byte("hidden"), 0644)
		}},
		{"operation", "operation", func(t *testing.T, w *store.Worktree) {
			p := gitT(t, w.Path, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
			_ = os.WriteFile(p, []byte(gitT(t, w.Path, "rev-parse", "HEAD")), 0644)
		}},
		{"main checkout", "not a linked", func(t *testing.T, w *store.Worktree) { w.Path = w.Repo; w.Branch = "main" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := cleanupRepo(t)
			gitT(t, w.Repo, "merge", "--ff-only", w.Branch)
			tc.change(t, w)
			if m := cleanupInspect(t, w); !strings.Contains(m["reason"], tc.reason) || m["merged"] == "1" {
				t.Fatalf("inspect: %v", m)
			}
			if _, err := os.Stat(w.Path); err != nil {
				t.Fatal("inspection removed folder", err)
			}
		})
	}
}

func TestCleanupRefreshesRemoteAndKeepsNewCommits(t *testing.T) {
	w, root := cleanupRepo(t)
	remote := filepath.Join(root, "remote.git")
	gitT(t, w.Repo, "init", "--bare", remote)
	gitT(t, w.Repo, "remote", "add", "origin", remote)
	gitT(t, w.Repo, "push", "origin", "main")
	// main is stale locally: the remote's main has the merged branch.
	gitT(t, w.Path, "push", "origin", "HEAD:main")
	m := cleanupInspect(t, w)
	if m["merged"] != "1" {
		t.Fatalf("remote merge not found: %v", m)
	}
	if gitT(t, w.Repo, "rev-parse", "main") != w.BaseSHA {
		t.Fatal("cleanup moved main checkout")
	}
	// Fresh local commits are not covered by that merge.
	gitT(t, w.Path, "commit", "--allow-empty", "-qm", "new work")
	if m = cleanupInspect(t, w); m["merged"] == "1" {
		t.Fatal("new commits considered merged", m)
	}
	// A stale local main is never enough when the remote cannot be refreshed.
	gitT(t, w.Repo, "merge", "--ff-only", w.Branch)
	gitT(t, w.Repo, "remote", "set-url", "origin", filepath.Join(root, "offline.git"))
	if m = cleanupInspect(t, w); !strings.Contains(m["reason"], "refresh") {
		t.Fatal("offline fetch accepted", m)
	}
}

func TestCleanupRemoveRechecksAndRestores(t *testing.T) {
	w, _ := cleanupRepo(t)
	gitT(t, w.Repo, "merge", "--ff-only", w.Branch)
	m := cleanupInspect(t, w)
	ref := "refs/pocket-ai/archive/test"
	args := []string{w.Path, w.Repo, w.Branch, m["head"], ref, "1"}
	gitT(t, w.Path, "commit", "--allow-empty", "-qm", "new work")
	if _, _, err := shOn(context.Background(), nil, time.Minute, wtCleanupRemove, args...); err == nil {
		t.Fatal("removed changed HEAD")
	}
	args[3] = gitT(t, w.Path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(w.Path, "draft"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := shOn(context.Background(), nil, time.Minute, wtCleanupRemove, args...); err == nil {
		t.Fatal("removed new file")
	}
	if err := os.Remove(filepath.Join(w.Path, "draft")); err != nil {
		t.Fatal(err)
	}
	runSh(t, wtCleanupRemove, args...)
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("folder remains", err)
	}
	if gitT(t, w.Repo, "rev-parse", ref) != args[3] {
		t.Fatal("recovery reference wrong")
	}
	runSh(t, wtRestore, w.Repo, w.Path, w.Branch, ref)
	if gitT(t, w.Path, "rev-parse", "HEAD") != args[3] {
		t.Fatal("restore did not recover original HEAD")
	}
}

// Worktree setup copies .env files in and installs node_modules; agents
// often switch to a branch named for their PR. Neither blocks cleanup.
func TestCleanupIgnoredFilesAndSwitchedBranch(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = ""
	_, u := person(t, s, h, "switched-cleanup@example.test", "member", true)
	w, _ := cleanupRepo(t)
	if err := os.WriteFile(filepath.Join(w.Repo, ".git", "info", "exclude"), []byte(".env\nnode_modules/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(w.Path, ".env"), []byte("SECRET=1\n"), 0644)
	_ = os.MkdirAll(filepath.Join(w.Path, "node_modules", "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(w.Path, "node_modules", "pkg", "index.js"), []byte("x"), 0644)
	gitT(t, w.Path, "checkout", "-qb", "agent-pr")
	gitT(t, w.Path, "commit", "--allow-empty", "-qm", "agent work")
	gitT(t, w.Repo, "merge", "--ff-only", "agent-pr")
	head := gitT(t, w.Path, "rev-parse", "HEAD")
	m := cleanupInspect(t, w)
	if m["merged"] != "1" || m["branch"] != "agent-pr" {
		t.Fatalf("inspect: %v", m)
	}
	// Removal still refuses a branch other than the one inspected.
	if _, _, err := shOn(context.Background(), nil, time.Minute, wtCleanupRemove, w.Path, w.Repo, "topic", head, "refs/pocket-ai/archive/x", "1"); err == nil {
		t.Fatal("removed with a stale branch")
	}
	w.UserID = u.ID
	ctx := context.Background()
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	result := s.cleanupOne(ctx, u.ID, w.ID, cleanupInput{DeleteBranches: true})
	if result.Status != "archived" || result.Branch != "agent-pr" {
		t.Fatalf("cleanup: %+v", result)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("folder remains", err)
	}
	if out := gitT(t, w.Repo, "branch", "--list", "agent-pr"); out != "" {
		t.Fatal("merged branch kept", out)
	}
	got, _ := s.store.GetWorktree(ctx, u.ID, w.ID)
	if got.Branch != "agent-pr" {
		t.Fatalf("restore would recreate the wrong branch: %+v", got)
	}
	runSh(t, wtRestore, w.Repo, w.Path, got.Branch, got.ArchiveRef)
	if gitT(t, w.Path, "rev-parse", "HEAD") != head {
		t.Fatal("restore did not recover the agent branch")
	}
}

func TestCleanupAPIAndAutomatic(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = "" // fixture repos use shared server paths
	alex, au := person(t, s, h, "alex-cleanup@example.test", "member", true)
	ben, bu := person(t, s, h, "ben-cleanup@example.test", "member", true)
	ctx := context.Background()
	w, _ := cleanupRepo(t)
	gitT(t, w.Repo, "merge", "--ff-only", w.Branch)
	w.UserID = au.ID
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	thread := &store.Thread{ID: "cleanup-thread", UserID: au.ID, Provider: "codex", Cwd: w.Path, WorktreeID: w.ID, Title: "Keep my history"}
	if err := s.store.CreateThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	var result cleanupResult
	ben.want("POST", "/api/v1/worktrees/cleanup", cleanupInput{IDs: []int64{w.ID}, DeleteBranches: true}, 200, &result)
	if len(result.Items) != 0 {
		t.Fatal("another user's worktree exposed", result)
	}
	alex.want("POST", "/api/v1/worktrees/cleanup", cleanupInput{Preview: true}, 200, &result)
	if len(result.Items) != 1 || result.Items[0].Status != "ready" {
		t.Fatalf("preview: %+v", result)
	}
	// Preview is read-only with respect to the worktree lifecycle.
	got, _ := s.store.GetWorktree(ctx, au.ID, w.ID)
	if got.Status != "active" {
		t.Fatal("preview archived")
	}
	s.autoCleanupWorktrees(ctx)
	got, _ = s.store.GetWorktree(ctx, au.ID, w.ID)
	if got.Status != "active" {
		t.Fatal("auto cleanup enabled by default")
	}
	alex.want("PUT", "/api/v1/settings", map[string]any{"worktree_auto_cleanup": true}, 200, nil)
	if s.store.UserSetting(ctx, bu.ID, "worktree_auto_cleanup") == "1" {
		t.Fatal("setting leaked across users")
	}
	// Turning it on runs a pass right away rather than an hour later.
	for i := 0; i < 100; i++ {
		if s.store.UserSetting(ctx, au.ID, "worktree_cleanup_result") != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ = s.store.GetWorktree(ctx, au.ID, w.ID)
	if got.Status != "archived" || got.ArchiveRef == "" {
		t.Fatalf("automatic cleanup: %+v", got)
	}
	if _, err := s.store.GetThread(ctx, au.ID, thread.ID); err != nil {
		t.Fatal("conversation lost", err)
	}
	alex.want("GET", "/api/v1/worktrees/cleanup", nil, 200, &result)
	if len(result.Archived) != 1 {
		t.Fatalf("last result: %+v", result)
	}
	unlock, err := s.useWorktreeFolder(ctx, 0, 0, w.Path)
	unlock()
	if err == nil {
		t.Fatal("direct conversation allowed in archived folder")
	}
	// Restore remains available through the normal UI action.
	alex.want("POST", fmt.Sprintf("/api/v1/worktrees/%d/restore", w.ID), map[string]any{}, 200, nil)
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal("restore failed", err)
	}
}

func TestCleanupDiscoversExactMergedPR(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = ""
	_, u := person(t, s, h, "pr-cleanup@example.test", "member", true)
	w, root := cleanupRepo(t)
	remote := filepath.Join(root, "remote.git")
	gitT(t, w.Repo, "init", "--bare", remote)
	gitT(t, w.Repo, "remote", "add", "origin", remote)
	gitT(t, w.Repo, "push", "origin", "main")
	head := gitT(t, w.Path, "rev-parse", "HEAD")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	// Fake only the forge response; all Git operations use real repositories.
	response := filepath.Join(root, "prs.json")
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat \"$CLEANUP_PR_FIXTURE\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLEANUP_PR_FIXTURE", response)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w.UserID = u.ID
	if err := s.store.CreateWorktree(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, head, base, branch, state string
		cross, ready                    bool
	}{
		{"squash exact", head, "main", "topic", "MERGED", false, true},
		{"old PR", w.BaseSHA, "main", "topic", "MERGED", false, false},
		{"wrong base", head, "release", "topic", "MERGED", false, false},
		{"wrong branch", head, "main", "another", "MERGED", false, false},
		{"closed", head, "main", "topic", "CLOSED", false, false},
		{"fork", head, "main", "topic", "MERGED", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal([]map[string]any{{"state": tc.state, "headRefOid": tc.head, "headRefName": tc.branch, "baseRefName": tc.base, "isCrossRepository": tc.cross, "url": "https://github.com/example/repo/pull/1"}})
			if err := os.WriteFile(response, data, 0644); err != nil {
				t.Fatal(err)
			}
			result := s.cleanupOne(context.Background(), u.ID, w.ID, cleanupInput{Preview: true})
			if (result.Status == "ready") != tc.ready {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestCleanupKeepsActiveWork(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = ""
	_, u := person(t, s, h, "busy-cleanup@example.test", "member", true)
	w, _ := cleanupRepo(t)
	gitT(t, w.Repo, "merge", "--ff-only", w.Branch)
	w.UserID = u.ID
	ctx := context.Background()
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	// A direct conversation in a subfolder also blocks cleanup.
	th := &store.Thread{ID: "direct-work", UserID: u.ID, Provider: "codex", Cwd: w.Path + "/sub", Title: "Work"}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func() func()
	}{
		{"running before status event", func() func() { s.rt.runs[th.ID] = &run{running: true}; return func() { delete(s.rt.runs, th.ID) } }},
		{"queued", func() func() {
			s.rt.runs[th.ID] = &run{queue: []queued{{key: "waiting"}}}
			return func() { delete(s.rt.runs, th.ID) }
		}},
		{"held", func() func() { s.holds().hold(th.ID); return func() { s.holds().release(th.ID) } }},
		{"restart check", func() func() { s.re.markPending([]string{th.ID}); return func() { s.re.resolve(th.ID) } }},
		{"terminal", func() func() {
			s.terms.terms = map[string]*terminal{"test": {Cwd: w.Path + "/sub"}}
			return func() { s.terms.terms = nil }
		}},
		{"setup", func() func() {
			w.Setup = "running"
			_ = s.store.UpdateWorktree(ctx, w)
			return func() { w.Setup = ""; _ = s.store.UpdateWorktree(ctx, w) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			undo := tc.set()
			defer undo()
			result := s.cleanupOne(ctx, u.ID, w.ID, cleanupInput{DeleteBranches: true})
			if result.Status != "skipped" {
				t.Fatalf("busy work deleted: %+v", result)
			}
			if _, err := os.Stat(w.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
	unlock, err := s.useWorktreeFolder(ctx, 0, 0, th.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	result := s.cleanupOne(ctx, u.ID, w.ID, cleanupInput{DeleteBranches: true})
	unlock()
	if result.Status != "skipped" || !strings.Contains(result.Reason, "in use") {
		t.Fatalf("use gate: %+v", result)
	}
}

// ageWorktree makes everything in a cleanupRepo fixture look old: the
// checkout's reflog, its files, and the records Pocket AI keeps.
func ageWorktree(t *testing.T, s *Server, w *store.Worktree, days int, files ...string) {
	t.Helper()
	old := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	paths := []string{gitT(t, w.Path, "rev-parse", "--path-format=absolute", "--git-path", "logs/HEAD")}
	for _, f := range files {
		paths = append(paths, filepath.Join(w.Path, f))
	}
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	at := old.UTC().Format(time.RFC3339Nano)
	if _, err := s.store.DB.Exec(`UPDATE worktrees SET created_at = ? WHERE id = ?`, at, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`UPDATE threads SET created_at = ?, activity_at = ?, prompt_at = ? WHERE worktree_id = ?`, at, at, at, w.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIdleCleanupArchivesAndRestoresEverything(t *testing.T) {
	t.Setenv("GIT_COMMITTER_DATE", time.Now().Add(-40*24*time.Hour).Format(time.RFC3339))
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = ""
	_, u := person(t, s, h, "idle-cleanup@example.test", "member", true)
	w, _ := cleanupRepo(t) // one unmerged commit
	_ = os.WriteFile(filepath.Join(w.Path, "draft"), []byte("unsaved idea\n"), 0644)
	_ = os.WriteFile(filepath.Join(w.Path, "file"), []byte("half done\n"), 0644)
	head := gitT(t, w.Path, "rev-parse", "HEAD")
	w.UserID = u.ID
	ctx := context.Background()
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	th := &store.Thread{ID: "idle-thread", UserID: u.ID, Provider: "codex", Cwd: w.Path, WorktreeID: w.ID, Title: "Old idea"}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	in := cleanupInput{DeleteBranches: true, IdleDays: 7}

	// Recent prompt: kept, whatever the files say.
	ageWorktree(t, s, w, 30, "draft", "file")
	if _, err := s.store.DB.Exec(`UPDATE threads SET activity_at = ? WHERE id = ?`, store.Now(), th.ID); err != nil {
		t.Fatal(err)
	}
	if r := s.cleanupOne(ctx, u.ID, w.ID, in); r.Status != "skipped" || !strings.Contains(r.Reason, "last active today") || !strings.Contains(r.Reason, "Uncommitted") {
		t.Fatalf("recent prompt: %+v", r)
	}
	// Recent edit to an uncommitted file: kept.
	ageWorktree(t, s, w, 30, "draft", "file")
	if err := os.Chtimes(filepath.Join(w.Path, "draft"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := s.cleanupOne(ctx, u.ID, w.ID, in); r.Status != "skipped" || !strings.Contains(r.Reason, "today") {
		t.Fatalf("recent edit: %+v", r)
	}
	// Recent commit or checkout (this checkout's reflog): kept.
	ageWorktree(t, s, w, 30, "draft", "file")
	reflog := gitT(t, w.Path, "rev-parse", "--path-format=absolute", "--git-path", "logs/HEAD")
	_ = os.Chtimes(reflog, time.Now(), time.Now())
	if r := s.cleanupOne(ctx, u.ID, w.ID, in); r.Status != "skipped" || !strings.Contains(r.Reason, "today") {
		t.Fatalf("recent commit: %+v", r)
	}
	ageWorktree(t, s, w, 30, "draft", "file")
	m := runSh(t, wtIdleInspect, w.Path, w.Repo, "")
	if act, _ := strconv.ParseInt(m["act"], 10, 64); time.Since(time.Unix(act, 0)) < 29*24*time.Hour || m["dirty"] != "1" {
		t.Fatalf("aged inspect: %v", m)
	}
	// Preview says ready and touches nothing.
	if r := s.cleanupOne(ctx, u.ID, w.ID, cleanupInput{Preview: true, IdleDays: 7, IdleOnly: true}); r.Status != "ready" {
		t.Fatalf("preview: %+v", r)
	}
	// A change after the check stops the archive.
	if _, _, err := shOn(ctx, nil, time.Minute, wtIdleArchive, w.Path, w.Repo, w.Branch, head, strconv.FormatInt(time.Now().Add(-40*24*time.Hour).Unix(), 10), "refs/pocket-ai/archive/x"); err == nil {
		t.Fatal("archived despite newer changes")
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal("stopped archive removed folder", err)
	}

	r := s.cleanupOne(ctx, u.ID, w.ID, in)
	if r.Status != "archived" || !strings.Contains(r.Reason, "uncommitted work") {
		t.Fatalf("idle cleanup: %+v", r)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("folder remains", err)
	}
	if gitT(t, w.Repo, "rev-parse", "refs/heads/topic") != head {
		t.Fatal("idle cleanup must keep the branch")
	}
	got, _ := s.store.GetWorktree(ctx, u.ID, w.ID)
	if got.Status != "archived" || got.ArchiveRef == "" || got.MergedAt != "" {
		t.Fatalf("record: %+v", got)
	}
	if _, err := s.store.GetThread(ctx, u.ID, th.ID); err != nil {
		t.Fatal("conversation lost", err)
	}
	runSh(t, wtRestore, w.Repo, w.Path, got.Branch, got.ArchiveRef)
	if gitT(t, w.Path, "rev-parse", "HEAD") != head {
		t.Fatal("restore lost commits")
	}
	for f, want := range map[string]string{"draft": "unsaved idea\n", "file": "half done\n"} {
		if b, _ := os.ReadFile(filepath.Join(w.Path, f)); string(b) != want {
			t.Fatalf("restore lost %s: %q", f, b)
		}
	}
}

func TestIdleCleanupSetting(t *testing.T) {
	t.Setenv("GIT_COMMITTER_DATE", time.Now().Add(-40*24*time.Hour).Format(time.RFC3339))
	s, h, _ := testServer(t)
	s.cfg.PeopleDir = ""
	alex, au := person(t, s, h, "idle-setting@example.test", "member", true)
	ctx := context.Background()
	w, _ := cleanupRepo(t)
	w.UserID = au.ID
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	ageWorktree(t, s, w, 10)
	alex.want("PUT", "/api/v1/settings", map[string]any{"worktree_idle_days": 999}, 400, nil)
	// Merged cleanup stays off; idle cleanup alone runs a pass right away.
	var st store.Settings
	alex.want("PUT", "/api/v1/settings", map[string]any{"worktree_idle_days": 30}, 200, &st)
	if st.WorktreeIdleDays != 30 || st.WorktreeAutoCleanup {
		t.Fatalf("settings: %+v", st)
	}
	for i := 0; i < 100 && s.store.UserSetting(ctx, au.ID, "worktree_cleanup_result") == ""; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if got, _ := s.store.GetWorktree(ctx, au.ID, w.ID); got.Status != "active" {
		t.Fatal("archived before 30 idle days")
	}
	_ = s.store.SetUserSetting(ctx, au.ID, "worktree_idle_days", "7")
	s.autoCleanupWorktrees(ctx)
	if got, _ := s.store.GetWorktree(ctx, au.ID, w.ID); got.Status != "archived" {
		t.Fatalf("idle cleanup did not run: %+v", got)
	}
	if out := gitT(t, w.Repo, "branch", "--list", "topic"); out == "" {
		t.Fatal("unmerged branch deleted")
	}
}
