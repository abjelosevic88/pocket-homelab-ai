package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Sub-agents of a conversation on another machine: Claude Code writes their
// transcripts there (~/.claude/projects/<dir>/<session>/subagents), so they
// are copied over ssh into DATA_DIR/remote-sessions and read from the copy.
// A copy younger than a few seconds is used as it is (the panel polls).

const remoteSubagentsTTL = 3 * time.Second

// remoteSubagentFiles packs the session's sub-agent folder: $1 session id.
const remoteSubagentFiles = `sid=$1
for d in "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$sid"/subagents; do
  [ -d "$d" ] || continue
  cd -- "$d" && tar -czf - . | base64
  exit 0
done
`

var (
	sessionIDRe      = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	subagentFileRe   = regexp.MustCompile(`^agent-[A-Za-z0-9_-]{1,80}\.(jsonl|meta\.json)$`)
	remoteSubagentMu sync.Mutex
	remoteSubagentAt = map[string]time.Time{}
)

// threadSessionPath is where a Claude thread's session file is read from
// (or "" for Codex threads and sessions that are gone): this server's own,
// or the local copy of another machine's.
func (s *Server) threadSessionPath(r *http.Request, t *store.Thread) string {
	if t.HostID == 0 {
		return s.claudeSessionPath(r, t.Provider, t.AccountID, t.ProviderSessionID)
	}
	if t.Provider != "claude" || !sessionIDRe.MatchString(t.ProviderSessionID) {
		return ""
	}
	h, err := s.host(r.Context(), t.HostID)
	if err != nil || h == nil {
		return ""
	}
	base := filepath.Join(s.cfg.DataDir, "remote-sessions", strconv.FormatInt(h.ID, 10))
	session := filepath.Join(base, t.ProviderSessionID+".jsonl") // never written: only its folder is
	if err := s.mirrorRemoteSubagents(r.Context(), h, t.HostHome, t.ProviderSessionID, filepath.Join(base, t.ProviderSessionID, "subagents")); err != nil {
		s.log.Info("remote sub-agents not copied", "host", h.Name, "session", t.ProviderSessionID, "err", err)
	}
	return session
}

func (s *Server) mirrorRemoteSubagents(ctx context.Context, h *store.Host, home, sid, dir string) error {
	remoteSubagentMu.Lock()
	defer remoteSubagentMu.Unlock()
	if at, ok := remoteSubagentAt[dir]; ok && time.Since(at) < remoteSubagentsTTL {
		return nil
	}
	out, errOut, err := shOn(ctx, h, 30*time.Second, inLogin("claude", home, remoteSubagentFiles), sid)
	if err != nil {
		return scriptErr(errOut, err)
	}
	remoteSubagentAt[dir] = time.Now()
	if strings.TrimSpace(out) == "" {
		return nil // no sub-agents (yet)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return err
	}
	return untarSubagents(raw, dir)
}

// untarSubagents replaces dir with the archive's sub-agent files (plain
// files with the names Claude Code gives them, nothing else).
func untarSubagents(raw []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	tmp := dir + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		name := strings.TrimPrefix(hd.Name, "./")
		if hd.Typeflag != tar.TypeReg || !subagentFileRe.MatchString(name) {
			continue
		}
		p := filepath.Join(tmp, name)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, 200<<20))
		f.Close()
		if err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		_ = os.Chtimes(p, hd.ModTime, hd.ModTime) // unchanged files keep their parse cache
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}
