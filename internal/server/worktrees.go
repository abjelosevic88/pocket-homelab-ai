package server

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Worktrees: a thread can work in its own git worktree — an isolated
// checkout on its own branch — so several agents can change one repository
// at once and every task can be reviewed, merged or thrown away on its own.
// Like Paseo, with these differences:
//   - folder and branch carry the same name, taken from the prompt (no rename later);
//   - archiving never loses work: everything uncommitted is saved under
//     refs/pocket-ai/archive/<id> first, and Restore brings it back;
//   - merging into base is computed with git merge-tree and lands by
//     fast-forward where base is checked out, so the main checkout's own
//     uncommitted work is never touched (git refuses rather than overwrites);
//   - "merged" is detected locally too (ancestry and patch-equivalence), and
//     through the pull request when there is one;
//   - setup lives in Pocket AI per repository (paseo.json / .pocket-ai.json
//     in the repo are read as well), with a file-copy list for .env files;
//   - other worktrees of the same repository (made by hand or by Paseo) can
//     be adopted or cleaned up, with the same backup.
// The git side is worktree_git.go; it runs the same here and on other machines.

// ---------- per-repository settings ----------

type wtConfig struct {
	Setup    []string `json:"setup"`
	Teardown []string `json:"teardown"`
	Copy     []string `json:"copy"`
	Base     string   `json:"base,omitempty"`
}

// defaultCopy: files that are usually ignored but needed to run the app.
// Only files git does not track are copied (tracked ones are there already).
var defaultCopy = []string{".env", ".env.*", "**/.env", "**/.env.*", "paseo.json", ".pocket-ai.json"}

// stringList reads "cmd" or ["cmd", …].
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		if strings.TrimSpace(one) != "" {
			*l = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

const wtReadConfig = `cd -- "$1" || exit 3
echo "@@origin=$(git remote get-url origin 2>/dev/null)"
for f in .pocket-ai.json paseo.json; do
  [ -f "$f" ] && { echo "@@file=$f"; head -c 200000 "$f"; exit 0; }
done
exit 0
`

type wtConfigView struct {
	File       string    `json:"file"`        // paseo.json / .pocket-ai.json in the repo, if any
	FileConfig wtConfig  `json:"file_config"` // what it says
	Saved      *wtConfig `json:"saved"`       // what Pocket AI keeps for the repo (wins)
	// Project: who the repo is on any machine (github.com/owner/repo, from
	// origin; "" = no remote). Scope: where Saved comes from — "project"
	// (every machine), "machine" (this checkout only, wins), "" (none).
	Project     string   `json:"project"`
	Scope       string   `json:"scope"`
	Effective   wtConfig `json:"effective"`
	DefaultCopy []string `json:"default_copy"`
}

func (s *Server) repoConfig(ctx context.Context, h *store.Host, hostID int64, repo string) wtConfigView {
	v := wtConfigView{DefaultCopy: defaultCopy}
	out, _, err := shOn(ctx, h, 20*time.Second, wtReadConfig, repo)
	if first, rest, _ := strings.Cut(out, "\n"); err == nil && strings.HasPrefix(first, "@@origin=") {
		v.Project = gitProjectID(strings.TrimPrefix(first, "@@origin="))
		out = rest
	}
	if err == nil && strings.HasPrefix(out, "@@file=") {
		first, body, _ := strings.Cut(out, "\n")
		v.File = strings.TrimPrefix(first, "@@file=")
		var f struct {
			Worktree struct {
				Setup    stringList `json:"setup"`
				Teardown stringList `json:"teardown"`
				Copy     stringList `json:"copy"`
				Base     string     `json:"base"`
			} `json:"worktree"`
		}
		if json.Unmarshal([]byte(body), &f) == nil {
			v.FileConfig = wtConfig{Setup: f.Worktree.Setup, Teardown: f.Worktree.Teardown, Copy: f.Worktree.Copy, Base: f.Worktree.Base}
		}
	}
	// This machine's own setup wins over the project's (every machine's).
	for _, src := range []struct{ scope, raw string }{
		{"machine", s.store.RepoSettings(ctx, hostID, repo)},
		{"project", s.store.ProjectSettings(ctx, v.Project)},
	} {
		var saved wtConfig
		if src.raw != "" && json.Unmarshal([]byte(src.raw), &saved) == nil {
			v.Saved, v.Scope = &saved, src.scope
			break
		}
	}
	v.Effective = v.FileConfig
	if v.Saved != nil {
		if v.Saved.Setup != nil {
			v.Effective.Setup = v.Saved.Setup
		}
		if v.Saved.Teardown != nil {
			v.Effective.Teardown = v.Saved.Teardown
		}
		if v.Saved.Copy != nil {
			v.Effective.Copy = v.Saved.Copy
		}
		if v.Saved.Base != "" {
			v.Effective.Base = v.Saved.Base
		}
	}
	v.Effective.Setup = nonEmpty(v.Effective.Setup)
	v.Effective.Teardown = nonEmpty(v.Effective.Teardown)
	v.Effective.Copy = nonEmpty(append(append([]string{}, defaultCopy...), v.Effective.Copy...))
	return v
}

func nonEmpty(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------- where and what name ----------

// worktreesDir is where a repository's worktrees go: one folder per repo
// (readable name + a short hash, so two "app" repos don't share one).
func (s *Server) worktreesDir(h *store.Host, repo string) string {
	sum := sha1.Sum([]byte(repo))
	name := path.Base(repo) + "-" + hex.EncodeToString(sum[:])[:6]
	if h != nil {
		return "~/.pocket-ai/worktrees/" + name
	}
	root := os.Getenv("WORKTREES_DIR")
	if root == "" {
		root = filepath.Join(s.cfg.Home, ".pocket-ai", "worktrees")
	}
	return filepath.Join(root, name)
}

var slugStop = map[string]bool{"the": true, "a": true, "an": true, "to": true, "and": true, "or": true, "of": true, "in": true, "on": true,
	"for": true, "with": true, "is": true, "it": true, "this": true, "that": true, "my": true, "me": true, "please": true, "can": true,
	"you": true, "could": true, "we": true, "our": true, "be": true, "i": true, "so": true, "just": true, "do": true, "does": true,
	"there": true, "what": true, "how": true, "why": true, "are": true, "should": true, "would": true, "let": true, "lets": true,
	"some": true, "also": true, "into": true, "from": true, "at": true, "by": true, "as": true, "if": true, "all": true, "any": true}

// slugify makes a folder and branch name from a prompt: up to five
// meaningful words, "task" when nothing is left.
func slugify(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !(r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r))) })
	var keep []string
	n := 0
	for _, w := range words {
		if slugStop[w] || len(w) < 2 && !unicode.IsDigit(rune(w[0])) {
			continue
		}
		if n+len(w) > 40 {
			break
		}
		keep = append(keep, w)
		n += len(w) + 1
		if len(keep) == 5 {
			break
		}
	}
	if len(keep) == 0 {
		return "task"
	}
	return strings.Join(keep, "-")
}

var branchNameOK = func(b string) bool {
	return b != "" && !strings.HasPrefix(b, "-") && !strings.ContainsAny(b, " ~^:?*[\\") && !strings.Contains(b, "..") && !strings.HasSuffix(b, ".lock")
}

// ---------- making one ----------

type newWorktree struct {
	Mode   string `json:"mode"`   // new (default) | branch | pr | existing
	Base   string `json:"base"`   // new: branch to start from (default: the repo's)
	Branch string `json:"branch"` // branch: the branch to check out
	PR     int    `json:"pr"`     // pr: the pull request number
	ID     int64  `json:"id"`     // existing: a worktree of the person's
	Name   string `json:"name"`   // new: the name (default: from the prompt)
}

// errInUse: the branch is checked out already, in Path.
type errInUse struct{ Path string }

func (e errInUse) Error() string {
	return "that branch is already checked out in " + e.Path + " — work there instead (start the thread in that folder)"
}

// createWorktree makes a worktree for a new thread started in cwd. It
// returns the worktree and the folder inside it matching cwd.
func (s *Server) createWorktree(ctx context.Context, u *store.User, h *store.Host, cwd string, spec newWorktree, text string) (*store.Worktree, string, error) {
	info, err := inspectRepo(ctx, h, cwd)
	if err != nil {
		return nil, "", err
	}
	if !info.Repo {
		return nil, "", badRequest(cwd + " is not in a git repository, so it can't get a worktree")
	}
	hostID := int64(0)
	if h != nil {
		hostID = h.ID
	}
	cfg := s.repoConfig(ctx, h, hostID, info.Main).Effective
	base := firstNonEmpty(strings.TrimSpace(spec.Base), cfg.Base, info.MainBranch, info.Default, "main")
	mode, arg, name := "new", "", slugify(firstNonEmpty(spec.Name, text))
	switch spec.Mode {
	case "", "new":
		if spec.Name != "" {
			name = strings.Trim(strings.ReplaceAll(strings.TrimSpace(spec.Name), " ", "-"), "/")
			if !branchNameOK(name) {
				return nil, "", badRequest("“" + spec.Name + "” can't be a branch name")
			}
		}
	case "branch":
		mode, arg = "branch", strings.TrimPrefix(strings.TrimSpace(spec.Branch), "origin/")
		if !branchNameOK(arg) {
			return nil, "", badRequest("pick a branch")
		}
		name = strings.ReplaceAll(arg, "/", "-")
	case "pr":
		if spec.PR <= 0 {
			return nil, "", badRequest("enter the pull request number")
		}
		mode, arg, name = "pr", strconv.Itoa(spec.PR), "pr-"+strconv.Itoa(spec.PR)
	default:
		return nil, "", badRequest("unknown worktree mode " + spec.Mode)
	}
	if mode == "new" && !branchNameOK(base) {
		return nil, "", badRequest("pick the branch to start from")
	}
	out, errOut, err := shOn(ctx, h, 2*time.Minute, wtAdd, info.Main, s.worktreesDir(h, info.Main), name, base, mode, arg)
	if err != nil {
		return nil, "", scriptErr(errOut, err)
	}
	m := kv(out)
	if m["inuse"] != "" {
		return nil, "", errInUse{m["inuse"]}
	}
	if m["path"] == "" {
		return nil, "", badRequest("git did not make the worktree: " + oneLine(firstNonEmpty(errOut, out), 300))
	}
	w := &store.Worktree{UserID: u.ID, HostID: hostID, Repo: info.Main, Path: m["path"], Branch: m["branch"], Base: firstNonEmpty(m["base"], base),
		BaseSHA: m["sha"], Title: titleFrom(text)}
	if len(cfg.Setup) > 0 {
		w.Setup = "running"
	}
	if err := s.store.CreateWorktree(ctx, w); err != nil {
		return nil, "", err
	}
	s.publishWorktrees(u.ID)
	rel := strings.TrimSuffix(info.Rel, "/")
	if rel != "" && !strings.HasPrefix(cwd, info.Top) {
		rel = ""
	}
	return w, rel, nil
}

// ---------- setup ----------

// holds: messages to threads whose worktree is still being set up; they go
// out (in order) once setup finished, or when the person continues without it.
type wtHolds struct {
	mu sync.Mutex
	m  map[string][]func()
}

func (s *Server) holds() *wtHolds {
	s.wtOnce.Do(func() { s.wtHold = &wtHolds{m: map[string][]func(){}} })
	return s.wtHold
}

func (hs *wtHolds) hold(threadID string) {
	hs.mu.Lock()
	if _, ok := hs.m[threadID]; !ok {
		hs.m[threadID] = []func(){}
	}
	hs.mu.Unlock()
}

// add keeps send for later if the thread is on hold.
func (hs *wtHolds) add(threadID string, send func()) bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	q, ok := hs.m[threadID]
	if ok {
		hs.m[threadID] = append(q, send)
	}
	return ok
}

func (hs *wtHolds) held(threadID string) bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	_, ok := hs.m[threadID]
	return ok
}

func (hs *wtHolds) release(threadID string) {
	hs.mu.Lock()
	q := hs.m[threadID]
	delete(hs.m, threadID)
	hs.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// sendHeld hands a message that waited for setup to the agent.
func (s *Server) sendHeld(threadID, key string, in agent.Input) {
	t, err := s.store.GetThreadAny(s.rt.ctx, threadID)
	if err != nil {
		return
	}
	if _, err := s.rt.send(t, key, in); err != nil {
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		s.rt.setStatus(t.ID, "error", err.Error())
	}
}

func wtEnv(w *store.Worktree, port int) []string {
	env := []string{
		"POCKET_AI=1", "POCKET_WORKTREE_PATH=" + w.Path, "POCKET_ROOT_PATH=" + w.Repo, "POCKET_BRANCH=" + w.Branch, "POCKET_BASE=" + w.Base,
		// Paseo's names, so its setup scripts run unchanged
		"PASEO_WORKTREE_PATH=" + w.Path, "PASEO_ROOT_PATH=" + w.Repo, "PASEO_SOURCE_CHECKOUT_PATH=" + w.Repo, "PASEO_BRANCH_NAME=" + w.Branch,
	}
	if port > 0 {
		env = append(env, "POCKET_WORKTREE_PORT="+strconv.Itoa(port), "PASEO_WORKTREE_PORT="+strconv.Itoa(port))
	}
	return env
}

// runCommands runs shell commands in the worktree one after another (stop
// at the first failure), calling out with the output so far.
func (s *Server) runCommands(ctx context.Context, h *store.Host, w *store.Worktree, cmds []string, timeout time.Duration, out func(string)) error {
	var remote *agent.Remote
	if h != nil {
		remote = remoteOf(h)
	}
	script := "set -e\n" + strings.Join(cmds, "\n")
	cmd := agent.Command(remote, w.Path, wtEnv(w, 20000+rand.Intn(30000)), "sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	var buf []byte
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(pr)
		chunk := make([]byte, 8192)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf = append(buf, chunk[:n]...)
				if len(buf) > 64<<10 {
					buf = buf[len(buf)-64<<10:]
				}
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); _ = pw.Close() }()
	deadline := time.After(timeout)
	var err error
loop:
	for {
		select {
		case err = <-exited:
			break loop
		case <-tick.C:
			mu.Lock()
			out(string(buf))
			mu.Unlock()
		case <-deadline:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = fmt.Errorf("stopped after %s", timeout)
			<-exited
			break loop
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = ctx.Err()
			<-exited
			break loop
		}
	}
	<-done
	mu.Lock()
	out(string(buf))
	mu.Unlock()
	return err
}

// runSetup copies the files and runs the setup commands, shown in the
// thread as one tool row. Messages held for threadID go out when it is done.
func (s *Server) runSetup(w *store.Worktree, threadID string, copyFiles bool) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("worktree setup crashed", "worktree", w.ID, "panic", p)
			w.Setup = "failed"
			_ = s.store.UpdateWorktree(s.rt.ctx, w)
			if threadID != "" {
				s.rt.setStatus(threadID, "idle", "")
			}
		}
	}()
	ctx := s.rt.ctx
	h, err := s.host(ctx, w.HostID)
	if err != nil {
		return
	}
	cfg := s.repoConfig(ctx, h, w.HostID, w.Repo).Effective
	var lines []string
	if copyFiles {
		if out, _, err := shOn(ctx, h, 2*time.Minute, wtCopy, append([]string{w.Repo, w.Path}, cfg.Copy...)...); err == nil {
			for _, f := range strings.Split(kv(out)["copied"], "\n") {
				if f != "" {
					lines = append(lines, f)
				}
			}
		}
	}
	key := fmt.Sprintf("wt-setup-%d-%d", w.ID, time.Now().UnixNano())
	head := ""
	if len(lines) > 0 {
		head = "Copied from " + w.Repo + ": " + strings.Join(lines, ", ") + "\n\n"
	}
	if len(cfg.Setup) == 0 {
		if head != "" && threadID != "" {
			s.rt.saveItem(threadID, agent.Item{Key: key, Kind: "notice", Level: "info", Text: "New worktree " + w.Branch + ". " + strings.TrimSpace(head)})
		}
		w.Setup = ""
		_ = s.store.UpdateWorktree(ctx, w)
		s.publishWorktrees(w.UserID)
		if threadID != "" {
			s.holds().release(threadID)
		}
		return
	}
	in, _ := json.Marshal(map[string]string{"command": strings.Join(cfg.Setup, "\n")})
	tool := &agent.Tool{Name: "Worktree setup", Kind: "bash", Title: "Setting up " + w.Branch, Input: in, Status: "running"}
	last := ""
	show := func(out string) {
		last = out
		if threadID == "" {
			return
		}
		tool.Output = head + out
		s.rt.saveItem(threadID, agent.Item{Key: key, Kind: "tool", Tool: tool})
	}
	if threadID != "" {
		s.rt.setStatus(threadID, "running", "")
	}
	w.Setup = "running"
	_ = s.store.UpdateWorktree(ctx, w)
	s.publishWorktrees(w.UserID)
	err = s.runCommands(ctx, h, w, cfg.Setup, 30*time.Minute, show)
	if err != nil {
		tool.Status = "error"
		code := 1
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		tool.ExitCode = &code
		show(last + "\n" + err.Error())
		w.Setup = "failed"
	} else {
		tool.Status = "done"
		zero := 0
		tool.ExitCode = &zero
		show(last)
		w.Setup = "ok"
	}
	_ = s.store.UpdateWorktree(ctx, w)
	s.publishWorktrees(w.UserID)
	if threadID == "" {
		return
	}
	if w.Setup == "failed" && s.holds().held(threadID) {
		s.rt.saveItem(threadID, agent.Item{Key: key + "-failed", Kind: "notice", Level: "warn",
			Text: "The worktree setup failed, so your message is waiting. Fix the setup and run it again from the worktree panel, or continue without it."})
		s.rt.setStatus(threadID, "idle", "")
		return
	}
	s.rt.setStatus(threadID, "idle", "")
	s.holds().release(threadID)
}

// ---------- views ----------

type wtThread struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Provider  string `json:"provider"`
	UpdatedAt string `json:"updated_at"`
	Live      bool   `json:"live"`
}

type worktreeView struct {
	*store.Worktree
	HostName string        `json:"host_name,omitempty"`
	Threads  []wtThread    `json:"threads"`
	State    *wtStatusInfo `json:"state,omitempty"`
}

func (s *Server) worktreeView(ctx context.Context, w *store.Worktree, withState bool) worktreeView {
	v := worktreeView{Worktree: w, Threads: []wtThread{}}
	h, herr := s.host(ctx, w.HostID)
	if h != nil {
		v.HostName = h.Name
	}
	for _, id := range s.store.WorktreeThreads(ctx, w.ID) {
		if t, err := s.store.GetThreadAny(ctx, id); err == nil {
			s.rt.decorate(t)
			v.Threads = append(v.Threads, wtThread{ID: t.ID, Title: t.Title, Status: t.Status, Provider: t.Provider, UpdatedAt: t.UpdatedAt, Live: t.Live})
		}
	}
	if withState {
		var st wtStatusInfo
		if herr != nil {
			st = wtStatusInfo{Error: herr.Error(), Commits: []wtCommit{}, Conflicts: []string{}}
		} else {
			st = worktreeStatus(ctx, h, w)
		}
		v.State = &st
		if st.Merged && w.MergedAt == "" && w.Status == "active" {
			w.MergedAt = store.Now()
			_ = s.store.UpdateWorktree(ctx, w)
		}
	}
	return v
}

func (s *Server) publishWorktrees(uid int64) {
	s.rt.toUser(uid, sseEvent{Name: "worktrees", Data: map[string]any{}})
}

// ---------- API ----------

func (s *Server) worktreeRoutes(r chi.Router) {
	r.Get("/worktrees", s.handleListWorktrees)
	r.Get("/worktrees/config", s.handleWorktreeConfig)
	r.Put("/worktrees/config", s.handleSaveWorktreeConfig)
	r.Get("/worktrees/others", s.handleOtherWorktrees)
	r.Post("/worktrees/adopt", s.handleAdoptWorktree)
	r.Post("/worktrees/others/remove", s.handleRemoveOtherWorktree)
	r.Post("/worktrees/cleanup", s.handleCleanupWorktrees)
	r.Get("/worktrees/{id}", s.handleGetWorktree)
	r.Get("/worktrees/{id}/diff", s.handleWorktreeDiff)
	r.Post("/worktrees/{id}/commit", s.handleWorktreeCommit)
	r.Post("/worktrees/{id}/update", s.handleWorktreeUpdate)
	r.Post("/worktrees/{id}/merge", s.handleWorktreeMerge)
	r.Post("/worktrees/{id}/push", s.handleWorktreePush)
	r.Post("/worktrees/{id}/setup", s.handleWorktreeSetup)
	r.Post("/worktrees/{id}/archive", s.handleWorktreeArchive)
	r.Post("/worktrees/{id}/restore", s.handleWorktreeRestore)
	r.Delete("/worktrees/{id}", s.handleForgetWorktree)
	r.Put("/worktrees/{id}/group", s.handlePlaceWorktree)
	r.Get("/worktree-groups", s.handleWorktreeGroups)
	r.Post("/worktree-groups", s.handleCreateWorktreeGroup)
	r.Put("/worktree-groups/order", s.handleOrderWorktreeGroups)
	r.Patch("/worktree-groups/{id}", s.handleUpdateWorktreeGroup)
	r.Delete("/worktree-groups/{id}", s.handleDeleteWorktreeGroup)
}

// worktree loads the person's worktree from the URL, with its machine.
func (s *Server) worktree(w http.ResponseWriter, r *http.Request) (*store.Worktree, *store.Host) {
	if !s.requireRun(w, r) {
		return nil, nil
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	wt, err := s.store.GetWorktree(r.Context(), me(r), id)
	if err != nil {
		s.fail(w, err, "worktree")
		return nil, nil
	}
	h, err := s.host(r.Context(), wt.HostID)
	if err != nil {
		s.fail(w, err, "worktree")
		return nil, nil
	}
	return wt, h
}

func (s *Server) handleListWorktrees(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	list, err := s.store.ListWorktrees(r.Context(), me(r), r.URL.Query().Get("status"))
	if err != nil {
		s.fail(w, err, "worktrees")
		return
	}
	withState := queryBool(r, "state")
	out := make([]worktreeView, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, wt := range list {
		wg.Add(1)
		go func(i int, wt *store.Worktree) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.worktreeView(r.Context(), wt, withState && wt.Status == "active")
		}(i, wt)
	}
	wg.Wait()
	if queryBool(r, "sizes") {
		s.addSizes(r.Context(), out)
	}
	writeJSON(w, http.StatusOK, out)
}

// addSizes measures the active worktrees' folders (du), per machine.
func (s *Server) addSizes(ctx context.Context, list []worktreeView) {
	byHost := map[int64][]int{}
	for i, v := range list {
		if v.Status == "active" {
			byHost[v.HostID] = append(byHost[v.HostID], i)
		}
	}
	for hid, idx := range byHost {
		h, err := s.host(ctx, hid)
		if err != nil {
			continue
		}
		paths := make([]string, len(idx))
		for j, i := range idx {
			paths[j] = list[i].Path
		}
		out, _, _ := shOn(ctx, h, time.Minute, wtSize, paths...)
		sizes := map[string]int64{}
		for _, line := range strings.Split(kv(out)["size"], "\n") {
			if kb, p, ok := strings.Cut(line, "\t"); ok {
				sizes[p], _ = strconv.ParseInt(kb, 10, 64)
			}
		}
		for _, i := range idx {
			if list[i].State == nil {
				list[i].State = &wtStatusInfo{Commits: []wtCommit{}, Conflicts: []string{}}
			}
			list[i].State.SizeKB = sizes[list[i].Path]
		}
	}
}

func (s *Server) handleGetWorktree(w http.ResponseWriter, r *http.Request) {
	wt, _ := s.worktree(w, r)
	if wt == nil {
		return
	}
	writeJSON(w, http.StatusOK, s.worktreeView(r.Context(), wt, true))
}

func (s *Server) handleWorktreeDiff(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	if wt.Status != "active" {
		writeErr(w, 409, "this worktree is archived; restore it to see its changes")
		return
	}
	diff, err := worktreeDiff(r.Context(), h, wt)
	if err != nil {
		s.fail(w, err, "diff")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, diff)
}

// busyThreads lists the worktree's threads whose agent is working.
func (s *Server) busyThreads(ctx context.Context, wt *store.Worktree) []string {
	var busy []string
	for _, id := range s.store.WorktreeThreads(ctx, wt.ID) {
		if t, err := s.store.GetThreadAny(ctx, id); err == nil {
			s.rt.decorate(t)
			if t.Status == "running" || t.Status == "waiting" || t.Queued > 0 {
				busy = append(busy, t.Title)
			}
		}
	}
	return busy
}

func (s *Server) handleWorktreeCommit(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		Message string `json:"message"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Message) == "" {
		writeErr(w, 400, "write a commit message")
		return
	}
	if _, errOut, err := shOn(r.Context(), h, time.Minute, wtCommitAll, wt.Path, strings.TrimSpace(in.Message)); err != nil {
		s.fail(w, scriptErr(errOut, err), "commit")
		return
	}
	s.publishWorktrees(wt.UserID)
	writeJSON(w, http.StatusOK, s.worktreeView(r.Context(), wt, true))
}

func (s *Server) handleWorktreeUpdate(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		How string `json:"how"` // merge | rebase | abort
	}
	_ = decode(r, &in)
	if in.How != "rebase" && in.How != "abort" {
		in.How = "merge"
	}
	if busy := s.busyThreads(r.Context(), wt); len(busy) > 0 && in.How != "abort" {
		writeErr(w, 409, "an agent is working in this worktree (“"+busy[0]+"”); wait until it is done")
		return
	}
	out, errOut, err := shOn(r.Context(), h, 2*time.Minute, wtUpdate, wt.Path, wt.Base, in.How)
	if err != nil {
		s.fail(w, scriptErr(errOut, err), "update")
		return
	}
	s.publishWorktrees(wt.UserID)
	v := s.worktreeView(r.Context(), wt, true)
	writeJSON(w, http.StatusOK, map[string]any{"worktree": v, "conflicts": splitLines(kv(out)["conflict"])})
}

func splitLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) handleWorktreeMerge(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		How          string `json:"how"` // merge | squash
		Message      string `json:"message"`
		PushBase     bool   `json:"push_base"`
		Archive      bool   `json:"archive"`
		DeleteBranch bool   `json:"delete_branch"`
	}
	_ = decode(r, &in)
	if in.How != "squash" {
		in.How = "merge"
	}
	ctx := r.Context()
	st := worktreeStatus(ctx, h, wt)
	if st.Dirty > 0 {
		writeErr(w, 409, fmt.Sprintf("%d file(s) are not committed yet; commit them (or ask the agent to) first", st.Dirty))
		return
	}
	if st.Merging || st.Rebasing {
		writeErr(w, 409, "a merge or rebase is in progress in the worktree; finish or abort it first")
		return
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = defaultMergeMessage(wt, st, in.How)
	}
	out, errOut, err := shOn(ctx, h, 2*time.Minute, wtMerge, wt.Repo, wt.Branch, wt.Base, in.How, msg)
	if err != nil {
		s.fail(w, scriptErr(errOut, err), "merge")
		return
	}
	m := kv(out)
	if c := splitLines(m["conflict"]); len(c) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "merging into " + wt.Base + " would conflict in " + strings.Join(c, ", "), "conflicts": c})
		return
	}
	res := map[string]any{"how": firstNonEmpty(m["how"], "already"), "commit": m["new"]}
	if in.PushBase {
		if _, errOut, err := shOn(ctx, h, 2*time.Minute, gitEnv+`cd -- "$1" && git push -q origin "refs/heads/$2:refs/heads/$2"`, wt.Repo, wt.Base); err != nil {
			res["push_error"] = scriptErr(errOut, err).Error()
		} else {
			res["pushed"] = true
		}
	}
	wt.MergedAt = store.Now()
	_ = s.store.UpdateWorktree(ctx, wt)
	if in.Archive {
		if err := s.archiveWorktree(ctx, h, wt, in.DeleteBranch); err != nil {
			res["archive_error"] = err.Error()
		}
	}
	s.publishWorktrees(wt.UserID)
	res["worktree"] = s.worktreeView(ctx, wt, wt.Status == "active")
	writeJSON(w, http.StatusOK, res)
}

func defaultMergeMessage(wt *store.Worktree, st wtStatusInfo, how string) string {
	title := firstNonEmpty(wt.Title, wt.Branch)
	if how != "squash" {
		return "Merge branch '" + wt.Branch + "'\n\n" + title
	}
	var b strings.Builder
	b.WriteString(title)
	if len(st.Commits) > 1 {
		b.WriteString("\n")
		for i := len(st.Commits) - 1; i >= 0; i-- {
			b.WriteString("\n- " + st.Commits[i].Subject)
		}
	}
	return b.String()
}

func (s *Server) handleWorktreePush(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		PR    bool   `json:"pr"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	_ = decode(r, &in)
	ctx := r.Context()
	st := worktreeStatus(ctx, h, wt)
	title := firstNonEmpty(strings.TrimSpace(in.Title), wt.Title, wt.Branch)
	body := strings.TrimSpace(in.Body)
	if body == "" {
		var b strings.Builder
		for i := len(st.Commits) - 1; i >= 0; i-- {
			b.WriteString("- " + st.Commits[i].Subject + "\n")
		}
		body = b.String()
	}
	pr := "0"
	if in.PR {
		pr = "1"
	}
	out, errOut, err := shOn(ctx, h, 3*time.Minute, wtPush, wt.Path, wt.Branch, wt.Base, pr, title, body)
	if err != nil {
		s.fail(w, scriptErr(errOut, err), "push")
		return
	}
	m := kv(out)
	res := map[string]any{"pushed": true}
	if m["pr"] != "" {
		wt.PRURL = m["pr"]
		_ = s.store.UpdateWorktree(ctx, wt)
		res["pr_url"] = m["pr"]
	} else if in.PR {
		res["compare_url"] = compareURL(m["remote_url"], wt.Base, wt.Branch)
	}
	s.publishWorktrees(wt.UserID)
	res["worktree"] = s.worktreeView(ctx, wt, true)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleWorktreeSetup(w http.ResponseWriter, r *http.Request) {
	wt, _ := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		Skip     bool   `json:"skip"`      // continue without setup: send what waits
		ThreadID string `json:"thread_id"` // show the run in this thread
	}
	_ = decode(r, &in)
	if in.Skip {
		for _, id := range s.store.WorktreeThreads(r.Context(), wt.ID) {
			s.holds().release(id)
		}
		writeJSON(w, http.StatusOK, s.worktreeView(r.Context(), wt, false))
		return
	}
	if wt.Status != "active" || wt.Setup == "running" {
		writeErr(w, 409, "the setup is running already (or the worktree is archived)")
		return
	}
	tid := in.ThreadID
	if tid != "" && !contains(s.store.WorktreeThreads(r.Context(), wt.ID), tid) {
		tid = ""
	}
	wt.Setup = "running"
	_ = s.store.UpdateWorktree(r.Context(), wt)
	go s.runSetup(wt, tid, false)
	writeJSON(w, http.StatusAccepted, s.worktreeView(r.Context(), wt, false))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// archiveWorktree stops its idle agents, runs teardown, saves everything
// under refs/pocket-ai/archive/<id> and removes the folder.
func (s *Server) archiveWorktree(ctx context.Context, h *store.Host, wt *store.Worktree, deleteBranch bool) error {
	if busy := s.busyThreads(ctx, wt); len(busy) > 0 {
		return errConflict("an agent is working in this worktree (“" + busy[0] + "”); stop it first")
	}
	for _, id := range s.store.WorktreeThreads(ctx, wt.ID) {
		s.rt.stop(id)
	}
	cfg := s.repoConfig(ctx, h, wt.HostID, wt.Repo).Effective
	if len(cfg.Teardown) > 0 {
		if err := s.runCommands(ctx, h, wt, cfg.Teardown, 5*time.Minute, func(string) {}); err != nil {
			s.log.Warn("worktree teardown failed", "worktree", wt.ID, "err", err) // archiving goes on: the folder must not be left behind
		}
	}
	del := "0"
	if deleteBranch {
		del = "1"
	}
	ref := "refs/pocket-ai/archive/" + strconv.FormatInt(wt.ID, 10)
	out, errOut, err := shOn(ctx, h, 3*time.Minute, wtArchive, wt.Path, wt.Repo, ref, wt.Branch, del)
	if err != nil {
		return scriptErr(errOut, err)
	}
	wt.Status, wt.ArchivedAt, wt.Setup = "archived", store.Now(), ""
	if kv(out)["saved"] != "" {
		wt.ArchiveRef = ref
	}
	return s.store.UpdateWorktree(ctx, wt)
}

type conflictErr struct{ msg string }

func (e conflictErr) Error() string { return e.msg }
func errConflict(msg string) error  { return conflictErr{msg} }

func (s *Server) handleWorktreeArchive(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	var in struct {
		DeleteBranch bool `json:"delete_branch"`
	}
	_ = decode(r, &in)
	if wt.Status != "active" {
		writeErr(w, 409, "it is archived already")
		return
	}
	if err := s.archiveWorktree(r.Context(), h, wt, in.DeleteBranch); err != nil {
		var ce conflictErr
		if errors.As(err, &ce) {
			writeErr(w, 409, ce.msg)
			return
		}
		s.fail(w, err, "archive")
		return
	}
	s.publishWorktrees(wt.UserID)
	writeJSON(w, http.StatusOK, s.worktreeView(r.Context(), wt, false))
}

func (s *Server) handleWorktreeRestore(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	if wt.Status == "active" {
		writeErr(w, 409, "it is not archived")
		return
	}
	ref := firstNonEmpty(wt.ArchiveRef, "refs/pocket-ai/archive/"+strconv.FormatInt(wt.ID, 10))
	if _, errOut, err := shOn(r.Context(), h, 3*time.Minute, wtRestore, wt.Repo, wt.Path, wt.Branch, ref); err != nil {
		s.fail(w, scriptErr(errOut, err), "restore")
		return
	}
	wt.Status, wt.ArchivedAt = "active", ""
	_ = s.store.UpdateWorktree(r.Context(), wt)
	// dependencies were not archived (ignored files): set it up again
	go s.runSetup(wt, "", true)
	s.publishWorktrees(wt.UserID)
	writeJSON(w, http.StatusOK, s.worktreeView(r.Context(), wt, true))
}

func (s *Server) handleForgetWorktree(w http.ResponseWriter, r *http.Request) {
	wt, h := s.worktree(w, r)
	if wt == nil {
		return
	}
	if wt.Status == "active" {
		writeErr(w, 409, "archive it first")
		return
	}
	del := "0"
	if queryBool(r, "delete_branch") {
		del = "1"
	}
	ref := firstNonEmpty(wt.ArchiveRef, "refs/pocket-ai/archive/"+strconv.FormatInt(wt.ID, 10))
	if _, errOut, err := shOn(r.Context(), h, time.Minute, wtForget, wt.Repo, ref, wt.Branch, del); err != nil {
		s.fail(w, scriptErr(errOut, err), "delete worktree")
		return
	}
	if err := s.store.DeleteWorktree(r.Context(), wt.ID); err != nil {
		s.fail(w, err, "delete worktree")
		return
	}
	s.publishWorktrees(wt.UserID)
	w.WriteHeader(http.StatusNoContent)
}

// handleCleanupWorktrees archives every merged worktree that is clean and idle.
func (s *Server) handleCleanupWorktrees(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		DeleteBranches bool `json:"delete_branches"`
	}
	_ = decode(r, &in)
	ctx := r.Context()
	list, _ := s.store.ListWorktrees(ctx, me(r), "active")
	done, skipped := []string{}, []string{}
	for _, wt := range list {
		h, err := s.host(ctx, wt.HostID)
		if err != nil {
			continue
		}
		st := worktreeStatus(ctx, h, wt)
		if !st.Merged {
			continue
		}
		if st.Dirty > 0 || len(s.busyThreads(ctx, wt)) > 0 {
			skipped = append(skipped, wt.Branch)
			continue
		}
		if err := s.archiveWorktree(ctx, h, wt, in.DeleteBranches); err != nil {
			skipped = append(skipped, wt.Branch)
			continue
		}
		done = append(done, wt.Branch)
	}
	s.publishWorktrees(me(r))
	writeJSON(w, http.StatusOK, map[string]any{"archived": done, "skipped": skipped})
}

// ---------- repository settings and other worktrees ----------

// repoAt resolves host + folder from a request to the repository's main checkout.
func (s *Server) repoAt(r *http.Request, hostID int64, p string) (*store.Host, repoInfo, error) {
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), hostID)
	if err != nil {
		return nil, repoInfo{}, err
	}
	if h == nil {
		if p, err = s.checkDir(p); err != nil {
			return nil, repoInfo{}, err
		}
	}
	info, err := inspectRepo(r.Context(), h, p)
	if err == nil && !info.Repo {
		err = badRequest(p + " is not in a git repository")
	}
	return h, info, err
}

const wtBranches = gitEnv + `cd -- "$1" || exit 3
git for-each-ref --sort=-committerdate --count=200 --format='b=%(refname:short)' refs/heads refs/remotes/origin
`

func (s *Server) handleWorktreeConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	hostID := queryInt64(r, "host")
	h, info, err := s.repoAt(r, hostID, r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "worktree settings")
		return
	}
	cfg := s.repoConfig(r.Context(), h, hostID, info.Main)
	out, _, _ := shOn(r.Context(), h, 20*time.Second, wtBranches, info.Main)
	branches := []string{}
	for _, b := range splitLines(kv(out)["b"]) {
		if b != "origin/HEAD" && b != "origin" {
			branches = append(branches, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repo": info.Main, "top": info.Top, "branch": info.Branch, "main_branch": info.MainBranch, "default": info.Default, "project_id": info.ProjectID,
		"base":     firstNonEmpty(cfg.Effective.Base, info.MainBranch, info.Default, "main"),
		"branches": branches, "config": cfg, "dir": s.worktreesDir(h, info.Main),
		"is_worktree": info.Top != info.Main,
	})
}

func (s *Server) handleSaveWorktreeConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Host  int64     `json:"host"`
		Path  string    `json:"path"`
		Saved *wtConfig `json:"saved"` // null = forget Pocket AI's settings (use the repo file)
		// "project" (default when the repo has a remote): every machine, and
		// this machine's own setup is dropped; "machine": this checkout only.
		Scope string `json:"scope"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	h, info, err := s.repoAt(r, in.Host, in.Path)
	if err != nil {
		s.fail(w, err, "worktree settings")
		return
	}
	data := ""
	if in.Saved != nil {
		in.Saved.Setup, in.Saved.Teardown, in.Saved.Copy = nonEmpty(in.Saved.Setup), nonEmpty(in.Saved.Teardown), nonEmpty(in.Saved.Copy)
		b, _ := json.Marshal(in.Saved)
		data = string(b)
	}
	project := info.ProjectID
	if in.Scope == "" && project != "" {
		in.Scope = "project"
	}
	if in.Scope == "project" {
		if project == "" {
			writeErr(w, 400, "this repository has no git remote, so its setup can only be kept for this machine")
			return
		}
		err = s.store.SaveProjectSettings(r.Context(), project, data)
		if err == nil {
			err = s.store.SaveRepoSettings(r.Context(), in.Host, info.Main, "")
		}
	} else {
		err = s.store.SaveRepoSettings(r.Context(), in.Host, info.Main, data)
	}
	if err != nil {
		s.fail(w, err, "worktree settings")
		return
	}
	writeJSON(w, http.StatusOK, s.repoConfig(r.Context(), h, in.Host, info.Main))
}

type otherWorktree struct {
	HostID   int64  `json:"host_id"`
	HostName string `json:"host_name,omitempty"`
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Missing  bool   `json:"missing"` // git knows it, the folder is gone
	Paseo    bool   `json:"paseo"`   // made by Paseo
}

const wtList = gitEnv + `cd -- "$1" || exit 3
git worktree list --porcelain
`

// handleOtherWorktrees lists worktrees of the person's repositories that
// Pocket AI did not make (by hand, Paseo…), to adopt or clean up.
func (s *Server) handleOtherWorktrees(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	mine, _ := s.store.ListWorktrees(ctx, me(r), "")
	type repoKey struct {
		host int64
		repo string
	}
	repos := map[repoKey]bool{}
	known := map[string]bool{}
	for _, wt := range mine {
		repos[repoKey{wt.HostID, wt.Repo}] = true
		if wt.Status == "active" {
			known[strconv.FormatInt(wt.HostID, 10)+":"+wt.Path] = true
		}
	}
	if p := r.URL.Query().Get("path"); p != "" {
		if h, info, err := s.repoAt(r, queryInt64(r, "host"), p); err == nil {
			_ = h
			repos[repoKey{queryInt64(r, "host"), info.Main}] = true
		}
	}
	out := []otherWorktree{}
	for k := range repos {
		h, err := s.host(ctx, k.host)
		if err != nil {
			continue
		}
		list, _, err := shOn(ctx, h, 20*time.Second, wtList, k.repo)
		if err != nil {
			continue
		}
		for _, block := range strings.Split(strings.TrimSpace(list), "\n\n") {
			var o otherWorktree
			for _, line := range strings.Split(block, "\n") {
				key, val, _ := strings.Cut(line, " ")
				switch key {
				case "worktree":
					o.Path = val
				case "HEAD":
					o.Head = val
				case "branch":
					o.Branch = strings.TrimPrefix(val, "refs/heads/")
				case "prunable":
					o.Missing = true
				case "bare":
					o.Path = ""
				}
			}
			if o.Path == "" || o.Path == k.repo || known[strconv.FormatInt(k.host, 10)+":"+o.Path] {
				continue
			}
			o.HostID, o.Repo, o.Paseo = k.host, k.repo, strings.Contains(o.Path, "/.paseo/worktrees/")
			if h != nil {
				o.HostName = h.Name
			}
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	writeJSON(w, http.StatusOK, out)
}

// handleAdoptWorktree takes over a worktree Pocket AI did not make.
func (s *Server) handleAdoptWorktree(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Host int64  `json:"host"`
		Path string `json:"path"`
		Base string `json:"base"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	h, info, err := s.repoAt(r, in.Host, in.Path)
	if err != nil {
		s.fail(w, err, "adopt")
		return
	}
	if info.Top == info.Main {
		writeErr(w, 400, "that is the repository's main checkout, not a worktree")
		return
	}
	if info.Branch == "" {
		writeErr(w, 400, "that worktree is not on a branch (detached HEAD); switch it to a branch first")
		return
	}
	cfg := s.repoConfig(r.Context(), h, in.Host, info.Main).Effective
	base := firstNonEmpty(in.Base, cfg.Base, info.MainBranch, info.Default, "main")
	sha, _, _ := shOn(r.Context(), h, 20*time.Second, gitEnv+`cd -- "$1" && git merge-base HEAD "$2" 2>/dev/null || git rev-parse HEAD`, info.Top, base)
	wt := &store.Worktree{UserID: me(r), HostID: in.Host, Repo: info.Main, Path: info.Top, Branch: info.Branch, Base: base,
		BaseSHA: strings.TrimSpace(sha), Title: info.Branch}
	if err := s.store.CreateWorktree(r.Context(), wt); err != nil {
		s.fail(w, err, "adopt")
		return
	}
	s.publishWorktrees(wt.UserID)
	writeJSON(w, http.StatusCreated, s.worktreeView(r.Context(), wt, true))
}

// handleRemoveOtherWorktree removes a worktree Pocket AI did not make, with
// the same backup of its uncommitted work (refs/pocket-ai/removed/<name>).
func (s *Server) handleRemoveOtherWorktree(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Host int64  `json:"host"`
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := decode(r, &in); err != nil || in.Repo == "" || in.Path == "" || in.Path == in.Repo {
		writeErr(w, 400, "bad body")
		return
	}
	h, info, err := s.repoAt(r, in.Host, in.Repo)
	if err != nil {
		s.fail(w, err, "remove worktree")
		return
	}
	// only a worktree git lists for this repository
	list, _, _ := shOn(r.Context(), h, 20*time.Second, wtList, info.Main)
	branch := ""
	found := false
	for _, block := range strings.Split(list, "\n\n") {
		if strings.Contains(block+"\n", "worktree "+in.Path+"\n") {
			found = true
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "branch ") {
					branch = strings.TrimPrefix(line, "branch refs/heads/")
				}
			}
		}
	}
	if !found {
		writeErr(w, 404, "git does not know that worktree")
		return
	}
	name := strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			return r
		}
		return '-'
	}, path.Base(in.Path))
	ref := "refs/pocket-ai/removed/" + name + "-" + strconv.FormatInt(time.Now().Unix(), 36)
	out, errOut, err := shOn(r.Context(), h, 3*time.Minute, wtArchive, in.Path, info.Main, ref, firstNonEmpty(branch, name), "0")
	if err != nil {
		s.fail(w, scriptErr(errOut, err), "remove worktree")
		return
	}
	res := map[string]any{"removed": true}
	if saved := kv(out)["saved"]; saved != "" {
		res["saved_ref"] = ref
	}
	writeJSON(w, http.StatusOK, res)
}

// threadWorktree puts a new thread into a worktree: a new one made from its
// folder, or an existing one of the person's (then it also runs there).
func (s *Server) threadWorktree(ctx context.Context, u *store.User, h *store.Host, t *store.Thread, spec newWorktree, text string) (*store.Worktree, error) {
	if spec.Mode == "existing" {
		wt, err := s.store.GetWorktree(ctx, u.ID, spec.ID)
		if err != nil {
			return nil, badRequest("that worktree is gone")
		}
		if wt.Status != "active" {
			return nil, badRequest("that worktree is archived; restore it first")
		}
		if wt.HostID != t.HostID {
			if _, err := s.checkHostUse(ctx, u, wt.HostID); err != nil {
				return nil, err
			}
			t.HostID, t.AccountID = wt.HostID, 0
		}
		t.Cwd, t.WorktreeID, t.WorkspaceID = wt.Path, wt.ID, nil
		return wt, nil
	}
	wt, rel, err := s.createWorktree(ctx, u, h, t.Cwd, spec, text)
	if err != nil {
		return nil, err
	}
	t.Cwd, t.WorktreeID, t.WorkspaceID = wt.Path, wt.ID, nil
	if rel != "" {
		t.Cwd = strings.TrimRight(wt.Path, "/") + "/" + rel
	}
	return wt, nil
}
