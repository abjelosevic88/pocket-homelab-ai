package server

import (
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// Pocket AI's own slash commands for working with other agents (like Paseo's
// /paseo-handoff, -advisor, -committee). They are not CLI skills: the
// message the person sees keeps "/committee …", the agent gets the
// instructions instead, so they work for Claude Code and Codex on every
// machine. They need the agent tools (agent_tools.go).

type agentCommand struct {
	name, description, argHint string
	brief                      func(provider, arg string) string
}

var agentCommands = []agentCommand{
	{"handoff", "Hand this work over to a fresh agent (Pocket AI)", "<what to hand off, or notes>", func(provider, arg string) string {
		return `[Pocket AI command /handoff] Hand this work over to a fresh agent with pocket_agent_start. Write it a complete, self-contained brief — it does not see this conversation: the goal, what has been done and decided here so far, the relevant files, commands and branches, what is left, and how to check the result. Unless the person says otherwise, use the same agent and folder as this conversation; if it will change code while others may work in this folder, give it its own worktree (worktree: true). After starting it, tell the person in a line or two what you handed off and stop working on it yourself. When its answer comes back to you, relay it briefly.
The person's notes for the handoff: ` + firstNonEmpty(arg, "(none — hand off the current task)")
	}},
	{"advisor", "Get a second opinion from the other agent (Pocket AI)", "<question>", func(provider, arg string) string {
		other := "codex"
		if provider == "codex" {
			other = "claude"
		}
		return `[Pocket AI command /advisor] Get a second opinion: start one agent with pocket_agent_start — provider "` + other + `" (the other agent than you), read_only: true, in this folder. Give it a self-contained brief, as it does not see this conversation: the question, the relevant context and files, and what you currently think and why. Ask for a concise, critical opinion, not an implementation. Then call pocket_agent_wait on it and answer the person: where it agrees, where it disagrees, and your own judgment.
The question: ` + firstNonEmpty(arg, "(none given — ask about your current approach in this conversation)")
	}},
	{"committee", "Ask Claude Code and Codex the same question in parallel and combine (Pocket AI)", "<question>", func(provider, arg string) string {
		return `[Pocket AI command /committee] Ask a committee: start two agents with pocket_agent_start right away, one with provider "claude" and one with provider "codex", both read_only: true in this folder, with the same self-contained brief (the question, the relevant context and files — they do not see this conversation; ask each for an independent, reasoned answer). Do not answer it yourself first. Call pocket_agent_wait for both (until: all), then reply with a synthesis: what they agree on, where they differ and why, and your recommendation.
The question: ` + firstNonEmpty(arg, "(none given — ask about the current problem in this conversation)")
	}},
}

// expandAgentCommand turns "/committee question" into what the agent is told.
func expandAgentCommand(provider, text string) string {
	t := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(t, "/") {
		return text
	}
	name, arg, _ := strings.Cut(t[1:], " ")
	if i := strings.IndexAny(name, "\n\t"); i >= 0 {
		name, arg = name[:i], name[i+1:]+" "+arg
	}
	for _, c := range agentCommands {
		if c.name == name {
			return c.brief(provider, strings.TrimSpace(arg))
		}
	}
	return text
}

// agentCommandSkills are the commands for the composer's "/" menu, minus the
// names the CLI has a skill or command of its own for.
func agentCommandSkills(have ...[]agent.Skill) []agent.Skill {
	taken := map[string]bool{}
	for _, list := range have {
		for _, s := range list {
			taken[s.Name] = true
		}
	}
	out := []agent.Skill{}
	for _, c := range agentCommands {
		if !taken[c.name] {
			out = append(out, agent.Skill{Name: c.name, Description: c.description, ArgHint: c.argHint, Scope: "pocket-ai", Invoke: "/" + c.name})
		}
	}
	return out
}
