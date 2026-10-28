package server

// Managing folders from the folder picker: a missing folder opens its
// nearest existing parent, and a new folder or a clone of a repository can
// be made right there (on this server inside the roots, on another machine
// wherever its ssh account may write).

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// listDirsNear is listDirs, but a folder that does not exist lists the
// nearest parent that does (inside the roots) with Missing set.
func (s *Server) listDirsNear(ctx context.Context, p string, hidden bool) (*dirListing, error) {
	l, err := s.listDirs(ctx, p, hidden)
	if err == nil || p == "" {
		return l, err
	}
	want := filepath.Clean(s.expand(ctx, p))
	if !filepath.IsAbs(want) {
		return nil, err
	}
	if _, serr := os.Stat(want); !os.IsNotExist(serr) {
		return nil, err // there, but not allowed or not a folder
	}
	for q := filepath.Dir(want); ; q = filepath.Dir(q) {
		if near, nerr := s.listDirs(ctx, q, hidden); nerr == nil {
			near.Missing = want
			return near, nil
		}
		if q == filepath.Dir(q) {
			return nil, err
		}
	}
}

// newFolderPath checks a folder about to be made on this server: absolute,
// and its nearest existing parent inside the roots (no symlink out).
func (s *Server) newFolderPath(ctx context.Context, p string) (string, error) {
	p = filepath.Clean(s.expand(ctx, p))
	if !filepath.IsAbs(p) {
		return "", badRequest("use an absolute folder path (or ~/…)")
	}
	q := p
	for {
		if _, err := os.Lstat(q); err == nil {
			break
		}
		if q == filepath.Dir(q) {
			return "", badRequest("no part of " + p + " exists")
		}
		q = filepath.Dir(q)
	}
	if _, err := s.checkDir(ctx, q); err != nil {
		return "", err
	}
	return p, nil
}

var badFolderName = regexp.MustCompile(`[\x00-\x1f]`)

// checkFolderName: one plain folder name, no path tricks.
func checkFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "", name == ".", name == "..":
		return "", badRequest("give the folder a name")
	case strings.ContainsAny(name, `/\`), badFolderName.MatchString(name):
		return "", badRequest("a folder name cannot contain / or control characters")
	case len(name) > 200:
		return "", badRequest("that name is too long")
	}
	return name, nil
}

// handleMkdir makes a folder: {path} itself ("Create it" for a missing
// folder, parents included) or {path, name} inside path.
func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Host int64  `json:"host"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeErr(w, http.StatusBadRequest, "say where")
		return
	}
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), in.Host)
	if err != nil {
		s.fail(w, err, "mkdir")
		return
	}
	target := strings.TrimSpace(in.Path)
	if in.Name != "" {
		name, err := checkFolderName(in.Name)
		if err != nil {
			s.fail(w, err, "mkdir")
			return
		}
		target = strings.TrimRight(target, "/") + "/" + name
	}
	var out *dirListing
	if h != nil {
		_, errOut, rerr := remoteOf(h).Run(r.Context(), 20*time.Second, `p=$1
case "$p" in "~") p=$HOME;; "~/"*) p="$HOME/${p#\~/}";; esac
mkdir -p -- "$p" || exit 3`, target)
		if rerr != nil {
			s.fail(w, badRequest(h.Name+": "+firstNonEmpty(remoteErr(errOut, rerr), "could not make the folder")), "mkdir")
			return
		}
		out, err = s.listRemoteDirs(r.Context(), h, target, false)
	} else {
		p, perr := s.newFolderPath(r.Context(), target)
		if perr != nil {
			s.fail(w, perr, "mkdir")
			return
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			s.fail(w, badRequest("could not make "+p+": "+err.Error()), "mkdir")
			return
		}
		out, err = s.listDirs(r.Context(), p, false)
	}
	if err != nil {
		s.fail(w, err, "mkdir")
		return
	}
	s.log.Info("folder made", "path", out.Path, "host", in.Host, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusOK, out)
}

// repoName is the folder a clone goes to: the last part of the URL.
func repoName(url string) string {
	u := strings.TrimRight(strings.TrimSpace(url), "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

var cloneURL = regexp.MustCompile(`^(https?://|ssh://|git://|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:)[^\s'"\\]+$`)

// handleClone clones a repository into {path}/{name} (name from the URL when
// empty) with the git and keys of that machine.
func (s *Server) handleClone(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Host int64  `json:"host"`
		Path string `json:"path"`
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeErr(w, http.StatusBadRequest, "say where")
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if !cloneURL.MatchString(in.URL) {
		writeErr(w, http.StatusBadRequest, "that does not look like a git URL (https://… or git@host:owner/repo)")
		return
	}
	name, err := checkFolderName(firstNonEmpty(in.Name, repoName(in.URL)))
	if err != nil {
		s.fail(w, err, "clone")
		return
	}
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), in.Host)
	if err != nil {
		s.fail(w, err, "clone")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
	defer cancel()
	var out *dirListing
	if h != nil {
		_, errOut, rerr := remoteOf(h).Run(ctx, 15*time.Minute, `cd -- "$1" || exit 3
[ -e "$3" ] && { echo "$3 is already there" >&2; exit 3; }
GIT_TERMINAL_PROMPT=0 GIT_SSH_COMMAND="ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new" git clone --quiet -- "$2" "$3"`, in.Path, in.URL, name)
		if rerr != nil {
			s.fail(w, badRequest(h.Name+": "+firstNonEmpty(remoteErr(errOut, rerr), "the clone failed")), "clone")
			return
		}
		out, err = s.listRemoteDirs(ctx, h, path.Join(in.Path, name), false)
	} else {
		dir, derr := s.checkDir(ctx, in.Path)
		if derr != nil {
			s.fail(w, derr, "clone")
			return
		}
		dest := filepath.Join(dir, name)
		if _, serr := os.Lstat(dest); serr == nil {
			s.fail(w, badRequest(name+" is already there"), "clone")
			return
		}
		// into a person's folder: in their jail (a file:// URL must not copy the server's repos)
		cmd, cerr := agent.LocalCmd(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new"}, "git", "clone", "--quiet", "--", in.URL, name)
		if cerr != nil {
			s.fail(w, cerr, "clone")
			return
		}
		if b, cerr := cmd.CombinedOutput(); cerr != nil {
			s.fail(w, badRequest("the clone failed: "+oneLine(firstNonEmpty(string(b), cerr.Error()), 300)), "clone")
			return
		}
		out, err = s.listDirs(ctx, dest, false)
	}
	if err != nil {
		s.fail(w, err, "clone")
		return
	}
	s.log.Info("repository cloned", "url", in.URL, "path", out.Path, "host", in.Host, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusOK, out)
}

// remoteErr: ssh's own trouble in words, else the script's last lines.
func remoteErr(stderr string, err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 255 {
		return sshError(stderr, err)
	}
	return lastLines(stderr, 3)
}
