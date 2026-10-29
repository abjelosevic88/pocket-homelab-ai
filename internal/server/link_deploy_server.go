package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The deploy server as a whole (Pocket Projects' server page): what runs on
// it, every branch of the repository that could go there instead (not only an
// item's), and the slot's recent deploys. Deploying one is the usual
// POST /link/deploys, with or without an item.

type serverBranch struct {
	Branch    string `json:"branch"`
	SHA       string `json:"sha"` // what a deploy would put there (the newer of local and origin)
	Local     bool   `json:"local"`
	Remote    bool   `json:"remote"`
	Ahead     int    `json:"ahead"`  // local commits origin lacks (-1 = not on origin yet)
	Behind    int    `json:"behind"` // origin commits the local branch lacks
	Path      string `json:"path,omitempty"`
	Subject   string `json:"subject"`
	Committed string `json:"committed_at"`
}

// wtServerBranches: $1 repo, $2 "1" = fetch origin first. Prints r= per ref
// (name|sha|date|upstream track|subject) and w= per checked-out branch.
const wtServerBranches = gitEnv + `cd -- "$1" || exit 3
if [ "$2" = 1 ]; then
  out=$(timeout 60 git fetch -q --prune origin 2>&1) || echo "fetch=$(printf '%s' "$out" | tail -n 2 | tr '\n' ' ')"
fi
git for-each-ref --sort=-committerdate --count=400 --format='r=%(refname)|%(objectname)|%(committerdate:iso-strict)|%(upstream:track,nobracket)|%(subject)' refs/heads refs/remotes/origin
git worktree list --porcelain | awk '/^worktree /{w=substr($0,10)} /^branch refs\/heads\//{print "w=" substr($0,19) "|" w}'
`

// wtCommitLine: $1 repo, $2 commit. Prints its full sha, date and subject.
const wtCommitLine = gitEnv + `cd -- "$1" || exit 3
git log -1 --format='c=%H|%cI|%s' "$2^{commit}" -- 2>/dev/null
`

func trackCount(track, word string) int {
	for _, p := range strings.Split(track, ",") {
		if f := strings.Fields(p); len(f) == 2 && f[0] == word {
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	return 0
}

func (s *Server) serverBranches(ctx context.Context, h *store.Host, repo string, fetch bool) ([]serverBranch, string) {
	f := "0"
	if fetch {
		f = "1"
	}
	timeout := 30 * time.Second
	if fetch {
		timeout = 90 * time.Second
	}
	out, errOut, err := shOn(ctx, h, timeout, wtServerBranches, repo, f)
	if err != nil {
		return []serverBranch{}, oneLine(firstNonEmpty(strings.TrimSpace(errOut), err.Error()), 300)
	}
	m := kv(out)
	by := map[string]*serverBranch{}
	var order []string
	for _, l := range splitLines(m["r"]) {
		p := strings.SplitN(l, "|", 5)
		if len(p) < 5 {
			continue
		}
		ref, name := p[0], ""
		local := strings.HasPrefix(ref, "refs/heads/")
		switch {
		case local:
			name = strings.TrimPrefix(ref, "refs/heads/")
		case strings.HasPrefix(ref, "refs/remotes/origin/"):
			name = strings.TrimPrefix(ref, "refs/remotes/origin/")
		}
		if name == "" || name == "HEAD" || !branchNameOK(name) {
			continue
		}
		b := by[name]
		if b == nil {
			// refs come newest first: the first one seen is what a deploy takes
			b = &serverBranch{Branch: name, SHA: p[1], Committed: p[2], Subject: p[4]}
			by[name] = b
			order = append(order, name)
		}
		if local {
			b.Local = true
			b.Ahead, b.Behind = trackCount(p[3], "ahead"), trackCount(p[3], "behind")
		} else {
			b.Remote = true
		}
	}
	for _, l := range splitLines(m["w"]) {
		if name, path, ok := strings.Cut(l, "|"); ok && by[name] != nil {
			by[name].Path = path
		}
	}
	list := make([]serverBranch, 0, len(order))
	for _, n := range order {
		b := by[n]
		if b.Local && !b.Remote {
			b.Ahead = -1
		}
		list = append(list, *b)
	}
	when := func(b serverBranch) time.Time { t, _ := time.Parse(time.RFC3339, b.Committed); return t }
	sort.SliceStable(list, func(i, j int) bool { return when(list[i]).After(when(list[j])) })
	return list, m["fetch"]
}

func (s *Server) handleLinkDeployServer(w http.ResponseWriter, r *http.Request) {
	u := s.linkUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	h, info, err := s.deployRepo(ctx, u, queryInt64(r, "host_id"), q.Get("cwd"))
	if err != nil {
		s.fail(w, err, "deploy")
		return
	}
	cfg, ok := s.deployConfig(ctx, info.ProjectID)
	if cfg.Run == nil {
		cfg.Run = []string{}
	}
	out := map[string]any{"project": info.ProjectID, "repo": info.Main, "default": info.Default, "configured": ok, "config": cfg,
		"branches": []serverBranch{}, "history": []*deployView{}}
	if th, _ := s.host(ctx, cfg.HostID); th != nil {
		out["host_name"] = th.Name
	}
	if !ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	branches, fetchErr := s.serverBranches(ctx, h, info.Main, q.Get("fetch") == "1")
	out["branches"] = branches
	if fetchErr != "" {
		out["fetch_error"] = fetchErr
	}
	latest, _ := s.store.LatestDeploy(ctx, info.ProjectID)
	good, _ := s.store.LastGoodDeploy(ctx, info.ProjectID)
	out["latest"], out["current"] = s.viewDeploy(ctx, u, latest), s.viewDeploy(ctx, u, good)
	out["live"] = s.deployLive(ctx, info.ProjectID, cfg, good)
	now := s.deployNow(ctx, info.ProjectID, cfg)
	if shaRe.MatchString(now.Now) {
		// the commit's own words, from the repository
		if c, _, err := shOn(ctx, h, 15*time.Second, wtCommitLine, info.Main, now.Now); err == nil {
			if p := strings.SplitN(kv(c)["c"], "|", 3); len(p) == 3 {
				out["now_commit"] = map[string]string{"sha": p[0], "committed_at": p[1], "subject": p[2]}
			}
		}
	}
	out["now"] = now
	if list, err := s.store.RecentDeploys(ctx, info.ProjectID, 25); err == nil {
		hist := make([]*deployView, 0, len(list))
		for _, d := range list {
			v := s.viewDeploy(ctx, u, d)
			cp := *v.Deploy
			cp.Log = "" // per deploy: GET /link/deploys/{id}
			v.Deploy = &cp
			hist = append(hist, v)
		}
		out["history"] = hist
	}
	writeJSON(w, http.StatusOK, out)
}
