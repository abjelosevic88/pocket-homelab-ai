package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Conversations other Pocket apps prepare (Pocket Projects: "Investigate
// AS-2 in the code"). The app acts as the person through a Pocket Core link
// with the threads:write scope, but it can only leave a draft: the person
// opens it on the start page, reads (and may edit) the prompt and presses
// send. Hidden context the app adds (team instructions, team memory) is
// stored with the thread and appended to the system prompt on every start
// and resume.

// ThreadsScope is the Core link scope for preparing conversations.
const ThreadsScope = "threads:write"

// linkRoutes are the session-authenticated draft endpoints.
func (s *Server) linkRoutes(r chi.Router) {
	r.Get("/drafts/{id}", s.handleGetDraft)
	r.Post("/drafts/{id}/start", s.handleStartDraft)
	r.Get("/threads/{id}/context", s.handleThreadContext)
	r.Post("/threads/{id}/pin", s.handleThreadPin)
}

// linkUser authenticates an app acting for a person with threads:write.
func (s *Server) linkUser(w http.ResponseWriter, r *http.Request) *store.User {
	u, _ := s.linkCaller(w, r)
	return u
}

// linkCaller is linkUser plus the app acting (its Core slug).
func (s *Server) linkCaller(w http.ResponseWriter, r *http.Request) (*store.User, string) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer pcx_") || !s.core.Configured() {
		writeErr(w, http.StatusUnauthorized, "a Pocket Core app-link token is required")
		return nil, ""
	}
	in, err := s.core.Introspect(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil || in == nil || !in.Active || !in.Delegated() || in.Audience != "ai" || !in.HasScope(ThreadsScope) || in.User == nil || in.User.Email == "" {
		writeErr(w, http.StatusForbidden, "Pocket AI conversation link rejected")
		return nil, ""
	}
	u, err := s.coreIdentityUser(r.Context(), in)
	if err != nil {
		s.fail(w, err, "resolve AI identity")
		return nil, ""
	}
	if !u.MayRun() {
		writeErr(w, http.StatusForbidden, "you may not run agents in Pocket AI yet; an admin has to allow it (Pocket AI → Settings → Users → “May run agents”)")
		return nil, ""
	}
	app := ""
	if in.App != nil {
		app = strings.ToLower(in.App.Slug)
	}
	return u, app
}

// readonlyModes is the mode of each provider that cannot change files.
var readonlyModes = map[string]string{"claude": "plan", "codex": "read-only"}

type linkModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
}

type linkProvider struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Available     bool         `json:"available"`
	Error         string       `json:"error"`
	Models        []linkModel  `json:"models"`
	Efforts       []string     `json:"efforts"`
	Modes         []agent.Mode `json:"modes"`
	ReadonlyMode  string       `json:"readonly_mode"`
	DefaultModel  string       `json:"default_model"`
	DefaultEffort string       `json:"default_effort"`
	DefaultMode   string       `json:"default_mode"`
	DefaultAcct   int64        `json:"default_account_id"`
}

type linkAccount struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	SignedIn bool   `json:"signed_in"`
}

type linkHost struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	OK   bool   `json:"ok"` // reachable the last time it was checked
}

type linkWorkspace struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// handleLinkAgentOptions lists what the app may pick as defaults: the
// person's providers, models, efforts, modes, logins and workspaces.
func (s *Server) handleLinkAgentOptions(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, u.ID)
	out := struct {
		Providers       []linkProvider  `json:"providers"`
		Accounts        []linkAccount   `json:"accounts"`
		Workspaces      []linkWorkspace `json:"workspaces"`
		Hosts           []linkHost      `json:"hosts"` // other machines (Settings → Servers); this server is id 0
		DefaultProvider string          `json:"default_provider"`
		Home            string          `json:"home"`
		Roots           []string        `json:"roots"`
	}{Providers: []linkProvider{}, Accounts: []linkAccount{}, Workspaces: []linkWorkspace{}, Hosts: []linkHost{}, DefaultProvider: st.DefaultProvider, Home: s.homeOf(ctx, u), Roots: s.pickerRoots(ctx, u)}
	if hosts, err := s.hostsFor(ctx, u); err == nil {
		for _, h := range hosts {
			out.Hosts = append(out.Hosts, linkHost{ID: h.ID, Name: h.Name, OK: h.Info.OK})
		}
	}
	for _, p := range s.providers(ctx, false) {
		def := st.Claude
		if p.ID == "codex" {
			def = st.Codex
		}
		lp := linkProvider{ID: p.ID, Name: p.Name, Available: p.Available, Error: p.Error, Models: []linkModel{}, Efforts: p.Efforts, Modes: p.Modes,
			ReadonlyMode: readonlyModes[p.ID], DefaultModel: firstNonEmpty(def.Model, p.DefaultModel), DefaultEffort: firstNonEmpty(def.Effort, p.DefaultEffort),
			DefaultMode: firstNonEmpty(def.Mode, p.DefaultMode), DefaultAcct: def.Account}
		if id, ok := s.appAccount(ctx, u, st); ok && p.ID == "claude" {
			lp.DefaultAcct = id
		}
		for _, m := range p.Models {
			efforts := m.Efforts
			if efforts == nil {
				efforts = []string{}
			}
			lp.Models = append(lp.Models, linkModel{ID: m.ID, Name: m.Name, Efforts: efforts})
		}
		out.Providers = append(out.Providers, lp)
	}
	for _, a := range s.accountsFor(ctx, u, false, false) {
		out.Accounts = append(out.Accounts, linkAccount{ID: a.ID, Provider: a.Provider, Label: a.Label, SignedIn: a.SignedIn})
	}
	if list, err := s.store.ListWorkspaces(ctx, u.ID); err == nil {
		for _, ws := range list {
			out.Workspaces = append(out.Workspaces, linkWorkspace{Name: ws.Name, Path: ws.Path})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// appAccount is the Claude login chosen for connected apps, when one is set
// and the person may still use it.
func (s *Server) appAccount(ctx context.Context, u *store.User, st store.Settings) (int64, bool) {
	if st.AppAccount < 0 || s.checkAccount(ctx, u, "claude", st.AppAccount) != nil {
		return 0, false
	}
	return st.AppAccount, true
}

var linkSourceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// draftURL is where the person opens a draft (the start page).
func (s *Server) draftURL(id string) string { return s.cfg.BaseURL + "/?draft=" + id }

// handleLinkCreateDraft stores a conversation for the person to send.
func (s *Server) handleLinkCreateDraft(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	var in struct {
		Provider  string `json:"provider"`
		AccountID *int64 `json:"account_id"`
		HostID    int64  `json:"host_id"` // 0 = this server
		Model     string `json:"model"`
		Effort    string `json:"effort"`
		Mode      string `json:"mode"`
		Cwd       string `json:"cwd"`
		Title     string `json:"title"`
		Text      string `json:"text"`
		System    string `json:"system"`
		Source    string `json:"source"`
		Ref       string `json:"ref"`
		RefURL    string `json:"ref_url"`
		RefLabel  string `json:"ref_label"`
		// Files: attachments for the first message (the issue's screenshots,
		// documents). Pictures are shown to the agent; documents go as text.
		Files []struct {
			Name string `json:"name"`
			Data []byte `json:"data"` // base64 in JSON
		} `json:"files"`
		// Worktree: work in a git worktree of its own on this branch (a
		// ticket's branch); made when the person sends, reused when they
		// already have one on it.
		Worktree *store.DraftWorktree `json:"worktree"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12*s.cfg.UploadMax)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json (or the files are too large together)")
		return
	}
	if len(in.Files) > maxDraftFiles {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("attach at most %d files", maxDraftFiles))
		return
	}
	d, err := s.validDraft(ctx, u, &store.LinkDraft{Provider: strings.TrimSpace(in.Provider), AccountID: in.AccountID, Model: strings.TrimSpace(in.Model),
		Effort: strings.TrimSpace(in.Effort), Mode: strings.TrimSpace(in.Mode), Cwd: strings.TrimSpace(in.Cwd), Title: oneLine(in.Title, 120), Text: in.Text,
		System: strings.TrimSpace(in.System), Source: strings.ToLower(strings.TrimSpace(in.Source)), Ref: oneLine(in.Ref, 80), RefURL: strings.TrimSpace(in.RefURL),
		RefLabel: oneLine(in.RefLabel, 140), Worktree: in.Worktree, HostID: in.HostID})
	if err != nil {
		s.fail(w, err, "draft")
		return
	}
	d.ID, d.UserID = uuid.NewString(), u.ID
	// a file the agent can't read is left out (and reported), not the whole draft
	type fileResult struct {
		Name  string `json:"name"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	files := []fileResult{}
	d.UploadIDs = []string{}
	for _, f := range in.Files {
		up, err := s.saveUpload(ctx, u.ID, f.Name, f.Data)
		if err != nil {
			files = append(files, fileResult{Name: f.Name, Error: err.Error()})
			continue
		}
		d.UploadIDs = append(d.UploadIDs, up.ID)
		files = append(files, fileResult{Name: f.Name, OK: true})
	}
	if err := s.store.CreateLinkDraft(ctx, d); err != nil {
		s.fail(w, err, "save draft")
		return
	}
	s.touch()
	writeJSON(w, http.StatusCreated, map[string]any{"id": d.ID, "url": s.draftURL(d.ID), "files": files})
}

// validDraft checks what an app asked for; the person can still change all
// of it before sending.
func (s *Server) validDraft(ctx context.Context, u *store.User, d *store.LinkDraft) (*store.LinkDraft, error) {
	if strings.TrimSpace(d.Text) == "" {
		return nil, badRequest("write the prompt (text) to start with")
	}
	if len(d.Text) > 300000 {
		return nil, badRequest("the prompt is longer than 300,000 bytes")
	}
	if len(d.System) > 64<<10 {
		return nil, badRequest("the hidden context (system) is longer than 64 KB")
	}
	if d.Source == "" {
		d.Source = "app"
	}
	if !linkSourceRe.MatchString(d.Source) {
		return nil, badRequest("source must be a short lowercase app name, e.g. projects")
	}
	if d.RefURL != "" {
		if pu, err := url.Parse(d.RefURL); err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return nil, badRequest("ref_url must be an http(s) address")
		}
	}
	if d.Provider != "" && d.Provider != "claude" && d.Provider != "codex" {
		return nil, badRequest("provider must be claude or codex")
	}
	prov := d.Provider
	if prov == "" {
		st, _ := s.store.GetSettings(ctx, u.ID)
		prov = firstNonEmpty(st.DefaultProvider, "claude")
	}
	if d.Mode != "" && !validMode(prov, d.Mode) {
		return nil, badRequest("unknown mode " + d.Mode + " for " + prov)
	}
	if d.Cwd == "" {
		return nil, badRequest("choose the folder the agent works in (cwd)")
	}
	if d.HostID != 0 {
		// another machine: its own login, and its ssh account's permissions are the boundary
		if _, err := s.checkHostUse(ctx, u, d.HostID); err != nil {
			return nil, err
		}
		d.AccountID = nil
		if !strings.HasPrefix(d.Cwd, "/") && d.Cwd != "~" && !strings.HasPrefix(d.Cwd, "~/") {
			return nil, badRequest("cwd on another machine must be an absolute folder (or ~/…)")
		}
	} else {
		if d.AccountID != nil {
			if err := s.checkAccount(ctx, u, prov, *d.AccountID); err != nil {
				if !s.jailed(ctx, u.ID) {
					return nil, err
				}
				d.AccountID = nil // a team's login (the owner's): they run on their own
			}
		}
		cwd, err := s.ownCopy(ctx, u, d.Cwd)
		if err != nil {
			return nil, err
		}
		clean, err := s.checkDirFor(ctx, u, cwd)
		if err != nil {
			return nil, err
		}
		d.Cwd = clean
	}
	if wt := d.Worktree; wt != nil {
		wt.Name, wt.Base = strings.Trim(strings.ReplaceAll(strings.TrimSpace(wt.Name), " ", "-"), "/"), strings.TrimSpace(wt.Base)
		if !branchNameOK(wt.Name) {
			return nil, badRequest("worktree.name must be a branch name")
		}
		if wt.Base != "" && !branchNameOK(wt.Base) {
			return nil, badRequest("worktree.base must be a branch name")
		}
	}
	return d, nil
}

type linkThread struct {
	DraftID  string  `json:"draft_id"`
	ThreadID *string `json:"thread_id"`
	Title    string  `json:"title"`
	Status   string  `json:"status"` // draft | idle | running | waiting | error | deleted
	URL      string  `json:"url"`
	// PullRequests the agent opened in this conversation (from its own commands' output)
	PullRequests []string `json:"pull_requests"`
	// Worktree: the branch of the worktree it works in ("" = the folder itself)
	Worktree   string `json:"worktree,omitempty"`
	WorktreeID int64  `json:"worktree_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// handleLinkThreads lists the person's conversations about one item.
func (s *Server) handleLinkThreads(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	drafts, err := s.store.LinkDrafts(ctx, u.ID, strings.ToLower(q.Get("source")), q.Get("ref"))
	if err != nil {
		s.fail(w, err, "drafts")
		return
	}
	out := []linkThread{}
	for _, d := range drafts {
		lt := linkThread{DraftID: d.ID, Title: d.Title, Status: "draft", URL: s.draftURL(d.ID), CreatedAt: d.CreatedAt, UpdatedAt: d.CreatedAt, PullRequests: []string{}}
		if d.ThreadID != nil {
			lt.ThreadID = d.ThreadID
			if t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID); err == nil {
				t = s.rt.decorate(t)
				lt.Title, lt.Status, lt.URL, lt.UpdatedAt = firstNonEmpty(t.Title, d.Title), linkStatus(t), s.threadURL(t.ID), firstNonEmpty(t.ActivityAt, t.UpdatedAt)
				if t.Pending > 0 {
					lt.Status = "waiting"
				}
				lt.PullRequests = s.threadPRs(ctx, t.ID)
				if t.WorktreeID != 0 {
					if wt, err := s.store.GetWorktree(ctx, u.ID, t.WorktreeID); err == nil {
						lt.Worktree, lt.WorktreeID = wt.Branch, wt.ID
					}
				}
			} else {
				lt.Status, lt.URL = "deleted", ""
			}
		}
		out = append(out, lt)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- the person's side ----------

// maxDraftFiles leaves room in the 10-file message limit for one more of the person's own.
const maxDraftFiles = 8

func (s *Server) handleGetDraft(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetLinkDraft(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "draft")
		return
	}
	// A draft without a login runs on the account chosen for connected apps
	// (looked up now, so a later change in App defaults still applies).
	if d.AccountID == nil && d.HostID == 0 { // another machine uses its own login
		st, _ := s.store.GetSettings(r.Context(), d.UserID)
		if id, ok := s.appAccount(r.Context(), userFrom(r.Context()), st); ok && firstNonEmpty(d.Provider, st.DefaultProvider, "claude") == "claude" {
			d.AccountID = &id
		}
	}
	uploads := []uploadOut{}
	for _, id := range d.UploadIDs {
		if up, err := s.store.GetUpload(r.Context(), d.UserID, id); err == nil {
			uploads = append(uploads, uploadOut{Upload: up, URL: "/api/v1/uploads/" + id})
		}
	}
	writeJSON(w, http.StatusOK, struct {
		*store.LinkDraft
		Uploads []uploadOut `json:"uploads"`
	}{d, uploads})
}

// linkStarts holds a draft while its thread starts: the first process start
// happens inside createThread, before the thread's id is known here.
var (
	linkStartMu sync.Mutex
	linkStarts  sync.Map // "<server>|<user>|<source>|<ref>" → *store.LinkDraft
)

func (s *Server) linkStartKey(uid int64, source, ref string) string {
	return fmt.Sprintf("%p|%d|%s|%s", s, uid, source, ref)
}

func (s *Server) handleStartDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	d, err := s.store.GetLinkDraft(ctx, u.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "draft")
		return
	}
	var in struct {
		newThread
		// DraftUploads: the draft's files still attached (the person may
		// remove some); absent = all of them.
		DraftUploads *[]string `json:"draft_uploads"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	keep := d.UploadIDs
	if in.DraftUploads != nil {
		keep = []string{}
		for _, id := range *in.DraftUploads {
			if contains(d.UploadIDs, id) {
				keep = append(keep, id)
			}
		}
	}
	for _, id := range keep {
		if !contains(in.UploadIDs, id) {
			in.UploadIDs = append(in.UploadIDs, id)
		}
	}
	ok, err := s.store.ClaimLinkDraft(ctx, u.ID, d.ID)
	if err != nil {
		s.fail(w, err, "draft")
		return
	}
	if !ok {
		d, _ = s.store.GetLinkDraft(ctx, u.ID, d.ID)
		tid := ""
		if d != nil && d.ThreadID != nil {
			tid = *d.ThreadID
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this conversation was already started", "thread_id": tid})
		return
	}
	// What the composer has wins; empty fields fall back to the draft.
	in.Provider = firstNonEmpty(in.Provider, d.Provider)
	in.Model = firstNonEmpty(in.Model, d.Model)
	in.Effort = firstNonEmpty(in.Effort, d.Effort)
	in.Mode = firstNonEmpty(in.Mode, d.Mode)
	in.Cwd = firstNonEmpty(in.Cwd, d.Cwd)
	if in.HostID == 0 {
		in.HostID = d.HostID
	}
	if in.AccountID == nil && in.HostID == 0 {
		in.AccountID = d.AccountID
	}
	if strings.TrimSpace(in.Text) == "" && len(in.UploadIDs) == 0 {
		in.Text = d.Text
	}
	in.Title = d.Title
	// the app's worktree (its name possibly changed by the person): reuse, base, fast-forward
	if in.Worktree != nil && d.Worktree != nil && (in.Worktree.Mode == "" || in.Worktree.Mode == "new") && in.Worktree.Name != "" {
		if h, err := s.checkHostUse(ctx, u, in.HostID); err == nil {
			in.Worktree = s.ticketWorktree(ctx, u, h, in.Cwd, *in.Worktree)
		}
	}
	source := "link:" + d.Source

	linkStartMu.Lock()
	key := s.linkStartKey(u.ID, source, d.Ref)
	linkStarts.Store(key, d)
	t, err := s.createThread(ctx, u, in.newThread, source, d.Ref)
	linkStarts.Delete(key)
	linkStartMu.Unlock()

	if t == nil {
		_ = s.store.FinishLinkDraft(ctx, d.ID, "")
		s.fail(w, err, "start conversation")
		return
	}
	_ = s.store.SaveThreadContext(ctx, draftContext(t.ID, d))
	_ = s.store.FinishLinkDraft(ctx, d.ID, t.ID)
	if err != nil {
		// The thread exists; the agent did not start. Show it with the error.
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		t = s.rt.setStatus(t.ID, "error", err.Error())
	}
	s.touch()
	writeJSON(w, http.StatusCreated, t)
}

// linkStatus is a decorated thread's state for other apps: waiting when it
// asks something, running while its turn or its background sub-agents work
// (the turn can end with sub-agents still at it).
func linkStatus(t *store.Thread) string {
	if t.Pending > 0 {
		return "waiting"
	}
	if subs, ok := t.Subagents.([]agent.Subagent); t.Status != "running" && t.Status != "waiting" && ok && len(subs) > 0 {
		return "running"
	}
	return t.Status
}

func draftContext(threadID string, d *store.LinkDraft) *store.ThreadContext {
	return &store.ThreadContext{ThreadID: threadID, System: d.System, Source: d.Source, Ref: d.Ref, RefURL: d.RefURL, RefLabel: d.RefLabel}
}

// threadContextPrompt is the hidden context appended to a thread's system
// prompt ("" for threads no app prepared).
func (s *Server) threadContextPrompt(ctx context.Context, t *store.Thread) string {
	c, err := s.store.GetThreadContext(ctx, t.ID)
	if errors.Is(err, store.ErrNotFound) && strings.HasPrefix(t.Source, "link:") {
		if v, ok := linkStarts.Load(s.linkStartKey(t.UserID, t.Source, t.SourceRef)); ok {
			c = draftContext(t.ID, v.(*store.LinkDraft))
			_ = s.store.SaveThreadContext(ctx, c)
			err = nil
		}
	}
	if err != nil || strings.TrimSpace(c.System) == "" {
		return ""
	}
	from := firstNonEmpty(c.RefLabel, c.Ref)
	pin := ""
	if c.Source == "projects" && !c.Unpinned {
		pin = "\nThis conversation is pinned to " + c.Ref + ": Pocket AI refuses Pocket Projects changes to anything but that issue, new sub-issues of it and its team's memory (reads stay open). If the person wants another issue changed, say so and let them unpin the conversation.\n"
	}
	return "\n\nContext from Pocket " + titleCase(c.Source) + " for this conversation (" + from + "):\n" + c.System + pin
}

// inheritLink makes a fork or rewind of a conversation another app prepared
// belong to the same item (an issue lists it among its conversations) and
// keep the app's hidden context (team instructions, memory).
func (s *Server) inheritLink(ctx context.Context, from, to *store.Thread) {
	if c, err := s.store.GetThreadContext(ctx, from.ID); err == nil {
		c.ThreadID = to.ID
		_ = s.store.SaveThreadContext(ctx, c)
	}
	d, err := s.store.LinkDraftOfThread(ctx, from.UserID, from.ID)
	if err != nil {
		return
	}
	n := *d
	n.ID, n.Title, n.Text, n.UploadIDs, n.ThreadID, n.UsedAt = uuid.NewString(), to.Title, "", nil, nil, nil
	if err := s.store.CreateLinkDraft(ctx, &n); err != nil {
		s.log.Warn("fork not linked", "thread", to.ID, "err", err)
		return
	}
	_, _ = s.store.ClaimLinkDraft(ctx, n.UserID, n.ID)
	_ = s.store.FinishLinkDraft(ctx, n.ID, to.ID)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// handleThreadContext tells the thread page where a thread came from.
func (s *Server) handleThreadContext(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessView)
	if t == nil {
		return
	}
	c, err := s.store.GetThreadContext(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "thread context")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleLinkDirs lists folders for a linked app's folder field (the same
// roots and checks as the folder picker; hidden folders only when asked).
func (s *Server) handleLinkDirs(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	h, err := s.checkHostUse(r.Context(), u, queryInt64(r, "host"))
	if err != nil {
		s.fail(w, err, "dirs")
		return
	}
	var out *dirListing
	if h != nil {
		out, err = s.listRemoteDirs(r.Context(), h, r.URL.Query().Get("path"), queryBool(r, "hidden"))
	} else {
		out, err = s.listDirs(withUser(r.Context(), u), r.URL.Query().Get("path"), queryBool(r, "hidden")) // their roots (people.go)
	}
	if err != nil {
		s.fail(w, err, "dirs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLinkActive lists the person's conversations from one app that are
// working or waiting right now, by ref, so lists can mark those items.
func (s *Server) handleLinkActive(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	drafts, err := s.store.StartedLinkDrafts(ctx, u.ID, strings.ToLower(r.URL.Query().Get("source")))
	if err != nil {
		s.fail(w, err, "drafts")
		return
	}
	type active struct {
		Ref      string `json:"ref"`
		Status   string `json:"status"`
		ThreadID string `json:"thread_id"`
		URL      string `json:"url"`
	}
	out := []active{}
	for _, d := range drafts {
		t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID)
		if err != nil {
			continue
		}
		st := linkStatus(s.rt.decorate(t))
		if st == "running" || st == "waiting" {
			out = append(out, active{Ref: d.Ref, Status: st, ThreadID: t.ID, URL: s.threadURL(t.ID)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// withLinkedAppTools keeps the tools of the app a conversation came from
// (pocket_projects_* for link:projects) in the list whatever the latest
// prompt says: the scoped catalog picks tools by the prompt's words, and a
// short reply like "yes" would otherwise hide the ticket tools mid-task.
func withLinkedAppTools(scoped any, all []askTool, t *store.Thread) any {
	app, ok := strings.CutPrefix(t.Source, "link:")
	m, isMap := scoped.(map[string]any)
	if !ok || app == "" || !isMap {
		return scoped
	}
	list, _ := m["tools"].([]askTool)
	have := map[string]bool{}
	for _, x := range list {
		have[x.Name] = true
	}
	prefix := "pocket_" + app + "_"
	for _, x := range all {
		if strings.HasPrefix(x.Name, prefix) && !have[x.Name] {
			list = append(list, x)
		}
	}
	m["tools"] = list
	return m
}

// prURL matches GitHub (/pull/N) and Forgejo/Gitea (/pulls/N) pull request addresses.
var prURL = regexp.MustCompile(`https?://[\w.-]+(?::\d+)?/[\w.-]+/[\w.-]+/pulls?/\d+`)

// prCreate recognises commands and tools that open a pull request, so PRs the
// agent merely listed or read are not taken for its own.
var prCreate = regexp.MustCompile(`(?i)\b(gh|tea|hub)\s+(pr|pull-request|pulls)\s+create\b|create_pull_request|/pulls\b[^\n]*-X\s*POST|-X\s*POST[^\n]*/pulls\b`)

// threadPRs lists the pull requests opened by commands in a thread, oldest first.
func (s *Server) threadPRs(ctx context.Context, threadID string) []string {
	out := []string{}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT data FROM items WHERE thread_id = ? AND kind = 'tool' AND data LIKE '%pull%' ORDER BY id`, threadID)
	if err != nil {
		return out
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil || !prCreate.MatchString(data) {
			continue
		}
		for _, u := range prURL.FindAllString(data, -1) {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// handleLinkPRs lists the pull requests the person's recent conversations from
// one app opened, by ref, so the app can attach them without asking the agent.
func (s *Server) handleLinkPRs(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	drafts, err := s.store.StartedLinkDrafts(ctx, u.ID, strings.ToLower(r.URL.Query().Get("source")))
	if err != nil {
		s.fail(w, err, "drafts")
		return
	}
	type pr struct {
		Ref      string `json:"ref"`
		URL      string `json:"url"`
		ThreadID string `json:"thread_id"`
	}
	out := []pr{}
	cutoff := time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339)
	for _, d := range drafts {
		t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID)
		if err != nil || firstNonEmpty(t.ActivityAt, t.UpdatedAt) < cutoff {
			continue
		}
		for _, url := range s.threadPRs(ctx, t.ID) {
			out = append(out, pr{Ref: d.Ref, URL: url, ThreadID: t.ID})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// linkDraftOf loads a draft the calling app made for the person (another
// app's conversations are not its business: 404).
func (s *Server) linkDraftOf(w http.ResponseWriter, r *http.Request) (*store.User, *store.LinkDraft) {
	u, app := s.linkCaller(w, r)
	if u == nil {
		return nil, nil
	}
	d, err := s.store.GetLinkDraft(r.Context(), u.ID, chi.URLParam(r, "draft"))
	if err != nil || app == "" || d.Source != app {
		writeErr(w, http.StatusNotFound, "no such conversation")
		return nil, nil
	}
	return u, d
}

// dropDraftUploads removes the files of a draft that was never sent.
func (s *Server) dropDraftUploads(ctx context.Context, d *store.LinkDraft) {
	if d.ThreadID != nil {
		return
	}
	for _, id := range d.UploadIDs {
		if up, err := s.store.GetUpload(ctx, d.UserID, id); err == nil && up.ThreadID == nil {
			s.removeUploadFile(up.Path)
			_ = s.store.DeleteUpload(ctx, up.UserID, up.ID)
		}
	}
}

// handleLinkUnlink takes a conversation off the app's item (Projects:
// "Remove from issue"); the conversation itself stays in Pocket AI.
func (s *Server) handleLinkUnlink(w http.ResponseWriter, r *http.Request) {
	u, d := s.linkDraftOf(w, r)
	if d == nil {
		return
	}
	s.dropDraftUploads(r.Context(), d)
	if err := s.store.DeleteLinkDraft(r.Context(), u.ID, d.ID); err != nil {
		s.fail(w, err, "unlink")
		return
	}
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}

// handleLinkDelete deletes the conversation (stopping its agent) and the draft.
func (s *Server) handleLinkDelete(w http.ResponseWriter, r *http.Request) {
	u, d := s.linkDraftOf(w, r)
	if d == nil {
		return
	}
	ctx := r.Context()
	if d.ThreadID != nil {
		if t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID); err == nil {
			if err := s.deleteThread(ctx, t); err != nil {
				s.fail(w, err, "delete conversation")
				return
			}
		}
	}
	s.dropDraftUploads(ctx, d)
	if err := s.store.DeleteLinkDraft(ctx, u.ID, d.ID); err != nil {
		s.fail(w, err, "delete conversation")
		return
	}
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}
