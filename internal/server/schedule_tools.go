package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Schedules from a chat: an agent sets a prompt to run later as its own new
// conversation, once ("tomorrow 09:00", "in 2h") or repeating. It is an
// ordinary schedule (Schedules page: edit, run now, remove) that remembers the
// conversation it came from and, by default for a one-time run, sends its
// answer back there (agentTurnDone). Unlike a heartbeat it does not need this
// conversation to be free, and it starts fresh.

const scheduleFromChatMax = 20 // waiting schedules set from one conversation

type scheduleCreate struct {
	Prompt   string `json:"prompt"`
	When     string `json:"when"`
	Name     string `json:"name"`
	Report   *bool  `json:"report"`
	Notify   string `json:"notify"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Machine  string `json:"machine"`
	Folder   string `json:"folder"`
	ReadOnly bool   `json:"read_only"`
	Mode     string `json:"mode"`
}

func (s *Server) scheduleTool(ctx, session context.Context, owner *store.User, t *store.Thread, sink *runSink, name string, raw json.RawMessage) json.RawMessage {
	loc := s.loc(ctx, t.UserID)
	switch name {
	case "pocket_agent_schedules":
		list, err := s.store.ListSchedules(ctx, t.UserID)
		if err != nil {
			return toolFailure(err.Error())
		}
		out := make([]map[string]any, 0, len(list))
		for _, sc := range list {
			row := map[string]any{"id": sc.ID, "name": sc.Name, "when": sc.Spec, "enabled": sc.Enabled, "once": IsOnce(sc.Spec),
				"from_this_conversation": sc.FromThreadID == t.ID, "prompt": tailHead(sc.Prompt, 400)}
			if sc.NextRunAt != "" {
				row["next_run"] = store.ParseTime(sc.NextRunAt).In(loc).Format("Mon 2 Jan 2006 15:04")
			}
			if sc.LastRunAt != "" {
				row["last_run"] = map[string]any{"at": store.ParseTime(sc.LastRunAt).In(loc).Format("Mon 2 Jan 2006 15:04"), "status": sc.LastStatus,
					"summary": sc.LastSummary, "agent_id": sc.LastThreadID}
			}
			out = append(out, row)
		}
		return toolResult(map[string]any{"schedules": out, "time_zone": loc.String(), "now": time.Now().In(loc).Format("Mon 2 Jan 2006 15:04")}, false)
	case "pocket_agent_schedule_delete":
		var in struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give the schedule's id (pocket_agent_schedules)")
		}
		sc, err := s.store.GetSchedule(ctx, t.UserID, in.ID)
		if err != nil {
			return toolFailure("No schedule with that id (pocket_agent_schedules lists them)")
		}
		if sc.FromThreadID != t.ID {
			return toolFailure("That schedule was not set from this conversation; the person can remove it on the Schedules page (" + s.publicURL("/schedules") + ")")
		}
		if err := s.store.DeleteSchedule(ctx, t.UserID, sc.ID); err != nil {
			return toolFailure(err.Error())
		}
		s.log.Info("schedule removed from a chat", "thread", t.ID, "id", sc.ID)
		return toolResult(map[string]any{"deleted": sc.ID}, false)
	}

	var in scheduleCreate
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Prompt) == "" || strings.TrimSpace(in.When) == "" {
		return toolFailure("Give the prompt and when it runs")
	}
	sc, err := s.scheduleFromChat(ctx, owner, t, in, loc)
	if err != nil {
		return toolFailure(err.Error())
	}
	if n, _ := s.store.CountWaitingSchedulesFrom(ctx, t.ID); n >= scheduleFromChatMax {
		return toolFailure("This conversation has set many schedules that are still waiting; remove one first (pocket_agent_schedule_delete)")
	}
	once := IsOnce(sc.Spec)
	next := store.ParseTime(sc.NextRunAt).In(loc)
	preview := map[string]any{
		"name":        sc.Name,
		"when":        next.Format("Mon 2 Jan 2006 15:04") + map[bool]string{true: " (once)", false: ", then " + sc.Spec}[once],
		"agent":       strings.TrimSpace(providerName(sc.Provider) + " · " + firstNonEmpty(sc.Model, "default model") + " " + sc.Effort),
		"where":       s.machineName(ctx, sc.HostID) + ": " + firstNonEmpty(sc.Cwd, "its home folder"),
		"permissions": modeName(sc.Provider, sc.Mode),
		"notify":      sc.Notify,
		"task":        tailHead(sc.Prompt, 4000),
	}
	if sc.Report {
		preview["answer"] = "back to this conversation"
	}
	pv, _ := json.Marshal(preview)
	tool := agentTool("pocket_agent_schedule_create")
	req := agent.Request{ID: "ask-" + uuid.NewString(), Type: "permission", ToolName: tool.Name, Title: map[bool]string{true: "Run this later", false: "Set a schedule"}[once],
		Input: pv, Status: "pending", ForID: owner.ID, App: tool.Meta.App, Kind: tool.Meta.Kind}
	match := awayMatch(tool.Name, raw)
	switch s.approveChange(ctx, session, t, sink, owner.ID, tool, req, match) {
	case "allow":
	case "defer":
		s.saveDeferred(t.ID, req, owner.ID, match)
		return toolFailure(awayDeferText)
	default:
		return toolFailure("The person did not approve the schedule. Nothing was set.")
	}
	// the approval may have taken a while
	if sp, err := ParseSpec(sc.Spec); err == nil {
		n := sp.Next(time.Now(), loc)
		if n.IsZero() {
			return toolFailure("That time passed while it waited for approval; nothing was set")
		}
		sc.NextRunAt = store.FormatTime(n)
	}
	if err := s.store.SaveSchedule(ctx, sc); err != nil {
		return toolFailure(err.Error())
	}
	s.log.Info("schedule set from a chat", "thread", t.ID, "id", sc.ID, "spec", sc.Spec, "next", sc.NextRunAt, "report", sc.Report)
	out := map[string]any{"schedule_id": sc.ID, "name": sc.Name, "when": sc.Spec, "first_run": store.ParseTime(sc.NextRunAt).In(loc).Format("Mon 2 Jan 2006 15:04"),
		"time_zone": loc.String(), "once": once, "reports_back": sc.Report, "page": s.publicURL("/schedules")}
	if sc.Report {
		out["note"] = "Its answer comes to this conversation as a message when it has run."
	}
	return toolResult(out, false)
}

// scheduleFromChat builds (but does not store) the schedule an agent asked for,
// with this conversation's agent, machine, folder and permissions as defaults.
func (s *Server) scheduleFromChat(ctx context.Context, owner *store.User, t *store.Thread, in scheduleCreate, loc *time.Location) (*store.Schedule, error) {
	spec, err := NormalizeSpec(in.When, time.Now(), loc)
	if err != nil {
		return nil, err
	}
	sp, err := ParseSpec(spec)
	if err != nil {
		return nil, err
	}
	next := sp.Next(time.Now(), loc)
	if next.IsZero() {
		if sp.Once() {
			return nil, errPast
		}
		return nil, errors.New("that schedule never runs")
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
	sc := &store.Schedule{UserID: t.UserID, Prompt: strings.TrimSpace(in.Prompt), Provider: provider, Model: in.Model, Effort: in.Effort, Mode: mode,
		HostID: host, Cwd: strings.TrimSpace(in.Folder), Spec: spec, Enabled: true, NextRunAt: store.FormatTime(next), FromThreadID: t.ID}
	if in.ReadOnly && provider == "claude" {
		sc.Prompt += "\n\n(You are in read-only mode: look, don't change anything, and answer in text. There is no implementation plan to approve, so do not call ExitPlanMode.)"
	}
	if host == t.HostID {
		if sc.Cwd == "" {
			sc.Cwd = t.Cwd
		}
		if provider == t.Provider {
			sc.Speed = t.Speed
		}
	}
	if provider == t.Provider && sc.Model == "" {
		sc.Model = t.Model
		if sc.Effort == "" {
			sc.Effort = t.Effort
		}
	}
	once := sp.Once()
	sc.Report = once
	if in.Report != nil {
		sc.Report = *in.Report
	}
	switch in.Notify {
	case "always", "attention", "never":
		sc.Notify = in.Notify
	default:
		sc.Notify = map[bool]string{true: "always", false: "attention"}[once]
	}
	sc.Name = strings.TrimSpace(in.Name)
	if sc.Name == "" {
		sc.Name = oneLine(firstNonEmpty(t.Title, "Scheduled run"), 60)
		if once {
			sc.Name = "Later: " + oneLine(sc.Prompt, 50)
		}
	}
	if err := s.checkAgentOn(ctx, owner, sc.HostID, &sc.Provider, &sc.Mode, &sc.Cwd); err != nil {
		return nil, err
	}
	return sc, nil
}
