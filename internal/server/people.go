package server

// People: the box owner (the first person, whose logins are the server's
// own: ~/.claude, ~/.codex) works in the server's folders as before.
// Everyone else gets a folder of their own, PEOPLE_DIR/<id>:
//
//	home/      $HOME of their agents: their Claude and Codex logins
//	           (.claude, .codex), worktrees, Ask's private folder
//	projects/  where their agents work (the folder picker's only root)
//
// Their agents, sign-ins and git run in a jail (agent/jail.go) that shows
// them that folder and nothing else of the server's disk. They sign in to
// Claude Code and Codex with their own subscription; the server's login
// and accounts in the owner's home are the owner's alone. Other machines
// are each person's own: theirs are reached with an ssh key in their
// folder (hosts.go), the owner's with the server's.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// jailed reports whether a person works in a folder of their own.
func (s *Server) jailed(ctx context.Context, uid int64) bool {
	return uid > 0 && s.cfg.PeopleDir != "" && !s.isOwner(ctx, uid)
}

func (s *Server) personRoot(uid int64) string {
	return filepath.Join(s.cfg.PeopleDir, strconv.FormatInt(uid, 10))
}

func (s *Server) personHome(uid int64) string { return filepath.Join(s.personRoot(uid), "home") }

func (s *Server) personProjects(uid int64) string {
	return filepath.Join(s.personRoot(uid), "projects")
}

// ensurePerson makes a person's folder (private to the server user).
func (s *Server) ensurePerson(uid int64) error {
	if err := os.MkdirAll(s.cfg.PeopleDir, 0o700); err != nil {
		return err
	}
	for _, d := range []string{s.personRoot(uid), s.personHome(uid), s.personProjects(uid)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// homeOf is where ~ points for a person (nil = the server: background work).
func (s *Server) homeOf(ctx context.Context, u *store.User) string {
	if u != nil && s.jailed(ctx, u.ID) {
		_ = s.ensurePerson(u.ID)
		return s.personProjects(u.ID)
	}
	return s.cfg.Home
}

// rootsOf are the folders a person's agents may work in.
func (s *Server) rootsOf(ctx context.Context, u *store.User) []string {
	if u != nil && s.jailed(ctx, u.ID) {
		_ = s.ensurePerson(u.ID)
		return []string{s.personProjects(u.ID), s.personHome(u.ID)}
	}
	return s.cfg.Roots
}

// pickerRoots are the roots the folder picker offers (a person's home
// holds their logins: it is reachable, but not offered).
func (s *Server) pickerRoots(ctx context.Context, u *store.User) []string {
	if u != nil && s.jailed(ctx, u.ID) {
		return []string{s.personProjects(u.ID)}
	}
	return s.cfg.Roots
}

// ownFolder reports whether dir lies in the person's own folder (always
// true for the owner).
func (s *Server) ownFolder(ctx context.Context, uid int64, dir string) bool {
	if !s.jailed(ctx, uid) {
		return true
	}
	root := s.personRoot(uid)
	return within(filepath.Clean(dir), root) && agent.Confined(dir)
}

// runDir is where the server puts files a person's agent reads (Pocket
// tools' MCP config): inside their jail when they have one.
func (s *Server) runDir(ctx context.Context, uid int64) string {
	if s.jailed(ctx, uid) {
		d := filepath.Join(s.personHome(uid), ".pocket-ai", "run")
		_ = os.MkdirAll(d, 0o700)
		return d
	}
	return s.cfg.DataDir
}

// checkRunFor is the last check before a thread's agent starts here: a
// person other than the owner runs in their own folder, on their own login
// (a thread still on the server's login moves to theirs).
func (s *Server) checkRunFor(ctx context.Context, t *store.Thread) error {
	if !s.jailed(ctx, t.UserID) {
		return nil
	}
	if !s.ownFolder(ctx, t.UserID, t.Cwd) {
		return badRequest("This conversation's folder (" + t.Cwd + ") is outside your own folder, so it cannot run here any more. Start a new conversation in " + s.personProjects(t.UserID) + ".")
	}
	owner, err := s.store.GetUser(ctx, t.UserID)
	if err != nil {
		return err
	}
	if err := s.checkAccount(ctx, owner, t.Provider, t.AccountID); err != nil {
		id, uerr := s.usableAccount(ctx, owner, t.Provider, t.AccountID)
		if uerr != nil {
			return uerr
		}
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE threads SET account_id=? WHERE id=?`, id, t.ID); err != nil {
			return err
		}
		t.AccountID = id
	}
	if on, off, why := agent.JailStatus(); !on && !off {
		return &agent.ErrJailUnavailable{Why: why}
	}
	return nil
}

// askBase holds Pocket Ask's workspace and Claude folders: the server's
// data folder, or for a person with a folder of their own, inside it (the
// CLI runs in their jail and must see them).
func (s *Server) askBase(ctx context.Context, uid int64) string {
	if s.jailed(ctx, uid) {
		return filepath.Join(s.personHome(uid), ".pocket-ai")
	}
	return s.cfg.DataDir
}

// ownCopy maps a folder another app shares for a whole team (Pocket
// Projects: the team's code folder, usually the owner's checkout) to the
// person's own copy of it: the folder of the same name in their projects.
func (s *Server) ownCopy(ctx context.Context, u *store.User, dir string) (string, error) {
	if !s.jailed(ctx, u.ID) || dir == "" || strings.HasPrefix(dir, "~") {
		return dir, nil
	}
	clean := filepath.Clean(dir)
	if s.ownFolder(ctx, u.ID, clean) {
		return clean, nil
	}
	mine := filepath.Join(s.personProjects(u.ID), filepath.Base(clean))
	if fi, err := os.Stat(mine); err == nil && fi.IsDir() && s.ownFolder(ctx, u.ID, mine) {
		return mine, nil
	}
	return "", badRequest("This works in " + clean + ", which is not in your own folder. Clone the repository into " + mine +
		" (Pocket AI → folder picker → Clone) and try again: your agents work on your own copy.")
}

// withUser puts the person into ctx for helpers that read it from there
// (app-link requests authenticate without the session middleware).
func withUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// providersFor is the CLI list as a person sees it: who the server's
// login is signed in as is the owner's business.
func (s *Server) providersFor(ctx context.Context, u *store.User, refresh bool) []agent.Provider {
	list := s.providers(ctx, refresh)
	if s.mayUseServerLogin(ctx, u) {
		return list
	}
	out := make([]agent.Provider, len(list))
	for i, p := range list {
		p.Account = ""
		out[i] = p
	}
	return out
}

// sandboxInfo is what Settings shows about how people are kept apart.
func (s *Server) sandboxInfo(ctx context.Context, u *store.User) map[string]any {
	on, off, why := agent.JailStatus()
	out := map[string]any{"jailed": u != nil && s.jailed(ctx, u.ID), "sandbox": on, "sandbox_off": off, "sandbox_error": why}
	if u != nil && s.jailed(ctx, u.ID) {
		out["folder"] = s.personRoot(u.ID)
		out["projects"] = s.personProjects(u.ID)
	}
	if owner, err := s.store.GetUser(ctx, s.store.OwnerID(ctx)); err == nil {
		out["owner"] = owner.Name
	}
	return out
}

// migratePeople moves everyone but the owner into their own folder (at
// every start; idempotent): saved folders outside it point at their
// projects, accounts of theirs elsewhere move in, and accounts nobody owns
// become the owner's (others can no longer run on the owner's home).
func (s *Server) migratePeople(ctx context.Context) {
	if s.cfg.PeopleDir == "" {
		return
	}
	owner := s.store.OwnerID(ctx)
	if owner == 0 {
		return
	}
	if res, err := s.store.DB.ExecContext(ctx, `UPDATE accounts SET user_id=? WHERE user_id IS NULL`, owner); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.log.Info("people: accounts nobody owned are now the owner's", "accounts", n)
		}
	}
	for _, uid := range s.store.UserIDs(ctx) {
		if uid == owner {
			continue
		}
		if err := s.ensurePerson(uid); err != nil {
			s.log.Warn("people: cannot make folder", "user", uid, "err", err)
			continue
		}
		root, projects := s.personRoot(uid), s.personProjects(uid)
		if list, err := s.store.ListWorkspaces(ctx, uid); err == nil {
			for _, ws := range list {
				if !within(filepath.Clean(ws.Path), root) {
					if _, err := s.store.DB.ExecContext(ctx, `UPDATE workspaces SET path=? WHERE id=?`, projects, ws.ID); err == nil {
						s.log.Info("people: saved folder moved into their own", "user", uid, "workspace", ws.Name, "from", ws.Path, "to", projects)
					}
				}
			}
		}
		// Pocket Ask chats: their workspace moved into their folder; the CLI
		// session starts afresh there (the chat stays as it was on screen)
		askCwd := filepath.Join(s.askBase(ctx, uid), "ask-workspace")
		if res, err := s.store.DB.ExecContext(ctx, `UPDATE threads SET cwd=?, provider_session_id='' WHERE user_id=? AND source='ask' AND cwd<>?`, askCwd, uid, askCwd); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				_ = os.MkdirAll(askCwd, 0o700)
				s.log.Info("people: Ask chats moved into their folder", "user", uid, "threads", n)
			}
		}
		if st, err := s.store.GetSettings(ctx, uid); err == nil && st.DefaultCwd != "" && !within(filepath.Clean(st.DefaultCwd), root) {
			_, _ = s.store.DB.ExecContext(ctx, `UPDATE user_settings SET value='' WHERE user_id=? AND key='default_cwd'`, uid)
		}
		accts, _ := s.store.ListAccounts(ctx)
		for _, a := range accts {
			if a.UserID == nil || *a.UserID != uid || within(filepath.Clean(a.Home), root) {
				continue
			}
			dst := s.newAccountHome(uid, a.Provider, filepath.Base(a.Home))
			if err := os.Rename(a.Home, dst); err != nil {
				s.log.Warn("people: account not moved; it stays unusable for them until it is added again", "account", a.ID, "from", a.Home, "err", err)
				continue
			}
			if _, err := s.store.DB.ExecContext(ctx, `UPDATE accounts SET home=? WHERE id=?`, dst, a.ID); err != nil {
				_ = os.Rename(dst, a.Home)
				continue
			}
			unshareSetup(dst)
			s.log.Info("people: account moved into their folder", "account", a.ID, "from", a.Home, "to", dst)
		}
	}
}

// newAccountHome is a free folder for a new login of a person: .claude /
// .codex in their home first, then .claude-<slug>.
func (s *Server) newAccountHome(uid int64, provider, slug string) string {
	home := s.cfg.Home
	if s.jailed(context.Background(), uid) {
		home = s.personHome(uid)
		if p := filepath.Join(home, "."+provider); !exists(p) {
			return p
		}
	}
	slug = strings.TrimPrefix(strings.TrimPrefix(slug, "."+provider+"-"), ".")
	if slug == "" {
		slug = "account"
	}
	base := filepath.Join(home, "."+provider+"-"+slug)
	p := base
	for i := 2; exists(p); i++ {
		p = base + "-" + strconv.Itoa(i)
	}
	return p
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// unshareSetup removes the links to the owner's settings, skills and
// plugins an account folder got when it was added next to the owner's
// (they lead out of the jail, and the owner's settings are the owner's).
func unshareSetup(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(p); err == nil && !strings.HasPrefix(t, dir) {
				_ = os.Remove(p)
			}
		}
	}
}
