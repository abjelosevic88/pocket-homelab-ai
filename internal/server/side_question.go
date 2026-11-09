package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

const maxSideQuestionBytes = 8000

const sideQuestionSystem = "You answer a quick side question about an ongoing conversation between a person and an AI agent. " +
	"The conversation so far is in the user message as reference text; the agent may still be working on it right now. " +
	"You cannot run tools, read files or change anything, and nothing you write reaches the agent. " +
	"Answer briefly and directly from the conversation and your general knowledge. " +
	"If the answer depends on something the conversation does not show, say so in one line instead of guessing. " +
	"Reply in the language of the question."

// handleSideQuestion answers a question about a chat without touching it: a
// one-shot, tool-less completion on the person's app model, from the same
// bounded text snapshot a fork carries. The running agent keeps working, its
// session, queue and history stay as they are, and the answer is not saved.
func (s *Server) handleSideQuestion(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	var in struct {
		Question string `json:"question"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		writeErr(w, 400, "type a question")
		return
	}
	question := strings.TrimSpace(in.Question)
	if len(question) > maxSideQuestionBytes {
		writeErr(w, 400, "keep a side question under 8,000 characters")
		return
	}
	if live := s.rt.get(t.ID); live != nil {
		s.rt.flush(live) // include what the agent streamed since the last history flush
	}
	ctx := r.Context()
	rows, err := s.store.ListItems(ctx, t.ID)
	if err != nil {
		s.fail(w, err, "read history")
		return
	}
	history, err := s.forkHistory(ctx, t, rows)
	if err != nil {
		s.fail(w, err, "read history")
		return
	}
	if history == "" {
		history = "# Reference chat history\n\n(The conversation has no messages yet.)\n"
	}
	select {
	case s.inferenceSlots <- struct{}{}:
		defer func() { <-s.inferenceSlots }()
	default:
		writeErr(w, 429, "Pocket AI is busy; try again shortly")
		return
	}
	u := userFrom(ctx)
	st, err := s.store.GetSettings(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "AI settings")
		return
	}
	home, err := s.inferenceHome(ctx, u, st)
	if err != nil {
		writeErr(w, 403, "Side questions use your Claude login: "+err.Error())
		return
	}
	base := filepath.Join(s.cfg.DataDir, "inference")
	if err := os.MkdirAll(base, 0700); err != nil {
		s.fail(w, err, "side question workspace")
		return
	}
	cwd, err := os.MkdirTemp(base, "side-")
	if err != nil {
		s.fail(w, err, "side question workspace")
		return
	}
	defer os.RemoveAll(cwd)
	cwd, _ = filepath.Abs(cwd)
	model, effort := appDefaultModel(st), ""
	if models, err := s.inferenceModels(ctx, fmt.Sprintf("%d:%s", u.ID, home), home, cwd); err == nil {
		found := false
		for _, m := range models {
			if m.ID == model {
				effort, found = inferenceEffort(st.AppEffort, m.efforts), true
				break
			}
		}
		if !found && len(models) > 0 {
			model, effort = models[0].ID, inferenceEffort(st.AppEffort, models[0].efforts)
		}
	}
	prompt := history + "\n\n---\n\n# Side question\n\n" + question
	cctx, cancel := context.WithTimeout(ctx, 140*time.Second)
	defer cancel()
	start := time.Now()
	text, usage, err := agent.CompleteClaudeInput(cctx, agent.Options{Bin: s.cfg.ClaudeBin, Cwd: cwd, Model: model, Effort: effort,
		SystemPrompt: sideQuestionSystem, Env: agent.AccountEnv("claude", home), Log: s.log}, agent.Input{Text: prompt})
	if err != nil {
		s.log.Warn("side question failed", "user", u.ID, "thread", t.ID, "err", err)
		writeErr(w, 502, "Could not answer the side question; check your Claude login in Pocket AI Settings")
		return
	}
	writeJSON(w, 200, map[string]any{"question": question, "answer": strings.TrimSpace(text), "model": model,
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "ms": time.Since(start).Milliseconds()})
}
