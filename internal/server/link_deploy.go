package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Deploys: an app (Pocket Projects) puts an item's branch on the project's
// deploy target with one click. How a project deploys is its own business —
// a few shell commands, kept here per project (every checkout of the repo
// shares them) — and Pocket AI only:
//   - finds the item's branches (its conversations' worktrees and folders,
//     branches named after it) and pushes the chosen one when it has commits
//     origin lacks, so the target can fetch it;
//   - runs the commands on the target machine, detached (a dropped ssh link or
//     a Pocket AI restart doesn't stop a deploy halfway), one at a time per
//     project: the target is a single slot, the newest deploy is what runs;
//   - asks the target what runs now (optional "current" command), so a deploy
//     replaced since (a nightly reset, someone by hand) isn't shown as live.
// The commands get POCKET_BRANCH, POCKET_SHA, POCKET_REF, POCKET_SOURCE,
// POCKET_PROJECT, POCKET_DEPLOY_ID, POCKET_DEPLOYED_BY, POCKET_REF_URL and
// POCKET_REF_TITLE (the item's address and title, when the app sent them).
//
// A person may also set up a deploy of their own for a project (their own
// staging box): it wins over the shared one for them and is a slot of its
// own (deploys, the target's state folder, what runs there). Everyone else
// keeps using the shared one, which anyone allowed to run agents may deploy
// with — its commands are fixed by whoever saved it, and only someone who may
// use its machine can save or remove it.

type deployConfig struct {
	HostID  int64    `json:"host_id"`           // where it runs (0 = this server)
	Dir     string   `json:"dir"`               // folder to run in ("" = home)
	Run     []string `json:"run"`               // the commands, stopping at the first failure
	URL     string   `json:"url"`               // where the deployed app is
	Current string   `json:"current,omitempty"` // prints the branch or commit that runs now
	// Check: what a deploy of $POCKET_BRANCH would do, shown before the click
	// (one line each; a line starting with "!" is a warning: "! resets the database")
	Check string `json:"check,omitempty"`
}

func deployKey(project string) string { return "deploy:" + project }

// mineSlot is a person's own deploy slot of a project.
func mineSlot(project string, uid int64) string { return project + "@" + strconv.FormatInt(uid, 10) }

func (s *Server) deployConfig(ctx context.Context, slot string) (deployConfig, bool) {
	var c deployConfig
	raw := s.store.ProjectSettings(ctx, deployKey(slot))
	if raw == "" || json.Unmarshal([]byte(raw), &c) != nil || len(c.Run) == 0 {
		return deployConfig{}, false
	}
	return c, true
}

// deploySetup is the deploy a person gets for a project.
type deploySetup struct {
	cfg  deployConfig
	ok   bool
	slot string // what keys its deploys, caches and the target's state folder
	mine bool   // the person's own, not the project's shared one
}

func (s *Server) deployFor(ctx context.Context, project string, u *store.User) deploySetup {
	if c, ok := s.deployConfig(ctx, mineSlot(project, u.ID)); ok {
		return deploySetup{cfg: c, ok: true, slot: mineSlot(project, u.ID), mine: true}
	}
	c, ok := s.deployConfig(ctx, project)
	return deploySetup{cfg: c, ok: ok, slot: project}
}

// mayDeployOn: the person may set up deploy commands for this machine. Pocket
// AI's own server runs them outside any sandbox, so only people who are not
// sandboxed there (its owner) may; another machine, those who may use it.
func (s *Server) mayDeployOn(ctx context.Context, u *store.User, hostID int64) (*store.Host, error) {
	if hostID == 0 {
		if !u.MayRun() {
			return nil, errForbidden("an admin has to allow you to run agents first")
		}
		if s.jailed(ctx, u.ID) {
			return nil, errForbidden("deploys on Pocket AI's own server are set up by its owner; pick one of your servers")
		}
		return nil, nil
	}
	return s.checkHostUse(ctx, u, hostID)
}

// deployTarget is the machine a deploy runs on. The person's own needs their
// right to use it; the shared one runs on its machine for anyone who may run
// agents (only its fixed commands, with the branch they picked).
func (s *Server) deployTarget(ctx context.Context, u *store.User, d deploySetup) (*store.Host, error) {
	if d.mine {
		return s.mayDeployOn(ctx, u, d.cfg.HostID)
	}
	if !u.MayRun() {
		return nil, errForbidden("an admin has to allow you to run agents first")
	}
	return s.host(ctx, d.cfg.HostID)
}

// deployRepo resolves the caller's machine + folder to the repository.
func (s *Server) deployRepo(ctx context.Context, u *store.User, hostID int64, cwd string) (*store.Host, repoInfo, error) {
	h, err := s.checkHostUse(ctx, u, hostID)
	if err != nil {
		return nil, repoInfo{}, err
	}
	if h == nil {
		if cwd, err = s.ownCopy(ctx, u, cwd); err != nil {
			return nil, repoInfo{}, err
		}
		if cwd, err = s.checkDirFor(ctx, u, cwd); err != nil {
			return nil, repoInfo{}, err
		}
	}
	info, err := inspectRepo(ctx, h, cwd)
	if err == nil && !info.Repo {
		err = badRequest(cwd + " is not in a git repository")
	}
	if err == nil && info.ProjectID == "" {
		err = badRequest(info.Main + " has no git remote, so there is nothing a deploy could fetch")
	}
	return h, info, err
}

// ---------- the item's branches ----------

type deployBranch struct {
	Branch    string `json:"branch"`
	SHA       string `json:"sha"`
	Ahead     int    `json:"ahead"` // commits origin lacks (-1 = not on origin yet)
	Dirty     int    `json:"dirty"` // uncommitted files where it is checked out (not deployed)
	Path      string `json:"path,omitempty"`
	Subject   string `json:"subject"`
	Committed string `json:"committed_at"`
}

// wtDeployBranches: $1 repo, then b:<branch>, p:<pull request branch> (fetched
// when this repository has never seen it) or d:<folder> (its branch).
const wtDeployBranches = gitEnv + `cd -- "$1" || exit 3
shift
for a in "$@"; do
  case "$a" in d:*) b=$(git -C "${a#d:}" branch --show-current 2>/dev/null) ;; *) b=${a#?:} ;; esac
  [ -n "$b" ] || continue
  l=$(git rev-parse -q --verify "refs/heads/$b^{commit}" 2>/dev/null)
  o=$(git rev-parse -q --verify "refs/remotes/origin/$b^{commit}" 2>/dev/null)
  if [ -z "$l$o" ] && [ "${a%%:*}" = p ]; then
    timeout 15 git fetch -q origin "+refs/heads/$b:refs/remotes/origin/$b" 2>/dev/null
    o=$(git rev-parse -q --verify "refs/remotes/origin/$b^{commit}" 2>/dev/null)
  fi
  [ -n "$l$o" ] || continue
  ahead=0
  if [ -n "$l" ]; then if [ -n "$o" ]; then ahead=$(git rev-list --count "$o..$l"); else ahead=-1; fi; fi
  p=$(git worktree list --porcelain | awk -v r="branch refs/heads/$b" '/^worktree /{w=substr($0,10)} $0==r{print w; exit}')
  dirty=0
  [ -n "$p" ] && dirty=$(git -C "$p" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
  c=${l:-$o}
  echo "c=$b|$c|$ahead|$dirty|$(git log -1 --format=%cI "$c")|$p|$(git log -1 --format=%s "$c" | tr '|' '/')"
done
`

// itemBranches are the branches an item's work is on: its open pull requests'
// branches first (newest first), then its conversations' worktrees and
// folders, then branches named after it. A conversation that worked in the
// main folder says only what that folder has checked out now — often another
// item's branch — so that guess is left out once the item has pull requests.
func (s *Server) itemBranches(ctx context.Context, u *store.User, h *store.Host, info repoInfo, source, ref, named string, prs []string) []deployBranch {
	hostID := int64(0)
	if h != nil {
		hostID = h.ID
	}
	var args []string
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			args = append(args, a)
		}
	}
	for _, b := range prs {
		if b = strings.TrimPrefix(strings.TrimSpace(b), "origin/"); branchNameOK(b) {
			add("p:" + b)
		}
	}
	if drafts, err := s.store.LinkDrafts(ctx, u.ID, source, ref); err == nil {
		for _, d := range drafts {
			if d.ThreadID == nil {
				continue
			}
			t, err := s.store.GetThread(ctx, u.ID, *d.ThreadID)
			if err != nil || t.HostID != hostID {
				continue
			}
			if t.WorktreeID != 0 {
				if wt, err := s.store.GetWorktree(ctx, u.ID, t.WorktreeID); err == nil && wt.Status == "active" {
					add("b:" + wt.Branch)
				}
				continue
			}
			if len(prs) == 0 {
				add("d:" + t.Cwd)
			}
		}
	}
	add("b:" + named)
	re := regexp.MustCompile(`(^|[/_.-])` + regexp.QuoteMeta(strings.ToLower(ref)) + `($|[/_.-])`)
	if out, _, err := shOn(ctx, h, 20*time.Second, wtBranches, info.Main); err == nil {
		for _, b := range splitLines(kv(out)["b"]) {
			if re.MatchString(strings.ToLower(b)) {
				add("b:" + strings.TrimPrefix(b, "origin/"))
			}
		}
	}
	list := []deployBranch{}
	if len(args) == 0 {
		return list
	}
	out, _, err := shOn(ctx, h, 30*time.Second, wtDeployBranches, append([]string{info.Main}, args...)...)
	if err != nil {
		return list
	}
	base := map[string]bool{info.Default: true, "main": true, "master": true}
	got := map[string]bool{}
	for _, l := range splitLines(kv(out)["c"]) {
		f := strings.SplitN(l, "|", 7)
		if len(f) < 7 || got[f[0]] || base[f[0]] {
			continue
		}
		got[f[0]] = true
		ahead, _ := strconv.Atoi(f[2])
		dirty, _ := strconv.Atoi(f[3])
		list = append(list, deployBranch{Branch: f[0], SHA: f[1], Ahead: ahead, Dirty: dirty, Committed: f[4], Path: f[5], Subject: f[6]})
	}
	return list
}

// ---------- the slot ----------

type deployView struct {
	*store.Deploy
	Mine     bool   `json:"mine"`
	By       string `json:"by,omitempty"` // who deployed it
	HostName string `json:"host_name,omitempty"`
}

func (s *Server) viewDeploy(ctx context.Context, u *store.User, d *store.Deploy) *deployView {
	if d == nil {
		return nil
	}
	s.watchDeploy(d)
	v := &deployView{Deploy: d, Mine: d.UserID == u.ID}
	if !v.Mine {
		cp := *d
		cp.Log = "" // the output is the person's who deployed
		v.Deploy = &cp
	}
	if h, _ := s.host(ctx, d.HostID); h != nil {
		v.HostName = h.Name
	}
	if who, err := s.store.GetUser(ctx, d.UserID); err == nil {
		v.By = firstNonEmpty(who.Name, who.Username, who.Email)
	}
	return v
}

type deployLive struct {
	Checked bool   `json:"checked"` // a "current" command answered
	Now     string `json:"now"`     // what it said
	Live    bool   `json:"live"`    // the last good deploy is what runs
}

// deployNow is what the target says runs there: the first word of the
// current command's first line (a commit or a branch), a later line that
// names the branch, and a JSON object it may print with the details
// (branch, sha, subject, deployed_at, by, ref, ref_title, ref_url).
type deployNow struct {
	Checked bool           `json:"checked"`
	Now     string         `json:"now"`
	Branch  string         `json:"branch,omitempty"`
	Info    map[string]any `json:"info,omitempty"`
	Error   string         `json:"error,omitempty"`
}

var deployNowCache sync.Map // project + "|now" → deployNowCached

type deployNowCached struct {
	at time.Time
	v  deployNow
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (s *Server) deployNow(ctx context.Context, project string, cfg deployConfig) deployNow {
	if cfg.Current == "" {
		return deployNow{}
	}
	key := project + "|now"
	if c, ok := deployNowCache.Load(key); ok && time.Since(c.(deployNowCached).at) < 15*time.Second {
		return c.(deployNowCached).v
	}
	th, err := s.host(ctx, cfg.HostID)
	if err != nil {
		return deployNow{Error: err.Error()}
	}
	out, errOut, err := shOn(ctx, th, 20*time.Second, deployCdScript(cfg.Dir)+cfg.Current)
	if err != nil {
		return deployNow{Error: oneLine(firstNonEmpty(strings.TrimSpace(errOut), err.Error()), 300)}
	}
	v := parseDeployNow(out)
	deployNowCache.Store(key, deployNowCached{time.Now(), v})
	return v
}

// parseDeployNow reads the current command's output.
func parseDeployNow(out string) deployNow {
	v := deployNow{Checked: true}
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		var info map[string]any
		if json.Unmarshal([]byte(out[i:j+1]), &info) == nil {
			v.Info = info
			out = out[:i] + out[j+1:]
		}
	}
	for _, l := range splitLines(out) {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		switch {
		case v.Now == "":
			v.Now = f[0]
			if !shaRe.MatchString(v.Now) && branchNameOK(strings.TrimPrefix(v.Now, "origin/")) {
				v.Branch = strings.TrimPrefix(v.Now, "origin/")
			}
		case v.Branch == "" && len(f) == 1 && branchNameOK(strings.TrimPrefix(f[0], "origin/")):
			v.Branch = strings.TrimPrefix(f[0], "origin/")
		}
	}
	if b, _ := v.Info["branch"].(string); v.Branch == "" && b != "" {
		v.Branch = b
	}
	return v
}

func (s *Server) deployLive(ctx context.Context, project string, cfg deployConfig, good *store.Deploy) deployLive {
	if cfg.Current == "" || good == nil {
		return deployLive{Live: good != nil}
	}
	n := s.deployNow(ctx, project, cfg)
	if !n.Checked {
		return deployLive{Live: true}
	}
	now := n.Now
	return deployLive{Checked: true, Now: now, Live: now != "" && (now == good.Branch || now == "origin/"+good.Branch ||
		len(now) >= 7 && strings.HasPrefix(good.SHA, now) || len(good.SHA) >= 7 && strings.HasPrefix(now, good.SHA))}
}

// ---------- before the click ----------

type deployCheckLine struct {
	Text string `json:"text"`
	Warn bool   `json:"warn"`
}

type deployCheckResult struct {
	Lines []deployCheckLine `json:"lines"`
	Error string            `json:"error,omitempty"`
}

var deployCheckCache sync.Map // project|branch|latest deploy → deployCheckCached

type deployCheckCached struct {
	at time.Time
	v  deployCheckResult
}

// deployCheck runs the project's check command for a branch on the deploy
// machine (?check=<branch> on GET /link/deploy). Kept a minute, and until the
// next deploy changes what is on the machine.
func (s *Server) deployCheck(ctx context.Context, project string, cfg deployConfig, branch string) deployCheckResult {
	if cfg.Check == "" {
		return deployCheckResult{Lines: []deployCheckLine{}}
	}
	latest := int64(0)
	if d, _ := s.store.LatestDeploy(ctx, project); d != nil {
		latest = d.ID
		if d.Status == "running" {
			latest = -d.ID // the slot is changing: ask again when it is done
		}
	}
	key := project + "|" + branch + "|" + strconv.FormatInt(latest, 10)
	if c, ok := deployCheckCache.Load(key); ok && time.Since(c.(deployCheckCached).at) < time.Minute {
		return c.(deployCheckCached).v
	}
	th, err := s.host(ctx, cfg.HostID)
	if err != nil {
		return deployCheckResult{Lines: []deployCheckLine{}, Error: err.Error()}
	}
	script := "export POCKET_BRANCH=" + deployQuote(branch) + "\n" + deployCdScript(cfg.Dir) + cfg.Check + "\n"
	out, errOut, err := shOn(ctx, th, 90*time.Second, script)
	v := deployCheckResult{Lines: []deployCheckLine{}}
	for _, l := range splitLines(cleanLog(out)) {
		if l = strings.TrimSpace(l); l == "" || len(v.Lines) >= 12 {
			continue
		}
		warn := strings.HasPrefix(l, "!")
		v.Lines = append(v.Lines, deployCheckLine{Text: oneLine(strings.TrimSpace(strings.TrimPrefix(l, "!")), 400), Warn: warn})
	}
	if err != nil {
		v.Error = oneLine(firstNonEmpty(strings.TrimSpace(errOut), err.Error()), 300)
	} else {
		deployCheckCache.Store(key, deployCheckCached{time.Now(), v})
	}
	return v
}

// ---------- API ----------

func (s *Server) handleLinkDeploy(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	hostID := queryInt64(r, "host_id")
	h, info, err := s.deployRepo(ctx, u, hostID, q.Get("cwd"))
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	out := map[string]any{"project": info.ProjectID, "repo": info.Main, "configured": false, "branches": []deployBranch{}}
	ds := s.deployFor(ctx, info.ProjectID, u)
	s.describeDeploy(ctx, out, info.ProjectID, u, ds)
	if !ds.ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cfg := ds.cfg
	out["configured"] = true
	if b := strings.TrimPrefix(strings.TrimSpace(q.Get("check")), "origin/"); b != "" {
		if !branchNameOK(b) {
			writeErr(w, http.StatusBadRequest, "bad branch")
			return
		}
		writeJSON(w, http.StatusOK, s.deployCheck(ctx, ds.slot, cfg, b))
		return
	}
	source, ref := strings.ToLower(q.Get("source")), strings.TrimSpace(q.Get("ref"))
	if ref != "" {
		out["branches"] = s.itemBranches(ctx, u, h, info, source, ref, strings.TrimSpace(q.Get("branch")), q["pr_branch"])
	}
	latest, _ := s.store.LatestDeploy(ctx, ds.slot)
	good, _ := s.store.LastGoodDeploy(ctx, ds.slot)
	out["latest"], out["current"] = s.viewDeploy(ctx, u, latest), s.viewDeploy(ctx, u, good)
	out["live"] = s.deployLive(ctx, ds.slot, cfg, good)
	writeJSON(w, http.StatusOK, out)
}

// describeDeploy fills what every deploy answer says about the setup: the
// config in use, whether it is the person's own, and both setups (for the
// settings: "shared" and "own" are null when not set up).
func (s *Server) describeDeploy(ctx context.Context, out map[string]any, project string, u *store.User, ds deploySetup) {
	cfg := ds.cfg
	if cfg.Run == nil {
		cfg.Run = []string{}
	}
	out["config"], out["mine"] = cfg, ds.mine
	if th, _ := s.host(ctx, cfg.HostID); th != nil {
		out["host_name"] = th.Name
	}
	view := func(c deployConfig, ok bool) any {
		if !ok {
			return nil
		}
		v := map[string]any{"config": c}
		if th, _ := s.host(ctx, c.HostID); th != nil {
			v["host_name"] = th.Name
		}
		return v
	}
	out["shared"] = view(s.deployConfig(ctx, project))
	out["own"] = view(s.deployConfig(ctx, mineSlot(project, u.ID)))
}

func (s *Server) handleLinkDeployConfig(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	var in struct {
		HostID int64         `json:"host_id"` // the caller's machine + folder: which project
		Cwd    string        `json:"cwd"`
		Config *deployConfig `json:"config"` // null = no deploys for the project
		// Scope: "mine" = the caller's own deploy of the project (null config =
		// back to the shared one); "" = the project's shared one
		Scope string `json:"scope"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	_, info, err := s.deployRepo(ctx, u, in.HostID, in.Cwd)
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	mine := in.Scope == "mine"
	slot := info.ProjectID
	if mine {
		slot = mineSlot(info.ProjectID, u.ID)
	} else if old, ok := s.deployConfig(ctx, slot); ok {
		// the shared one is changed or removed only by someone who may use its machine
		if _, err := s.mayDeployOn(ctx, u, old.HostID); err != nil {
			writeErr(w, http.StatusForbidden, "only someone who may use the machine it deploys to can change the shared deploy")
			return
		}
	}
	data := ""
	if in.Config != nil {
		c := in.Config
		c.Run, c.Dir, c.URL, c.Current, c.Check = nonEmpty(c.Run), strings.TrimSpace(c.Dir), strings.TrimSpace(c.URL), strings.TrimSpace(c.Current), strings.TrimSpace(c.Check)
		if len(c.Run) > 0 {
			if _, err := s.mayDeployOn(ctx, u, c.HostID); err != nil {
				s.fail(w, err, "deploy")
				return
			}
			if c.URL != "" && !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
				writeErr(w, http.StatusBadRequest, "the address starts with https://")
				return
			}
			b, _ := json.Marshal(c)
			data = string(b)
		}
	}
	if err := s.store.SaveProjectSettings(ctx, deployKey(slot), data); err != nil {
		s.fail(w, err, "deploy")
		return
	}
	ds := s.deployFor(ctx, info.ProjectID, u)
	out := map[string]any{"project": info.ProjectID, "configured": ds.ok}
	s.describeDeploy(ctx, out, info.ProjectID, u, ds)
	writeJSON(w, http.StatusOK, out)
}

var deployStarting sync.Map // project → *sync.Mutex

func (s *Server) handleLinkStartDeploy(w http.ResponseWriter, r *http.Request) {
	u, app := s.linkCaller(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	var in struct {
		HostID int64  `json:"host_id"`
		Cwd    string `json:"cwd"`
		Source string `json:"source"`
		Ref    string `json:"ref"`
		Branch string `json:"branch"`
		// RefURL, RefLabel: the item's address and title, for the deploy commands
		// (POCKET_REF_URL, POCKET_REF_TITLE: a "what's deployed" badge can link back)
		RefURL   string `json:"ref_url"`
		RefLabel string `json:"ref_label"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Branch = strings.TrimPrefix(strings.TrimSpace(in.Branch), "origin/")
	if !branchNameOK(in.Branch) {
		writeErr(w, http.StatusBadRequest, "pick the branch to deploy")
		return
	}
	h, info, err := s.deployRepo(ctx, u, in.HostID, in.Cwd)
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	ds := s.deployFor(ctx, info.ProjectID, u)
	if !ds.ok {
		writeErr(w, http.StatusBadRequest, "this project has no deploy set up yet")
		return
	}
	cfg := ds.cfg
	th, err := s.deployTarget(ctx, u, ds)
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	mu, _ := deployStarting.LoadOrStore(ds.slot, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if d, _ := s.store.LatestDeploy(ctx, ds.slot); d != nil && d.Status == "running" {
		what := d.Branch
		if d.Ref != "" {
			what = d.Ref + " (" + d.Branch + ")"
		}
		writeErr(w, http.StatusConflict, what+" is being deployed right now; try again when it is done")
		return
	}
	// origin must have the branch as it is here, or the target deploys an older one
	out, errOut, err := shOn(ctx, h, 2*time.Minute, wtDeployPush, info.Main, in.Branch)
	if err != nil {
		s.fail(w, scriptErr(errOut, err), "deploy")
		return
	}
	m := kv(out)
	if m["err"] != "" {
		writeErr(w, http.StatusBadRequest, m["err"])
		return
	}
	d := &store.Deploy{UserID: u.ID, Project: ds.slot, HostID: cfg.HostID, Branch: in.Branch, SHA: m["sha"],
		Source: firstNonEmpty(strings.ToLower(in.Source), app), Ref: strings.TrimSpace(in.Ref), Status: "running", URL: cfg.URL}
	switch {
	case m["pushed"] == "new":
		d.Note = "Pushed " + in.Branch + " to origin (new there).\n"
	case m["pushed"] != "":
		d.Note = "Pushed " + m["pushed"] + " commit(s) of " + in.Branch + " to origin.\n"
	case m["behind"] != "":
		d.Note = "Deploying origin/" + in.Branch + ": it has " + m["behind"] + " commit(s) the local branch lacks.\n"
	}
	if err := s.store.CreateDeploy(ctx, d); err != nil {
		s.fail(w, err, "deploy")
		return
	}
	who := firstNonEmpty(u.Name, u.Username, u.Email)
	env := [][2]string{{"POCKET_DEPLOY", "1"}, {"POCKET_DEPLOY_ID", strconv.FormatInt(d.ID, 10)}, {"POCKET_BRANCH", d.Branch}, {"POCKET_SHA", d.SHA},
		{"POCKET_REF", d.Ref}, {"POCKET_SOURCE", d.Source}, {"POCKET_PROJECT", info.ProjectID}, {"POCKET_DEPLOYED_BY", who},
		{"POCKET_REF_URL", oneLine(strings.TrimSpace(in.RefURL), 500)}, {"POCKET_REF_TITLE", oneLine(strings.TrimSpace(in.RefLabel), 200)}}
	var run strings.Builder
	for _, e := range env {
		run.WriteString("export " + e[0] + "=" + deployQuote(e[1]) + "\n")
	}
	run.WriteString(deployCdScript(cfg.Dir) + "set -e\n" + strings.Join(cfg.Run, "\n") + "\n")
	sout, serr, err := shOn(ctx, th, 45*time.Second, wtDeployStart, deployDir(d.Project), strconv.FormatInt(d.ID, 10), run.String())
	sm := kv(sout)
	if err != nil || sm["started"] == "" {
		msg := firstNonEmpty(sm["busy"], strings.TrimSpace(serr), oneLine(sout, 300))
		if sm["busy"] != "" {
			msg = "a deploy started earlier is still running on that machine"
		} else if err != nil && msg == "" {
			msg = err.Error()
		}
		d.Status, d.Error, d.FinishedAt = "failed", "could not start: "+msg, store.Now()
		_ = s.store.UpdateDeploy(ctx, d)
		writeErr(w, http.StatusBadGateway, d.Error)
		return
	}
	s.log.Info("deploy started", "id", d.ID, "project", d.Project, "branch", d.Branch, "ref", d.Ref, "host", cfg.HostID)
	writeJSON(w, http.StatusOK, s.viewDeploy(ctx, u, d))
}

func (s *Server) handleLinkGetDeploy(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	d, err := s.store.GetDeploy(r.Context(), id)
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	writeJSON(w, http.StatusOK, s.viewDeploy(r.Context(), u, d))
}

// ---------- running it ----------

// wtDeployPush: $1 repo, $2 branch. Pushes the branch when origin lacks
// commits of it; prints sha= (what gets deployed), pushed=, behind= or err=.
const wtDeployPush = gitEnv + `cd -- "$1" || exit 3
b=$2
git remote get-url origin >/dev/null 2>&1 || { echo "err=the repository has no origin to deploy from"; exit 0; }
git fetch -q origin "+refs/heads/$b:refs/remotes/origin/$b" 2>/dev/null
l=$(git rev-parse -q --verify "refs/heads/$b^{commit}" 2>/dev/null)
o=$(git rev-parse -q --verify "refs/remotes/origin/$b^{commit}" 2>/dev/null)
[ -n "$l$o" ] || { echo "err=there is no branch $b"; exit 0; }
if [ -z "$l" ] || [ "$l" = "$o" ]; then echo "sha=$o"; exit 0; fi
if [ -n "$o" ] && git merge-base --is-ancestor "$l" "$o"; then
  echo "behind=$(git rev-list --count "$l..$o")"; echo "sha=$o"; exit 0
fi
if [ -n "$o" ] && ! git merge-base --is-ancestor "$o" "$l"; then
  echo "err=$b here and on origin have gone apart; pull or push it yourself first"; exit 0
fi
n=new
[ -n "$o" ] && n=$(git rev-list --count "$o..$l")
if out=$(git push -q origin "refs/heads/$b:refs/heads/$b" 2>&1); then
  echo "pushed=$n"; echo "sha=$l"
else
  echo "err=git push failed: $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
fi
`

// wtDeployStart: $1 slot folder name, $2 deploy id, $3 the script. Starts it
// detached — through systemd-run when the account has a user manager (it
// outlives ssh and a restart of whatever started it), else setsid/nohup.
const wtDeployStart = `d="$HOME/.pocket-ai/deploys/$1"
mkdir -p "$d" || exit 3
p=$(cat "$d/pid" 2>/dev/null)
if [ -n "$p" ] && [ ! -f "$d/exit" ] && kill -0 "$p" 2>/dev/null; then echo "busy=$(cat "$d/id" 2>/dev/null)"; exit 0; fi
printf '%s' "$3" > "$d/run.sh"
rm -f "$d/exit" "$d/pid"
: > "$d/log"
echo "$2" > "$d/id"
# the wrapper is a file: systemd-run would expand $ in an inline script
printf '%s\n' 'echo $$ > "$1/pid"; sh "$1/run.sh" >"$1/log" 2>&1; echo $? > "$1/exit.tmp"; mv "$1/exit.tmp" "$1/exit"' > "$d/wrap.sh"
if command -v systemd-run >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1 &&
   systemd-run --user --collect --quiet --unit="pocket-deploy-$2-$$" --setenv=PATH="$PATH" --setenv=HOME="$HOME" sh "$d/wrap.sh" "$d" >/dev/null 2>&1; then
  echo "started=systemd"
elif command -v setsid >/dev/null 2>&1; then
  setsid nohup sh "$d/wrap.sh" "$d" </dev/null >/dev/null 2>&1 &
  echo "started=setsid"
else
  nohup sh "$d/wrap.sh" "$d" </dev/null >/dev/null 2>&1 &
  echo "started=nohup"
fi
`

// wtDeployPoll: $1 slot folder name, $2 deploy id.
const wtDeployPoll = `d="$HOME/.pocket-ai/deploys/$1"
[ "$(cat "$d/id" 2>/dev/null)" = "$2" ] || { echo "gone=1"; exit 0; }
[ -f "$d/exit" ] && echo "exit=$(cat "$d/exit")"
p=$(cat "$d/pid" 2>/dev/null)
if [ -z "$p" ]; then echo "nopid=1"; elif kill -0 "$p" 2>/dev/null; then echo "alive=1"; fi
echo "@@log"
tail -c 60000 "$d/log" 2>/dev/null
`

func deployDir(project string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' {
			return r
		}
		return '-'
	}, project)
}

func deployCdScript(dir string) string {
	switch {
	case dir == "" || dir == "~":
		return "cd \"$HOME\" || exit 3\n"
	case strings.HasPrefix(dir, "~/"):
		return "cd \"$HOME\"/" + deployQuote(dir[2:]) + " || exit 3\n"
	}
	return "cd -- " + deployQuote(dir) + " || exit 3\n"
}

func deployQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

// cleanLog strips colours and keeps the last state of rewritten lines.
func cleanLog(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); j >= 0 {
			lines[i] = l[j+1:]
		}
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

var deployWatching sync.Map // deploy id → true

// watchDeploy follows a running deploy until it ends (one watcher each;
// started again lazily after a restart, when someone looks at it).
func (s *Server) watchDeploy(d *store.Deploy) {
	if d.Status != "running" {
		return
	}
	if _, dup := deployWatching.LoadOrStore(d.ID, true); dup {
		return
	}
	go func() {
		defer deployWatching.Delete(d.ID)
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("deploy watcher crashed", "id", d.ID, "panic", p)
			}
		}()
		ctx := s.rt.ctx
		h, err := s.host(ctx, d.HostID)
		if err != nil {
			d.Status, d.Error, d.FinishedAt = "failed", err.Error(), store.Now()
			_ = s.store.UpdateDeploy(ctx, d)
			return
		}
		started, _ := time.Parse(time.RFC3339, d.StartedAt)
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			out, _, err := shOn(ctx, h, 30*time.Second, wtDeployPoll, deployDir(d.Project), strconv.FormatInt(d.ID, 10))
			if err != nil {
				if failures++; failures > 200 { // ~20 minutes without reaching the machine
					d.Status, d.Error, d.FinishedAt = "failed", "lost the machine while deploying: "+err.Error(), store.Now()
					_ = s.store.UpdateDeploy(ctx, d)
					return
				}
				continue
			}
			failures = 0
			meta, logText, _ := strings.Cut(out, "@@log\n")
			m := kv(meta)
			body := cleanLog(logText)
			changed := body != d.Log
			d.Log = body
			switch {
			case m["gone"] != "":
				d.Status, d.Error = "failed", "another deploy took the machine's slot"
			case m["exit"] != "":
				if strings.TrimSpace(m["exit"]) == "0" {
					d.Status = "ok"
					if u := lastURL(logText); u != "" {
						d.URL = u
					}
				} else {
					d.Status, d.Error = "failed", "the deploy command failed (exit "+strings.TrimSpace(m["exit"])+")"
				}
			case m["alive"] == "" && (m["nopid"] == "" || time.Since(started) > time.Minute):
				d.Status, d.Error = "failed", "the deploy stopped without finishing (was the machine restarted?)"
			case time.Since(started) > 2*time.Hour:
				d.Status, d.Error = "failed", "still running after 2 hours; Pocket AI stopped watching it"
			}
			if d.Status != "running" {
				d.FinishedAt = store.Now()
				_ = s.store.UpdateDeploy(ctx, d)
				deployNowCache.Delete(d.Project + "|now")
				s.log.Info("deploy finished", "id", d.ID, "status", d.Status, "error", d.Error)
				return
			}
			if changed {
				_ = s.store.UpdateDeploy(ctx, d)
			}
		}
	}()
}

var urlLineRe = regexp.MustCompile(`^\s*(https?://\S+)\s*$`)

// lastURL: a deploy that ends by printing an address says where it is.
func lastURL(log string) string {
	lines := strings.Split(strings.TrimSpace(cleanLog(log)), "\n")
	if len(lines) == 0 {
		return ""
	}
	if m := urlLineRe.FindStringSubmatch(lines[len(lines)-1]); m != nil {
		return m[1]
	}
	return ""
}
