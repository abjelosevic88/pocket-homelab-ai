package server

import (
	"context"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"strings"
)

func (s *Server) coreIdentityUser(ctx context.Context, in *core.Introspection) (*store.User, error) {
	issuer, subject := s.core.IdentityKey(in)
	return s.identityUser(ctx, issuer, subject, in.User.Email, in.User.Name, true)
}
func (s *Server) identityUser(ctx context.Context, issuer, subject, email, name string, create bool) (*store.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	id, err := s.core.ResolveIdentity(ctx, issuer, subject, func() (int64, error) {
		u, err := s.store.GetUserByEmail(ctx, email)
		if err != nil && create {
			u, err = s.firstOrSystemUser(ctx, email, name)
		}
		if err != nil {
			return 0, err
		}
		return u.ID, nil
	})
	if err != nil {
		return nil, err
	}
	// Keep background integrations using the person's current verified email.
	if _, err = s.store.DB.ExecContext(ctx, `UPDATE users SET email=?, name=CASE WHEN ?='' THEN name ELSE ? END WHERE id=?`, email, name, name, id); err != nil {
		return nil, err
	}
	return s.store.GetUser(ctx, id)
}
func (s *Server) sessionUser(ctx context.Context, id string) (*store.User, error) {
	u, err := s.store.GetSessionUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.core.ValidateSession(ctx, id); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Server) coreOutboxHealth(ctx context.Context) any {
	status, err := s.core.OutboxStatus(ctx)
	if err != nil {
		return map[string]any{"available": false}
	}
	return status
}
