package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Auto-approval of Pocket app changes, per person. Rules name a whole app at
// a level ("add" = operations that create something new, "all" = every
// change) or single operations. The kind of an operation comes from Core's
// tool _meta (pocket/app, pocket/kind); without it nothing auto-approves.
type appAutoRules struct {
	Apps  map[string]string `json:"apps"`  // app slug → add | all
	Tools []string          `json:"tools"` // full tool names, e.g. pocket_projects_link_pr
}

const appAutoKey = "app_auto_approve"

var appSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
var appToolRe = regexp.MustCompile(`^pocket_[a-z0-9_]{1,80}$`)

func (s *Server) appAutoRules(ctx context.Context, uid int64) appAutoRules {
	var r appAutoRules
	_ = json.Unmarshal([]byte(s.store.UserSetting(ctx, uid, appAutoKey)), &r)
	return r.clean()
}

func (r appAutoRules) clean() appAutoRules {
	out := appAutoRules{Apps: map[string]string{}, Tools: []string{}}
	for app, level := range r.Apps {
		if appSlugRe.MatchString(app) && (level == "add" || level == "all") {
			out.Apps[app] = level
		}
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if appToolRe.MatchString(t) && !seen[t] && len(out.Tools) < 500 {
			seen[t] = true
			out.Tools = append(out.Tools, t)
		}
	}
	sort.Strings(out.Tools)
	return out
}

// allows reports whether tool runs without asking, and why ("" = ask).
func (r appAutoRules) allows(tool askTool) string {
	for _, t := range r.Tools {
		if t == tool.Name {
			return "this operation"
		}
	}
	app, kind := tool.Meta.App, tool.Meta.Kind
	switch {
	case app == "" || (kind != "add" && kind != "change"):
		return ""
	case r.Apps[app] == "all":
		return "all " + appTitle(app) + " changes"
	case r.Apps[app] == "add" && kind == "add":
		return "everything new in " + appTitle(app)
	}
	return ""
}

func appTitle(slug string) string {
	if len(slug) <= 2 {
		return strings.ToUpper(slug) // AI
	}
	return strings.ToUpper(slug[:1]) + strings.ReplaceAll(slug[1:], "-", " ")
}

type appAutoOp struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type appAutoApp struct {
	Slug  string      `json:"slug"`
	Name  string      `json:"name"`
	Ops   []appAutoOp `json:"ops"`
	Adds  int         `json:"adds"`
	Total int         `json:"total"`
}

// GET /pocket-tools/auto-approve: your rules, plus (when Ask is connected)
// every app change you could auto-approve, grouped by app.
func (s *Server) handleGetAppAuto(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"rules": s.appAutoRules(r.Context(), me(r)), "apps": []appAutoApp{}}
	catalog, err := s.catalog(r.Context(), me(r))
	if err != nil {
		out["catalog_error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}
	byApp := map[string]*appAutoApp{}
	for _, t := range catalog {
		if t.Annotations.ReadOnly || t.Meta.App == "" || (t.Meta.Kind != "add" && t.Meta.Kind != "change") {
			continue
		}
		a := byApp[t.Meta.App]
		if a == nil {
			a = &appAutoApp{Slug: t.Meta.App, Name: appTitle(t.Meta.App)}
			byApp[t.Meta.App] = a
		}
		title := askActionTitle(t.Name)
		if i := strings.Index(title, " · "); i >= 0 {
			title = title[i+len(" · "):]
		}
		a.Ops = append(a.Ops, appAutoOp{Name: t.Name, Title: title, Kind: t.Meta.Kind, Description: firstSentence(t.Description)})
		a.Total++
		if t.Meta.Kind == "add" {
			a.Adds++
		}
	}
	apps := []appAutoApp{}
	for _, a := range byApp {
		sort.Slice(a.Ops, func(i, j int) bool {
			if a.Ops[i].Kind != a.Ops[j].Kind {
				return a.Ops[i].Kind == "add"
			}
			return a.Ops[i].Title < a.Ops[j].Title
		})
		apps = append(apps, *a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	out["apps"] = apps
	writeJSON(w, 200, out)
}

// PUT replaces your rules; POST adds one (from an approval card):
// {"app":"projects","level":"add"} or {"tool":"pocket_projects_link_pr"}.
func (s *Server) handlePutAppAuto(w http.ResponseWriter, r *http.Request) {
	var in appAutoRules
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	s.saveAppAuto(w, r, in.clean())
}

func (s *Server) handleAddAppAuto(w http.ResponseWriter, r *http.Request) {
	var in struct {
		App   string `json:"app"`
		Level string `json:"level"`
		Tool  string `json:"tool"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	rules := s.appAutoRules(r.Context(), me(r))
	switch {
	case in.Tool != "" && appToolRe.MatchString(in.Tool):
		rules.Tools = append(rules.Tools, in.Tool)
	case appSlugRe.MatchString(in.App) && (in.Level == "add" || in.Level == "all"):
		if rules.Apps[in.App] != "all" {
			rules.Apps[in.App] = in.Level
		}
	default:
		writeErr(w, 400, "name an operation, or an app with level add or all")
		return
	}
	s.saveAppAuto(w, r, rules.clean())
}

func (s *Server) saveAppAuto(w http.ResponseWriter, r *http.Request, rules appAutoRules) {
	b, _ := json.Marshal(rules)
	if err := s.store.SetUserSetting(r.Context(), me(r), appAutoKey, string(b)); err != nil {
		s.fail(w, err, "save auto-approve rules")
		return
	}
	writeJSON(w, 200, map[string]any{"rules": rules})
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
