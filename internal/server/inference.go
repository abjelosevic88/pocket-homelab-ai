package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if r.Method == http.MethodPost {
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
	effort := "medium"
	if r.Method == http.MethodGet || body.Model != "" {
		models, err := s.inferenceModels(r.Context(), fmt.Sprintf("%d:%s", u.ID, home), home, cwd)
		if err != nil {
			writeErr(w, 503, "Could not load models from your Claude account; try again shortly")
			return
		}
		if r.Method == http.MethodGet {
			out := core.InferenceModels{Models: []core.InferenceModel{}, DefaultModel: model, DefaultName: model}
			for _, m := range models {
				out.Models = append(out.Models, m.InferenceModel)
				if m.ID == model {
					out.DefaultName = m.Name
				}
			}
			writeJSON(w, 200, out)
			return
		}
		found := false
		for _, m := range models {
			if m.ID == body.Model {
				effort = ""
				for _, e := range m.efforts {
					if e == "medium" {
						effort = e
						break
					}
				}
				found = true
				break
			}
		}
		if !found {
			writeErr(w, 400, "This model is no longer offered by your Claude account; choose an available model or Use Pocket AI default")
			return
		}
		model = body.Model
	}
	system := body.System
	if body.JSON {
		system += "\nReturn only one valid JSON object. No markdown fences or commentary."
	}
	ctx, cancel := context.WithTimeout(r.Context(), 140*time.Second)
	defer cancel()
	text, usage, err := agent.CompleteClaude(ctx, agent.Options{Bin: s.cfg.ClaudeBin, Cwd: cwd, Model: model, Effort: effort, SystemPrompt: system, Env: agent.AccountEnv("claude", home), Log: s.log}, body.Prompt)
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

type inferenceModel struct {
	core.InferenceModel
	efforts []string
}

type inferenceModelEntry struct {
	models []inferenceModel
	at     time.Time
}

// Cache only catalog data, scoped to the caller and selected account. Account
// access and Core grants are checked on every request before consulting this.
func (s *Server) inferenceModels(ctx context.Context, key, home, cwd string) ([]inferenceModel, error) {
	s.inferenceModelsMu.Lock()
	defer s.inferenceModelsMu.Unlock()
	if cached, ok := s.inferenceModelCache[key]; ok && time.Since(cached.at) < 5*time.Minute {
		return cached.models, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider := agent.ProbeInferenceClaude(ctx, s.cfg.ClaudeBin, cwd, agent.AccountEnv("claude", home))
	models := []inferenceModel{}
	for _, m := range provider.Models {
		// "default" is represented by the explicit follow-Pocket-AI option.
		if m.ID != "default" && m.ID != "" && core.ValidInferenceModel(m.ID) {
			models = append(models, inferenceModel{InferenceModel: core.InferenceModel{ID: m.ID, Name: m.Name, Description: m.Description}, efforts: m.Efforts})
		}
	}
	if !provider.Available || len(models) == 0 {
		return nil, errors.New("model discovery unavailable")
	}
	if s.inferenceModelCache == nil {
		s.inferenceModelCache = map[string]inferenceModelEntry{}
	}
	// Evict expired entries so old accounts do not accumulate indefinitely.
	for k, v := range s.inferenceModelCache {
		if time.Since(v.at) >= 5*time.Minute {
			delete(s.inferenceModelCache, k)
		}
	}
	s.inferenceModelCache[key] = inferenceModelEntry{models: models, at: time.Now()}
	return models, nil
}
