package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

type skillsEntry struct {
	skills   []agent.Skill
	commands []agent.Skill
	err      string
	at       time.Time
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
		writeJSON(w, http.StatusOK, map[string]any{"skills": []agent.Skill{}, "commands": remoteCommands(provider), "remote": true})
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
		prev, prevCmds := e.skills, e.commands
		var list, cmds []agent.Skill
		var err error
		if provider == "codex" {
			list, err = agent.ListCodexSkills(ctx, s.cfg.CodexBin, cwd, env)
			cmds = agent.CodexCommands
		} else {
			list, cmds, err = agent.ListClaudeSkills(ctx, s.cfg.ClaudeBin, cwd, env)
		}
		e = skillsEntry{skills: list, commands: cmds, at: time.Now()}
		if err != nil {
			s.log.Warn("list skills", "provider", provider, "cwd", cwd, "err", err)
			e.err = err.Error()
			if len(e.skills) == 0 {
				e.skills = prev // keep the last good list
			}
			if len(e.commands) == 0 {
				e.commands = prevCmds
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
	if e.commands == nil {
		e.commands = []agent.Skill{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": e.skills, "commands": e.commands, "error": e.err})
}

// claudeBasicCommands stand in for Claude Code's built-in commands until it
// has listed them once here.
var claudeBasicCommands = []agent.Skill{
	{Name: "compact", Description: "Free up context by summarizing the conversation so far", ArgHint: "<optional custom summarization instructions>", Invoke: "/compact"},
	{Name: "context", Description: "Show current context usage", Invoke: "/context"},
	{Name: "init", Description: "Initialize a new CLAUDE.md file with codebase documentation", Invoke: "/init"},
	{Name: "mcp", Description: "Manage MCP servers", Invoke: "/mcp"},
	{Name: "usage", Description: "Show session cost, plan usage, and what's contributing to your limits", Invoke: "/usage"},
}

// remoteCommands: the commands for an agent on another machine, where the
// skills are not listed. Built-in ones are the CLI's own, so the list this
// server's CLI gave last stands in for them.
func remoteCommands(provider string) []agent.Skill {
	if provider == "codex" {
		return agent.CodexCommands
	}
	skillsMu.Lock()
	defer skillsMu.Unlock()
	var best skillsEntry
	for k, v := range skillsCache {
		if strings.HasPrefix(k, provider+"|") && len(v.commands) > 0 && v.at.After(best.at) {
			best = v
		}
	}
	if len(best.commands) > 0 {
		return best.commands
	}
	return claudeBasicCommands
}
