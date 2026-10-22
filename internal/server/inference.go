package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// handleInference accepts only app-bound delegation. It is deliberately outside
// general account authentication: this scope cannot read threads or run tools.
func (s *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer pcx_") || !s.core.Configured() {
		writeErr(w, 401, "a Pocket Core app-link token is required")
		return
	}
	in, err := s.core.Introspect(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil || in == nil || !in.Active || !in.Delegated() || in.Audience != "ai" || !in.HasScope(core.InferenceScope) || in.User == nil || in.User.Email == "" {
		writeErr(w, 403, "Pocket AI text-generation link rejected")
		return
	}
	u, err := s.coreIdentityUser(r.Context(), in)
	if err != nil {
		s.fail(w, err, "resolve AI identity")
		return
	}
	var body core.InferenceRequest
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" || len(body.Prompt)+len(body.System) > 300000 {
		writeErr(w, 400, "provide a prompt and system instructions totaling at most 300,000 bytes")
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		writeErr(w, 400, "provide one JSON request object")
		return
	}
	select {
	case s.inferenceSlots <- struct{}{}:
		defer func() { <-s.inferenceSlots }()
	default:
		writeErr(w, 429, "Pocket AI is busy; try again shortly")
		return
	}
	st, err := s.store.GetSettings(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err, "AI settings")
		return
	}
	if err := s.checkAccount(r.Context(), u, "claude", st.Claude.Account); err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	// Resolve the selected account without falling back to another person's login.
	home := s.defaultHome("claude")
	if st.Claude.Account != 0 {
		a, err := s.store.GetAccount(r.Context(), st.Claude.Account)
		if err != nil || !mayUseAccount(u, a) || a.Provider != "claude" {
			writeErr(w, 403, "Claude account unavailable")
			return
		}
		home = a.Home
	}
	base := filepath.Join(s.cfg.DataDir, "inference")
	if err := os.MkdirAll(base, 0700); err != nil {
		s.fail(w, err, "inference workspace")
		return
	}
	cwd, err := os.MkdirTemp(base, "request-")
	if err != nil {
		s.fail(w, err, "inference workspace")
		return
	}
	defer os.RemoveAll(cwd)
	cwd, _ = filepath.Abs(cwd)
	model := st.Claude.Model
	if model == "" || model == "default" {
		model = "sonnet"
	}
	system := body.System
	if body.JSON {
		system += "\nReturn only one valid JSON object. No markdown fences or commentary."
	}
	ctx, cancel := context.WithTimeout(r.Context(), 140*time.Second)
	defer cancel()
	text, usage, err := agent.CompleteClaude(ctx, agent.Options{Bin: s.cfg.ClaudeBin, Cwd: cwd, Model: model, Effort: "medium", SystemPrompt: system, Env: agent.AccountEnv("claude", home), Log: s.log}, body.Prompt)
	if err != nil {
		s.log.Warn("app inference failed", "user", u.ID, "err", err)
		writeErr(w, 502, "Pocket AI could not generate an answer; check your Claude account in Pocket AI")
		return
	}
	if body.JSON {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(text), "```json"), "```"), "```"))
		if !json.Valid([]byte(text)) {
			writeErr(w, 502, "Pocket AI returned invalid JSON")
			return
		}
	}
	writeJSON(w, 200, core.InferenceReply{Text: text, Model: "pocket-ai/" + model, InputTokens: int(usage.InputTokens), OutputTokens: int(usage.OutputTokens)})
}
