package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// never prompt for credentials; don't take index.lock for status (an agent may be committing)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
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

func readGitInfo(ctx context.Context, dir string) gitInfo {
	root, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitInfo{}
	}
	info := gitInfo{Repo: true, Root: strings.TrimSpace(root)}
	if b, err := runGit(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		info.Branch = strings.TrimSpace(b)
	} else {
		info.Detached = true
	}
	if h, err := runGit(ctx, dir, "rev-parse", "--short", "HEAD"); err == nil {
		info.Head = strings.TrimSpace(h)
	}
	if st, err := runGit(ctx, dir, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		for _, l := range strings.Split(st, "\n") {
			if strings.TrimSpace(l) != "" {
				info.Dirty++
			}
		}
	}
	if c, err := runGit(ctx, dir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); err == nil {
		if f := strings.Fields(c); len(f) == 2 {
			info.Behind, _ = strconv.Atoi(f[0])
			info.Ahead, _ = strconv.Atoi(f[1])
		}
	}
	refs, _ := runGit(ctx, dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(HEAD)%00%(refname)%00%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(contents:subject)",
		"refs/heads", "refs/remotes")
	info.Branches = parseGitRefs(refs)
	return info
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
	dir, err := s.checkDir(r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "git")
		return
	}
	writeJSON(w, http.StatusOK, readGitInfo(r.Context(), dir))
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
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, badRequest("bad body"), "git switch")
		return
	}
	dir, err := s.checkDir(in.Path)
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
		if _, err := runGit(r.Context(), dir, "check-ref-format", "--branch", name); err != nil {
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
	if _, err := runGit(r.Context(), dir, args...); err != nil {
		s.fail(w, err, "git switch")
		return
	}
	writeJSON(w, http.StatusOK, readGitInfo(r.Context(), dir))
}
