package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sessions on another machine: the CLIs keep them there (~/.claude/projects,
// ~/.codex/sessions, and the same in every other login folder), so they are
// copied over ssh into a mirror folder here and read like this server's own.
// Session files only grow, so a later copy fetches just the bytes added
// since; a file that got shorter is copied anew.
//
// Mirror layout: <dir>/claude/<login>/projects/<folder>/<id>.jsonl and
// <dir>/codex/<login>/sessions/.../<name>.jsonl, <login> being "default"
// or the folder's name (".claude-max"), so MirrorHomes(dir) reads it.

// remoteSessionList prints "<rel>\t<size>\t<mtime>" per session file.
const remoteSessionList = RemoteHomesSh + `one() { printf '%s\t%s\t%s\n' "$1" "$(wc -c <"$2" | tr -d ' ')" "$(date -r "$2" +%s 2>/dev/null || echo 0)"; }
c0="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; x0="${CODEX_HOME:-$HOME/.codex}"
homes claude | while IFS= read -r c; do
  b=default; [ "$c" = "$c0" ] || b=${c##*/}
  for f in "$c"/projects/*/*.jsonl; do [ -f "$f" ] && one "claude/$b/${f#"$c"/}" "$f"; done
done
homes codex | while IFS= read -r x; do
  b=default; [ "$x" = "$x0" ] || b=${x##*/}
  [ -d "$x/sessions" ] && find "$x/sessions" -type f -name '*.jsonl' 2>/dev/null | while IFS= read -r f; do one "codex/$b/${f#"$x"/}" "$f"; done
done
exit 0
`

// remoteSessionFetch prints "<rel>\t<base64 of the bytes from offset on>"
// for each (rel, offset) pair in its arguments.
const remoteSessionFetch = `c0="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; x0="${CODEX_HOME:-$HOME/.codex}"
while [ $# -ge 2 ]; do
  rel=$1; off=$2; shift 2
  kind=${rel%%/*}; r=${rel#*/}; b=${r%%/*}; rest=${r#*/}
  case $kind in claude) d=$c0 ;; codex) d=$x0 ;; *) continue ;; esac
  [ "$b" = default ] || d="$HOME/$b"
  f="$d/$rest"
  [ -f "$f" ] || continue
  printf '%s\t' "$rel"; tail -c +$((off + 1)) "$f" | base64 | tr -d '\n'; printf '\n'
done
exit 0
`

var remoteSessionRel = regexp.MustCompile(`^(claude/(default|\.claude[-_][A-Za-z0-9._-]{1,60})/projects/[^/]+/[A-Za-z0-9_-]{8,80}\.jsonl|codex/(default|\.codex[-_][A-Za-z0-9._-]{1,60})/sessions(/[A-Za-z0-9_.-]+){1,6}\.jsonl)$`)

// Per fetch: so the command line and the answer stay a sensible size. A
// first copy of a busy machine takes a few rounds.
const (
	mirrorBatchFiles = 60
	mirrorBatchBytes = 24 << 20
)

type remoteFile struct {
	rel  string
	size int64
	mod  time.Time
}

// MirrorSessions brings dir up to date with the machine's session files
// (only, if `only` is set: the ones whose path contains it).
func MirrorSessions(ctx context.Context, r *Remote, dir, only string) error {
	return mirrorSessions(ctx, r.Run, dir, only)
}

type runFn func(ctx context.Context, timeout time.Duration, script string, args ...string) (string, string, error)

func mirrorSessions(ctx context.Context, run runFn, dir, only string) error {
	out, errOut, err := run(ctx, 2*time.Minute, remoteSessionList)
	if err != nil {
		return runErr(errOut, err)
	}
	remote := parseSessionList(out)
	seen := map[string]bool{}
	var todo []remoteFile
	var offsets []int64
	for _, f := range remote {
		seen[f.rel] = true
		if only != "" && !strings.Contains(f.rel, only) {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(f.rel))
		have := int64(0)
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			have = info.Size()
			if have == f.size {
				if !info.ModTime().Equal(f.mod) && !f.mod.IsZero() {
					_ = os.Chtimes(p, f.mod, f.mod)
				}
				continue
			}
			if have > f.size {
				have = 0 // rewritten: copy it anew
			}
		}
		todo = append(todo, f)
		offsets = append(offsets, have)
	}
	for i := 0; i < len(todo); {
		args, bytes, j := []string{}, int64(0), i
		for ; j < len(todo) && j-i < mirrorBatchFiles && (j == i || bytes+todo[j].size-offsets[j] <= mirrorBatchBytes); j++ {
			args = append(args, todo[j].rel, strconv.FormatInt(offsets[j], 10))
			bytes += todo[j].size - offsets[j]
		}
		out, errOut, err := run(ctx, 3*time.Minute, remoteSessionFetch, args...)
		if err != nil {
			return runErr(errOut, err)
		}
		got := map[string][]byte{}
		for _, line := range strings.Split(out, "\n") {
			rel, b64, ok := strings.Cut(line, "\t")
			if !ok {
				continue
			}
			if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
				got[rel] = data
			}
		}
		for k := i; k < j; k++ {
			data, ok := got[todo[k].rel]
			if !ok {
				continue
			}
			if err := mirrorWrite(filepath.Join(dir, filepath.FromSlash(todo[k].rel)), offsets[k], data, todo[k].mod); err != nil {
				return err
			}
		}
		i = j
	}
	if only == "" {
		pruneMirror(dir, seen)
	}
	return nil
}

func parseSessionList(out string) []remoteFile {
	var list []remoteFile
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || !remoteSessionRel.MatchString(parts[0]) || strings.Contains(parts[0], "..") {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size <= 0 {
			continue
		}
		f := remoteFile{rel: parts[0], size: size}
		if sec, err := strconv.ParseInt(parts[2], 10, 64); err == nil && sec > 0 {
			f.mod = time.Unix(sec, 0)
		}
		list = append(list, f)
	}
	return list
}

// mirrorWrite puts data at offset (0 = the whole file) and gives the copy
// the remote file's time, which is when the session was last active.
func mirrorWrite(p string, offset int64, data []byte, mod time.Time) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(p, flags|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if offset > 0 {
		if end, err := f.Seek(0, io.SeekEnd); err != nil || end != offset {
			f.Close()
			return errors.New("mirror of " + filepath.Base(p) + " changed while copying")
		}
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !mod.IsZero() {
		_ = os.Chtimes(p, mod, mod)
	}
	return err
}

// pruneMirror removes copies of sessions the machine no longer has.
func pruneMirror(dir string, keep map[string]bool) {
	for _, top := range []string{"claude", "codex"} {
		root := filepath.Join(dir, top)
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err == nil && !keep[filepath.ToSlash(rel)] {
				_ = os.Remove(p)
			}
			return nil
		})
	}
}

func runErr(stderr string, err error) error {
	if stderr != "" {
		return errors.New(stderr)
	}
	return err
}
