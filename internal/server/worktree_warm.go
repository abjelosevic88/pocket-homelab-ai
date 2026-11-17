package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A conversation another app prepares in a worktree (Projects' "Chat in a
// worktree") used to spend all its git work after the person pressed send:
// look at the repo twice, read its worktree config twice, fetch the base,
// then make the worktree — ~6 s on a machine across the ocean, each ssh
// round trip a quarter of a second, before the agent even started. Now the
// draft warms up while the person reads the prompt: the repo is looked at,
// the config read and the base fetched once, and the start reuses that.

// repoMemoTTL: how long a start may reuse what the warm-up saw of a repo
// (its folders and branches, the .pocket-ai.json) — the repo does not move
// while someone reads a prompt.
const repoMemoTTL = 2 * time.Minute

// baseFreshFor: a base fetched this recently is not fetched again for the
// next worktree on it.
const baseFreshFor = 5 * time.Minute

type repoMemo struct {
	at time.Time
	v  any
}

var (
	repoMemos   sync.Map // "<server>|<kind>|<host>|<key>" → repoMemo
	baseFetches sync.Map // the same key → chan struct{} closed when that fetch is done
)

// useRepoMemo (a context key) lets inspectRepo and repoConfig reuse what a
// warm-up saw; anything else always asks git.
type useRepoMemo struct{}

func withRepoMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, useRepoMemo{}, true)
}

func repoMemoOn(ctx context.Context) bool {
	on, _ := ctx.Value(useRepoMemo{}).(bool)
	return on
}

func hostIDOf(h *store.Host) int64 {
	if h == nil {
		return 0
	}
	return h.ID
}

func (s *Server) repoMemoKey(kind string, hostID int64, key string) string {
	return fmt.Sprintf("%p|%s|%d|%s", s, kind, hostID, key)
}

func recentMemo(key string, ttl time.Duration) (any, bool) {
	m, ok := repoMemos.Load(key)
	if !ok || time.Since(m.(repoMemo).at) > ttl {
		return nil, false
	}
	return m.(repoMemo).v, true
}

// inspectRepoMemo is inspectRepo, reusing a recent answer when ctx allows.
func (s *Server) inspectRepoMemo(ctx context.Context, h *store.Host, dir string) (repoInfo, error) {
	if !repoMemoOn(ctx) {
		return inspectRepo(ctx, h, dir)
	}
	key := s.repoMemoKey("inspect", hostIDOf(h), dir)
	if v, ok := recentMemo(key, repoMemoTTL); ok {
		return v.(repoInfo), nil
	}
	info, err := inspectRepo(ctx, h, dir)
	if err == nil {
		repoMemos.Store(key, repoMemo{at: time.Now(), v: info})
	}
	return info, err
}

// readRepoConfig runs wtReadConfig (the repo's .pocket-ai.json and origin),
// reusing a recent answer when ctx allows.
func (s *Server) readRepoConfig(ctx context.Context, h *store.Host, hostID int64, repo string) (string, error) {
	key := s.repoMemoKey("config", hostID, repo)
	if repoMemoOn(ctx) {
		if v, ok := recentMemo(key, repoMemoTTL); ok {
			return v.(string), nil
		}
	}
	out, _, err := shOn(ctx, h, 20*time.Second, wtReadConfig, repo)
	if err == nil {
		repoMemos.Store(key, repoMemo{at: time.Now(), v: out})
	}
	return out, err
}

// freshenBase fast-forwards the local base branch to origin's, once per
// baseFreshFor: a start right after the warm-up (or another worktree on the
// same base) skips the fetch, and one that comes while the warm-up still
// fetches waits for that fetch instead of running a second one beside it.
func (s *Server) freshenBase(ctx context.Context, h *store.Host, repo, base string) {
	base = strings.TrimPrefix(base, "origin/")
	key := s.repoMemoKey("base", hostIDOf(h), repo+"\x00"+base)
	for {
		if _, ok := recentMemo(key, baseFreshFor); ok {
			return
		}
		done := make(chan struct{})
		if other, busy := baseFetches.LoadOrStore(key, done); busy {
			select {
			case <-other.(chan struct{}):
				continue // it finished (or failed: then this one tries)
			case <-ctx.Done():
				return
			}
		}
		if _, errOut, err := shOn(ctx, h, 45*time.Second, wtFastForward, repo, base); err != nil {
			s.log.Info("worktree base not brought up to date", "repo", repo, "base", base, "err", strings.TrimSpace(errOut))
		} else {
			repoMemos.Store(key, repoMemo{at: time.Now()})
		}
		baseFetches.Delete(key)
		close(done)
		return
	}
}

// warmDraftWorktree does a worktree draft's git work ahead of its send (in
// the background, errors left for the start to report): it also opens the
// ssh connection to that machine, which the start then shares.
func (s *Server) warmDraftWorktree(u *store.User, d *store.LinkDraft) {
	if d.Worktree == nil || strings.TrimSpace(d.Worktree.Name) == "" || d.Cwd == "" {
		return
	}
	spec := newWorktree{Mode: "new", Name: strings.TrimSpace(d.Worktree.Name), Base: strings.TrimSpace(d.Worktree.Base)}
	go func() {
		ctx, cancel := context.WithTimeout(withRepoMemo(s.rt.ctx), 90*time.Second)
		defer cancel()
		h, err := s.checkHostUse(ctx, u, d.HostID)
		if err != nil {
			return
		}
		s.ticketWorktree(ctx, u, h, d.Cwd, spec)
	}()
}

// draftsStarting: drafts whose start is running in this process. A claimed
// draft without a thread that is not in here is left over from a start that
// died (a restart), and may be sent again.
var draftsStarting sync.Map // "<server>|<draft id>" → struct{}

func (s *Server) draftStartKey(id string) string { return fmt.Sprintf("%p|%s", s, id) }

// draftClaimedHook (tests) runs when a draft start has claimed its draft.
var draftClaimedHook func()
