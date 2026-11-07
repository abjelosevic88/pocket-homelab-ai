package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) useWorktree(id int64) func() {
	if id <= 0 {
		return func() {}
	}
	mu := &s.wtUseMu[uint64(id)%uint64(len(s.wtUseMu))]
	mu.RLock()
	return mu.RUnlock
}

// Sends and terminal starts use the same gate, including conversations
// opened in a worktree folder without an explicit worktree_id.
func (s *Server) useWorktreeFolder(ctx context.Context, hostID, id int64, folder string) (func(), error) {
	if id == 0 {
		wt, err := s.store.WorktreeForFolder(ctx, hostID, path.Clean(folder))
		if errors.Is(err, store.ErrNotFound) {
			return func() {}, nil
		}
		if err != nil {
			return func() {}, err
		}
		id = wt.ID
	}
	unlock := s.useWorktree(id)
	wt, err := s.store.GetWorktreeAny(ctx, id)
	if err == nil && wt.Status != "active" {
		err = errConflict("This worktree is archived; restore it from Worktrees to continue")
	}
	if err != nil {
		unlock()
		return func() {}, err
	}
	return unlock, nil
}

func (s *Server) lockWorktreeRequest(r *http.Request) func() {
	id, _ := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	mu := &s.wtUseMu[id%uint64(len(s.wtUseMu))]
	mu.Lock()
	return mu.Unlock
}

type cleanupItem struct {
	ID     int64  `json:"id"`
	Branch string `json:"branch"`
	Status string `json:"status"` // ready | archived | skipped
	Reason string `json:"reason"`
}
type cleanupResult struct {
	Archived  []string      `json:"archived"`
	Skipped   []string      `json:"skipped"`
	Items     []cleanupItem `json:"items"`
	CheckedAt string        `json:"checked_at"`
}
type cleanupInput struct {
	DeleteBranches bool    `json:"delete_branches"`
	Preview        bool    `json:"preview"`
	IDs            []int64 `json:"ids"` // nil = all; [] = none
}

// A separate path from manual Archive: never force removal, run arbitrary
// teardown commands, or use a stale display badge as permission to delete.
func (s *Server) handleCleanupWorktrees(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in cleanupInput
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	result, err := s.cleanupWorktrees(ctx, me(r), in)
	if err != nil {
		s.fail(w, err, "clean up worktrees")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) cleanupWorktrees(ctx context.Context, uid int64, in cleanupInput) (cleanupResult, error) {
	result := cleanupResult{Archived: []string{}, Skipped: []string{}, Items: []cleanupItem{}, CheckedAt: store.Now()}
	if !s.wtCleanupMu.TryLock() {
		return result, errConflict("a worktree cleanup is already running; try again shortly")
	}
	defer s.wtCleanupMu.Unlock()
	list, err := s.store.ListWorktrees(ctx, uid, "active")
	if err != nil {
		return result, err
	}
	selected := map[int64]bool{}
	for _, id := range in.IDs {
		selected[id] = true
	}
	for _, wt := range list {
		if in.IDs != nil && !selected[wt.ID] {
			continue
		}
		item := cleanupItem{ID: wt.ID, Branch: wt.Branch, Status: "skipped", Reason: "Check timed out; try again"}
		if ctx.Err() == nil {
			item = s.cleanupOne(ctx, uid, wt.ID, in)
		}
		result.Items = append(result.Items, item)
		if item.Status == "archived" {
			result.Archived = append(result.Archived, item.Branch)
		}
		if item.Status == "skipped" {
			result.Skipped = append(result.Skipped, item.Branch)
		}
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if len(result.Archived) > 0 {
		s.publishWorktrees(uid)
	}
	// Retain the latest real result for background runs and page reloads.
	if !in.Preview {
		data, err := json.Marshal(result)
		if err == nil {
			err = s.store.SetUserSetting(saveCtx, uid, "worktree_cleanup_result", string(data))
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Server) cleanupOne(ctx context.Context, uid, id int64, in cleanupInput) cleanupItem {
	mu := &s.wtUseMu[uint64(id)%uint64(len(s.wtUseMu))]
	item := cleanupItem{ID: id, Status: "skipped"}
	if !mu.TryLock() {
		if wt, err := s.store.GetWorktree(ctx, uid, id); err == nil {
			item.Branch = wt.Branch
		}
		item.Reason = "Worktree is in use; try again shortly"
		return item
	}
	defer mu.Unlock()
	wt, err := s.store.GetWorktree(ctx, uid, id)
	if err != nil {
		item.Reason = "Worktree is no longer available"
		return item
	}
	item.Branch = wt.Branch
	if wt.Status != "active" {
		item.Reason = "Already archived"
		return item
	}
	if wt.Setup == "running" {
		item.Reason = "Setup is running"
		return item
	}
	if len(s.busyThreads(ctx, wt)) > 0 {
		item.Reason = "An agent is working or has queued messages"
		return item
	}
	// Holds and reattached processes are also work, even before their status
	// has caught up after a restart. Fail closed on a failed thread query.
	threads, err := s.store.WorktreeUse(ctx, wt.HostID, wt.Path, wt.ID)
	if err != nil {
		item.Reason = "Could not check conversations"
		return item
	}
	for _, t := range threads {
		s.rt.decorate(t)
		s.re.mu.Lock()
		pending := s.re.pending[t.ID] != nil
		s.re.mu.Unlock()
		runtimeBusy := false
		if run := s.rt.get(t.ID); run != nil {
			run.mu.Lock()
			runtimeBusy = run.working() || len(run.tasks) > 0 || len(run.pending) > 0 || run.linkDown || run.remote
			run.mu.Unlock()
		}
		if runtimeBusy || t.External || t.Reconnecting || pending || t.Status == "running" || t.Status == "waiting" || t.Queued > 0 || s.holds().held(t.ID) || s.isReattached(t.ID) {
			item.Reason = "An agent is working or has queued messages"
			return item
		}
	}
	s.terms.mu.Lock()
	for _, t := range s.terms.terms {
		t.mu.Lock()
		active := !t.ended && t.HostID == wt.HostID && (path.Clean(t.Cwd) == path.Clean(wt.Path) || strings.HasPrefix(path.Clean(t.Cwd), path.Clean(wt.Path)+"/"))
		t.mu.Unlock()
		if active {
			item.Reason = "A terminal is open in this worktree"
			break
		}
	}
	s.terms.mu.Unlock()
	if item.Reason != "" {
		return item
	}
	h, err := s.host(ctx, wt.HostID)
	if err != nil {
		item.Reason = "Machine is unavailable: " + err.Error()
		return item
	}
	if (h != nil && !s.mayUseHost(ctx, uid, h)) || (h == nil && !s.ownFolder(ctx, uid, wt.Path)) {
		item.Reason = "Access to this machine is no longer allowed"
		return item
	}
	// Custom teardown can mutate files or start background processes. Leave
	// those worktrees to the explicit Archive action, which runs their hooks.
	cfg := s.repoConfig(ctx, h, wt.HostID, wt.Repo).Effective
	if len(cfg.Teardown) > 0 {
		item.Reason = "Custom teardown configured; use Archive from the worktree panel"
		return item
	}
	// Agents often switch to a branch of their own; judge the one checked out.
	out, stderr, err := shOn(ctx, h, 90*time.Second, wtCleanupInspect, wt.Path, wt.Repo, "", wt.Base, wt.BaseSHA, strconv.FormatInt(wt.ID, 10))
	if err != nil {
		item.Reason = "Could not verify merge: " + scriptErr(stderr, err).Error()
		return item
	}
	m := kv(out)
	if m["reason"] != "" {
		item.Reason = m["reason"]
		return item
	}
	head, branch := m["head"], m["branch"]
	if head == "" || branch == "" {
		item.Reason = "Could not verify the current commit"
		return item
	}
	item.Branch = branch
	merged := m["merged"] == "1"
	prURL := ""
	if !merged {
		var prs []struct {
			State  string `json:"state"`
			Head   string `json:"headRefOid"`
			Branch string `json:"headRefName"`
			Base   string `json:"baseRefName"`
			URL    string `json:"url"`
			Cross  bool   `json:"isCrossRepository"`
		}
		if json.Unmarshal([]byte(m["prs"]), &prs) == nil {
			for _, pr := range prs {
				if pr.State == "MERGED" && pr.Head == head && pr.Branch == branch && pr.Base == m["base"] && !pr.Cross {
					merged, prURL = true, pr.URL
					break
				}
			}
		}
	}
	if !merged {
		item.Reason = "Current commits are not confirmed merged"
		if m["pr_error"] == "1" {
			item.Reason += "; PR lookup unavailable"
		}
		return item
	}
	item.Status, item.Reason = "ready", "Merged and safe to clean up"
	if in.Preview {
		return item
	}
	for _, t := range threads {
		s.rt.stop(t.ID)
	}
	ref := "refs/pocket-ai/archive/" + strconv.FormatInt(wt.ID, 10)
	del := "0"
	if in.DeleteBranches {
		del = "1"
	}
	// Rechecks HEAD, branch, registration and files immediately before removal.
	out, stderr, err = shOn(ctx, h, time.Minute, wtCleanupRemove, wt.Path, wt.Repo, branch, head, ref, del)
	if err != nil {
		item.Status, item.Reason = "skipped", "Cleanup stopped: "+scriptErr(stderr, err).Error()
		return item
	}
	// Restore recreates the branch that was actually checked out.
	wt.Branch, wt.Status, wt.ArchivedAt, wt.MergedAt, wt.Setup, wt.ArchiveRef = branch, "archived", store.Now(), store.Now(), "", ref
	if prURL != "" {
		wt.PRURL = prURL
	}
	// Once removal succeeds, finish bookkeeping even if the HTTP client leaves.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.store.UpdateWorktree(saveCtx, wt); err != nil {
		item.Status, item.Reason = "skipped", "Folder removed and recovery saved, but recording the archive failed: "+err.Error()
		return item
	}
	item.Status, item.Reason = "archived", "Cleaned up; conversation and recovery reference kept"
	if in.DeleteBranches && kv(out)["branch_deleted"] != "1" {
		item.Reason += "; local branch retained because it changed or is in use"
	}
	return item
}

func (s *Server) handleWorktreeCleanupResult(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var result *cleanupResult
	raw := s.store.UserSetting(r.Context(), me(r), "worktree_cleanup_result")
	_ = json.Unmarshal([]byte(raw), &result)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) worktreeCleanupLoop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.autoCleanupWorktrees(ctx)
			timer.Reset(time.Hour)
		}
	}
}

func (s *Server) autoCleanupWorktrees(ctx context.Context) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		if u.MayRun() {
			s.autoCleanupUser(ctx, u.ID)
		}
	}
}

// autoCleanupUser runs one automatic pass for a person who opted in; also
// right when they turn it on, instead of an hour later.
func (s *Server) autoCleanupUser(ctx context.Context, uid int64) {
	if s.store.UserSetting(ctx, uid, "worktree_auto_cleanup") != "1" {
		return
	}
	// Bound a pass across offline hosts and leave shutdown cancellable.
	passCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	_, err := s.cleanupWorktrees(passCtx, uid, cleanupInput{DeleteBranches: true})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("automatic worktree cleanup", "user", uid, "error", fmt.Sprint(err))
	}
}
