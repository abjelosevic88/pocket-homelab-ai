package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

type ctxKey int

const userKey ctxKey = 1

const sessionCookie = "pai_session"

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

func actorID(ctx context.Context) int64 {
	if u := userFrom(ctx); u != nil {
		return u.ID
	}
	return 0
}

// requireAuth resolves the current user from a session cookie, bearer token,
// or trusted proxy header depending on AUTH_MODE.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.resolveUser(r)
		if err != nil || u == nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) resolveUser(r *http.Request) (*store.User, error) {
	ctx := r.Context()
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		tok = strings.TrimSpace(h[7:])
	} else if t := r.Header.Get("X-API-Key"); t != "" {
		tok = t
	}
	if tok == "" && r.URL.Query().Get("access_token") != "" && strings.HasSuffix(r.URL.Path, "/events") {
		// EventSource cannot send headers; scripts may pass the token here.
		tok = r.URL.Query().Get("access_token")
	}
	if tok != "" {
		if s.core.Configured() && core.IsCoreToken(tok) {
			return s.coreUser(ctx, tok)
		}
		return s.store.GetUserByAPIToken(ctx, tok)
	}
	switch s.cfg.AuthMode {
	case "none":
		return s.firstOrSystemUser(ctx, "admin@localhost", "Admin")
	case "proxy":
		name := r.Header.Get(s.cfg.AuthProxyHeader)
		if name == "" {
			return nil, http.ErrNoCookie
		}
		email := r.Header.Get(s.cfg.AuthProxyEmailHeader)
		if email == "" {
			email = name
			if !strings.Contains(email, "@") {
				email += "@proxy"
			}
		}
		return s.firstOrSystemUser(ctx, strings.ToLower(email), name)
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, err
	}
	return s.sessionUser(ctx, c.Value)
}

// coreUser validates a token Pocket Core issued to a paired phone (pcd_), a
// person (pcu_) or an OIDC client (pco_) and maps it to the local account
// with the same email, creating a member on first sight.
func (s *Server) coreUser(ctx context.Context, tok string) (*store.User, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !in.Active || in.Delegated() || in.User == nil || in.User.Email == "" {
		return nil, http.ErrNoCookie
	}
	return s.coreIdentityUser(ctx, in)
}

func (s *Server) firstOrSystemUser(ctx context.Context, email, name string) (*store.User, error) {
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		return u, nil
	}
	role := "member"
	if n, _ := s.store.CountUsers(ctx); n == 0 {
		role = "admin"
	}
	u := &store.User{Email: email, Name: name, Role: role}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	s.seedUser(ctx, u)
	s.log.Info("auto-provisioned user", "email", email, "mode", s.cfg.AuthMode, "role", role)
	return u, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, id string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	st, _ := s.store.GetSettings(r.Context(), 0)
	u, _ := s.resolveUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup":     n == 0 && s.cfg.AuthMode == "local",
		"core_configured": s.core.Configured(),
		"auth_mode":       s.cfg.AuthMode,
		"authenticated":   u != nil,
		"user":            u,
		"version":         Version,
		"title":           st.Title,
		"oidc":            s.oidcEnabled() && s.cfg.AuthMode == "local",
		"oidc_name":       s.oidcName(),
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	if n > 0 || s.cfg.AuthMode != "local" {
		writeErr(w, http.StatusForbidden, "setup already completed")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "email and a password of at least 8 characters are required")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	u := &store.User{Email: in.Email, Name: in.Name, PasswordHash: string(hash), Role: "admin"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	s.seedUser(ctx, u)
	st, _ := s.store.GetSettings(ctx, 0)
	st.SetupComplete = true
	_ = s.store.SaveBoxSettings(ctx, st)
	id, err := s.store.CreateSession(ctx, u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	s.log.Info("initial setup completed", "email", u.Email)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	u, err := s.store.GetUserByEmail(r.Context(), strings.TrimSpace(strings.ToLower(in.Email)))
	if err != nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	id, err := s.store.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r.Context()))
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var in struct {
		Name            string `json:"name"`
		Email           string `json:"email"`
		Username        string `json:"username"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name != "" {
		u.Name = in.Name
	}
	if in.Email != "" {
		u.Email = strings.ToLower(strings.TrimSpace(in.Email))
	}
	if in.Username != "" {
		name := store.Slug(in.Username)
		if name == "" || len(name) > 40 {
			writeErr(w, http.StatusBadRequest, "the CalDAV name may hold letters, digits, dots, dashes and underscores")
			return
		}
		u.Username = name
	}
	if in.NewPassword != "" {
		if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.CurrentPassword)) != nil {
			writeErr(w, http.StatusBadRequest, "current password is incorrect")
			return
		}
		if len(in.NewPassword) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	s.rt.loadShares() // names in shared threads
	writeJSON(w, http.StatusOK, u)
}

// ---- users (household members) ----

// handleListUsers lists the accounts for admins, who manage them; a member
// sees only their own (the share picker uses /people instead).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if u := userFrom(r.Context()); u == nil || u.Role != "admin" {
		writeJSON(w, http.StatusOK, []*store.User{u})
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "list users")
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if u := userFrom(r.Context()); u == nil || u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only an admin can manage users")
		return false
	}
	return true
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		CanRun   *bool  `json:"can_run"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
		return
	}
	if s.cfg.AuthMode == "local" && len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if in.Role != "admin" {
		in.Role = "member"
	}
	u := &store.User{Email: in.Email, Name: strings.TrimSpace(in.Name), Role: in.Role, Username: store.Slug(in.Username), CanRun: in.CanRun != nil && *in.CanRun}
	if in.Password != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	s.seedUser(r.Context(), u)
	s.log.Info("user created", "email", u.Email, "role", u.Role, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	u, err := s.store.GetUser(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		CanRun   *bool  `json:"can_run"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Email != "" {
		u.Email = strings.TrimSpace(strings.ToLower(in.Email))
	}
	if n := store.Slug(in.Username); n != "" {
		u.Username = n
	}
	if in.Name != "" {
		u.Name = strings.TrimSpace(in.Name)
	}
	if in.Role == "admin" || in.Role == "member" {
		if in.Role == "member" && u.Role == "admin" {
			if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
				writeErr(w, http.StatusBadRequest, "there must be at least one admin")
				return
			}
		}
		u.Role = in.Role
	}
	if in.CanRun != nil {
		u.CanRun = *in.CanRun
	}
	if in.Password != "" {
		if len(in.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	s.rt.loadShares() // names in shared threads
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := idParam(r, "id")
	if id == userFrom(r.Context()).ID {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	target, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	if target.Role == "admin" {
		if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
			writeErr(w, http.StatusBadRequest, "there must be at least one admin")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, err, "delete user")
		return
	}
	s.rt.loadShares() // their shares went with them
	w.WriteHeader(http.StatusNoContent)
}

// ---- tokens ----

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.ListAPITokens(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "list tokens")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	_ = decode(r, &in)
	if in.Name == "" {
		in.Name = "token"
	}
	plain, t, err := s.store.CreateAPIToken(r.Context(), userFrom(r.Context()).ID, in.Name)
	if err != nil {
		s.fail(w, err, "create token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": plain, "info": t})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIToken(r.Context(), userFrom(r.Context()).ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
