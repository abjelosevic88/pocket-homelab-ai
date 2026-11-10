package server

import (
	"context"
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
	skillsMu      sync.Mutex
	skillsCache   = map[string]skillsEntry{}
	skillsLoading = map[string]chan struct{}{} // a probe running for a key
)

// Skills change rarely. A list younger than skillsFresh is served as is; an
// older one (up to skillsKeep) is served at once while the CLI is asked again
// in the background, so the "/" menu never waits on a CLI start after the
// first time. Saving, copying or deleting a skill drops the cache
// (forgetSkills), so those show up at once.
const (
	skillsFresh = time.Minute
	skillsKeep  = 24 * time.Hour
)

// handleSkills lists the skills a new message in this folder can use, as the
// CLI itself reports them (user, project and plugin skills; the account's own
// folder decides which). On another machine its CLI is asked over ssh, in the
// login folder the chat runs on (host_home).
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
	var probe func(context.Context) ([]agent.Skill, []agent.Skill, error)
	var key string
	if id := queryInt64(r, "host"); id != 0 {
		h, err := s.checkHostUse(ctx, u, id)
		if err != nil || h == nil {
			writeJSON(w, http.StatusOK, map[string]any{"skills": agentCommandSkills(), "commands": remoteCommands(provider), "remote": true})
			return
		}
		cwd := q.Get("cwd") // a path over there: not checked here, the CLI just runs in it
		home := strings.TrimSpace(q.Get("host_home"))
		if home != "" && !agent.ValidHostHome(provider, h.Info.Home, home) {
			home = ""
		}
		env, bin, rem := agent.HomeEnv(provider, home), hostBin(h, provider), remoteOf(h)
		key = fmt.Sprintf("%s|host%d:%s|%s", provider, h.ID, home, cwd)
		probe = func(ctx context.Context) ([]agent.Skill, []agent.Skill, error) {
			if provider == "codex" {
				l, err := agent.ListCodexSkills(ctx, bin, cwd, env, rem)
				return l, agent.CodexCommands, err
			}
			return agent.ListClaudeSkills(ctx, bin, cwd, env, rem)
		}
	} else {
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
		key = fmt.Sprintf("%s|%s|%s", provider, home, cwd)
		probe = func(ctx context.Context) ([]agent.Skill, []agent.Skill, error) {
			if provider == "codex" {
				l, err := agent.ListCodexSkills(ctx, s.cfg.CodexBin, cwd, env)
				return l, agent.CodexCommands, err
			}
			return agent.ListClaudeSkills(ctx, s.cfg.ClaudeBin, cwd, env)
		}
	}

	skillsMu.Lock()
	e, ok := skillsCache[key]
	skillsMu.Unlock()
	switch {
	case !ok || q.Get("refresh") == "1":
		e = s.loadSkills(ctx, key, probe) // nothing to show yet: wait for it
	case time.Since(e.at) > skillsFresh:
		go s.loadSkills(s.rt.ctx, key, probe) // show the last list, refresh behind it
	}
	if e.skills == nil {
		e.skills = []agent.Skill{}
	}
	if e.commands == nil {
		e.commands = []agent.Skill{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": append(agentCommandSkills(e.skills, e.commands), e.skills...), "commands": e.commands, "error": e.err})
}

// loadSkills asks the CLI for key's list and caches it. One probe per key at
// a time: a second caller waits for the running one and gets its result.
func (s *Server) loadSkills(ctx context.Context, key string, probe func(context.Context) ([]agent.Skill, []agent.Skill, error)) skillsEntry {
	skillsMu.Lock()
	if ch, busy := skillsLoading[key]; busy {
		skillsMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		skillsMu.Lock()
		e := skillsCache[key]
		skillsMu.Unlock()
		return e
	}
	ch := make(chan struct{})
	skillsLoading[key] = ch
	prev := skillsCache[key]
	skillsMu.Unlock()

	// not the request's context: a closed tab must not waste a probe others wait on
	pctx, cancel := context.WithTimeout(s.rt.ctx, 45*time.Second)
	list, cmds, err := probe(pctx)
	cancel()
	e := skillsEntry{skills: list, commands: cmds, at: time.Now()}
	if err != nil {
		s.log.Warn("list skills", "key", key, "err", err)
		e.err = err.Error()
		if len(e.skills) == 0 {
			e.skills = prev.skills // keep the last good list
		}
		if len(e.commands) == 0 {
			e.commands = prev.commands
		}
	}
	skillsMu.Lock()
	for k, v := range skillsCache {
		if time.Since(v.at) > skillsKeep {
			delete(skillsCache, k)
		}
	}
	skillsCache[key] = e
	delete(skillsLoading, key)
	close(ch)
	skillsMu.Unlock()
	return e
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
