package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) panelRoutes(r chi.Router) {
	r.Get("/panels", s.handleListPanels)
	r.Post("/panels", s.handleStartPanel)
	r.Get("/panels/options", s.handlePanelOptions)
	r.Post("/panels/lineups", s.handleSaveLineup)
	r.Put("/panels/lineups/{lid}", s.handleSaveLineup)
	r.Delete("/panels/lineups/{lid}", s.handleDeleteLineup)
	r.Post("/panels/kinds", s.handleSavePanelKind)
	r.Put("/panels/kinds/{rid}", s.handleSavePanelKind)
	r.Delete("/panels/kinds/{rid}", s.handleDeletePanelKind)
	r.Get("/panels/{id}", s.handleGetPanel)
	r.Patch("/panels/{id}", s.handleRenamePanel)
	r.Delete("/panels/{id}", s.handleDeletePanel)
	r.Post("/panels/{id}/stop", s.handleStopPanel)
	r.Post("/panels/{id}/again", s.handlePanelAgain)
	r.Post("/panels/{id}/continue", s.handlePanelContinue)
	r.Get("/panels/{id}/export.md", s.handlePanelExport)
	r.Get("/threads/{id}/panels", s.handleThreadPanels)
}

// panelSeatView is a participant as the web app shows it.
type panelSeatView struct {
	*store.PanelSeat
	Name    string `json:"name"`    // the agent: "Claude Code"
	Who     string `json:"who"`     // "Claude opus, Sceptic (A)"
	Machine string `json:"machine"` // "" = this server
	// Activity: starting | working | waiting_for_person | done | failed | skipped | waiting
	Activity string `json:"activity"`
	Live     string `json:"live,omitempty"` // what it has written so far in the step it is at
	// While it works: how many things it has looked at or run in this step,
	// the latest of them in a few words, and since when it is at it.
	Steps   int     `json:"steps,omitempty"`
	Doing   string  `json:"doing,omitempty"`
	Since   string  `json:"since,omitempty"`
	CostUSD float64 `json:"cost_usd"`
	Tokens  int64   `json:"tokens"`
}

type panelView struct {
	*store.Panel
	Depth     string               `json:"depth"`
	Seats     []panelSeatView      `json:"seats"`
	Answers   []*store.PanelAnswer `json:"answers,omitempty"`
	CostUSD   float64              `json:"cost_usd"`
	FromTitle string               `json:"from_title,omitempty"`
}

// viewPanel builds a panel for the web app; full adds the answers and what
// the participants are writing right now.
func (s *Server) viewPanel(ctx context.Context, p *store.Panel, full bool) panelView {
	v := panelView{Panel: p, Depth: panelDepthNames[min(max(p.Spec.Depth, 0), panelDeep)], Seats: []panelSeatView{}}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	for _, st := range seats {
		sv := panelSeatView{PanelSeat: st, Name: providerName(st.Provider), Who: seatWho(st), Activity: st.State}
		if st.Provider == "claude" {
			sv.Name = "Claude Code"
		}
		if st.HostID != 0 {
			sv.Machine = s.machineName(ctx, st.HostID)
		}
		if st.ThreadID != "" {
			if th, err := s.store.GetThreadAny(ctx, st.ThreadID); err == nil {
				sv.CostUSD, sv.Tokens = th.Usage.CostUSD, th.Usage.InputTokens+th.Usage.OutputTokens
				v.CostUSD += th.Usage.CostUSD
				if st.State == "working" && p.Status == "running" {
					info := s.agentState(ctx, th, false)
					if !info.Finished {
						sv.Activity = info.State
					}
					sv.Since = firstNonEmpty(th.PromptAt, th.ActivityAt)
					if full {
						sv.Live, _, _ = s.store.LastReply(ctx, th.ID)
						sv.Steps, sv.Doing = s.seatDoing(ctx, th.ID)
					}
				}
			}
		}
		v.Seats = append(v.Seats, sv)
	}
	if full {
		v.Answers, _ = s.store.PanelAnswers(ctx, p.ID)
		if v.Answers == nil {
			v.Answers = []*store.PanelAnswer{}
		}
		if p.FromThreadID != "" {
			if th, err := s.store.GetThreadAny(ctx, p.FromThreadID); err == nil && th.UserID == p.UserID {
				v.FromTitle = firstNonEmpty(th.Title, "a conversation")
			}
		}
	}
	return v
}

// longPathRe finds a long absolute path, to show only its last two parts.
var longPathRe = regexp.MustCompile(`/(?:[^/\s]+/){2,}([^/\s]+/[^/\s]+)`)

// seatDoing is what a participant has done since it was asked: how many tool
// calls, and the latest one in a few words.
func (s *Server) seatDoing(ctx context.Context, threadID string) (int, string) {
	items, _, err := s.store.ListItemsTail(ctx, threadID, 0, 1, 300)
	if err != nil {
		return 0, ""
	}
	live := s.rt.liveItems(threadID)
	n, last := 0, ""
	for _, it := range items {
		switch it.Kind {
		case "user":
			n, last = 0, ""
		case "tool":
			var v agent.Item
			if lv, ok := live[it.Key]; ok {
				v = lv
			} else if json.Unmarshal(it.Data, &v) != nil {
				continue
			}
			if v.Tool == nil {
				continue
			}
			n++
			last = oneLine(longPathRe.ReplaceAllString(firstNonEmpty(v.Tool.Title, v.Tool.Name), "…/$1"), 90)
		}
	}
	return n, last
}

func (s *Server) handleListPanels(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPanels(r.Context(), me(r), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, err, "list panels")
		return
	}
	out := make([]panelView, 0, len(list))
	for _, p := range list {
		p.Material = "" // the list does not need it
		out = append(out, s.viewPanel(r.Context(), p, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleThreadPanels(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	list, err := s.store.PanelsFrom(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "list panels")
		return
	}
	out := make([]panelView, 0, len(list))
	for _, p := range list {
		p.Material = ""
		out = append(out, s.viewPanel(r.Context(), p, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) panel(w http.ResponseWriter, r *http.Request) *store.Panel {
	p, err := s.store.GetPanel(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "panel")
		return nil
	}
	return p
}

func (s *Server) handleGetPanel(w http.ResponseWriter, r *http.Request) {
	if p := s.panel(w, r); p != nil {
		writeJSON(w, http.StatusOK, s.viewPanel(r.Context(), p, true))
	}
}

func (s *Server) handleStartPanel(w http.ResponseWriter, r *http.Request) {
	var in panelStart
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	p, err := s.startPanel(r.Context(), userFrom(r.Context()), in)
	if err != nil {
		s.fail(w, err, "start panel")
		return
	}
	writeJSON(w, http.StatusCreated, s.viewPanel(r.Context(), p, true))
}

func (s *Server) handleStopPanel(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	if p.Status == "running" {
		if err := s.stopPanel(r.Context(), p); err != nil {
			s.fail(w, err, "stop panel")
			return
		}
	}
	if fresh, err := s.store.GetPanel(r.Context(), p.UserID, p.ID); err == nil {
		p = fresh
	}
	writeJSON(w, http.StatusOK, s.viewPanel(r.Context(), p, true))
}

func (s *Server) handleRenamePanel(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	title := oneLine(in.Title, 140)
	if title == "" {
		writeErr(w, http.StatusBadRequest, "Give it a name")
		return
	}
	if _, err := s.store.RenamePanel(r.Context(), p.UserID, p.ID, title, ""); err != nil {
		s.fail(w, err, "rename panel")
		return
	}
	p.Title = title
	s.panelChanged(p)
	writeJSON(w, http.StatusOK, s.viewPanel(r.Context(), p, true))
}

func (s *Server) handleDeletePanel(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	if err := s.deletePanel(r.Context(), p); err != nil {
		s.fail(w, err, "delete panel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// panelAgainStart is a finished panel's settings, to run it once more.
func (s *Server) panelAgainStart(ctx context.Context, p *store.Panel) panelStart {
	in := panelStart{Kind: p.Kind, RecipeID: p.Spec.RecipeID, Question: p.Question, Material: p.Material, Title: p.Title,
		Depth: panelDepthNames[min(max(p.Spec.Depth, 0), panelDeep)], Cwd: p.Spec.Cwd, HostID: &p.Spec.HostID,
		FromThreadID: p.FromThreadID, LimitMin: p.Spec.LimitMin, Mode: p.Spec.Mode}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	for _, st := range seats {
		sp := st.PanelSeatSpec
		if st.Chair {
			if in.Chair == nil { // the one first chosen, not its replacement
				in.Chair = &sp
			}
			continue
		}
		in.Seats = append(in.Seats, sp)
	}
	if p.FromThreadID != "" {
		if _, err := s.store.GetThread(ctx, p.UserID, p.FromThreadID); err != nil {
			in.FromThreadID = ""
		}
	}
	return in
}

func (s *Server) handlePanelAgain(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	np, err := s.startPanel(r.Context(), userFrom(r.Context()), s.panelAgainStart(r.Context(), p))
	if err != nil {
		s.fail(w, err, "start panel")
		return
	}
	writeJSON(w, http.StatusCreated, s.viewPanel(r.Context(), np, true))
}

// handlePanelContinue starts an ordinary conversation that carries the
// panel's outcome, to go on from it with one agent.
func (s *Server) handlePanelContinue(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	p := s.panel(w, r)
	if p == nil {
		return
	}
	var in struct {
		Text     string `json:"text"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	seats, _ := s.store.PanelSeats(r.Context(), p.ID)
	answers, _ := s.store.PanelAnswers(r.Context(), p.ID)
	out := panelOutcome(p, seats, answers, 60000)
	if out == "" {
		writeErr(w, http.StatusBadRequest, "There is nothing to continue from yet")
		return
	}
	attr := func(v string) string {
		return strings.NewReplacer(`"`, "'", "<", "‹", ">", "›", "\n", " ").Replace(v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<pocket-panel-report kind=\"%s\" title=\"%s\" panel-id=\"%s\" state=\"%s\">\n", attr(p.Spec.KindName), attr(p.Title), p.ID, p.Status)
	fmt.Fprintf(&b, "A panel of AI models (%s) was asked:\n\n%s\n\n", p.Spec.KindName, firstNonEmpty(p.Question, "(see the material)"))
	if m := strings.TrimSpace(p.Material); m != "" {
		fmt.Fprintf(&b, "The material it was given:\n\n%s\n\n", tailHead(m, 30000))
	}
	fmt.Fprintf(&b, "The outcome:\n\n%s\n</pocket-panel-report>\n\n", out)
	b.WriteString(firstNonEmpty(strings.TrimSpace(in.Text), "Let's continue from this outcome. Tell me briefly how you read it and what you would do next."))
	nt := newThread{Provider: in.Provider, Model: in.Model, Text: b.String(), Title: oneLine("After "+strings.ToLower(p.Spec.KindName)+": "+p.Title, 140)}
	if p.Spec.HostID == 0 {
		nt.Cwd = p.Spec.Cwd
	}
	t, err := s.createThread(r.Context(), userFrom(r.Context()), nt, "web", "")
	if err != nil && t == nil {
		s.fail(w, err, "continue from panel")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handlePanelExport(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	seats, _ := s.store.PanelSeats(r.Context(), p.ID)
	answers, _ := s.store.PanelAnswers(r.Context(), p.ID)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if queryBool(r, "download") {
		w.Header().Set("Content-Disposition", `attachment; filename="panel-`+p.ID[:8]+`.md"`)
	}
	_, _ = w.Write([]byte(panelMarkdown(p, seats, answers)))
}

// panelMarkdown is a whole panel as one document.
func panelMarkdown(p *store.Panel, seats []*store.PanelSeat, answers []*store.PanelAnswer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", p.Spec.KindName, p.Title)
	if when, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err == nil {
		fmt.Fprintf(&b, "%s · ", when.Local().Format("2 January 2006, 15:04"))
	}
	who := []string{}
	by := map[int64]*store.PanelSeat{}
	for _, st := range seats {
		by[st.ID] = st
		if !st.Chair {
			who = append(who, seatWho(st))
		}
	}
	fmt.Fprintf(&b, "%s\n\n## The question\n\n%s\n\n", strings.Join(who, "; "), firstNonEmpty(p.Question, "(see the material)"))
	if m := strings.TrimSpace(p.Material); m != "" {
		fmt.Fprintf(&b, "## The material\n\n%s\n\n", m)
	}
	if res := strings.TrimSpace(p.Result); res != "" {
		fmt.Fprintf(&b, "## Outcome\n\n%s\n\n", res)
	}
	if p.Error != "" {
		fmt.Fprintf(&b, "> %s\n\n", p.Error)
	}
	for i, sg := range p.Spec.Stages {
		wrote := false
		for _, a := range answers {
			st := by[a.SeatID]
			if a.Stage != i || st == nil || st.Chair {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, "## Step %d: %s\n\n", i+1, sg.Name)
				wrote = true
			}
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", seatWho(st), strings.TrimSpace(a.Text))
		}
	}
	return b.String()
}

// ---------- what the start sheet offers ----------

// panelLogin is a login a participant can run on.
type panelLogin struct {
	Provider  string  `json:"provider"`
	AccountID int64   `json:"account_id"`
	HostID    int64   `json:"host_id"`
	HostHome  string  `json:"host_home"`
	Label     string  `json:"label"`
	Where     string  `json:"where"` // "This server" or the machine's name
	SignedIn  bool    `json:"signed_in"`
	Email     string  `json:"email,omitempty"`
	Plan      string  `json:"plan,omitempty"`
	Used      float64 `json:"used"` // of its plan limit, 0..1; -1 = not known
	ResetsAt  string  `json:"resets_at,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// panelLogins lists every login the person can seat a participant on: this
// server's and their machines' (a machine that does not answer within a few
// seconds is left out this time).
func (s *Server) panelLogins(ctx context.Context, u *store.User) []panelLogin {
	out := []panelLogin{}
	quota := func(l *panelLogin) {
		l.Used = -1
		if used, resets, ok := s.rt.loginUsed(seatLimitsKey(store.PanelSeatSpec{Provider: l.Provider, AccountID: l.AccountID, HostID: l.HostID, HostHome: l.HostHome})); ok {
			l.Used, l.ResetsAt = used, resets
		}
	}
	for _, a := range s.accountsFor(ctx, u, false, false) {
		if _, ok := s.agentProvider(ctx, a.Provider); !ok {
			continue
		}
		l := panelLogin{Provider: a.Provider, AccountID: a.ID, Label: a.Label, Where: "This server", SignedIn: a.SignedIn, Email: a.Email, Plan: a.Plan, Error: a.Error}
		quota(&l)
		out = append(out, l)
	}
	// agents without logins of their own (keys in Settings): one entry each
	seen := map[string]bool{}
	for _, l := range out {
		seen[l.Provider] = true
	}
	if s.mayUseServerLogin(ctx, u) {
		for _, p := range s.providers(ctx, false) {
			if p.Available && !seen[p.ID] {
				l := panelLogin{Provider: p.ID, Label: "Server", Where: "This server", SignedIn: true}
				quota(&l)
				out = append(out, l)
			}
		}
	}
	hosts, _ := s.hostsFor(ctx, u)
	var mu sync.Mutex
	var wg sync.WaitGroup
	done := make(chan struct{})
	for _, h := range hosts {
		wg.Add(1)
		go func(h *store.Host) {
			defer wg.Done()
			views := s.hostLogins(ctx, h, false)
			mu.Lock()
			defer mu.Unlock()
			select {
			case <-done:
				return // too late for this answer; it is cached for the next one
			default:
			}
			for _, v := range views {
				if v.Missing {
					continue
				}
				l := panelLogin{Provider: v.Provider, HostID: h.ID, HostHome: v.Dir, Label: firstNonEmpty(v.Label, "Default login"), Where: h.Name,
					SignedIn: v.SignedIn, Email: v.Email, Plan: v.Plan, Error: v.Error}
				quota(&l)
				out = append(out, l)
			}
		}(h)
	}
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	select {
	case <-all:
	case <-time.After(4 * time.Second):
	}
	mu.Lock()
	close(done)
	list := append([]panelLogin(nil), out...)
	mu.Unlock()
	return list
}

func (s *Server) handlePanelOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	lineups, _ := s.store.ListPanelLineups(ctx, u.ID)
	recipes, _ := s.store.ListPanelRecipes(ctx, u.ID)
	seats, chair := s.defaultSeats(ctx, u)
	if seats == nil {
		seats = []store.PanelSeatSpec{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds": panelKinds,
		"depths": []map[string]string{
			{"id": "quick", "name": "Quick", "description": "One round, then the summary"},
			{"id": "standard", "name": "Standard", "description": "They also see and answer each other"},
			{"id": "deep", "name": "Deep", "description": "One more round to revise; slower and costs more"},
		},
		"roles":     panelRoles,
		"lineups":   lineups,
		"own_kinds": recipes,
		"logins":    s.panelLogins(ctx, u),
		"suggested": map[string]any{"seats": seats, "chair": chair},
		"automatic": s.autoSeats(ctx, u), // every agent that works here, before the person chose

		"max_seats": panelMaxSeats,
	})
}

// ---------- saved line-ups and own kinds ----------

func (s *Server) handleSaveLineup(w http.ResponseWriter, r *http.Request) {
	var in store.PanelLineup
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	in.UserID = me(r)
	in.ID, _ = strconv.ParseInt(chi.URLParam(r, "lid"), 10, 64)
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 60 {
		writeErr(w, http.StatusBadRequest, "Give the line-up a short name")
		return
	}
	if len(in.Seats) == 0 || len(in.Seats) > panelMaxSeats {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("A line-up has 1 to %d participants", panelMaxSeats))
		return
	}
	if err := s.store.SavePanelLineup(r.Context(), &in); err != nil {
		s.fail(w, err, "save line-up")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteLineup(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "lid"), 10, 64)
	if err := s.store.DeletePanelLineup(r.Context(), me(r), id); err != nil {
		s.fail(w, err, "delete line-up")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSavePanelKind(w http.ResponseWriter, r *http.Request) {
	var in store.PanelRecipe
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	in.UserID = me(r)
	in.ID, _ = strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || len(in.Name) > 60 {
		writeErr(w, http.StatusBadRequest, "Give it a short name")
		return
	}
	if len(in.Description) > 300 {
		in.Description = in.Description[:300]
	}
	stages, err := checkStages(in.Stages)
	if err != nil {
		s.fail(w, err, "save panel kind")
		return
	}
	in.Stages = stages
	if err := s.store.SavePanelRecipe(r.Context(), &in); err != nil {
		s.fail(w, err, "save panel kind")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeletePanelKind(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
	if err := s.store.DeletePanelRecipe(r.Context(), me(r), id); err != nil {
		s.fail(w, err, "delete panel kind")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
