package server

import (
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// Pocket AI's own slash commands for working with other agents: /handoff, and
// the ones that start a panel of models (/council, /brainstorm, …). They are not CLI skills: the
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
	panelCommand("ask-all", "ask", "Ask several models the same question and compare (Pocket AI panel)", "<question>"),
	panelCommand("brainstorm", "brainstorm", "Brainstorm an idea with several models (Pocket AI panel)", "<what you want ideas for>"),
	panelCommand("council", "council", "Have a council of models judge a plan or idea (Pocket AI panel)", "<the plan or idea>"),
	panelCommand("debate", "debate", "Two models argue for and against, a third rules (Pocket AI panel)", "<the question>"),
	panelCommand("second-opinion", "review", "Other models review the last answer or change (Pocket AI panel)", "<what to look at>"),
}

// The commands Pocket AI had before panels: they still work when typed.
var oldPanelCommands = map[string]string{"committee": "ask-all", "advisor": "second-opinion"}

// panelCommand is a "/" command that starts a panel. In the web app it opens
// the panel's start sheet (who takes part, how deep); anywhere else the agent
// starts it with pocket_panel_start on the person's default line-up.
func panelCommand(name, kind, description, argHint string) agentCommand {
	return agentCommand{name, description, argHint, func(provider, arg string) string {
		what := map[string]string{
			"ask":        "question = what the person asks, written out in full.",
			"brainstorm": "question = what ideas are wanted for; material = the background from this conversation that the ideas have to fit (what exists, limits, what was tried).",
			"council":    "question = what the council should judge; material = the plan or idea itself in full, with the context needed to judge it.",
			"debate":     "question = the question, put so that it can be argued for and against; material = the background.",
			"review":     "material = what is reviewed, in full (your last answer, the plan, or the change with its diff); question = what to look at in particular.",
		}[kind]
		return `[Pocket AI command /` + name + `] Start a panel of models with pocket_panel_start, kind "` + kind + `": ` + what + ` The participants do not see this conversation, so write both out completely; they can read the files in this folder. Do not answer it yourself first. Then call pocket_panel_wait (again if it times out) and tell the person the outcome briefly: the recommendation, where the models agreed and where they did not, and your own judgment in a sentence. They see every answer on the panel's page, so do not repeat it all.
The person's words: ` + firstNonEmpty(arg, "(none given — use what this conversation is about right now)")
	}}
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
	if now, ok := oldPanelCommands[name]; ok {
		name = now
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
