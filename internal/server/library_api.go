package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) libraryRoutes(r chi.Router) {
	r.Get("/workspaces", s.handleListWorkspaces)
	r.Post("/workspaces", s.handleSaveWorkspace)
	r.Put("/workspaces/{id}", s.handleSaveWorkspace)
	r.Delete("/workspaces/{id}", s.handleDeleteWorkspace)
	r.Get("/fs/dirs", s.handleDirs)
	r.Get("/fs/git", s.handleGitInfo)
	r.Post("/fs/git/switch", s.handleGitSwitch)
	r.Get("/skills", s.handleSkills)

	r.Get("/recipes", s.handleListRecipes)
	r.Get("/recipes/suggestions", s.handleRecipeSuggestions)
	r.Post("/recipes", s.handleSaveRecipe)
	r.Put("/recipes/{id}", s.handleSaveRecipe)
	r.Delete("/recipes/{id}", s.handleDeleteRecipe)
	r.Post("/recipes/{id}/run", s.handleRunRecipe)
}

// ---------- folders ----------

func (s *Server) expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		return s.cfg.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(s.cfg.Home, p[2:])
	}
	return p
}

func within(p, root string) bool {
	return p == root || root == "/" || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// checkDir resolves a folder and makes sure it exists inside the allowed roots.
func (s *Server) checkDir(p string) (string, error) {
	p = s.expand(p)
	if !filepath.IsAbs(p) {
		return "", badRequest("use an absolute folder path (or ~/…)")
	}
	p = filepath.Clean(p)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", badRequest("the folder " + p + " does not exist")
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", badRequest(p + " is not a folder")
	}
	for _, root := range s.cfg.Roots {
		rr, err := filepath.EvalSymlinks(root)
		if err != nil {
			rr = root
		}
		if within(real, rr) { // the resolved path: a symlink out of a root is outside
			return p, nil
		}
	}
	return "", badRequest(p + " is outside the folders agents may use (" + strings.Join(s.cfg.Roots, ", ") + ")")
}

func (s *Server) handleDirs(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), queryInt64(r, "host"))
	if err != nil {
		s.fail(w, err, "dirs")
		return
	}
	var out *dirListing
	if h != nil {
		out, err = s.listRemoteDirs(r.Context(), h, r.URL.Query().Get("path"), queryBool(r, "hidden"))
	} else {
		out, err = s.listDirsNear(r.URL.Query().Get("path"), queryBool(r, "hidden"))
	}
	if err != nil {
		s.fail(w, err, "dirs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

type dirListing struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent"`
	Dirs   []dirEntry `json:"dirs"`
	Roots  []string   `json:"roots"`
	Home   string     `json:"home"`
	// Places: working folders outside home on another machine (/work, /srv/app).
	Places []string `json:"places,omitempty"`
	// Missing: the folder asked for does not exist; Path is the nearest one that does.
	Missing string `json:"missing,omitempty"`
}

// listDirs lists the sub-folders of p (home when empty) inside the roots.
func (s *Server) listDirs(p string, hidden bool) (*dirListing, error) {
	if p == "" {
		p = s.cfg.Home
	}
	clean, err := s.checkDir(p)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(clean)
	if err != nil {
		return nil, err
	}
	dirs := []dirEntry{}
	for _, e := range ents {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(clean, name)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(full); err == nil {
				isDir = fi.IsDir()
			}
		}
		if !isDir {
			continue
		}
		_, gerr := os.Stat(filepath.Join(full, ".git"))
		dirs = append(dirs, dirEntry{Name: name, Path: full, Git: gerr == nil})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(clean)
	if _, err := s.checkDir(parent); err != nil || parent == clean {
		parent = ""
	}
	return &dirListing{Path: clean, Parent: parent, Dirs: dirs, Roots: s.cfg.Roots, Home: s.cfg.Home}, nil
}

// ---------- workspaces ----------

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListWorkspaces(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "workspaces")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSaveWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	ws := &store.Workspace{UserID: me(r)}
	if id := idParam(r, "id"); id > 0 {
		cur, err := s.store.GetWorkspace(ctx, me(r), id)
		if err != nil {
			s.fail(w, err, "workspace")
			return
		}
		ws = cur
	}
	var in struct {
		Name      *string  `json:"name"`
		Path      *string  `json:"path"`
		Color     *string  `json:"color"`
		SortOrder *float64 `json:"sort_order"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Path != nil {
		p, err := s.checkDir(*in.Path)
		if err != nil {
			s.fail(w, err, "workspace")
			return
		}
		ws.Path = p
	}
	if in.Name != nil {
		ws.Name = strings.TrimSpace(*in.Name)
	}
	if ws.Name == "" && ws.Path != "" {
		ws.Name = filepath.Base(ws.Path)
	}
	if in.Color != nil {
		ws.Color = *in.Color
	}
	if in.SortOrder != nil {
		ws.SortOrder = *in.SortOrder
	}
	if ws.Path == "" {
		writeErr(w, http.StatusBadRequest, "pick a folder")
		return
	}
	created := ws.ID == 0
	if err := s.store.SaveWorkspace(ctx, ws); err != nil {
		s.fail(w, err, "save workspace")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, ws)
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteWorkspace(r.Context(), me(r), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete workspace")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- recipes ----------

func (s *Server) handleListRecipes(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRecipes(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "recipes")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSaveRecipe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := &store.Recipe{UserID: me(r), Pinned: true}
	if id := idParam(r, "id"); id > 0 {
		cur, err := s.store.GetRecipe(ctx, me(r), id)
		if err != nil {
			s.fail(w, err, "recipe")
			return
		}
		rc = cur
	}
	var in store.Recipe
	in.Pinned = rc.Pinned
	in.SortOrder = rc.SortOrder
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.ID, in.UserID, in.Runs = rc.ID, rc.UserID, rc.Runs
	in.Name, in.Prompt = strings.TrimSpace(in.Name), strings.TrimSpace(in.Prompt)
	if in.Name == "" || in.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "a recipe needs a name and a prompt")
		return
	}
	if err := s.checkAgentFields(&in.Provider, &in.Mode, &in.Cwd); err != nil {
		s.fail(w, err, "recipe")
		return
	}
	created := in.ID == 0
	if err := s.store.SaveRecipe(ctx, &in); err != nil {
		s.fail(w, err, "save recipe")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, in)
}

// checkAgentFields validates the optional provider/mode/folder of a recipe or schedule.
func (s *Server) checkAgentFields(provider, mode, cwd *string) error {
	switch *provider {
	case "", "claude", "codex":
	default:
		return badRequest("provider must be claude or codex")
	}
	if *mode != "" && *provider != "" && !validMode(*provider, *mode) {
		return badRequest("unknown mode " + *mode)
	}
	if strings.TrimSpace(*cwd) != "" {
		p, err := s.checkDir(*cwd)
		if err != nil {
			return err
		}
		*cwd = p
	}
	return nil
}

func (s *Server) handleDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRecipe(r.Context(), me(r), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete recipe")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_ -]+?)\s*\}\}`)

// fill replaces {{name}} placeholders; a missing value is an error.
func fill(prompt string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(prompt, func(m string) string {
		name := strings.TrimSpace(placeholder.FindStringSubmatch(m)[1])
		if v, ok := vars[name]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		missing = append(missing, name)
		return m
	})
	if len(missing) > 0 {
		return "", badRequest("fill in: " + strings.Join(missing, ", "))
	}
	return out, nil
}

func (s *Server) handleRunRecipe(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	rc, err := s.store.GetRecipe(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "recipe")
		return
	}
	var in struct {
		Vars map[string]string `json:"vars"`
	}
	_ = decode(r, &in)
	prompt, err := fill(rc.Prompt, in.Vars)
	if err != nil {
		s.fail(w, err, "run recipe")
		return
	}
	t, err := s.createThread(ctx, userFrom(ctx), newThread{Provider: rc.Provider, Model: rc.Model, Effort: rc.Effort, Mode: rc.Mode,
		Speed: optSpeed(rc.Speed), Cwd: rc.Cwd, Text: prompt, Title: rc.Name}, "recipe", strconv.FormatInt(rc.ID, 10))
	if err != nil && t == nil {
		s.fail(w, err, "run recipe")
		return
	}
	s.store.CountRecipeRun(ctx, rc.ID)
	writeJSON(w, http.StatusCreated, t)
}

// optSpeed: an empty saved speed means "my default", not "standard".
func optSpeed(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// handleRecipeSuggestions offers starter recipes for looking after a server.
func (s *Server) handleRecipeSuggestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, starterRecipes)
}

var starterRecipes = []store.Recipe{
	{Name: "Server health check", Icon: "🩺", Mode: "", Pinned: true, Prompt: "Check the health of this server and report like an on-call engineer. " +
		"Look at: uptime and load, memory and swap pressure, disk usage of every mounted filesystem (warn above 85%), failed systemd units (system and user), " +
		"Docker containers that are unhealthy, restarting or exited unexpectedly, and anything alarming in the last hour of the journal. " +
		"Do not change anything. Start with one line: everything fine, or what needs attention; then a short table."},
	{Name: "Check an app", Icon: "🔍", Pinned: true, Prompt: "Check the app {{app}} on this server. Find how it runs (Docker container, compose stack or systemd service), " +
		"whether it is up and healthy, its recent logs (errors and warnings in the last 24 hours), restarts, resource use, and whether its web endpoint answers. " +
		"Do not change anything without asking. Summarise what is wrong, if anything, and what you would do about it."},
	{Name: "Why is it slow?", Icon: "🐢", Pinned: false, Prompt: "The server feels slow. Find out why: CPU, memory, I/O wait, swap, the top processes and containers by CPU and memory, " +
		"network saturation, and anything stuck. Read-only. Tell me the most likely cause first."},
	{Name: "What is eating disk?", Icon: "💽", Pinned: false, Prompt: "Find what is using disk space on {{path}}. Show the biggest folders and files, Docker images/volumes/build cache that can be reclaimed, " +
		"and old logs. Do not delete anything; give me the exact commands to reclaim space, safest first."},
	{Name: "Container updates", Icon: "📦", Pinned: false, Prompt: "List the running Docker containers whose images have newer versions available or that run an image tag older than 60 days. " +
		"Do not pull or restart anything. Group by compose project and flag anything with a major version jump."},
	{Name: "Review recent errors", Icon: "🚨", Pinned: false, Prompt: "Go through the logs of the last {{hours}} hours (journal, Docker containers) and list recurring errors grouped by service, " +
		"with a count, the first and last time seen, and a one-line guess at the cause. Read-only."},
}
