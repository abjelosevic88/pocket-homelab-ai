package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// PanelsScope is the Core link scope for putting a question to a panel of the
// person's models from another Pocket app (a plan in Pocket Projects, a draft
// in Pocket Docs, …). The panel runs on the person's default line-up, with no
// folder to look at, and they read every answer in Pocket AI.
const PanelsScope = "panels:write"

// panelCaller authenticates an app acting for a person with panels:write.
func (s *Server) panelCaller(w http.ResponseWriter, r *http.Request) (*store.User, string) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer pcx_") || !s.core.Configured() {
		writeErr(w, http.StatusUnauthorized, "a Pocket Core app-link token is required")
		return nil, ""
	}
	in, err := s.core.Introspect(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil || in == nil || !in.Active || !in.Delegated() || in.Audience != "ai" || !in.HasScope(PanelsScope) ||
		in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		writeErr(w, http.StatusForbidden, "Pocket AI panel link rejected")
		return nil, ""
	}
	u, err := s.coreIdentityUser(r.Context(), in)
	if err != nil {
		s.fail(w, err, "resolve AI identity")
		return nil, ""
	}
	return u, strings.ToLower(in.App.Slug)
}

// linkPanel is a panel as the app that started it sees it.
type linkPanel struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Status   string `json:"status"` // running | done | failed | stopped
	Step     string `json:"step,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Error    string `json:"error,omitempty"`
	URL      string `json:"url"`
	Finished bool   `json:"finished"`
}

func (s *Server) linkPanelOf(r *http.Request, p *store.Panel) linkPanel {
	out := linkPanel{ID: p.ID, Kind: p.Kind, Title: p.Title, Status: p.Status, Error: p.Error, Finished: p.Status != "running",
		URL: firstNonEmpty(s.publicURL("/panels/"+p.ID), "/panels/"+p.ID)}
	if p.Status == "running" && p.Stage < len(p.Spec.Stages) {
		out.Step = p.Spec.Stages[p.Stage].Name
	}
	if out.Finished {
		seats, _ := s.store.PanelSeats(r.Context(), p.ID)
		answers, _ := s.store.PanelAnswers(r.Context(), p.ID)
		out.Outcome = panelOutcome(p, seats, answers, 60000)
	}
	return out
}

// handleLinkStartPanel starts a panel for the person on their default line-up.
//
//	POST /api/v1/link/panels {kind: ask|brainstorm|council|debate|review, question, material?, depth?, title?}
func (s *Server) handleLinkStartPanel(w http.ResponseWriter, r *http.Request) {
	u, app := s.panelCaller(w, r)
	if u == nil {
		return
	}
	var in struct {
		Kind     string `json:"kind"`
		Question string `json:"question"`
		Material string `json:"material"`
		Depth    string `json:"depth"`
		Title    string `json:"title"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "invalid panel")
		return
	}
	kind, ok := panelKindByID(in.Kind)
	if !ok || kind.ID == "build" {
		writeErr(w, http.StatusBadRequest, "kind must be one of ask, brainstorm, council, debate, review")
		return
	}
	// one app does not run a person's quota dry: two of its panels at a time
	if list, err := s.store.ListPanels(r.Context(), u.ID, 50); err == nil {
		running := 0
		for _, p := range list {
			if p.Status == "running" && p.Spec.App == app {
				running++
			}
		}
		if running >= 2 {
			writeErr(w, http.StatusTooManyRequests, "two panels from this app are still running")
			return
		}
	}
	p, err := s.startPanel(r.Context(), u, panelStart{Kind: kind.ID, Question: in.Question, Material: in.Material, Depth: in.Depth, Title: oneLine(in.Title, 140), App: app})
	if err != nil {
		s.fail(w, err, "start linked panel")
		return
	}
	writeJSON(w, http.StatusCreated, s.linkPanelOf(r, p))
}

// handleLinkPanel is the state of a panel the app started, with its outcome
// once it has finished.
//
//	GET /api/v1/link/panels/{id}
func (s *Server) handleLinkPanel(w http.ResponseWriter, r *http.Request) {
	u, app := s.panelCaller(w, r)
	if u == nil {
		return
	}
	p, err := s.store.GetPanel(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil || p.Spec.App != app {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, s.linkPanelOf(r, p))
}
