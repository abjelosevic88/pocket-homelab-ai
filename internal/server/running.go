package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// runningItem is one entry of "Running now": a session with a turn in
// progress anywhere on the server, or one of the person's threads that is
// running or waiting in Pocket AI.
type runningItem struct {
	agent.ActiveSession
	ThreadID string `json:"thread_id"` // the person's thread for it, if any
	Status   string `json:"status"`    // running | waiting
}

// runningFor lists what is running for a person: what the CLIs on the server
// are doing that the person may see (sessionFilter; only for people who may
// run agents) plus the person's own Pocket AI threads.
func (s *Server) runningFor(ctx context.Context, u *store.User) []runningItem {
	out := []runningItem{}
	seen := map[string]bool{}
	threads, _ := s.store.ListThreads(ctx, u.ID, store.ThreadFilter{Limit: 200})
	byID := map[string]*store.Thread{}
	for _, t := range threads {
		if t.ProviderSessionID != "" {
			byID[t.Provider+":"+t.ProviderSessionID] = t
		}
	}
	if u.MayRun() {
		mine := s.accountIDsFor(ctx, u)
		visible := s.sessionFilter(ctx, u)
		for _, a := range s.rt.activeSnapshot() {
			if !mine[a.Provider+"/"+strconv.FormatInt(a.Account, 10)] || !visible(a.Provider, a.SessionID, a.Account) {
				continue // someone else's login or chat
			}
			it := runningItem{ActiveSession: a, Status: "running"}
			if t := byID[a.Provider+":"+a.SessionID]; t != nil {
				it.ThreadID = t.ID
				s.rt.decorate(t)
				if t.Live {
					it.Origin = "pocket-ai"
				}
				if t.Pending > 0 || t.Status == "waiting" {
					it.Status = "waiting"
				}
				seen[t.ID] = true
			}
			out = append(out, it)
		}
	}
	for _, t := range threads {
		if seen[t.ID] {
			continue
		}
		s.rt.decorate(t)
		if t.Status != "running" && t.Status != "waiting" && t.Subagents == nil {
			continue
		}
		status, prompt := t.Status, t.LastPrompt
		if status != "running" && status != "waiting" { // the turn ended, its sub-agents work on
			status = "running"
			if subs, ok := t.Subagents.([]agent.Subagent); ok {
				prompt = fmt.Sprintf("%d sub-agents working: %s", len(subs), subs[0].Description)
				if len(subs) == 1 {
					prompt = "A sub-agent is working: " + subs[0].Description
				}
			}
		}
		out = append(out, runningItem{ActiveSession: agent.ActiveSession{Provider: t.Provider, SessionID: t.ProviderSessionID, Cwd: t.Cwd,
			Title: t.Title, Prompt: prompt, PromptAt: firstNonEmpty(t.PromptAt, t.ActivityAt), UpdatedAt: t.ActivityAt, Origin: "pocket-ai"},
			ThreadID: t.ID, Status: status})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PromptAt > out[j].PromptAt })
	return out
}

func (s *Server) handleRunning(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runningFor(r.Context(), userFrom(r.Context())))
}

// activeSnapshot is the last scan of the session files.
func (rt *engine) activeSnapshot() []agent.ActiveSession {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]agent.ActiveSession(nil), rt.active...)
}

// activeLoop rescans the CLIs' session files every few seconds while
// someone has the app open, and tells them when the list changed.
func (rt *engine) activeLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	var last []byte
	for {
		rt.mu.Lock()
		watching := len(rt.userSubs) > 0
		rt.mu.Unlock()
		if watching || last == nil {
			list := agent.ActiveSessions(rt.s.homes(ctx), rt.s.cfg.PaseoHome)
			b, _ := json.Marshal(list)
			if string(b) != string(last) {
				rt.mu.Lock()
				rt.active = list
				for _, subs := range rt.userSubs {
					for ch := range subs {
						deliver(ch, sseEvent{Name: "running", Data: len(list)})
					}
				}
				rt.mu.Unlock()
				last = b
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
