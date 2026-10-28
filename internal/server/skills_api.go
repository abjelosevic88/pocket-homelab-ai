package server

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

type skillsEntry struct {
	skills []agent.Skill
	err    string
	at     time.Time
}

var (
	skillsMu    sync.Mutex
	skillsCache = map[string]skillsEntry{}
)

// handleSkills lists the skills a new message in this folder can use, as the
// CLI itself reports them (user, project and plugin skills; the account's own
// folder decides which). Remote hosts report none: their CLI is not probed.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	q := r.URL.Query()
	provider := q.Get("provider")
	if provider != "claude" && provider != "codex" {
		writeErr(w, http.StatusBadRequest, "provider must be claude or codex")
		return
	}
	if queryInt64(r, "host") != 0 {
		writeJSON(w, http.StatusOK, map[string]any{"skills": []agent.Skill{}, "remote": true})
		return
	}
	cwd := s.cfg.Home
	if p := q.Get("cwd"); p != "" {
		clean, err := s.checkDir(ctx, p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cwd = clean
	}
	acct := queryInt64(r, "account")
	if err := s.checkAccount(ctx, u, provider, acct); err != nil {
		acct = 0
	}
	home, env := s.accountHome(ctx, provider, acct)
	key := fmt.Sprintf("%s|%s|%s", provider, home, cwd)

	skillsMu.Lock()
	e, ok := skillsCache[key]
	skillsMu.Unlock()
	// Skills change rarely; a minute keeps new ones showing up quickly.
	if !ok || time.Since(e.at) > time.Minute || q.Get("refresh") == "1" {
		prev := e.skills
		var list []agent.Skill
		var err error
		if provider == "codex" {
			list, err = agent.ListCodexSkills(ctx, s.cfg.CodexBin, cwd, env)
		} else {
			list, err = agent.ListClaudeSkills(ctx, s.cfg.ClaudeBin, cwd, env)
		}
		e = skillsEntry{skills: list, at: time.Now()}
		if err != nil {
			s.log.Warn("list skills", "provider", provider, "cwd", cwd, "err", err)
			e.err = err.Error()
			if len(e.skills) == 0 {
				e.skills = prev // keep the last good list
			}
		}
		skillsMu.Lock()
		for k, v := range skillsCache {
			if time.Since(v.at) > 10*time.Minute {
				delete(skillsCache, k)
			}
		}
		skillsCache[key] = e
		skillsMu.Unlock()
	}
	if e.skills == nil {
		e.skills = []agent.Skill{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": e.skills, "error": e.err})
}
