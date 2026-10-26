package server

import (
	"net/http"
	"strings"
	"time"
)

type reviewFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func parseReviewStatus(status string) []reviewFile {
	files := []reviewFile{}
	rows := strings.Split(status, "\x00")
	for i := 0; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 4 {
			continue
		}
		files = append(files, reviewFile{Path: row[3:], Status: row[:2]})
		// -z emits the original name as a separate field for renames/copies.
		if strings.ContainsAny(row[:2], "RC") {
			i++
		}
	}
	return files
}

func (s *Server) handleGitReview(w http.ResponseWriter, r *http.Request) {
	// Repository files may contain private work from other conversations.
	// Sharing a chat does not grant access to its working directory.
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	if t.Source == "ask" || t.Cwd == "" {
		writeErr(w, 400, "this conversation has no repository")
		return
	}
	h, cwd, err := s.gitDir(r.Context(), userFrom(r.Context()), t.HostID, t.Cwd)
	if err != nil {
		s.fail(w, err, "open repository")
		return
	}
	status, stderr, err := shOn(r.Context(), h, 15*time.Second, gitEnv+`cd -- "$1" && git --no-optional-locks status --porcelain=v1 -z --untracked-files=normal`, cwd)
	if err != nil {
		writeErr(w, 400, "Could not read Git status: "+stderr)
		return
	}
	branch, _, _ := shOn(r.Context(), h, 10*time.Second, gitEnv+`cd -- "$1" && git symbolic-ref --short -q HEAD`, cwd)
	// Both staged and unstaged changes relative to HEAD. For an unborn branch,
	// use the empty tree, which git hash-object computes for this repository.
	diff, stderr, err := shOn(r.Context(), h, 15*time.Second, gitEnv+`cd -- "$1" || exit 1
base=HEAD
git rev-parse --verify HEAD >/dev/null 2>&1 || base=$(git hash-object -t tree /dev/null)
output=$(mktemp) || exit 1
trap 'rm -f -- "$output"' EXIT HUP INT TERM
git --no-pager diff --no-ext-diff --no-textconv --no-color "$base" -- > "$output" || exit 1
head -c 2097153 "$output"`, cwd)
	if err != nil {
		s.fail(w, scriptErr(stderr, err), "read diff")
		return
	}
	truncated := len(diff) > 2*1024*1024
	if truncated {
		diff = diff[:2*1024*1024]
	}
	writeJSON(w, 200, map[string]any{"branch": strings.TrimSpace(branch), "files": parseReviewStatus(status), "diff": diff, "truncated": truncated})
}
