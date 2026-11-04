package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/google/uuid"
)

// Agents that start agents (like Paseo's): every coding conversation's Pocket
// tool connection also carries the pocket_agent_* tools. With them an agent
// starts another conversation (Claude Code or Codex, on any of the person's
// machines, in a folder or a new worktree), messages it, waits for it and
// reads what it wrote. The started one is a thread like any other (source
// "agent", source_ref = the thread that started it) on the same person's
// logins; starting one asks the person first unless they always allow it.
// Its answer comes back to the parent as a message when its turn ends
// (agent_reports), unless the parent is waiting for it with pocket_agent_wait.

const (
	agentMaxDepth    = 2  // the person's conversation → an agent → an agent, no further
	agentMaxChildren = 12 // agents one conversation may start
	agentMaxRunning  = 4  // of those, working at once
	agentReplyMax    = 8000
	agentWaitMax     = 600 // seconds; Codex gives a tool call 660 s
)

const agentToolsInstructions = `Other agents: with pocket_agent_start you can hand work to another coding agent (Claude Code or Codex, on this or another of the person's machines, optionally in its own git worktree). It runs as its own Pocket AI conversation, which the person can open and join. Use it when the person asks (hand off, a second opinion, several agents at once) or when independent parallel work clearly pays off; do small things yourself. An agent does not see this conversation: give it a complete, self-contained task. Starting one asks the person first unless they always allow it. When it finishes, its answer comes back to you as a message (or directly, if you wait with pocket_agent_wait); continue it with pocket_agent_send. Agents that change code at the same time as you or each other need their own worktree (worktree: true). You can also message the person's other conversations (pocket_agent_list scope all; that asks the person first), and set a heartbeat (pocket_agent_heartbeat_create) to be reminded here on a schedule, e.g. to check on something long-running.`

// agentTools are the pocket_agent_* tool definitions.
func agentTools() []askTool {
	defs := []struct {
		name, description, schema string
		read                      bool
	}{
		{"pocket_agent_options", "What pocket_agent_start can use: the agents (Claude Code, Codex) with their models and the permission modes allowed from this conversation, the person's machines, and this conversation's own agent, machine and folder.", `{"type":"object","properties":{}}`, true},
		{"pocket_agent_start", "Start another coding agent as its own Pocket AI conversation with a task. The person approves the start unless they always allow it. It does not see this conversation, so the prompt must be complete and self-contained. Returns its agent_id; its answer comes back to you as a message when it finishes (report: false to skip), or wait for it with pocket_agent_wait.", `{"type":"object","properties":{
"prompt":{"type":"string","maxLength":100000,"description":"The whole task: goal, context, relevant files, constraints and how to check the result."},
"title":{"type":"string","maxLength":120,"description":"A short name for its conversation."},
"provider":{"type":"string","enum":["claude","codex"],"description":"Default: the same agent as this conversation."},
"model":{"type":"string","description":"A model id from pocket_agent_options. Default: the person's default for that agent."},
"effort":{"type":"string","description":"Reasoning effort, e.g. low, medium, high."},
"machine":{"type":"string","description":"Machine name or id from pocket_agent_options. Default: this conversation's machine."},
"folder":{"type":"string","description":"Working folder on that machine. Default: this conversation's folder (on another machine: its home)."},
"worktree":{"type":"boolean","description":"Work in a new git worktree of the folder's repository, so it can change code alongside others."},
"worktree_name":{"type":"string","description":"Branch name for the new worktree (default: from the prompt)."},
"worktree_base":{"type":"string","description":"Branch the new worktree starts from (default: the repository's)."},
"read_only":{"type":"boolean","description":"It may look but not change anything (Claude: plan mode, Codex: read only). For reviews, research and opinions."},
"mode":{"type":"string","description":"A permission mode id from pocket_agent_options; never more permissive than this conversation's. Default: like this conversation."},
"report":{"type":"boolean","description":"Send its answer to this conversation as a message when it finishes (default true)."}
},"required":["prompt"]}`, false},
		{"pocket_agent_send", "Send a message to an agent this conversation started (a follow-up, an answer to its question, a correction), or to another of the person's conversations (pocket_agent_list with scope all) — that one asks the person first. If it is working, the message waits for its turn to end, or goes into its current turn with now: true. Its answer comes back to you as a message.", `{"type":"object","properties":{"agent_id":{"type":"string"},"message":{"type":"string","maxLength":100000},"now":{"type":"boolean","description":"Into its current turn instead of after it."},"report":{"type":"boolean","description":"Send its answer here when it finishes (default true)."}},"required":["agent_id","message"]}`, false},
		{"pocket_agent_wait", "Wait until agents (or other conversations of the person's) have finished their turn (all of them, or the first one with until: any), up to timeout_seconds (default 300, at most 600). Returns each one's state and, when finished, its answer. An agent waiting for the person's approval keeps waiting; call again to wait longer.", `{"type":"object","properties":{"agent_ids":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":12},"until":{"type":"string","enum":["all","any"]},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["agent_ids"]}`, true},
		{"pocket_agent_read", "Read the latest messages of an agent this conversation started or of another of the person's conversations (what it was asked and what it answered; not its tool calls).", `{"type":"object","properties":{"agent_id":{"type":"string"},"messages":{"type":"integer","minimum":1,"maximum":30,"description":"How many of the latest messages (default 6)."}},"required":["agent_id"]}`, true},
		{"pocket_agent_list", "List the agents this conversation started, with their state; scope all lists the person's other recent conversations too (search them with query).", `{"type":"object","properties":{"scope":{"type":"string","enum":["started","all"]},"query":{"type":"string","maxLength":100}}}`, true},
		{"pocket_agent_stop", "Stop an agent this conversation started (it ends its current turn; nothing is reported back). Stopping another conversation's agent asks the person first.", `{"type":"object","properties":{"agent_id":{"type":"string"}},"required":["agent_id"]}`, false},
		{"pocket_agent_heartbeat_create", "Set a heartbeat: a prompt that comes back to THIS conversation on a schedule (e.g. check on a deploy every 30 minutes, a daily report), until removed, max_runs are used or it ends. The person approves it unless they always allow it. A beat is skipped while you are still working.", `{"type":"object","properties":{
"prompt":{"type":"string","maxLength":20000,"description":"What you will be told each time (write it for your future self)."},
"spec":{"type":"string","description":"When: \"every 30m\", \"every 2h\", \"hourly\", \"daily 08:00\", \"weekdays 09:00\", \"weekly mon,thu 08:00\" or a five-field cron line, in the person's time zone. At least 5 minutes apart."},
"max_runs":{"type":"integer","minimum":1,"maximum":1000,"description":"Stop after this many beats (default: no limit)."},
"ends_after_hours":{"type":"integer","minimum":0,"maximum":2160,"description":"Stop after this many hours (default 168 = a week; 0 = never)."}
},"required":["prompt","spec"]}`, false},
		{"pocket_agent_heartbeats", "List this conversation's heartbeats.", `{"type":"object","properties":{}}`, true},
		{"pocket_agent_heartbeat_delete", "Remove one of this conversation's heartbeats.", `{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`, false},
	}
	out := make([]askTool, 0, len(defs))
	for _, d := range defs {
		t := askTool{Name: d.name, Description: d.description, InputSchema: json.RawMessage(d.schema)}
		t.Annotations.ReadOnly = d.read
		if d.name == "pocket_agent_start" || d.name == "pocket_agent_heartbeat_create" {
			// "Always allow… everything new in AI" covers starting agents
			t.Meta.App, t.Meta.Kind = "ai", "add"
		}
		out = append(out, t)
	}
	return out
}

// agentTool is one of the agent tool definitions.
func agentTool(name string) askTool {
	for _, t := range agentTools() {
		if t.Name == name {
			return t
		}
	}
	return askTool{Name: name}
}

// agentToolsOn: coding conversations get the agent tools (Ask chats don't).
func agentToolsOn(t *store.Thread) bool { return t.Source != "ask" }

// withAgentTools adds the agent tools to a tools/list result.
func withAgentTools(result any, t *store.Thread) any {
	if !agentToolsOn(t) {
		return result
	}
	m, ok := result.(map[string]any)
	if !ok {
		return map[string]any{"tools": agentTools()}
	}
	list, _ := m["tools"].([]askTool)
	m["tools"] = append(append([]askTool{}, list...), agentTools()...)
	return m
}

// ---------- waiting ----------

// agentWaiters counts the pocket_agent_wait calls waiting on a thread: its
// answer goes to them instead of coming as a message.
var agentWaiters sync.Map // struct{S *Server; ID string} → *atomic.Int32

func (s *Server) waitOn(id string) func() {
	key := struct {
		S  *Server
		ID string
	}{s, id}
	v, _ := agentWaiters.LoadOrStore(key, new(atomic.Int32))
	n := v.(*atomic.Int32)
	n.Add(1)
	return func() { n.Add(-1) }
}

func (s *Server) awaited(id string) bool {
	v, ok := agentWaiters.Load(struct {
		S  *Server
		ID string
	}{s, id})
	return ok && v.(*atomic.Int32).Load() > 0
}

// ---------- state ----------

type agentInfo struct {
	ID       string `json:"agent_id"`
	Title    string `json:"title"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Machine  string `json:"machine"`
	Folder   string `json:"folder"`
	// State: starting | working | waiting_for_person | done | failed | stopped
	State    string `json:"state"`
	Finished bool   `json:"finished"`
	Error    string `json:"error,omitempty"`
	Reply    string `json:"reply,omitempty"`
	URL      string `json:"url,omitempty"`
}

// agentState is where an agent stands; withReply adds what it answered
// since its last message (once its turn is over).
func (s *Server) agentState(ctx context.Context, c *store.Thread, withReply bool) agentInfo {
	c = s.rt.decorate(c)
	info := agentInfo{ID: c.ID, Title: c.Title, Provider: c.Provider, Model: c.Model, Machine: s.machineName(ctx, c.HostID), Folder: c.Cwd, URL: s.threadURL(c.ID)}
	reply, ended, _ := s.store.LastReply(ctx, c.ID)
	stopped := false
	if ended {
		if it, err := s.store.LastTurn(ctx, c.ID); err == nil && it.Interrupted {
			stopped = true
		}
	}
	switch {
	case c.Status == "waiting" || c.Pending > 0:
		info.State = "waiting_for_person"
	case c.Status == "running" || c.Queued > 0 || c.Reconnecting:
		info.State = "working"
	case c.Status == "error":
		info.State, info.Finished, info.Error = "failed", true, c.Error
	case ended && stopped:
		info.State, info.Finished = "stopped", true
	case ended:
		info.State, info.Finished = "done", true
	default:
		info.State = "starting" // its worktree is being set up, or its first message is on the way
	}
	if withReply && info.Finished {
		info.Reply = tailText(reply, agentReplyMax)
	}
	return info
}

func tailText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	for i := 0; i < 4 && i < len(cut); i++ { // not in the middle of a character
		if cut[i]&0xC0 != 0x80 {
			cut = cut[i:]
			break
		}
	}
	return "…" + cut
}

func (s *Server) machineName(ctx context.Context, hostID int64) string {
	if hostID == 0 {
		return "This server (Pocket AI)"
	}
	if h, err := s.store.GetHost(ctx, hostID); err == nil {
		return h.Name
	}
	return "Removed machine"
}

// agentDepth: 0 for a person's conversation, 1 for an agent it started, …
func (s *Server) agentDepth(ctx context.Context, t *store.Thread) int {
	d := 0
	for t != nil && t.Source == "agent" && t.SourceRef != "" && d <= agentMaxDepth {
		d++
		p, err := s.store.GetThreadAny(ctx, t.SourceRef)
		if err != nil {
			break
		}
		t = p
	}
	return d
}

// ---------- permission modes ----------

// modeLevel orders permission modes of both agents from read only (0) to
// full access (4), so an agent never starts one freer than itself.
var modeLevel = map[string]int{
	"plan": 0, "default": 1, "acceptEdits": 2, "auto": 3, "bypassPermissions": 4,
	"read-only": 0, "untrusted": 1, "on-request": 2, "full-access": 4,
}

func modesOf(provider string) []agent.Mode {
	if provider == "codex" {
		return agent.CodexModes
	}
	return agent.ClaudeModes
}

// allowedModes are the provider's modes no freer than the parent's.
func allowedModes(provider, parentMode string) []string {
	top, ok := modeLevel[parentMode]
	if !ok {
		top = 1
	}
	out := []string{}
	for _, m := range modesOf(provider) {
		if modeLevel[m.ID] <= top {
			out = append(out, m.ID)
		}
	}
	return out
}

// childMode picks the agent's permission mode.
func childMode(provider string, parent *store.Thread, asked string, readOnly bool) (string, error) {
	allowed := allowedModes(provider, parent.Mode)
	if readOnly {
		if provider == "codex" {
			return "read-only", nil
		}
		return "plan", nil
	}
	if asked != "" {
		for _, m := range allowed {
			if m == asked {
				return m, nil
			}
		}
		return "", fmt.Errorf("mode %q is not allowed from here (allowed: %s)", asked, strings.Join(allowed, ", "))
	}
	if provider == parent.Provider && validMode(provider, parent.Mode) {
		return parent.Mode, nil
	}
	// the other agent: its freest mode within this conversation's
	best, lvl := "", -1
	for _, m := range allowed {
		if modeLevel[m] > lvl {
			best, lvl = m, modeLevel[m]
		}
	}
	if best == "" {
		return "", errors.New("no permission mode of that agent is allowed from here")
	}
	return best, nil
}

func modeName(provider, id string) string {
	for _, m := range modesOf(provider) {
		if m.ID == id {
			return m.Name
		}
	}
	return id
}

// ---------- the tools ----------

func (s *Server) callAgentTool(ctx, session context.Context, t *store.Thread, sink *runSink, name string, raw json.RawMessage) json.RawMessage {
	if !agentToolsOn(t) {
		return toolFailure("Agent tools are not available in this conversation")
	}
	owner, err := s.store.GetUser(ctx, t.UserID)
	if err != nil || !owner.MayRun() {
		return toolFailure("An admin has not allowed this conversation's owner to run agents")
	}
	switch name {
	case "pocket_agent_options":
		return toolResult(s.agentOptions(ctx, owner, t), false)
	case "pocket_agent_start":
		var in agentStart
		if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Prompt) == "" {
			return toolFailure("Write the task in prompt")
		}
		out, err := s.agentStart(ctx, session, owner, t, sink, in)
		if err != nil {
			return toolFailure(err.Error())
		}
		return toolResult(out, false)
	case "pocket_agent_send":
		var in struct {
			ID      string `json:"agent_id"`
			Message string `json:"message"`
			Now     bool   `json:"now"`
			Report  *bool  `json:"report"`
		}
		if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Message) == "" {
			return toolFailure("Give agent_id and the message")
		}
		c, child, err := s.agentTarget(ctx, t, in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		if c.Archived {
			return toolFailure("That conversation is archived")
		}
		if s.rt.isExternal(c.ID) {
			return toolFailure("That conversation is being driven elsewhere right now; try again when it is idle")
		}
		if !child {
			// another of the person's conversations: its agent works on with its
			// own permissions, so the person says yes first
			if why := s.approveOther(ctx, session, owner, t, sink, "pocket_agent_send", "Message another conversation", c, raw, map[string]any{"message": tailHead(in.Message, 4000)}); why != "" {
				return toolFailure(why)
			}
		}
		if in.Report == nil || *in.Report {
			_ = s.store.OweReport(ctx, c.ID, t.ID)
		} else {
			s.store.TakeReportFrom(ctx, c.ID, t.ID)
		}
		queued, err := s.postMessage(context.WithoutCancel(ctx), owner, c, in.Message, nil, in.Now)
		if err != nil {
			s.store.TakeReportFrom(ctx, c.ID, t.ID)
			return toolFailure(err.Error())
		}
		s.log.Info("agent message", "thread", t.ID, "to", c.ID, "child", child, "now", in.Now)
		return toolResult(map[string]any{"agent_id": c.ID, "sent": true, "waits_for_its_turn": queued}, false)
	case "pocket_agent_wait":
		var in struct {
			IDs     []string `json:"agent_ids"`
			ID      string   `json:"agent_id"`
			Until   string   `json:"until"`
			Timeout int      `json:"timeout_seconds"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give agent_ids")
		}
		if in.ID != "" {
			in.IDs = append(in.IDs, in.ID)
		}
		if len(in.IDs) == 0 || len(in.IDs) > agentMaxChildren {
			return toolFailure("Give 1 to 12 agent_ids")
		}
		return s.agentWait(ctx, session, t, in.IDs, in.Until == "any", in.Timeout)
	case "pocket_agent_read":
		var in struct {
			ID string `json:"agent_id"`
			N  int    `json:"messages"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give agent_id")
		}
		c, _, err := s.agentTarget(ctx, t, in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		return toolResult(s.agentRead(ctx, c, in.N), false)
	case "pocket_agent_list":
		var in struct {
			Scope string `json:"scope"`
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &in)
		var list []*store.Thread
		if in.Scope == "all" {
			all, err := s.store.ListThreads(ctx, t.UserID, store.ThreadFilter{Q: in.Query, Limit: 40})
			if err != nil {
				return toolFailure(err.Error())
			}
			for _, c := range all {
				if c.ID != t.ID && c.Source != "ask" {
					list = append(list, c)
				}
			}
		} else if list, err = s.store.ChildThreads(ctx, t.ID); err != nil {
			return toolFailure(err.Error())
		}
		out := []map[string]any{}
		for _, c := range list {
			st := s.agentState(ctx, c, false)
			row := map[string]any{"agent_id": st.ID, "title": st.Title, "provider": st.Provider, "machine": st.Machine, "folder": st.Folder, "state": st.State,
				"started_by_you": c.Source == "agent" && c.SourceRef == t.ID, "last_activity": c.ActivityAt}
			if in.Scope == "all" {
				row["permissions"] = modeName(c.Provider, c.Mode)
			}
			out = append(out, row)
		}
		return toolResult(map[string]any{"agents": out}, false)
	case "pocket_agent_stop":
		var in struct {
			ID string `json:"agent_id"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give agent_id")
		}
		c, child, err := s.agentTarget(ctx, t, in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		if !child {
			if why := s.approveOther(ctx, session, owner, t, sink, "pocket_agent_stop", "Stop another conversation's agent", c, raw, nil); why != "" {
				return toolFailure(why)
			}
		}
		s.store.TakeReportFrom(ctx, c.ID, t.ID)
		if !s.stopReattached(c.ID) {
			if err := s.rt.interrupt(c.ID); err != nil {
				return toolFailure(err.Error())
			}
		}
		s.log.Info("agent stopped by another", "thread", t.ID, "agent", c.ID, "child", child)
		return toolResult(map[string]any{"agent_id": c.ID, "stopped": true}, false)
	case "pocket_agent_heartbeat_create", "pocket_agent_heartbeats", "pocket_agent_heartbeat_delete":
		return s.heartbeatTool(ctx, session, owner, t, sink, name, raw)
	}
	return toolFailure("Unknown agent tool")
}

// agentTarget loads a conversation an agent may talk to: one it started
// (child = true), or another of the same person's coding conversations.
func (s *Server) agentTarget(ctx context.Context, t *store.Thread, id string) (*store.Thread, bool, error) {
	c, err := s.store.GetThreadAny(ctx, strings.TrimSpace(id))
	if err != nil || c.UserID != t.UserID || c.Source == "ask" {
		return nil, false, errors.New("No conversation with that id (pocket_agent_list lists them; scope all for every conversation of the person's)")
	}
	if c.ID == t.ID {
		return nil, false, errors.New("That is this conversation")
	}
	return c, c.Source == "agent" && c.SourceRef == t.ID, nil
}

// approveOther asks the person before an agent acts on a conversation it did
// not start. "" = approved, else why not.
func (s *Server) approveOther(ctx, session context.Context, owner *store.User, t *store.Thread, sink *runSink, toolName, title string, c *store.Thread, raw json.RawMessage, extra map[string]any) string {
	tool := askTool{Name: toolName}
	tool.Meta.App, tool.Meta.Kind = "ai", "change"
	preview := map[string]any{"conversation": firstNonEmpty(c.Title, c.ID), "agent": providerName(c.Provider), "where": s.machineName(ctx, c.HostID) + ": " + c.Cwd, "permissions": modeName(c.Provider, c.Mode)}
	for k, v := range extra {
		preview[k] = v
	}
	pv, _ := json.Marshal(preview)
	req := agent.Request{ID: "ask-" + uuid.NewString(), Type: "permission", ToolName: tool.Name, Title: title, Input: pv, Status: "pending", ForID: owner.ID, App: "ai", Kind: "change"}
	match := awayMatch(tool.Name, raw)
	switch s.approveChange(ctx, session, t, sink, owner.ID, tool, req, match) {
	case "allow":
		return ""
	case "defer":
		s.saveDeferred(t.ID, req, owner.ID, match)
		return awayDeferText
	}
	return "The person did not approve it. Nothing was sent."
}

func (s *Server) agentOptions(ctx context.Context, owner *store.User, t *store.Thread) map[string]any {
	agents := []map[string]any{}
	for _, id := range []string{"claude", "codex"} {
		p, ok := s.agentProvider(ctx, id)
		a := map[string]any{"provider": id, "name": providerName(id), "available_on_this_server": ok, "modes": allowedModes(id, t.Mode)}
		if ok {
			models := []string{}
			for _, m := range p.Models {
				models = append(models, m.ID)
			}
			a["models"], a["default_model"], a["efforts"] = models, p.DefaultModel, p.Efforts
		} else if p.Error != "" {
			a["error"] = p.Error
		}
		agents = append(agents, a)
	}
	machines := []map[string]any{{"id": 0, "name": "This server (Pocket AI)"}}
	if hosts, err := s.hostsFor(ctx, owner); err == nil {
		for _, h := range hosts {
			machines = append(machines, map[string]any{"id": h.ID, "name": h.Name, "home": h.Info.Home})
		}
	}
	children, _ := s.store.ChildThreads(ctx, t.ID)
	depth := s.agentDepth(ctx, t)
	return map[string]any{
		"this_conversation": map[string]any{"provider": t.Provider, "model": t.Model, "mode": t.Mode, "machine": s.machineName(ctx, t.HostID), "machine_id": t.HostID, "folder": t.Cwd},
		"agents":            agents,
		"machines":          machines,
		"limits": map[string]any{"may_start": depth < agentMaxDepth, "started": len(children), "max_started": agentMaxChildren, "max_working_at_once": agentMaxRunning,
			"note": "Agents started by an agent cannot start more agents."},
	}
}

type agentStart struct {
	Prompt       string `json:"prompt"`
	Title        string `json:"title"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	Machine      string `json:"machine"`
	Folder       string `json:"folder"`
	Worktree     bool   `json:"worktree"`
	WorktreeName string `json:"worktree_name"`
	WorktreeBase string `json:"worktree_base"`
	ReadOnly     bool   `json:"read_only"`
	Mode         string `json:"mode"`
	Report       *bool  `json:"report"`
}

// resolveMachine finds a machine of the person's by id or name ("" = the
// conversation's own).
func (s *Server) resolveMachine(ctx context.Context, owner *store.User, t *store.Thread, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return t.HostID, nil
	}
	low := strings.ToLower(name)
	if low == "0" || low == "local" || low == "this server" || strings.HasPrefix(low, "this server") || low == "pocket ai" {
		return 0, nil
	}
	hosts, err := s.hostsFor(ctx, owner)
	if err != nil {
		return 0, err
	}
	id, _ := strconv.ParseInt(name, 10, 64)
	for _, h := range hosts {
		if h.ID == id || strings.EqualFold(h.Name, name) || strings.EqualFold(h.Target, name) {
			return h.ID, nil
		}
	}
	return 0, fmt.Errorf("No machine %q (pocket_agent_options lists them)", name)
}

func (s *Server) agentStart(ctx, session context.Context, owner *store.User, t *store.Thread, sink *runSink, in agentStart) (map[string]any, error) {
	if s.agentDepth(ctx, t) >= agentMaxDepth {
		return nil, errors.New("An agent started by another agent cannot start more agents; do the work yourself or report back")
	}
	children, err := s.store.ChildThreads(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	working := 0
	for _, c := range children {
		if st := s.agentState(ctx, c, false); !st.Finished {
			working++
		}
	}
	if len(children) >= agentMaxChildren {
		return nil, fmt.Errorf("This conversation has started %d agents already, the most it may; continue one of them with pocket_agent_send", len(children))
	}
	if working >= agentMaxRunning {
		return nil, fmt.Errorf("%d agents from this conversation are still working; wait for one (pocket_agent_wait) before starting another", working)
	}
	provider := firstNonEmpty(in.Provider, t.Provider)
	if provider != "claude" && provider != "codex" {
		return nil, errors.New("provider must be claude or codex")
	}
	host, err := s.resolveMachine(ctx, owner, t, in.Machine)
	if err != nil {
		return nil, err
	}
	mode, err := childMode(provider, t, in.Mode, in.ReadOnly)
	if err != nil {
		return nil, err
	}
	nt := newThread{Provider: provider, HostID: host, Model: in.Model, Effort: in.Effort, Mode: mode, Cwd: strings.TrimSpace(in.Folder),
		Title: strings.TrimSpace(in.Title), Text: in.Prompt}
	if host == t.HostID {
		if nt.Cwd == "" {
			nt.Cwd = t.Cwd
		}
		if provider == t.Provider {
			if host == 0 {
				acct := t.AccountID
				nt.AccountID = &acct
			}
			nt.HostHome = t.HostHome
			nt.Speed = &t.Speed
		}
	}
	if provider == t.Provider && nt.Model == "" {
		nt.Model = t.Model
	}
	if in.Worktree || in.WorktreeName != "" || in.WorktreeBase != "" {
		nt.Worktree = &newWorktree{Mode: "new", Name: strings.TrimSpace(in.WorktreeName), Base: strings.TrimSpace(in.WorktreeBase)}
	}
	if in.ReadOnly && provider == "claude" {
		nt.Text += "\n\n(You are in read-only mode: look, don't change anything, and answer in text. There is no implementation plan to approve, so do not call ExitPlanMode.)"
	}
	report := in.Report == nil || *in.Report

	// the person approves the start (or always allows it)
	tool := agentTool("pocket_agent_start")
	where := s.machineName(ctx, host) + ": " + firstNonEmpty(nt.Cwd, "its home folder")
	preview := map[string]any{
		"agent":       strings.TrimSpace(providerName(provider) + " · " + firstNonEmpty(nt.Model, "default model") + " " + nt.Effort),
		"where":       where,
		"permissions": modeName(provider, mode),
		"task":        tailHead(in.Prompt, 4000),
	}
	if nt.Worktree != nil {
		preview["worktree"] = "a new one" + map[bool]string{true: " (" + nt.Worktree.Name + ")"}[nt.Worktree.Name != ""] + map[bool]string{true: " from " + nt.Worktree.Base}[nt.Worktree.Base != ""]
	}
	if nt.Title != "" {
		preview["title"] = nt.Title
	}
	pv, _ := json.Marshal(preview)
	args, _ := json.Marshal(in)
	req := agent.Request{ID: "ask-" + uuid.NewString(), Type: "permission", ToolName: tool.Name, Title: "Start another agent", Input: pv, Status: "pending",
		ForID: owner.ID, App: tool.Meta.App, Kind: tool.Meta.Kind}
	match := awayMatch(tool.Name, args)
	switch s.approveChange(ctx, session, t, sink, owner.ID, tool, req, match) {
	case "allow":
	case "defer":
		s.saveDeferred(t.ID, req, owner.ID, match)
		return nil, errors.New(awayDeferText)
	default:
		return nil, errors.New("The person did not approve starting this agent. Nothing was started.")
	}

	bg := context.WithValue(context.WithoutCancel(ctx), startInBackground{}, true)
	c, err := s.createThread(bg, owner, nt, "agent", t.ID)
	if err != nil && c == nil {
		return nil, err
	}
	if err != nil {
		s.rt.saveItem(c.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		s.rt.setStatus(c.ID, "error", err.Error())
		return nil, fmt.Errorf("Its conversation was made but the agent did not start: %v", err)
	}
	if report {
		_ = s.store.OweReport(ctx, c.ID, t.ID)
	}
	s.log.Info("agent started an agent", "thread", t.ID, "agent", c.ID, "provider", provider, "host", host, "mode", mode, "worktree", nt.Worktree != nil, "report", report)
	if fresh, err := s.store.GetThreadAny(ctx, c.ID); err == nil {
		c = fresh
	}
	out := map[string]any{"agent_id": c.ID, "title": c.Title, "provider": c.Provider, "model": c.Model, "mode": c.Mode,
		"machine": s.machineName(ctx, c.HostID), "folder": c.Cwd, "url": s.threadURL(c.ID), "reports_back": report}
	if c.WorktreeID != 0 {
		if wt, err := s.store.GetWorktree(ctx, c.UserID, c.WorktreeID); err == nil {
			out["worktree"] = map[string]any{"branch": wt.Branch, "path": wt.Path}
		}
	}
	if report {
		out["next"] = "Its answer comes to you as a message when it finishes; or wait for it now with pocket_agent_wait."
	}
	return out, nil
}

func tailHead(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	if len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0xC0 {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

func (s *Server) agentWait(ctx, session context.Context, t *store.Thread, ids []string, first bool, timeout int) json.RawMessage {
	if timeout <= 0 {
		timeout = 300
	}
	if timeout > agentWaitMax {
		timeout = agentWaitMax
	}
	kids := []*store.Thread{}
	seen := map[string]bool{}
	for _, id := range ids {
		c, _, err := s.agentTarget(ctx, t, id)
		if err != nil {
			return toolFailure(err.Error() + ": " + id)
		}
		if !seen[c.ID] {
			seen[c.ID] = true
			kids = append(kids, c)
			defer s.waitOn(c.ID)()
		}
	}
	deadline := time.After(time.Duration(timeout) * time.Second)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		states := make([]agentInfo, 0, len(kids))
		done := 0
		for _, k := range kids {
			c, err := s.store.GetThreadAny(ctx, k.ID)
			if err != nil {
				states = append(states, agentInfo{ID: k.ID, Title: k.Title, State: "deleted", Finished: true})
				done++
				continue
			}
			st := s.agentState(ctx, c, true)
			if st.Finished {
				done++
			}
			states = append(states, st)
		}
		if done == len(kids) || (first && done > 0) {
			for _, st := range states {
				if st.Finished {
					s.store.TakeReportFrom(ctx, st.ID, t.ID) // the answer is here: no message as well
				}
			}
			return toolResult(map[string]any{"agents": states}, false)
		}
		select {
		case <-tick.C:
		case <-deadline:
			return toolResult(map[string]any{"agents": states, "timed_out": true,
				"note": "Not finished yet. Wait again, do something else meanwhile, or stop it; its answer still comes as a message unless you wait for it."}, false)
		case <-ctx.Done():
			return toolFailure("The wait was cancelled")
		case <-session.Done():
			return toolFailure("The wait was cancelled")
		}
	}
}

func (s *Server) agentRead(ctx context.Context, c *store.Thread, n int) map[string]any {
	if n <= 0 {
		n = 6
	}
	if n > 30 {
		n = 30
	}
	items, _, _ := s.store.ListItemsTail(ctx, c.ID, 0, n, 600)
	msgs := []map[string]string{}
	for _, it := range items {
		if (it.Kind == "user" || it.Kind == "assistant") && strings.TrimSpace(it.Body) != "" {
			msgs = append(msgs, map[string]string{"role": map[bool]string{true: "asked", false: "agent"}[it.Kind == "user"], "text": tailHead(it.Body, 6000)})
		}
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	st := s.agentState(ctx, c, false)
	return map[string]any{"agent": st, "messages": msgs}
}

// ---------- reporting back ----------

// agentTurnDone runs after an agent's turn is over: the answer goes back to
// the conversation that started it, if it owes one and nobody waits for it.
func (s *Server) agentTurnDone(childID string) {
	ctx := s.rt.ctx
	if s.awaited(childID) {
		return // pocket_agent_wait hands it over
	}
	c, err := s.store.GetThreadAny(ctx, childID)
	if err != nil {
		return
	}
	st := s.agentState(ctx, c, true)
	if !st.Finished {
		return // another message is on its way to it
	}
	parentID := s.store.TakeReport(ctx, childID)
	if parentID == "" {
		return
	}
	p, err := s.store.GetThreadAny(ctx, parentID)
	if err != nil || p.Archived || p.UserID != c.UserID {
		return
	}
	owner, err := s.store.GetUser(ctx, p.UserID)
	if err != nil || !owner.MayRun() {
		return
	}
	if s.rt.isExternal(p.ID) {
		s.log.Info("agent report not delivered: its parent is driven elsewhere", "agent", c.ID, "parent", p.ID)
		return
	}
	if _, err := s.postMessage(ctx, owner, p, agentReport(st, c.Source == "agent" && c.SourceRef == p.ID), nil); err != nil {
		s.log.Warn("agent report not delivered", "agent", c.ID, "parent", p.ID, "err", err)
		return
	}
	s.log.Info("agent reported back", "agent", c.ID, "parent", p.ID, "state", st.State)
}

// agentReport is the message an agent's answer comes back as (the timeline
// folds it into one row).
func agentReport(st agentInfo, started bool) string {
	who := "The agent you started"
	if !started {
		who = "The conversation you messaged"
	}
	attr := func(v string) string {
		return strings.NewReplacer(`"`, "'", "<", "‹", ">", "›", "\n", " ").Replace(v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<pocket-agent-report from-name=\"%s\" agent-id=\"%s\" state=\"%s\">\n", attr(st.Title), st.ID, st.State)
	switch st.State {
	case "failed":
		fmt.Fprintf(&b, "%s stopped with an error: %s\n", who, firstNonEmpty(st.Error, "unknown error"))
		if st.Reply != "" {
			fmt.Fprintf(&b, "What it wrote before that:\n\n%s\n", st.Reply)
		}
	case "stopped":
		fmt.Fprintf(&b, "%s was stopped before it finished. What it wrote:\n\n%s\n", who, firstNonEmpty(st.Reply, "(nothing)"))
	default:
		fmt.Fprintf(&b, "%s finished. Its answer:\n\n%s\n", who, firstNonEmpty(st.Reply, "(it wrote nothing)"))
	}
	fmt.Fprintf(&b, "\n(agent_id %s — continue it with pocket_agent_send; the person can open it at %s)\n</pocket-agent-report>", st.ID, firstNonEmpty(st.URL, "/t/"+st.ID))
	return b.String()
}
