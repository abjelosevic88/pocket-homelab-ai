package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/google/uuid"
)

// The pocket_panel_* tools: an agent puts a question to a panel of models
// (panels.go) the way the person does from the Panels page. Starting one
// asks the person first, like starting another agent.

const panelToolsInstructions = ` Panels: with pocket_panel_start you put one question to several AI models at once (the person's other agents and models), run by Pocket AI in rounds: "ask" (same question, answers compared and merged), "brainstorm", "council" (they judge a plan or idea and criticise each other blind), "debate" (two sides, a judge) and "review" (a second opinion on an answer, plan or change). Use it when the person asks for other models' views, a council, a debate or a second opinion, or on their "/council"-style commands; not on your own for ordinary work, since it uses several models' quota. The participants do not see this conversation: write the question and the material out in full. It asks the person first unless they always allow it; then wait for the outcome with pocket_panel_wait.`

func panelToolDefs() []struct {
	name, description, schema string
	read                      bool
} {
	return []struct {
		name, description, schema string
		read                      bool
	}{
		{"pocket_panel_start", "Put one question to a panel of several AI models (the person's agents and models), run by Pocket AI in rounds; one model writes the summary. The person approves the start unless they always allow it, and sees every answer on the panel's page. The participants do not see this conversation, so question and material must be complete. Returns its panel_id: wait for the outcome with pocket_panel_wait (or it comes back to you as a message when it finishes).", `{"type":"object","properties":{
"kind":{"type":"string","enum":["ask","brainstorm","council","debate","review"],"description":"ask: the same question to all, answers compared and merged. brainstorm: ideas, built on each other, grouped. council: they judge a plan or idea from different points of view and criticise each other without seeing who wrote what. debate: two sides are assigned and argue, a judge rules. review: a second opinion on an answer, plan or change (material)."},
"question":{"type":"string","maxLength":20000,"description":"What they are asked, written so that a model without this conversation understands it."},
"material":{"type":"string","maxLength":200000,"description":"What is judged or reviewed, in full: the plan, the idea, the answer, the diff, the background. They can also read files in this conversation's folder."},
"depth":{"type":"string","enum":["quick","standard","deep"],"description":"quick: one round and the summary. standard (default): they also see and answer each other. deep: one more round to revise; slower, more quota."},
"title":{"type":"string","maxLength":120},
"report":{"type":"boolean","description":"Send the outcome to this conversation as a message when it finishes (default true)."}
},"required":["kind","question"]}`, false},
		{"pocket_panel_wait", "Wait until a panel started from this conversation has finished, up to timeout_seconds (default 300, at most 600). Returns its state, who took part and, when finished, the outcome. Call again to wait longer: a standard panel takes a few minutes.", `{"type":"object","properties":{"panel_id":{"type":"string"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["panel_id"]}`, true},
		{"pocket_panel_read", "Read a panel started from this conversation: its state, the summary, and each participant's latest answer.", `{"type":"object","properties":{"panel_id":{"type":"string"}},"required":["panel_id"]}`, true},
	}
}

// panelToolList is the panel tools as tool definitions.
func panelToolList() []askTool {
	out := []askTool{}
	for _, d := range panelToolDefs() {
		t := askTool{Name: d.name, Description: d.description, InputSchema: json.RawMessage(d.schema)}
		t.Annotations.ReadOnly = d.read
		if d.name == "pocket_panel_start" {
			t.Meta.App, t.Meta.Kind = "ai", "add" // "Always allow… everything new in AI" covers it
		}
		out = append(out, t)
	}
	return out
}

func panelTool(name string) askTool {
	for _, t := range panelToolList() {
		if t.Name == name {
			return t
		}
	}
	return askTool{Name: name}
}

// panelState is a panel as an agent is told about it.
func (s *Server) panelState(ctx context.Context, p *store.Panel, answersToo bool) map[string]any {
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	answers, _ := s.store.PanelAnswers(ctx, p.ID)
	who := []map[string]any{}
	latest := map[int64]*store.PanelAnswer{}
	for _, a := range answers {
		if cur := latest[a.SeatID]; cur == nil || a.Stage > cur.Stage {
			latest[a.SeatID] = a
		}
	}
	for _, st := range seats {
		if st.Chair {
			continue
		}
		m := map[string]any{"participant": seatWho(st), "state": st.State}
		if st.Error != "" {
			m["error"] = st.Error
		}
		if a := latest[st.ID]; a != nil && answersToo {
			m["latest_answer"] = tailHead(a.Text, 6000)
		}
		who = append(who, m)
	}
	out := map[string]any{"panel_id": p.ID, "kind": p.Kind, "title": p.Title, "state": p.Status, "finished": p.Status != "running",
		"participants": who, "url": firstNonEmpty(s.publicURL("/panels/"+p.ID), "/panels/"+p.ID)}
	if p.Status == "running" && p.Stage < len(p.Spec.Stages) {
		out["step"] = fmt.Sprintf("%d of %d: %s", p.Stage+1, len(p.Spec.Stages), p.Spec.Stages[p.Stage].Name)
	}
	if p.Status != "running" {
		if p.Error != "" {
			out["error"] = p.Error
		}
		out["outcome"] = panelOutcome(p, seats, answers, 24000)
	}
	return out
}

func (s *Server) callPanelTool(ctx, session context.Context, owner *store.User, t *store.Thread, sink *runSink, name string, raw json.RawMessage) json.RawMessage {
	if t.Source == "panel" {
		return toolFailure("A panel's participant cannot start a panel")
	}
	mine := func(id string) (*store.Panel, error) {
		p, err := s.store.GetPanel(ctx, owner.ID, strings.TrimSpace(id))
		if err != nil || p.FromThreadID != t.ID {
			return nil, errors.New("No panel with that id was started from this conversation")
		}
		return p, nil
	}
	switch name {
	case "pocket_panel_start":
		var in struct {
			Kind     string `json:"kind"`
			Question string `json:"question"`
			Material string `json:"material"`
			Depth    string `json:"depth"`
			Title    string `json:"title"`
			Report   *bool  `json:"report"`
		}
		if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Question) == "" {
			return toolFailure("Write the question")
		}
		kind, ok := panelKindByID(in.Kind)
		if !ok || kind.ID == "build" {
			return toolFailure("kind must be one of ask, brainstorm, council, debate, review")
		}
		if s.agentDepth(ctx, t) >= agentMaxDepth {
			return toolFailure("An agent started by another agent cannot start a panel")
		}
		if list, err := s.store.PanelsFrom(ctx, t.ID); err == nil {
			running := 0
			for _, p := range list {
				if p.Status == "running" {
					running++
				}
			}
			if running >= 2 {
				return toolFailure("Two panels from this conversation are still running; wait for one with pocket_panel_wait")
			}
		}
		start := panelStart{Kind: kind.ID, Question: in.Question, Material: in.Material, Depth: in.Depth, Title: in.Title, FromThreadID: t.ID, Report: in.Report == nil || *in.Report}
		seats, chair, access := s.defaultSeats(ctx, owner)
		// never freer than this conversation's own agent
		capLevel, known := modeLevel[t.Mode]
		if !known {
			capLevel = 1
		}
		start.accessCap = &capLevel
		if panelAccessLevel[access] > capLevel {
			access = map[bool]string{true: "auto", false: ""}[capLevel >= panelAccessLevel["auto"]]
		}
		names := []string{}
		for _, sp := range seats {
			st := store.PanelSeat{PanelSeatSpec: sp}
			names = append(names, seatWho(&st))
		}
		if len(seats) < 2 && kind.ID != "review" || len(seats) == 0 {
			return toolFailure("A panel needs at least two agents or models that work on this server, and only " + fmt.Sprint(len(seats)) + " does. The person can set up a line-up on the Panels page.")
		}
		tool := panelTool(name)
		preview := map[string]any{
			"panel":        kind.Name + " · " + panelDepthNames[panelDepth(in.Depth)],
			"participants": strings.Join(names, "; "),
			"question":     tailHead(in.Question, 3000),
		}
		preview["they may"] = map[string]string{"": "only look (read-only)", "auto": "work on their own, each in its automatic mode", "full": "do anything without asking (full access)"}[access]
		if chair != nil {
			st := store.PanelSeat{PanelSeatSpec: *chair}
			st.Role = ""
			preview["summary by"] = seatWho(&st)
		}
		if m := strings.TrimSpace(in.Material); m != "" {
			preview["material"] = tailHead(m, 3000)
		}
		pv, _ := json.Marshal(preview)
		req := agent.Request{ID: "ask-" + uuid.NewString(), Type: "permission", ToolName: tool.Name, Title: "Start a panel of models", Input: pv, Status: "pending",
			ForID: owner.ID, App: tool.Meta.App, Kind: tool.Meta.Kind}
		match := awayMatch(tool.Name, raw)
		switch s.approveChange(ctx, session, t, sink, owner.ID, tool, req, match) {
		case "allow":
		case "defer":
			s.saveDeferred(t.ID, req, owner.ID, match)
			return toolFailure(awayDeferText)
		default:
			return toolFailure("The person did not approve starting this panel. Nothing was started.")
		}
		p, err := s.startPanel(context.WithoutCancel(ctx), owner, start)
		if err != nil {
			return toolFailure(err.Error())
		}
		out := s.panelState(ctx, p, false)
		out["next"] = "Wait for the outcome with pocket_panel_wait (a standard panel takes a few minutes)."
		return toolResult(out, false)
	case "pocket_panel_wait":
		var in struct {
			ID      string `json:"panel_id"`
			Timeout int    `json:"timeout_seconds"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give panel_id")
		}
		p, err := mine(in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		if in.Timeout <= 0 {
			in.Timeout = 300
		}
		if in.Timeout > agentWaitMax {
			in.Timeout = agentWaitMax
		}
		defer s.panelWaitOn(p.ID)()
		deadline := time.After(time.Duration(in.Timeout) * time.Second)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			fresh, err := s.store.GetPanelAny(ctx, p.ID)
			if err != nil {
				return toolFailure("The panel was deleted")
			}
			if fresh.Status != "running" {
				return toolResult(s.panelState(ctx, fresh, false), false)
			}
			select {
			case <-tick.C:
			case <-deadline:
				out := s.panelState(ctx, fresh, false)
				out["timed_out"] = true
				out["note"] = "Not finished yet. Wait again; its outcome still comes as a message unless you are waiting for it."
				return toolResult(out, false)
			case <-ctx.Done():
				return toolFailure("The wait was cancelled")
			case <-session.Done():
				return toolFailure("The wait was cancelled")
			}
		}
	case "pocket_panel_read":
		var in struct {
			ID string `json:"panel_id"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return toolFailure("Give panel_id")
		}
		p, err := mine(in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		return toolResult(s.panelState(ctx, p, true), false)
	}
	return toolFailure("Unknown panel tool")
}
