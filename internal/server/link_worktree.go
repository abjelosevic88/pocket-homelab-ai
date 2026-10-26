package server

import (
	"context"
	"strings"
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
