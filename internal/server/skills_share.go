package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// One set of skills for every login: each extra Claude Code / Codex login on
// this server (~/.claude-max …) has its skills folder linked to the default
// one (~/.claude/skills, ~/.codex/skills), so a skill added once is there
// whichever login a chat runs on. People with their own folder (people.go)
// keep theirs.

// shareSkills links every login's skills folder to the shared one. A login
// that has a folder of its own gives its skills to the shared folder first;
// one whose name the shared folder already has goes to that login's
// .pocket-ai-trash.
func (s *Server) shareSkills(ctx context.Context) {
	list, err := s.store.ListAccounts(ctx)
	if err != nil {
		return
	}
	for _, a := range list {
		if agent.IsPersonPath(a.Home) || a.Home == "" {
			continue
		}
		shared := filepath.Join(s.defaultHome(a.Provider), "skills")
		moved, trashed, err := linkSkills(shared, filepath.Join(a.Home, "skills"))
		if err != nil {
			s.log.Warn("share skills", "login", a.Label, "err", err)
		} else if moved+trashed > 0 {
			s.log.Info("skills shared", "login", a.Label, "moved", moved, "trashed", trashed)
		}
	}
	forgetSkills()
}

// linkSkills makes dst a link to shared, moving what a real dst folder holds
// into shared first (names shared has already go to the trash next to dst).
func linkSkills(shared, dst string) (moved, trashed int, err error) {
	if err := os.MkdirAll(shared, 0o755); err != nil {
		return 0, 0, err
	}
	fi, err := os.Lstat(dst)
	switch {
	case os.IsNotExist(err):
		return 0, 0, os.Symlink(shared, dst)
	case err != nil:
		return 0, 0, err
	case fi.Mode()&os.ModeSymlink != 0:
		if _, err := os.Stat(dst); err != nil { // a link to nowhere
			_ = os.Remove(dst)
			return 0, 0, os.Symlink(shared, dst)
		}
		return 0, 0, nil // a link somewhere: someone set it up so
	case !fi.IsDir():
		return 0, 0, fmt.Errorf("%s is not a folder", dst)
	}
	if a, err1 := filepath.EvalSymlinks(dst); err1 == nil {
		if b, err2 := filepath.EvalSymlinks(shared); err2 == nil && a == b {
			return 0, 0, nil
		}
	}
	ents, err := os.ReadDir(dst)
	if err != nil {
		return 0, 0, err
	}
	stamp := time.Now().Format("20060102-150405")
	trash := filepath.Join(filepath.Dir(dst), ".pocket-ai-trash", "skills")
	for _, e := range ents {
		from := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(filepath.Join(shared, e.Name())); os.IsNotExist(err) {
			if err := os.Rename(from, filepath.Join(shared, e.Name())); err != nil {
				return moved, trashed, err
			}
			moved++
			continue
		}
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return moved, trashed, err
		}
		if err := os.Rename(from, filepath.Join(trash, e.Name()+"-"+stamp)); err != nil {
			return moved, trashed, err
		}
		trashed++
	}
	if err := os.Remove(dst); err != nil {
		return moved, trashed, err
	}
	return moved, trashed, os.Symlink(shared, dst)
}
