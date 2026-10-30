package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The Core link Pocket AI → Pocket Projects (projects:read). Conversations a
// Projects issue started (source link:projects, source_ref = issue key) are
// grouped by issue in the sidebar and the tab bar while it is on, with each
// issue's title and state read from Projects as the person.
const projectsScope = "projects:read"

var projectsUse = core.Use{App: "projects", Scopes: []string{projectsScope},
	Reason: "Group the conversations your Projects issues started by issue, in the sidebar and the tab bar, with each issue's title and state"}

type projectsIssue struct {
	Title     string `json:"title"`
	StateType string `json:"state_type"`
	URL       string `json:"url"`
}

type projectsCached struct {
	at    time.Time
	issue *projectsIssue // nil = not found / no access
}

var (
	projectsCache   sync.Map // email + "\x00" + ref → projectsCached
	projectsRefRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,9}-[0-9]{1,9}$`)
	projectsHTTP    = &http.Client{Timeout: 8 * time.Second}
	projectsCacheTT = 2 * time.Minute
)

// GET /projects-link?refs=AS-7,AS-13: is the link on for me, and those issues.
func (s *Server) handleProjectsLink(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	out := map[string]any{"configured": s.core.Configured(), "on": false, "issues": map[string]projectsIssue{}}
	if s.core.Configured() {
		if o := s.core.OIDC(); o.Issuer != "" {
			out["core_links_url"] = strings.TrimRight(o.Issuer, "/") + "/links"
		}
		if app, err := s.core.Resolve(r.Context(), "projects"); err == nil && app != nil {
			out["projects_url"] = app.PublicURL
		}
	}
	if !s.core.Configured() {
		out["reason"] = "There is no Pocket Core on this box."
		writeJSON(w, 200, out)
		return
	}
	on, reason := s.core.LinkOn(r.Context(), u.Email, "projects", projectsScope)
	out["on"] = on
	if !on {
		out["reason"] = reason
		writeJSON(w, 200, out)
		return
	}
	// every conversation of mine that belongs to a Projects issue, forks and
	// rewinds included (they inherit the context; their source says "fork")
	threads := map[string]string{}
	if rows, err := s.store.DB.QueryContext(r.Context(), `SELECT c.thread_id, c.ref FROM thread_context c JOIN threads t ON t.id = c.thread_id
		WHERE t.user_id = ? AND c.source = 'projects' AND c.ref != ''`, u.ID); err == nil {
		for rows.Next() {
			var id, ref string
			if rows.Scan(&id, &ref) == nil {
				threads[id] = strings.ToUpper(ref)
			}
		}
		rows.Close()
	}
	out["threads"] = threads
	// one Projects workspace at a time is open to Pocket AI: the conversations
	// about the others are paused (Projects refuses their tool calls)
	if ws := s.projectsWorkspaces(r.Context(), u.Email); len(ws) > 1 {
		active, own := "", ""
		for _, w := range ws {
			if w.Active {
				active = w.Address
			}
			if w.Own {
				own = w.Address
			}
		}
		out["agent_workspace"] = active
		paused := map[string]string{}
		for id, ref := range threads {
			addr := own
			if i := strings.LastIndex(ref, "/"); i > 0 {
				addr = strings.ToLower(ref[:i])
			}
			if active != "" && addr != active {
				paused[id] = "Paused: " + addr + " isn't the workspace open in Pocket Projects (" + active + " is), so this conversation can't read or change its tickets. Open " + addr + " in Projects to continue with them."
			}
		}
		out["paused"] = paused
	}
	refs := []string{}
	for _, ref := range strings.Split(r.URL.Query().Get("refs"), ",") {
		if ref = strings.ToUpper(strings.TrimSpace(ref)); projectsRefRe.MatchString(ref) && len(refs) < 40 {
			refs = append(refs, ref)
		}
	}
	for _, ref := range threads { // issues known only through forks get titles too
		if len(refs) < 40 && !slices.Contains(refs, ref) && projectsRefRe.MatchString(ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) > 0 {
		out["issues"] = s.projectsIssues(r.Context(), u.Email, refs)
	}
	writeJSON(w, 200, out)
}

func (s *Server) projectsIssues(ctx context.Context, email string, refs []string) map[string]projectsIssue {
	got := map[string]projectsIssue{}
	missing := []string{}
	for _, ref := range refs {
		if v, ok := projectsCache.Load(email + "\x00" + ref); ok && time.Since(v.(projectsCached).at) < projectsCacheTT {
			if is := v.(projectsCached).issue; is != nil {
				got[ref] = *is
			}
			continue
		}
		missing = append(missing, ref)
	}
	if len(missing) == 0 {
		return got
	}
	tok, err := s.core.ActAs(ctx, email, "projects", projectsScope)
	if err != nil {
		s.log.Warn("projects link token", "err", err)
		return got
	}
	base := onThisHost(strings.TrimRight(tok.URL, "/"))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ref := range missing {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			is, err := projectsGetIssue(ctx, base, tok.AccessToken, ref)
			if err != nil && is == nil { // network trouble: try again next time
				return
			}
			projectsCache.Store(email+"\x00"+ref, projectsCached{at: time.Now(), issue: is})
			if is != nil {
				mu.Lock()
				got[ref] = *is
				mu.Unlock()
			}
		}(ref)
	}
	wg.Wait()
	return got
}

type projectsWorkspace struct {
	Address string `json:"address"`
	Own     bool   `json:"own"`
	Active  bool   `json:"active"`
}

var projectsWS sync.Map // email → projectsWSCached

type projectsWSCached struct {
	at   time.Time
	list []projectsWorkspace
}

// projectsWorkspaces: the person's Projects workspaces and the one open to
// Pocket AI (nil when Projects can't say; then nothing is paused).
func (s *Server) projectsWorkspaces(ctx context.Context, email string) []projectsWorkspace {
	if v, ok := projectsWS.Load(email); ok && time.Since(v.(projectsWSCached).at) < 10*time.Second {
		return v.(projectsWSCached).list
	}
	tok, err := s.core.ActAs(ctx, email, "projects", projectsScope)
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, onThisHost(strings.TrimRight(tok.URL, "/"))+"/api/v1/workspaces", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	res, err := projectsHTTP.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var list []projectsWorkspace
	if res.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&list) != nil {
		return nil // an older Projects without workspaces
	}
	projectsWS.Store(email, projectsWSCached{at: time.Now(), list: list})
	return list
}

// projectsGetIssue: nil, nil = the issue is gone or not yours.
func projectsGetIssue(ctx context.Context, base, token, ref string) (*projectsIssue, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/issues/"+url.PathEscape(ref), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	res, err := projectsHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusForbidden {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("projects: %s", res.Status)
	}
	var is projectsIssue
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&is); err != nil {
		return nil, err
	}
	return &is, nil
}

// onThisHost: Core hands out addresses for its Docker apps
// (host.docker.internal); Pocket AI runs natively on that host, where the
// name does not resolve, so it means 127.0.0.1 here.
func onThisHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "host.docker.internal" {
		return raw
	}
	if port := u.Port(); port != "" {
		u.Host = "127.0.0.1:" + port
	} else {
		u.Host = "127.0.0.1"
	}
	return u.String()
}
