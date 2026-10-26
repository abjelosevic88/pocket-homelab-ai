package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// ticketWorktree turns an app's "a worktree for this ticket" into what to do
// now: the person's worktree that is already on that branch, the branch itself
// when the repo has it (an earlier conversation made it), else a new branch of
// that name. Anything it can't tell is left to createWorktree to report.
func (s *Server) ticketWorktree(ctx context.Context, u *store.User, cwd string, spec newWorktree) *newWorktree {
	info, err := inspectRepo(ctx, nil, cwd)
	if err != nil || !info.Repo {
		return &spec
	}
	if list, err := s.store.ListWorktrees(ctx, u.ID, "active"); err == nil {
		for _, w := range list {
			if w.HostID == 0 && w.Repo == info.Main && w.Branch == spec.Name {
				return &newWorktree{Mode: "existing", ID: w.ID}
			}
		}
	}
	if out, _, err := shOn(ctx, nil, 20*time.Second, wtBranches, info.Main); err == nil {
		for _, l := range splitLines(out) {
			if b := strings.TrimPrefix(l, "b="); b == spec.Name || b == "origin/"+spec.Name {
				return &newWorktree{Mode: "branch", Branch: spec.Name}
			}
		}
	}
	return &spec
}

// linkWorktree is a worktree of an item (a ticket), for the app to list.
type linkWorktree struct {
	ID        int64    `json:"id"`
	Branch    string   `json:"branch"`
	Base      string   `json:"base"`
	Repo      string   `json:"repo"`
	Path      string   `json:"path"`
	Host      string   `json:"host,omitempty"` // "" = this server
	Status    string   `json:"status"`         // active | archived
	Setup     string   `json:"setup"`
	MergedAt  string   `json:"merged_at"`
	PRURL     string   `json:"pr_url"`
	CreatedAt string   `json:"created_at"`
	Threads   []string `json:"thread_ids"`
	Live      bool     `json:"live"` // an agent works in it now
	// from git (active ones): commits on the branch, changed files, unpushed
	Ahead    int    `json:"ahead"`
	Dirty    int    `json:"dirty"`
	Unpushed int    `json:"unpushed"`
	Merged   bool   `json:"merged"`
	PRState  string `json:"pr_state,omitempty"`
	URL      string `json:"url"`
}

// wtStateCache keeps git's word on a worktree briefly: apps poll their item pages.
var wtStateCache sync.Map // "<server>|<id>" → wtCachedState

type wtCachedState struct {
	at time.Time
	st wtStatusInfo
}

// handleLinkWorktrees lists the person's worktrees about one item: the ones
// its conversations work in, and any whose branch is named after it (made by
// hand, in a terminal or by an earlier conversation).
func (s *Server) handleLinkWorktrees(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	source, ref := strings.ToLower(q.Get("source")), strings.TrimSpace(q.Get("ref"))
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	ids := map[int64]bool{}
	if drafts, err := s.store.LinkDrafts(ctx, u.ID, source, ref); err == nil {
		for _, d := range drafts {
			if d.ThreadID == nil {
				continue
			}
			if t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID); err == nil && t.WorktreeID != 0 {
				ids[t.WorktreeID] = true
			}
		}
	}
	named := regexp.MustCompile(`(^|[/_.-])` + regexp.QuoteMeta(strings.ToLower(ref)) + `($|[/_.-])`)
	list, err := s.store.ListWorktrees(ctx, u.ID, "")
	if err != nil {
		s.fail(w, err, "worktrees")
		return
	}
	out := []linkWorktree{}
	for _, wt := range list {
		if !ids[wt.ID] && !named.MatchString(strings.ToLower(wt.Branch)) && (q.Get("branch") == "" || wt.Branch != q.Get("branch")) {
			continue
		}
		lw := linkWorktree{ID: wt.ID, Branch: wt.Branch, Base: wt.Base, Repo: wt.Repo, Path: wt.Path, Status: wt.Status, Setup: wt.Setup, MergedAt: wt.MergedAt,
			PRURL: wt.PRURL, CreatedAt: wt.CreatedAt, Threads: []string{}, URL: s.cfg.BaseURL + "/worktrees?open=" + strconv.FormatInt(wt.ID, 10)}
		h, herr := s.host(ctx, wt.HostID)
		if h != nil {
			lw.Host = h.Name
		}
		for _, id := range s.store.WorktreeThreads(ctx, wt.ID) {
			if t, err := s.store.GetThread(ctx, u.ID, id); err == nil {
				lw.Threads = append(lw.Threads, id)
				if s.rt.decorate(t).Status == "running" {
					lw.Live = true
				}
			}
		}
		if wt.Status == "active" && herr == nil {
			key := fmt.Sprintf("%p|%d", s, wt.ID)
			c, ok := wtStateCache.Load(key)
			if !ok || time.Since(c.(wtCachedState).at) > 20*time.Second {
				c = wtCachedState{at: time.Now(), st: worktreeStatus(ctx, h, wt)}
				wtStateCache.Store(key, c)
			}
			st := c.(wtCachedState).st
			lw.Ahead, lw.Dirty, lw.Unpushed, lw.Merged, lw.PRState = st.Ahead, st.Dirty, st.Unpushed, st.Merged, st.PRState
		}
		out = append(out, lw)
	}
	writeJSON(w, http.StatusOK, out)
}
