package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Heartbeats (like Paseo's): an agent sets a prompt to come back to its own
// conversation on a schedule — check a deploy every 30 minutes, a daily
// report. The schedule loop sends due ones (scheduleLoop → runHeartbeats); a
// beat is skipped while the conversation is still busy. The person approves
// a new one (agent tools) and sees and removes them in the thread.

const (
	heartbeatMinGap    = 5 * time.Minute
	heartbeatMax       = 5   // per conversation
	heartbeatHoursDflt = 168 // a week
)

// heartbeatSpec parses a schedule and checks its beats are far enough apart.
func heartbeatSpec(spec string, loc *time.Location) (*Spec, time.Time, error) {
	sp, err := ParseSpec(spec)
	if err != nil {
		return nil, time.Time{}, err
	}
	now := time.Now()
	next := sp.Next(now, loc)
	if next.IsZero() && sp.Once() {
		return nil, time.Time{}, errPast
	}
	if next.IsZero() {
		return nil, time.Time{}, errors.New("that schedule never runs")
	}
	if after := sp.Next(next, loc); !after.IsZero() && after.Sub(next) < heartbeatMinGap {
		return nil, time.Time{}, errors.New("heartbeats must be at least 5 minutes apart")
	}
	return sp, next, nil
}

func (s *Server) heartbeatTool(ctx, session context.Context, owner *store.User, t *store.Thread, sink *runSink, name string, raw json.RawMessage) json.RawMessage {
	switch name {
	case "pocket_agent_heartbeats":
		list, err := s.store.ThreadHeartbeats(ctx, t.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		return toolResult(map[string]any{"heartbeats": list}, false)
	case "pocket_agent_heartbeat_delete":
		var in struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(raw, &in) != nil || !s.store.DeleteHeartbeat(ctx, t.ID, in.ID) {
			return toolFailure("No heartbeat with that id in this conversation")
		}
		s.rt.toThread(t.ID, sseEvent{Name: "heartbeats", Data: map[string]any{}})
		return toolResult(map[string]any{"deleted": in.ID}, false)
	}
	var in struct {
		Prompt  string `json:"prompt"`
		Spec    string `json:"spec"`
		MaxRuns int    `json:"max_runs"`
		Hours   *int   `json:"ends_after_hours"`
	}
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Prompt) == "" || strings.TrimSpace(in.Spec) == "" {
		return toolFailure("Give the prompt and when (spec)")
	}
	loc := s.loc(ctx, t.UserID)
	spec, err := NormalizeSpec(in.Spec, time.Now(), loc)
	if err != nil {
		return toolFailure(err.Error())
	}
	in.Spec = spec
	sp, next, err := heartbeatSpec(in.Spec, loc)
	if err != nil {
		return toolFailure(err.Error())
	}
	if sp.Once() {
		in.MaxRuns, in.Hours = 1, new(int) // it ends when it has run
	}
	if have, _ := s.store.ThreadHeartbeats(ctx, t.ID); len(have) >= heartbeatMax {
		return toolFailure(fmt.Sprintf("This conversation has %d heartbeats already, the most it may; remove one first", len(have)))
	}
	hours := heartbeatHoursDflt
	if in.Hours != nil {
		hours = *in.Hours
	}
	h := &store.Heartbeat{ThreadID: t.ID, UserID: t.UserID, Prompt: strings.TrimSpace(in.Prompt), Spec: strings.TrimSpace(in.Spec), NextAt: store.FormatTime(next), MaxRuns: in.MaxRuns}
	if hours > 0 {
		h.EndsAt = store.FormatTime(time.Now().Add(time.Duration(hours) * time.Hour))
	}
	ends := "never"
	if h.EndsAt != "" {
		ends = store.ParseTime(h.EndsAt).In(loc).Format("Mon 2 Jan 15:04")
	}
	preview := map[string]any{"when": h.Spec, "first": next.In(loc).Format("Mon 2 Jan 15:04"), "ends": ends, "prompt": tailHead(h.Prompt, 3000)}
	if h.MaxRuns > 0 {
		preview["runs"] = h.MaxRuns
	}
	pv, _ := json.Marshal(preview)
	tool := agentTool("pocket_agent_heartbeat_create")
	req := agent.Request{ID: "ask-" + uuid.NewString(), Type: "permission", ToolName: tool.Name, Title: "Set a heartbeat", Input: pv, Status: "pending", ForID: owner.ID, App: tool.Meta.App, Kind: tool.Meta.Kind}
	match := awayMatch(tool.Name, raw)
	switch s.approveChange(ctx, session, t, sink, owner.ID, tool, req, match) {
	case "allow":
	case "defer":
		s.saveDeferred(t.ID, req, owner.ID, match)
		return toolFailure(awayDeferText)
	default:
		return toolFailure("The person did not approve the heartbeat. Nothing was set.")
	}
	if err := s.store.AddHeartbeat(ctx, h); err != nil {
		return toolFailure(err.Error())
	}
	s.rt.toThread(t.ID, sseEvent{Name: "heartbeats", Data: map[string]any{}})
	s.log.Info("heartbeat set", "thread", t.ID, "id", h.ID, "spec", h.Spec, "next", h.NextAt)
	return toolResult(map[string]any{"heartbeat": h, "note": "It comes back to this conversation as a <pocket-heartbeat> message."}, false)
}

// runHeartbeats sends the heartbeats that are due.
func (s *Server) runHeartbeats(ctx context.Context) {
	now := time.Now()
	due, err := s.store.DueHeartbeats(ctx, store.FormatTime(now))
	if err != nil {
		return
	}
	for _, h := range due {
		s.beat(ctx, h, now)
	}
}

func (s *Server) beat(ctx context.Context, h *store.Heartbeat, now time.Time) {
	t, err := s.store.GetThreadAny(ctx, h.ThreadID)
	over := err != nil || t.Archived || (h.EndsAt != "" && !now.Before(store.ParseTime(h.EndsAt))) || (h.MaxRuns > 0 && h.Runs >= h.MaxRuns)
	sp, perr := ParseSpec(h.Spec)
	if over || perr != nil {
		s.store.DeleteHeartbeat(ctx, h.ThreadID, h.ID)
		return
	}
	next := store.FormatTime(sp.Next(now, s.loc(ctx, h.UserID)))
	owner, err := s.store.GetUser(ctx, t.UserID)
	t = s.rt.decorate(t)
	if err != nil || !owner.MayRun() || t.Status == "running" || t.Status == "waiting" || t.Queued > 0 || s.rt.isExternal(t.ID) {
		if sp.Once() {
			next = store.FormatTime(now.Add(time.Minute)) // one time only: as soon as the conversation is free
		}
		_ = s.store.BeatHeartbeat(ctx, h.ID, next, false) // busy: the next one
		return
	}
	text := fmt.Sprintf("<pocket-heartbeat id=\"%d\" spec=\"%s\">\n%s\n</pocket-heartbeat>", h.ID, strings.ReplaceAll(h.Spec, `"`, "'"), h.Prompt)
	if _, err := s.postMessage(ctx, owner, t, text, nil); err != nil {
		s.log.Warn("heartbeat not sent", "thread", t.ID, "id", h.ID, "err", err)
	}
	_ = s.store.BeatHeartbeat(ctx, h.ID, next, true)
	if sp.Once() || (h.MaxRuns > 0 && h.Runs+1 >= h.MaxRuns) {
		s.store.DeleteHeartbeat(ctx, h.ThreadID, h.ID)
	}
	s.rt.toThread(t.ID, sseEvent{Name: "heartbeats", Data: map[string]any{}})
}

// GET /threads/{id}/heartbeats, DELETE /threads/{id}/heartbeats/{hid}
func (s *Server) handleThreadHeartbeats(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessView)
	if t == nil {
		return
	}
	list, err := s.store.ThreadHeartbeats(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "heartbeats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"heartbeats": list})
}

func (s *Server) handleDeleteHeartbeat(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "hid"), 10, 64)
	if !s.store.DeleteHeartbeat(r.Context(), t.ID, id) {
		writeErr(w, http.StatusNotFound, "no such heartbeat")
		return
	}
	s.rt.toThread(t.ID, sseEvent{Name: "heartbeats", Data: map[string]any{}})
	w.WriteHeader(http.StatusNoContent)
}

// GET /heartbeats: all of the person's heartbeats (Schedules page), with
// their conversation's title. DELETE /heartbeats/{hid} removes one.
func (s *Server) handleMyHeartbeats(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.UserHeartbeats(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "heartbeats")
		return
	}
	type row struct {
		*store.Heartbeat
		ThreadTitle string `json:"thread_title"`
	}
	out := make([]row, 0, len(list))
	titles := map[string]string{}
	for _, h := range list {
		title, ok := titles[h.ThreadID]
		if !ok {
			if t, err := s.store.GetThreadAny(r.Context(), h.ThreadID); err == nil {
				if t.Archived {
					continue
				}
				title = t.Title
			}
			titles[h.ThreadID] = title
		}
		out = append(out, row{h, title})
	}
	writeJSON(w, http.StatusOK, map[string]any{"heartbeats": out})
}

func (s *Server) handleDeleteMyHeartbeat(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "hid"), 10, 64)
	threadID, ok := s.store.DeleteUserHeartbeat(r.Context(), me(r), id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such heartbeat")
		return
	}
	s.rt.toThread(threadID, sseEvent{Name: "heartbeats", Data: map[string]any{}})
	w.WriteHeader(http.StatusNoContent)
}
