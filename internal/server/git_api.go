package server

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Git branch of a working folder: shown next to the folder button and in the
// thread header, with a switcher. Only `git switch` is ever run — never a
// reset, stash or checkout of files — so uncommitted work is either carried
// over by git or the switch is refused with git's own message.

type gitBranch struct {
	Name     string `json:"name"`
	Remote   bool   `json:"remote,omitempty"`
	Current  bool   `json:"current,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Time     int64  `json:"time,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

type gitInfo struct {
	Repo     bool        `json:"repo"`
	Root     string      `json:"root,omitempty"`
	Branch   string      `json:"branch,omitempty"`
	Detached bool        `json:"detached,omitempty"`
	Head     string      `json:"head,omitempty"`
	Dirty    int         `json:"dirty"`
	Ahead    int         `json:"ahead"`
	Behind   int         `json:"behind"`
	Branches []gitBranch `json:"branches,omitempty"`
}

const gitMaxBranches = 300

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// never prompt for credentials; don't take index.lock for status (an agent may be committing);
	// a person's repo (and its hooks) only in their jail
	cmd, err := agent.LocalCmd(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}, "git", append([]string{"-C", dir}, args...)...)
	if err != nil {
		return "", err
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), badRequest(msg)
	}
	return out.String(), nil
}

// gitInfoCmds are the git commands readGitInfo runs, in order.
var gitInfoCmds = [][]string{
	{"rev-parse", "--show-toplevel"},
	{"symbolic-ref", "--short", "-q", "HEAD"},
	{"rev-parse", "--short", "HEAD"},
	{"status", "--porcelain", "--untracked-files=normal"},
	{"rev-list", "--left-right", "--count", "@{upstream}...HEAD"},
	{"for-each-ref", "--sort=-committerdate",
		"--format=%(HEAD)%00%(refname)%00%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(contents:subject)",
		"refs/heads", "refs/remotes"},
}

func readGitInfo(ctx context.Context, dir string) gitInfo {
	return gitInfoFrom(func(i int) (string, error) { return runGit(ctx, dir, gitInfoCmds[i]...) })
}

// gitInfoFrom builds the info from the output of gitInfoCmds[i].
func gitInfoFrom(git func(i int) (string, error)) gitInfo {
	root, err := git(0)
	if err != nil {
		return gitInfo{}
	}
	info := gitInfo{Repo: true, Root: strings.TrimSpace(root)}
	if b, err := git(1); err == nil {
		info.Branch = strings.TrimSpace(b)
	} else {
		info.Detached = true
	}
	if h, err := git(2); err == nil {
		info.Head = strings.TrimSpace(h)
	}
	if st, err := git(3); err == nil {
		for _, l := range strings.Split(st, "\n") {
			if strings.TrimSpace(l) != "" {
				info.Dirty++
			}
		}
	}
	if c, err := git(4); err == nil {
		if f := strings.Fields(c); len(f) == 2 {
			info.Behind, _ = strconv.Atoi(f[0])
			info.Ahead, _ = strconv.Atoi(f[1])
		}
	}
	refs, _ := git(5)
	info.Branches = parseGitRefs(refs)
	return info
}

// remoteGitInfo is readGitInfo for a folder on another machine, in one
// ssh round trip: each command's output is followed by \x1e<exit code>\x1e.
func remoteGitInfo(ctx context.Context, h *store.Host, dir string) gitInfo {
	var b strings.Builder
	b.WriteString(`cd -- "$1" 2>/dev/null || exit 3
export GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 LC_ALL=C
`)
	for _, c := range gitInfoCmds {
		b.WriteString("git")
		for _, a := range c {
			b.WriteString(" " + agent.ShellQuote(a))
		}
		b.WriteString(" 2>/dev/null </dev/null; printf '\\036%d\\036' $?\n")
	}
	out, _, err := remoteOf(h).Run(ctx, 20*time.Second, b.String(), dir)
	if err != nil {
		return gitInfo{}
	}
	parts := strings.Split(out, "\x1e") // out, code, out, code, …, ""
	return gitInfoFrom(func(i int) (string, error) {
		if 2*i+1 >= len(parts) || parts[2*i+1] != "0" {
			return "", badRequest("git failed")
		}
		return parts[2*i], nil
	})
}

// gitOn runs one git command in a folder here (h nil) or on another machine.
func gitOn(ctx context.Context, h *store.Host, dir string, args ...string) (string, error) {
	if h == nil {
		return runGit(ctx, dir, args...)
	}
	script := `cd -- "$1" || exit 3
shift
GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 LC_ALL=C exec git "$@" </dev/null`
	out, errOut, err := remoteOf(h).Run(ctx, 30*time.Second, script, append([]string{dir}, args...)...)
	if err != nil {
		return out, badRequest(firstNonEmpty(errOut, err.Error()))
	}
	return out, nil
}

// gitDir resolves the folder of a git request, here or on a host.
func (s *Server) gitDir(ctx context.Context, u *store.User, hostID int64, p string) (*store.Host, string, error) {
	h, err := s.checkHostUse(ctx, u, hostID)
	if err != nil {
		return nil, "", err
	}
	if h != nil {
		dir, err := s.checkRemoteDir(ctx, h, p)
		return h, dir, err
	}
	dir, err := s.checkDir(ctx, p)
	return nil, dir, err
}

// parseGitRefs keeps local branches plus remote branches that no local one
// already stands for (by tracking or by name), newest first.
func parseGitRefs(out string) []gitBranch {
	var local, remote []gitBranch
	covered := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x00")
		if len(f) < 6 {
			continue
		}
		t, _ := strconv.ParseInt(f[4], 10, 64)
		b := gitBranch{Name: f[2], Upstream: f[3], Time: t, Subject: f[5], Current: f[0] == "*"}
		switch {
		case strings.HasPrefix(f[1], "refs/heads/"):
			local = append(local, b)
			if b.Upstream != "" {
				covered[b.Upstream] = true
			}
		case strings.HasPrefix(f[1], "refs/remotes/"):
			if strings.HasSuffix(f[1], "/HEAD") || !strings.Contains(b.Name, "/") {
				continue
			}
			b.Remote, b.Upstream = true, ""
			remote = append(remote, b)
		}
	}
	names := map[string]bool{}
	for _, b := range local {
		names[b.Name] = true
	}
	list := local
	for _, b := range remote {
		short := b.Name[strings.Index(b.Name, "/")+1:]
		if covered[b.Name] || names[short] {
			continue
		}
		list = append(list, b)
	}
	if len(list) > gitMaxBranches {
		list = list[:gitMaxBranches]
	}
	return list
}

func (s *Server) handleGitInfo(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	h, dir, err := s.gitDir(r.Context(), userFrom(r.Context()), queryInt64(r, "host"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "git")
		return
	}
	if h != nil {
		writeJSON(w, http.StatusOK, remoteGitInfo(r.Context(), h, dir))
		return
	}
	writeJSON(w, http.StatusOK, readGitInfo(r.Context(), dir))
}

// A lightweight identity lookup: no git status, branch listing or setup config.
func (s *Server) handleGitProject(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	_, info, err := s.repoAt(r, queryInt64(r, "host"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "git project")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": info.Main, "branch": info.Branch, "project_id": info.ProjectID})
}

func (s *Server) handleGitSwitch(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
		Create bool   `json:"create"` // new branch from the current HEAD
		Remote bool   `json:"remote"` // Branch is "origin/x": make a local x tracking it
		Host   int64  `json:"host"`   // 0 = this server
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, badRequest("bad body"), "git switch")
		return
	}
	h, dir, err := s.gitDir(r.Context(), userFrom(r.Context()), in.Host, in.Path)
	if err != nil {
		s.fail(w, err, "git switch")
		return
	}
	name := strings.TrimSpace(in.Branch)
	if name == "" || strings.HasPrefix(name, "-") {
		s.fail(w, badRequest("pick a branch"), "git switch")
		return
	}
	if !in.Remote {
		if _, err := gitOn(r.Context(), h, dir, "check-ref-format", "--branch", name); err != nil {
			s.fail(w, badRequest("“"+name+"” is not a valid branch name"), "git switch")
			return
		}
	}
	args := []string{"switch", name}
	switch {
	case in.Create:
		args = []string{"switch", "-c", name}
	case in.Remote:
		args = []string{"switch", "--track", name}
	}
	if _, err := gitOn(r.Context(), h, dir, args...); err != nil {
		s.fail(w, err, "git switch")
		return
	}
	if h != nil {
		writeJSON(w, http.StatusOK, remoteGitInfo(r.Context(), h, dir))
		return
	}
	writeJSON(w, http.StatusOK, readGitInfo(r.Context(), dir))
}
