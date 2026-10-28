package server

import (
	"context"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// seedUser gives a new person a workspace for the home folder and the
// pinned starter recipes, so the first screen has something to tap.
func (s *Server) seedUser(ctx context.Context, u *store.User) {
	if list, err := s.store.ListWorkspaces(ctx, u.ID); err == nil && len(list) == 0 && s.cfg.Home != "" {
		name, home := "Home", s.homeOf(ctx, u) // anyone but the owner: their own projects folder (people.go)
		if s.jailed(ctx, u.ID) {
			name = "Projects"
		}
		ws := &store.Workspace{UserID: u.ID, Name: name, Path: home}
		if err := s.store.SaveWorkspace(ctx, ws); err != nil {
			s.log.Warn("seed workspace", "user", u.Email, "err", err)
		}
	}
	if list, err := s.store.ListRecipes(ctx, u.ID); err == nil && len(list) == 0 {
		for _, r := range starterRecipes {
			if !r.Pinned {
				continue
			}
			rc := r
			rc.UserID = u.ID
			_ = s.store.SaveRecipe(ctx, &rc)
		}
	}
}
