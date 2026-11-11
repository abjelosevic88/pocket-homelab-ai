package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A notebook from Pocket Projects: an issue (its description, sub-issues,
// relations and comments as one source; its shared notes, files and the
// Pocket Notes about it as their own) or a project (overview, milestones,
// links, updates and its issues as one source; its docs as their own).
// Importing again updates what is there and adds what is new: every source
// knows the item it came from (the first word of its origin).

const (
	projectsFileMax   = 25       // files taken from one issue
	projectsIssueMax  = 200      // issues listed for a project
	projectsDescCut   = 1500     // bytes of a sub-issue's / project issue's description
	projectsCommentAt = 300      // comments per issue
	projectsRawMax    = 50 << 20 // bytes of one attachment
)

func init() {
	for k, v := range map[string]string{"issue": "Pocket Projects issue", "project": "Pocket Projects project",
		"shared-note": "Shared note of an issue", "project-doc": "Project doc"} {
		sourceKindLabel[k] = v
	}
}

// projectsRaw reads a file from Projects (an attachment), like projectsCall.
func (s *Server) projectsRaw(ctx context.Context, email, path, workspace string) ([]byte, error) {
	tok, err := s.core.ActAs(ctx, email, "projects", projectsScope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, onThisHost(strings.TrimRight(tok.URL, "/"))+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	if workspace != "" {
		req.Header.Set("X-Pocket-Source", "mcp")
		req.Header.Set("X-Pocket-Workspace", workspace)
	}
	res, err := projectsHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, projectsRawMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > projectsRawMax {
		return nil, errors.New("larger than 50 MB")
	}
	return data, nil
}

// splitRef: "athena/ATH-340" → ("athena", "ATH-340").
func splitRef(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "/"); i > 0 {
		return strings.ToLower(ref[:i]), ref[i+1:]
	}
	return "", ref
}

func joinRef(ws, ident string) string {
	if ws == "" {
		return ident
	}
	return ws + "/" + ident
}

// originKey is the item a source came from: the first word of its origin.
func originKey(origin string) string {
	k, _, _ := strings.Cut(strings.TrimSpace(origin), " ")
	return k
}

func withURL(key, link string) string {
	if strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
		return key + " " + link
	}
	return key
}

// ---------- the person's issues and projects ----------

// GET /projects-projects?q=: projects to make a notebook from.
func (s *Server) handleProjectsProjects(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !s.projectsLinkReady(w, r, u.Email) {
		return
	}
	ws := s.projectsAgentWorkspace(r.Context(), u.Email)
	q := url.Values{}
	if text := strings.TrimSpace(r.URL.Query().Get("q")); text != "" {
		q.Set("q", text)
	}
	var got []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if _, err := s.projectsCall(r.Context(), u.Email, "/api/v1/projects?"+q.Encode(), ws, &got); err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Projects: "+err.Error())
		return
	}
	type out struct {
		Ref     string `json:"ref"`
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	list := []out{}
	for _, p := range got {
		if len(list) == 30 {
			break
		}
		list = append(list, out{Ref: joinRef(ws, strconv.FormatInt(p.ID, 10)), ID: p.ID, Name: p.Name, Status: p.Status, Summary: p.Summary})
	}
	writeJSON(w, 200, map[string]any{"workspace": ws, "projects": list})
}

// ---------- importing ----------

type importReport struct {
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Failed  []string `json:"failed"`
	// what it was: for a new notebook's name and link
	Label string `json:"label"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Link  string `json:"link"`
}

// importer adds or updates the sources of one import.
type importer struct {
	s     *Server
	ctx   context.Context
	nb    *store.Notebook
	email string
	uid   int64
	have  map[string]*store.NotebookSource
	rep   *importReport
}

func (s *Server) newImporter(ctx context.Context, u *store.User, nb *store.Notebook) *importer {
	im := &importer{s: s, ctx: ctx, nb: nb, email: u.Email, uid: u.ID, have: map[string]*store.NotebookSource{}, rep: &importReport{Failed: []string{}}}
	list, _ := s.store.ListNotebookSources(ctx, nb.ID)
	for _, x := range list {
		im.have[originKey(x.Origin)] = x
	}
	return im
}

// put stores one source: a new one, or the new version of the one from the
// same item. A file that is already there is kept (files do not change).
func (im *importer) put(x *store.NotebookSource, text string, data []byte, name string) {
	key := originKey(x.Origin)
	if old := im.have[key]; old != nil {
		if data != nil {
			return
		}
		old.Title, old.Origin, old.Mime = x.Title, x.Origin, x.Mime
		if err := im.s.writeSource(im.ctx, im.nb, old, text, nil, ""); err != nil {
			im.fail(x.Title, err)
			return
		}
		im.rep.Updated++
		return
	}
	got, err := im.s.addSource(im.ctx, im.nb, x, text, data, name)
	if err != nil {
		im.fail(x.Title, err)
		return
	}
	im.have[key] = got
	im.rep.Added++
}

func (im *importer) fail(what string, err error) {
	var ue uploadErr
	msg := err.Error()
	if errors.As(err, &ue) {
		msg = ue.msg
	}
	im.rep.Failed = append(im.rep.Failed, what+": "+msg)
}

// people: Projects user id → name, for comments and leads.
func (im *importer) people(ws string) map[int64]string {
	var list []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	out := map[int64]string{}
	if _, err := im.s.projectsCall(im.ctx, im.email, "/api/v1/people", ws, &list); err == nil {
		for _, p := range list {
			if p.ID > 0 {
				out[p.ID] = firstNonEmpty(p.Name, p.Email)
			}
		}
	}
	return out
}

type projIssue struct {
	ID          int64   `json:"id"`
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	StateType   string  `json:"state_type"`
	Priority    int     `json:"priority"`
	DueDate     *string `json:"due_date"`
	AssigneeID  *int64  `json:"assignee_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

var priorityName = map[int]string{1: "Urgent", 2: "High", 3: "Medium", 4: "Low"}

func issueLine(is projIssue, people map[int64]string) string {
	return fmt.Sprintf("%s %s (%s)", is.Identifier, is.Title, issueState(is, people))
}

func issueState(is projIssue, people map[int64]string) string {
	parts := []string{firstNonEmpty(is.StateType, "unstarted")}
	if p := priorityName[is.Priority]; p != "" {
		parts = append(parts, p)
	}
	if is.AssigneeID != nil && people[*is.AssigneeID] != "" {
		parts = append(parts, people[*is.AssigneeID])
	}
	if is.DueDate != nil && *is.DueDate != "" {
		parts = append(parts, "due "+*is.DueDate)
	}
	return strings.Join(parts, ", ")
}

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + " …"
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+pre)
}

// importIssue brings one issue in (ref: "athena/ATH-340" or "ATH-340").
func (im *importer) importIssue(ref string) error {
	ws, ident := splitRef(ref)
	if !projectsRefRe.MatchString(strings.ToUpper(ident)) {
		return badRequest("Name the issue by its key, like ATH-340")
	}
	var d struct {
		projIssue
		URL    string `json:"url"`
		Branch string `json:"branch_name"`
		Team   *struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"team"`
		Parent *struct {
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
		} `json:"parent"`
		SubIssues []projIssue `json:"sub_issues"`
		Relations []struct {
			Kind  string `json:"kind"`
			Issue *struct {
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
			} `json:"issue"`
		} `json:"relations"`
		Attachments []struct {
			ID       int64  `json:"id"`
			Kind     string `json:"kind"`
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
			URL      string `json:"url"`
			FileName string `json:"file_name"`
			Mime     string `json:"mime"`
			Size     int64  `json:"size"`
		} `json:"attachment_list"`
		SharedNotes []struct {
			ID         int64  `json:"id"`
			Title      string `json:"title"`
			SourceNote string `json:"source_note"`
		} `json:"shared_notes"`
		Comments []struct {
			UserID     *int64  `json:"user_id"`
			Body       string  `json:"body"`
			SourceApp  string  `json:"source_app"`
			CreatedAt  string  `json:"created_at"`
			ResolvedAt *string `json:"resolved_at"`
		} `json:"comment_list"`
		TimeMinutes int `json:"time_minutes"`
	}
	if code, err := im.s.projectsCall(im.ctx, im.email, "/api/v1/issues/"+url.PathEscape(ident), ws, &d); err != nil {
		if code == http.StatusNotFound || code == http.StatusForbidden {
			return uploadErr{404, "Pocket Projects has no issue " + ident + " you can see"}
		}
		return uploadErr{502, "Pocket Projects: " + err.Error()}
	}
	ident = firstNonEmpty(d.Identifier, ident)
	ref = joinRef(ws, ident)
	people := im.people(ws)
	key := "projects-issue:" + ref
	im.rep.Label, im.rep.Title, im.rep.URL, im.rep.Link = ident, d.Title, d.URL, key

	var b strings.Builder
	fmt.Fprintf(&b, "# %s · %s\n\n", ident, d.Title)
	fmt.Fprintf(&b, "- State: %s\n", issueState(d.projIssue, people))
	if d.Team != nil {
		fmt.Fprintf(&b, "- Team: %s\n", d.Team.Name)
	}
	if d.Parent != nil {
		fmt.Fprintf(&b, "- Part of: %s %s\n", d.Parent.Identifier, d.Parent.Title)
	}
	if d.Branch != "" {
		fmt.Fprintf(&b, "- Branch: %s\n", d.Branch)
	}
	if d.TimeMinutes > 0 {
		fmt.Fprintf(&b, "- Time logged: %dh %02dm\n", d.TimeMinutes/60, d.TimeMinutes%60)
	}
	fmt.Fprintf(&b, "- Created %s, updated %s\n", dayOf(d.CreatedAt), dayOf(d.UpdatedAt))
	if d.URL != "" {
		fmt.Fprintf(&b, "- Page: %s\n", d.URL)
	}
	fmt.Fprintf(&b, "\n## Description\n\n%s\n", firstNonEmpty(strings.TrimSpace(d.Description), "(none)"))
	if len(d.SubIssues) > 0 {
		fmt.Fprintf(&b, "\n## Sub-issues (%d)\n\n", len(d.SubIssues))
		for _, si := range d.SubIssues {
			fmt.Fprintf(&b, "- %s\n", issueLine(si, people))
			if desc := cut(si.Description, projectsDescCut); desc != "" {
				fmt.Fprintf(&b, "%s\n", indent(desc, "  > "))
			}
		}
	}
	if len(d.Relations) > 0 {
		b.WriteString("\n## Related issues\n\n")
		for _, rel := range d.Relations {
			if rel.Issue != nil {
				fmt.Fprintf(&b, "- %s: %s %s\n", strings.ReplaceAll(rel.Kind, "_", " "), rel.Issue.Identifier, rel.Issue.Title)
			}
		}
	}
	var files []int
	if len(d.Attachments) > 0 {
		b.WriteString("\n## Attachments\n\n")
		for i, a := range d.Attachments {
			name := firstNonEmpty(a.Title, a.FileName, a.URL)
			if a.Kind == "file" {
				fmt.Fprintf(&b, "- File: %s (its own source in this notebook)\n", name)
				files = append(files, i)
			} else {
				fmt.Fprintf(&b, "- %s: %s %s\n", firstNonEmpty(a.Kind, "link"), name, a.URL)
			}
		}
	}
	if len(d.SharedNotes) > 0 {
		b.WriteString("\n## Shared notes (each is its own source in this notebook)\n\n")
		for _, n := range d.SharedNotes {
			fmt.Fprintf(&b, "- %s\n", n.Title)
		}
	}
	if len(d.Comments) > 0 {
		comments := d.Comments
		if len(comments) > projectsCommentAt {
			comments = comments[len(comments)-projectsCommentAt:]
		}
		fmt.Fprintf(&b, "\n## Comments (%d, oldest first)\n", len(d.Comments))
		for _, c := range comments {
			who := "Someone"
			if c.UserID != nil && people[*c.UserID] != "" {
				who = people[*c.UserID]
			} else if c.SourceApp != "" {
				who = "Pocket " + titleCase(c.SourceApp)
			}
			state := ""
			if c.ResolvedAt != nil && *c.ResolvedAt != "" {
				state = " (resolved)"
			}
			fmt.Fprintf(&b, "\n### %s · %s%s\n\n%s\n", who, dayOf(c.CreatedAt), state, strings.TrimSpace(c.Body))
		}
	}
	im.put(&store.NotebookSource{Kind: "issue", Title: ident + " · " + d.Title, Origin: withURL(key, d.URL), Mime: "text/markdown"}, b.String(), nil, "")

	for _, n := range d.SharedNotes {
		var note struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if _, err := im.s.projectsCall(im.ctx, im.email, fmt.Sprintf("/api/v1/issues/%s/shared-notes/%d", url.PathEscape(ident), n.ID), ws, &note); err != nil {
			im.fail(n.Title, err)
			continue
		}
		if strings.TrimSpace(note.Body) == "" {
			continue
		}
		im.put(&store.NotebookSource{Kind: "shared-note", Title: firstNonEmpty(note.Title, n.Title), Origin: withURL(fmt.Sprintf("%s#shared-%d", key, n.ID), d.URL), Mime: "text/markdown"}, note.Body, nil, "")
	}

	for i, at := range files {
		a := d.Attachments[at]
		name := firstNonEmpty(a.FileName, a.Title, fmt.Sprintf("attachment-%d", a.ID))
		okey := fmt.Sprintf("%s#file-%d", key, a.ID)
		if im.have[okey] != nil {
			continue
		}
		if i >= projectsFileMax {
			im.rep.Failed = append(im.rep.Failed, fmt.Sprintf("%d more files of %s: add them by hand", len(files)-projectsFileMax, ident))
			break
		}
		if a.Size > notebookSourceMax {
			im.rep.Failed = append(im.rep.Failed, name+": larger than 50 MB")
			continue
		}
		data, err := im.s.projectsRaw(im.ctx, im.email, fmt.Sprintf("/api/v1/attachments/%d/file", a.ID), ws)
		if err != nil {
			im.fail(name, err)
			continue
		}
		text, ctype, err := im.s.fileText(im.ctx, name, data)
		if err != nil {
			im.fail(name, err)
			continue
		}
		im.put(&store.NotebookSource{Kind: "file", Title: name, Origin: okey, Mime: ctype}, text, data, name)
	}

	// Pocket Notes that mention the issue (the issue's Notes panel), except
	// the ones shared into it
	shared := map[string]bool{}
	for _, n := range d.SharedNotes {
		if n.SourceNote != "" {
			shared[n.SourceNote] = true
		}
	}
	var notes struct {
		Status string           `json:"status"`
		Items  []map[string]any `json:"items"`
	}
	if _, err := im.s.projectsCall(im.ctx, im.email, "/api/v1/issues/"+url.PathEscape(ident)+"/notes", ws, &notes); err == nil && notes.Status == "ok" {
		for _, it := range notes.Items {
			id, _ := it["id"].(string)
			title, _ := it["title"].(string)
			link, _ := it["url"].(string)
			if id == "" || shared[id] {
				continue // shared into the issue already: that copy is a source
			}
			text, _, err := im.s.pocketText(im.ctx, im.uid, "notes", id, notebookTextMax)
			if err != nil {
				im.fail(firstNonEmpty(title, "a note"), err)
				continue
			}
			im.put(&store.NotebookSource{Kind: "note", Title: firstNonEmpty(title, "Note "+id), Origin: withURL("notes:"+id, link), Mime: "text/markdown"}, text, nil, "")
		}
	}
	return nil
}

// importProject brings one project in (ref: "athena/12" or "12").
func (im *importer) importProject(ref string) error {
	ws, idText := splitRef(ref)
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id < 1 {
		return badRequest("Choose a project")
	}
	var p struct {
		ID         int64   `json:"id"`
		Name       string  `json:"name"`
		Summary    string  `json:"summary"`
		Content    string  `json:"content"`
		Status     string  `json:"status"`
		Health     string  `json:"health"`
		LeadID     *int64  `json:"lead_id"`
		StartDate  *string `json:"start_date"`
		TargetDate *string `json:"target_date"`
		Progress   any     `json:"progress"`
		URL        string  `json:"url"`
		Docs       []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"docs"`
		Links []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"links"`
		Updates []struct {
			UserID    *int64 `json:"user_id"`
			Health    string `json:"health"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
		} `json:"updates"`
		Milestones []struct {
			Name       string  `json:"name"`
			TargetDate *string `json:"target_date"`
			Done       any     `json:"completed_at"`
		} `json:"milestones"`
	}
	if code, err := im.s.projectsCall(im.ctx, im.email, fmt.Sprintf("/api/v1/projects/%d", id), ws, &p); err != nil {
		if code == http.StatusNotFound || code == http.StatusForbidden {
			return uploadErr{404, "Pocket Projects has no such project you can see"}
		}
		return uploadErr{502, "Pocket Projects: " + err.Error()}
	}
	if len(p.Milestones) == 0 {
		_, _ = im.s.projectsCall(im.ctx, im.email, fmt.Sprintf("/api/v1/projects/%d/milestones", id), ws, &p.Milestones)
	}
	people := im.people(ws)
	key := "projects-project:" + joinRef(ws, strconv.FormatInt(id, 10))
	im.rep.Label, im.rep.Title, im.rep.URL, im.rep.Link = "Project", p.Name, p.URL, key

	var issues struct {
		Issues []projIssue `json:"issues"`
		Total  int         `json:"total"`
	}
	q := url.Values{"project_id": {strconv.FormatInt(id, 10)}, "limit": {strconv.Itoa(projectsIssueMax)}, "sort": {"updated"}}
	if _, err := im.s.projectsCall(im.ctx, im.email, "/api/v1/issues?"+q.Encode(), ws, &issues); err != nil {
		im.fail("its issues", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Project: %s\n\n", p.Name)
	fmt.Fprintf(&b, "- Status: %s", firstNonEmpty(p.Status, "—"))
	if p.Health != "" {
		fmt.Fprintf(&b, ", health %s", p.Health)
	}
	b.WriteString("\n")
	if p.LeadID != nil && people[*p.LeadID] != "" {
		fmt.Fprintf(&b, "- Lead: %s\n", people[*p.LeadID])
	}
	if p.StartDate != nil && *p.StartDate != "" {
		fmt.Fprintf(&b, "- Start: %s\n", *p.StartDate)
	}
	if p.TargetDate != nil && *p.TargetDate != "" {
		fmt.Fprintf(&b, "- Target: %s\n", *p.TargetDate)
	}
	if p.URL != "" {
		fmt.Fprintf(&b, "- Page: %s\n", p.URL)
	}
	if strings.TrimSpace(p.Summary) != "" {
		fmt.Fprintf(&b, "\n## Summary\n\n%s\n", strings.TrimSpace(p.Summary))
	}
	if strings.TrimSpace(p.Content) != "" {
		fmt.Fprintf(&b, "\n## Overview\n\n%s\n", strings.TrimSpace(p.Content))
	}
	if len(p.Milestones) > 0 {
		b.WriteString("\n## Milestones\n\n")
		for _, m := range p.Milestones {
			line := "- " + m.Name
			if m.TargetDate != nil && *m.TargetDate != "" {
				line += " (target " + *m.TargetDate + ")"
			}
			if done, _ := m.Done.(string); done != "" {
				line += " — done"
			}
			b.WriteString(line + "\n")
		}
	}
	if len(p.Links) > 0 {
		b.WriteString("\n## Links\n\n")
		for _, l := range p.Links {
			fmt.Fprintf(&b, "- %s %s\n", l.Title, l.URL)
		}
	}
	if len(p.Docs) > 0 {
		b.WriteString("\n## Docs (each is its own source in this notebook)\n\n")
		for _, d := range p.Docs {
			fmt.Fprintf(&b, "- %s\n", d.Title)
		}
	}
	if len(p.Updates) > 0 {
		b.WriteString("\n## Updates (newest first)\n")
		for _, u := range p.Updates {
			who := "Someone"
			if u.UserID != nil && people[*u.UserID] != "" {
				who = people[*u.UserID]
			}
			h := ""
			if u.Health != "" {
				h = " · " + u.Health
			}
			fmt.Fprintf(&b, "\n### %s · %s%s\n\n%s\n", dayOf(u.CreatedAt), who, h, strings.TrimSpace(u.Body))
		}
	}
	if len(issues.Issues) > 0 {
		fmt.Fprintf(&b, "\n## Issues (%d", len(issues.Issues))
		if issues.Total > len(issues.Issues) {
			fmt.Fprintf(&b, " of %d, most recently updated", issues.Total)
		}
		b.WriteString(")\n\nAdd an issue to this notebook on its own for its comments, shared notes and files.\n\n")
		for _, is := range issues.Issues {
			fmt.Fprintf(&b, "- %s\n", issueLine(is, people))
			if desc := cut(is.Description, projectsDescCut); desc != "" {
				fmt.Fprintf(&b, "%s\n", indent(desc, "  > "))
			}
		}
	}
	im.put(&store.NotebookSource{Kind: "project", Title: "Project: " + p.Name, Origin: withURL(key, p.URL), Mime: "text/markdown"}, b.String(), nil, "")

	for _, d := range p.Docs {
		body := d.Body
		if strings.TrimSpace(body) == "" {
			var full struct {
				Body string `json:"body"`
			}
			if _, err := im.s.projectsCall(im.ctx, im.email, fmt.Sprintf("/api/v1/docs/%d", d.ID), ws, &full); err != nil {
				im.fail(d.Title, err)
				continue
			}
			body = full.Body
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		im.put(&store.NotebookSource{Kind: "project-doc", Title: firstNonEmpty(d.Title, "Doc"), Origin: withURL(fmt.Sprintf("%s#doc-%d", key, d.ID), p.URL), Mime: "text/markdown"}, body, nil, "")
	}
	return nil
}

// importFrom runs one import ("issue"/"project" + ref) into a notebook.
func (s *Server) importFrom(ctx context.Context, u *store.User, nb *store.Notebook, kind, ref string) (*importReport, error) {
	im := s.newImporter(ctx, u, nb)
	var err error
	switch kind {
	case "issue":
		err = im.importIssue(ref)
	case "project":
		err = im.importProject(ref)
	default:
		err = badRequest("Choose an issue or a project")
	}
	return im.rep, err
}

// POST /notebooks/{id}/sources/projects {kind, ref}: add an issue or a project.
func (s *Server) handleNotebookProjects(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.projectsLinkReady(w, r, u.Email) {
		return
	}
	var in struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	rep, err := s.importFrom(r.Context(), u, nb, in.Kind, in.Ref)
	if err != nil {
		s.sourceResult(w, r, nb, nil, err)
		return
	}
	if nb.Link == "" { // a notebook's first import names where it is from
		nb.Link, nb.LinkLabel, nb.LinkURL = rep.Link, strings.TrimSpace(rep.Label+" "+rep.Title), rep.URL
		if rep.Label == "Project" {
			nb.LinkLabel = rep.Title
		}
		_ = s.store.SaveNotebook(r.Context(), nb)
	}
	s.writeNotebookIndex(r.Context(), nb)
	writeJSON(w, 200, map[string]any{"report": rep, "notebook": s.notebookFull(r.Context(), nb)})
}

// POST /notebooks/{id}/sync: every issue and project in it, imported again.
func (s *Server) handleNotebookSync(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.projectsLinkReady(w, r, u.Email) {
		return
	}
	list, _ := s.store.ListNotebookSources(r.Context(), nb.ID)
	total := &importReport{Failed: []string{}}
	for _, x := range list {
		key := originKey(x.Origin)
		var kind, ref string
		switch {
		case x.Kind == "issue" && strings.HasPrefix(key, "projects-issue:"):
			kind, ref = "issue", strings.TrimPrefix(key, "projects-issue:")
		case x.Kind == "project" && strings.HasPrefix(key, "projects-project:"):
			kind, ref = "project", strings.TrimPrefix(key, "projects-project:")
		default:
			continue
		}
		rep, err := s.importFrom(r.Context(), u, nb, kind, ref)
		if err != nil {
			total.Failed = append(total.Failed, x.Title+": "+err.Error())
			continue
		}
		total.Added += rep.Added
		total.Updated += rep.Updated
		total.Failed = append(total.Failed, rep.Failed...)
	}
	s.writeNotebookIndex(r.Context(), nb)
	writeJSON(w, 200, map[string]any{"report": total, "notebook": s.notebookFull(r.Context(), nb)})
}
