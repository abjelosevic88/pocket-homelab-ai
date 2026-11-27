package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A Refine panel is the home of the plan (or text) it works on: it keeps
// every version (the original, each rewrite, what the person changed in the
// panel's chat with the editor or by hand), the newest being the current
// one. The person goes on from there: another round of verdicts and a
// rewrite, a word with the editor, an edit of their own. Other conversations
// refer to the panel and get its current version (panelRefNote).

const (
	panelVersionEnd   = "--- end of the version ---"
	panelVersionStart = "--- new version ---"
	panelVersionMax   = 400000
)

var (
	versionEndRe   = regexp.MustCompile(`(?im)^[ \t>*_=–—-]*end of the version[ \t*_=–—-]*$`)
	versionStartRe = regexp.MustCompile(`(?im)^[ \t>*_=–—-]*new version[ \t*_=–—-]*$`)
	changedHeadRe  = regexp.MustCompile(`(?im)^[ \t]*(?:#{1,6}[ \t]*|\*\*)what changed\b`)
)

// splitRewrite takes an editor's answer apart: the new version, and what it
// says about it (what changed, what was not taken).
func splitRewrite(text string) (version, note string) {
	text = strings.TrimSpace(text)
	if loc := versionEndRe.FindStringIndex(text); loc != nil {
		version, note = text[:loc[0]], text[loc[1]:]
	} else if loc := changedHeadRe.FindStringIndex(text); loc != nil && loc[0] > 0 {
		version, note = text[:loc[0]], text[loc[0]:]
	} else {
		version = text
	}
	if loc := versionStartRe.FindStringIndex(version); loc != nil {
		version = version[loc[1]:]
	}
	version = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(version), "-–—=_* \t\n"))
	return version, strings.TrimSpace(note)
}

// splitProposal finds a new version in an editor's chat reply: what it said,
// and the version it proposes ("" = it only answered).
func splitProposal(text string) (said, proposal string) {
	start := versionStartRe.FindStringIndex(text)
	if start == nil {
		return strings.TrimSpace(text), ""
	}
	rest := text[start[1]:]
	after := ""
	if end := versionEndRe.FindStringIndex(rest); end != nil {
		rest, after = rest[:end[0]], rest[end[1]:]
	}
	return strings.TrimSpace(strings.TrimSpace(text[:start[0]]) + "\n\n" + strings.TrimSpace(after)), strings.TrimSpace(rest)
}

// hasVersions: the kind of panel that works on a text and keeps its versions.
func hasVersions(p *store.Panel) bool { return p.Kind == "refine" }

// panelVersions lists a panel's versions, making them from what it has for
// one that ran before versions were kept: the material, then each rewrite.
func (s *Server) panelVersions(ctx context.Context, p *store.Panel) []*store.PanelVersion {
	if !hasVersions(p) {
		return nil
	}
	list, err := s.store.PanelVersions(ctx, p.ID)
	if err != nil || len(list) > 0 {
		return list
	}
	if m := strings.TrimSpace(p.Material); m != "" {
		_ = s.store.AddPanelVersion(ctx, &store.PanelVersion{PanelID: p.ID, Text: m, Source: "original", Stage: -1})
	}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	answers, _ := s.store.PanelAnswers(ctx, p.ID)
	chair := map[int64]bool{}
	for _, st := range seats {
		chair[st.ID] = st.Chair
	}
	for _, a := range answers { // by step
		if chair[a.SeatID] && a.Stage < len(p.Spec.Stages) && p.Spec.Stages[a.Stage].Who == "chair" {
			text, note := splitRewrite(revealNames(a.Text, seats))
			_ = s.store.AddPanelVersion(ctx, &store.PanelVersion{PanelID: p.ID, Text: text, Source: "rewrite", Note: note, Stage: a.Stage})
		}
	}
	list, _ = s.store.PanelVersions(ctx, p.ID)
	return list
}

// currentVersion is the newest version of what a panel works on (nil = none).
func (s *Server) currentVersion(ctx context.Context, p *store.Panel) *store.PanelVersion {
	list := s.panelVersions(ctx, p)
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// addVersion appends a version and makes it what the panel shows as its outcome.
func (s *Server) addVersion(ctx context.Context, p *store.Panel, v *store.PanelVersion) error {
	s.panelVersions(ctx, p) // the earlier ones first, for a panel from before versions
	v.PanelID = p.ID
	if len(v.Text) > panelVersionMax {
		return badRequest("That is too long for one version")
	}
	if err := s.store.AddPanelVersion(ctx, v); err != nil {
		return err
	}
	p.Result = v.Text
	return nil
}

// ---------- the web API ----------

func (s *Server) panelVersionRoutes(r chi.Router) {
	r.Post("/panels/{id}/versions", s.handleAddVersion)
	r.Post("/panels/{id}/versions/{n}/restore", s.handleRestoreVersion)
	r.Post("/panels/{id}/rounds", s.handlePanelRound)
	r.Post("/panels/{id}/chat", s.handlePanelChat)
	r.Post("/panels/{id}/chat/{mid}/accept", s.handleAcceptProposal)
	r.Post("/panels/{id}/save", s.handleSavePanelFile)
}

// versioned loads a panel that keeps versions and is not at work.
func (s *Server) versioned(w http.ResponseWriter, r *http.Request) *store.Panel {
	p := s.panel(w, r)
	if p == nil {
		return nil
	}
	if !hasVersions(p) {
		writeErr(w, http.StatusBadRequest, "Only a Refine panel keeps versions")
		return nil
	}
	if p.Status == "running" {
		writeErr(w, http.StatusConflict, "The panel is still working; wait for it or stop it first")
		return nil
	}
	return p
}

func (s *Server) saved(w http.ResponseWriter, r *http.Request, p *store.Panel) {
	if err := s.store.SavePanel(r.Context(), p); err != nil {
		s.fail(w, err, "save panel")
		return
	}
	s.panelChanged(p)
	writeJSON(w, http.StatusOK, s.viewPanel(r.Context(), p, true))
}

// handleAddVersion: the person changed the text by hand.
func (s *Server) handleAddVersion(w http.ResponseWriter, r *http.Request) {
	p := s.versioned(w, r)
	if p == nil {
		return
	}
	var in struct {
		Text string `json:"text"`
		Note string `json:"note"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, http.StatusBadRequest, "Write the new version")
		return
	}
	if cur := s.currentVersion(r.Context(), p); cur != nil && strings.TrimSpace(cur.Text) == strings.TrimSpace(in.Text) {
		writeErr(w, http.StatusBadRequest, "Nothing was changed")
		return
	}
	if err := s.addVersion(r.Context(), p, &store.PanelVersion{Text: strings.TrimSpace(in.Text), Source: "edit", Note: oneLine(in.Note, 300), Stage: -1}); err != nil {
		s.fail(w, err, "add version")
		return
	}
	s.saved(w, r, p)
}

// handleRestoreVersion makes an earlier version the current one again (as a new version).
func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	p := s.versioned(w, r)
	if p == nil {
		return
	}
	n, _ := strconv.Atoi(chi.URLParam(r, "n"))
	list := s.panelVersions(r.Context(), p)
	for _, v := range list {
		if v.N == n && n != list[len(list)-1].N {
			if err := s.addVersion(r.Context(), p, &store.PanelVersion{Text: v.Text, Source: "restore", Note: fmt.Sprintf("Back to version %d", n), Stage: -1}); err != nil {
				s.fail(w, err, "restore version")
				return
			}
			s.saved(w, r, p)
			return
		}
	}
	writeErr(w, http.StatusBadRequest, "That version is the current one, or is not there")
}

// handlePanelRound runs one more round on the current version: the
// participants judge it, the editor rewrites it from their verdicts.
func (s *Server) handlePanelRound(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	p := s.versioned(w, r)
	if p == nil {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	ctx := r.Context()
	cur := s.currentVersion(ctx, p)
	if cur == nil {
		writeErr(w, http.StatusBadRequest, "There is no version to judge yet")
		return
	}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	judges, editor := 0, false
	for _, st := range seats {
		switch {
		case st.Chair && st.State != "failed":
			editor = true
		case !st.Chair && st.State != "failed" && st.State != "skipped" && st.ThreadID != "":
			judges++
		}
	}
	if judges == 0 {
		writeErr(w, http.StatusBadRequest, "None of this panel's participants is left to judge; run it again as a new panel")
		return
	}
	if !editor { // the one that wrote before is gone: a participant takes over
		sub := panelSubstitute(seats, &store.PanelSeat{})
		if sub == nil {
			writeErr(w, http.StatusBadRequest, "Nobody is left to rewrite it; run it again as a new panel")
			return
		}
		nc := &store.PanelSeat{PanelID: p.ID, Idx: len(seats), Chair: true, PanelSeatSpec: *sub, Asked: -1, State: "waiting"}
		nc.Role = ""
		if err := s.store.AddPanelSeat(ctx, nc); err != nil {
			s.fail(w, err, "add editor")
			return
		}
	}
	wish := ""
	if note := strings.TrimSpace(in.Note); note != "" {
		wish = "\n\nThe person asks for this round: " + tailHead(note, 4000)
	}
	p.Spec.Stages = append(p.Spec.Stages,
		store.PanelStage{Name: fmt.Sprintf("Verdicts on version %d", cur.N), Who: "all", Sees: "revision", Prompt: panelRefineAgain + wish},
		store.PanelStage{Name: fmt.Sprintf("Version %d", cur.N+1), Who: "chair", Sees: "all", Prompt: fmt.Sprintf("These are the verdicts on version %d. ", cur.N) + panelRefineRewrite + wish})
	p.Stage = len(p.Spec.Stages) - 2
	p.Status, p.Error, p.FinishedAt, p.StageAt = "running", "", "", store.Now()
	p.Spec.ChairSwap = false
	if err := s.store.SavePanel(ctx, p); err != nil {
		s.fail(w, err, "start round")
		return
	}
	s.log.Info("panel: another round", "panel", p.ID, "on_version", cur.N)
	s.panelChanged(p)
	s.goPanel(p.ID)
	writeJSON(w, http.StatusOK, s.viewPanel(ctx, p, true))
}

// ---------- a word with the editor ----------

// panelEditor is the seat that rewrites (nil = there is none to talk to).
func panelEditor(seats []*store.PanelSeat) *store.PanelSeat {
	var ed *store.PanelSeat
	for _, st := range seats {
		if st.Chair && st.State != "failed" && st.ThreadID != "" {
			ed = st
		}
	}
	return ed
}

// handlePanelChat sends the editor what the person says about the current
// version; its answer comes into the panel's chat, with the new version it
// proposes, if any, to take or leave.
func (s *Server) handlePanelChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	p := s.versioned(w, r)
	if p == nil {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, http.StatusBadRequest, "Write what you want to say")
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	ed := panelEditor(seats)
	if ed == nil {
		writeErr(w, http.StatusBadRequest, "This panel's editor is gone. Run another round: a participant takes over as editor.")
		return
	}
	chat, _ := s.store.PanelChat(ctx, p.ID)
	for _, m := range chat {
		if m.Role == "editor" && m.State == "working" {
			writeErr(w, http.StatusConflict, "The editor is still answering")
			return
		}
	}
	cur := s.currentVersion(ctx, p)
	if cur == nil {
		writeErr(w, http.StatusBadRequest, "There is no version to talk about yet")
		return
	}
	th, err := s.store.GetThreadAny(ctx, ed.ThreadID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "The editor's conversation was deleted. Run another round: a participant takes over as editor.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The person who started the panel writes to you about the text, in the panel's chat. The current version is version %d.\n", cur.N)
	if p.Spec.EditorKnows != cur.N {
		fmt.Fprintf(&b, "It is not the last one you wrote or proposed (they changed it, went back to an earlier one, or did not take your proposal). The current version:\n\n%s\n\n%s\n", cur.Text, panelVersionEnd)
	}
	fmt.Fprintf(&b, "\nThe person says:\n\n%s\n\n", strings.TrimSpace(in.Text))
	b.WriteString("If they ask for a change: say in a few lines what you change, then write the complete new version between a line that says exactly `" + panelVersionStart + "` and a line that says exactly `" + panelVersionEnd + "`. Change only what they ask for and what follows from it; keep everything else word for word. If it is a question or they want your opinion, answer it and write no version. They decide whether to take a version you propose.")
	if err := s.store.AddPanelChat(ctx, &store.PanelChatMessage{PanelID: p.ID, Role: "person", Text: strings.TrimSpace(in.Text)}); err != nil {
		s.fail(w, err, "panel chat")
		return
	}
	reply := &store.PanelChatMessage{PanelID: p.ID, Role: "editor", State: "working"}
	if err := s.store.AddPanelChat(ctx, reply); err != nil {
		s.fail(w, err, "panel chat")
		return
	}
	if _, err := s.postMessage(context.WithoutCancel(ctx), u, th, b.String(), nil); err != nil {
		reply.State, reply.Text = "failed", "It could not be asked: "+err.Error()
		_ = s.store.SavePanelChat(ctx, reply)
		s.fail(w, err, "panel chat")
		return
	}
	p.Spec.EditorKnows = cur.N
	_ = s.store.SavePanel(ctx, p)
	go s.awaitEditor(p.ID, reply.ID)
	s.panelChanged(p)
	writeJSON(w, http.StatusOK, s.viewPanel(ctx, p, true))
}

// awaitEditor waits for the editor's answer to a chat message and files it.
func (s *Server) awaitEditor(panelID string, msgID int64) {
	ctx := s.rt.ctx
	p, err := s.store.GetPanelAny(ctx, panelID)
	if err != nil {
		return
	}
	find := func() *store.PanelChatMessage {
		chat, _ := s.store.PanelChat(ctx, panelID)
		for _, m := range chat {
			if m.ID == msgID {
				return m
			}
		}
		return nil
	}
	m := find()
	seats, _ := s.store.PanelSeats(ctx, panelID)
	ed := panelEditor(seats)
	if m == nil || m.State != "working" {
		return
	}
	done := func(state, text, proposal string) {
		m.State, m.Text, m.Proposal = state, text, proposal
		_ = s.store.SavePanelChat(ctx, m)
		if fresh, err := s.store.GetPanelAny(ctx, panelID); err == nil {
			if proposal != "" { // it now takes its proposal for the text; it is told otherwise if that is not taken
				fresh.Spec.EditorKnows = 0
				_ = s.store.SavePanel(ctx, fresh)
			}
			s.panelChanged(fresh)
		}
	}
	if ed == nil {
		done("failed", "The editor is gone.", "")
		return
	}
	limit := time.Duration(max(p.Spec.LimitMin, 10)) * 2 * time.Minute
	began := time.Now()
	tick := time.NewTicker(panelPollEvery)
	defer tick.Stop()
	for {
		th, err := s.store.GetThreadAny(ctx, ed.ThreadID)
		if err != nil {
			done("failed", "The editor's conversation was deleted.", "")
			return
		}
		info := s.agentState(ctx, th, false)
		if info.Finished {
			text, _, _ := s.store.LastReply(ctx, th.ID)
			text = strings.TrimSpace(text)
			switch {
			case info.State == "done" && text != "":
				said, proposal := splitProposal(revealNames(text, seats))
				done("done", said, proposal)
			case info.State == "failed":
				done("failed", firstNonEmpty(oneLine(info.Error, 300), "It stopped with an error"), "")
			default:
				done("failed", "It finished without an answer", "")
			}
			return
		}
		if time.Since(began) > limit && info.State != "waiting_for_person" {
			if !s.stopReattached(th.ID) {
				_ = s.rt.interrupt(th.ID)
			}
			done("failed", "It did not answer in time", "")
			return
		}
		select {
		case <-ctx.Done():
			return // taken up again when the server is back (resumePanelChats)
		case <-tick.C:
		}
	}
}

// resumePanelChats waits again for the editors that were answering when the server stopped.
func (s *Server) resumePanelChats(ctx context.Context) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT panel_id, id FROM panel_chat WHERE state = 'working'`)
	if err != nil {
		return
	}
	type row struct {
		panel string
		id    int64
	}
	var list []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.panel, &x.id) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		go s.awaitEditor(x.panel, x.id)
	}
}

// handleAcceptProposal takes a version the editor proposed in the chat.
func (s *Server) handleAcceptProposal(w http.ResponseWriter, r *http.Request) {
	p := s.versioned(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "mid"), 10, 64)
	chat, _ := s.store.PanelChat(ctx, p.ID)
	asked := ""
	for i, m := range chat {
		if m.Role == "person" {
			asked = m.Text
		}
		if m.ID != id {
			continue
		}
		if m.Proposal == "" || m.VersionN != 0 {
			break
		}
		v := &store.PanelVersion{Text: m.Proposal, Source: "chat", Note: oneLine(asked, 300), Stage: -1}
		if err := s.addVersion(ctx, p, v); err != nil {
			s.fail(w, err, "take version")
			return
		}
		m.VersionN = v.N
		_ = s.store.SavePanelChat(ctx, m)
		latest := true
		for _, later := range chat[i+1:] {
			if later.Role == "editor" && later.Proposal != "" {
				latest = false
			}
		}
		if latest {
			p.Spec.EditorKnows = v.N // it is what the editor wrote last
		}
		s.saved(w, r, p)
		return
	}
	writeErr(w, http.StatusBadRequest, "That proposal is not there any more")
}

// ---------- outside the panel ----------

// handleSavePanelFile writes the current version (or a panel's outcome) as a
// Markdown file into a folder on this server.
func (s *Server) handleSavePanelFile(w http.ResponseWriter, r *http.Request) {
	p := s.panel(w, r)
	if p == nil {
		return
	}
	var in struct {
		Dir     string `json:"dir"`
		Name    string `json:"name"`
		Replace bool   `json:"replace"`
	}
	_ = decode(r, &in)
	ctx := r.Context()
	text := s.panelText(ctx, p)
	if strings.TrimSpace(text) == "" {
		writeErr(w, http.StatusBadRequest, "There is nothing to save yet")
		return
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" && p.Spec.HostID == 0 {
		dir = p.Spec.Cwd
	}
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "Pick the folder to save it in")
		return
	}
	clean, err := s.checkDirFor(ctx, userFrom(ctx), dir)
	if err != nil {
		s.fail(w, err, "save file")
		return
	}
	name := fileSlug(firstNonEmpty(in.Name, p.Title))
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	path := filepath.Join(clean, name)
	if old, err := os.ReadFile(path); err == nil && !in.Replace && strings.TrimSpace(string(old)) != strings.TrimSpace(text) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": name + " is already there", "exists": true, "path": path})
		return
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.fail(w, badRequest("That file cannot be read: "+err.Error()), "save file")
		return
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(text)+"\n"), 0o644); err != nil {
		s.fail(w, badRequest("Could not write the file: "+err.Error()), "save file")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

var fileSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// fileSlug makes a file name of a title.
func fileSlug(title string) string {
	base := strings.TrimSuffix(strings.TrimSpace(title), ".md")
	if i := strings.LastIndex(base, ": "); i >= 0 && i < 30 { // "Refine: plan.md" → "plan"
		base = strings.TrimSuffix(base[i+2:], ".md")
	}
	slug := strings.Trim(fileSlugRe.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return firstNonEmpty(slug, "plan")
}

// panelText is what a panel stands for when it is referred to: the current
// version of what it works on, or its outcome.
func (s *Server) panelText(ctx context.Context, p *store.Panel) string {
	if v := s.currentVersion(ctx, p); v != nil {
		return v.Text
	}
	seats, _ := s.store.PanelSeats(ctx, p.ID)
	answers, _ := s.store.PanelAnswers(ctx, p.ID)
	return panelOutcome(p, seats, answers, 100000)
}

// panelItems finds the person's panels by name for the "@panels #…" picker:
// a link to one hands a conversation its current version or outcome.
func (s *Server) panelItems(ctx context.Context, uid int64, q string, limit int) []pocketItem {
	list, err := s.store.PanelsByTitle(ctx, uid, strings.TrimSpace(q), limit)
	if err != nil {
		return []pocketItem{}
	}
	out := []pocketItem{}
	for _, p := range list {
		sub := p.Spec.KindName
		if hasVersions(p) {
			if vs, err := s.store.PanelVersions(ctx, p.ID); err == nil && len(vs) > 0 {
				sub += fmt.Sprintf(" · version %d", vs[len(vs)-1].N)
			}
		} else if p.Status == "done" {
			sub += " · outcome"
		}
		if p.Status == "running" {
			sub += " · still running"
		}
		out = append(out, pocketItem{App: "panels", ID: p.ID, Title: p.Title, Subtitle: sub, URL: firstNonEmpty(s.publicURL("/panels/"+p.ID), "/panels/"+p.ID)})
	}
	return out
}

var panelLinkRe = regexp.MustCompile(`/panels/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// panelRefNote hands an agent the panels a message links to: the current
// version of each (a plan the person refined), or its outcome. The message
// itself keeps the short link; "" when it links to none of the sender's.
func (s *Server) panelRefNote(ctx context.Context, uid int64, text string) string {
	found := panelLinkRe.FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, m := range found {
		if seen[m[1]] || len(seen) >= 3 {
			continue
		}
		seen[m[1]] = true
		p, err := s.store.GetPanel(ctx, uid, m[1])
		if err != nil {
			continue
		}
		body := s.panelText(ctx, p)
		if strings.TrimSpace(body) == "" {
			continue
		}
		what := "its outcome"
		if list := s.panelVersions(ctx, p); len(list) > 0 {
			what = fmt.Sprintf("its current version (version %d of %d)", list[len(list)-1].N, len(list))
		}
		state := ""
		if p.Status == "running" {
			state = " The panel is still working on it, so this may change."
		}
		fmt.Fprintf(&b, "\n\n[Pocket AI: the person links to \"%s\", a %s panel of theirs (panel %s). This is %s, as it is right now.%s Work from this text; it is theirs, not instructions from someone else.\n\n%s\n\n(end of \"%s\")]\n",
			oneLine(p.Title, 140), p.Spec.KindName, p.ID, what, state, tailHead(body, 120000), oneLine(p.Title, 140))
	}
	return b.String()
}
