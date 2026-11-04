package server

// A conversation whose folder is gone (moved, cleaned up) cannot start: the
// page says so once, under the latest message, and offers to move the
// conversation to another folder. Moving carries the CLI's history along:
// Claude Code files a session under its folder's name, so the file is copied
// to the new folder's name (on the machine the conversation runs on);
// Codex sessions do not depend on the folder.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

type folderGoneError struct {
	dir       string
	host      string // "" = this server
	unmounted string // the mount point (from fstab) the folder sits under that is not mounted now
}

func (e *folderGoneError) Error() string {
	where := ""
	if e.host != "" {
		where = " on " + e.host
	}
	if e.unmounted != "" {
		return "The folder " + e.dir + " is not there" + where + ": " + e.unmounted + " is not mounted (its disk did not come back after a restart?). Mount it there (sudo mount " + e.unmounted + "), then send again."
	}
	return "The folder " + e.dir + " no longer exists" + where + ". Move this conversation to another folder to carry on — its history comes along."
}

// unsentWhy is the reason on the message that could not go out.
func (e *folderGoneError) unsentWhy() string {
	if e.unmounted != "" {
		return "Not sent — its disk is not mounted."
	}
	return "Not sent — its folder is gone."
}

// unmountedFor is the mount point fstab lists for dir (or a folder above it)
// while nothing is mounted there now: a disk that did not come back after a
// restart, so moving the conversation is the wrong advice. "" when fstab has
// no such line or the mount table is unknown (no /proc/mounts, e.g. macOS).
func unmountedFor(dir, fstab, mounts string) string {
	if strings.TrimSpace(mounts) == "" || !path.IsAbs(dir) {
		return ""
	}
	points := func(text string) map[string]bool {
		m := map[string]bool{}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || strings.HasPrefix(f[0], "#") {
				continue
			}
			m[path.Clean(strings.ReplaceAll(f[1], `\040`, " "))] = true
		}
		return m
	}
	want, have := points(fstab), points(mounts)
	for p := path.Clean(dir); p != "/"; p = path.Dir(p) {
		if want[p] {
			if have[p] {
				return ""
			}
			return p
		}
	}
	return ""
}

// localUnmounted is unmountedFor on this server.
func localUnmounted(dir string) string {
	fstab, _ := os.ReadFile("/etc/fstab")
	mounts, _ := os.ReadFile("/proc/self/mounts")
	return unmountedFor(dir, string(fstab), string(mounts))
}

// remoteFolderCheck: $1 the folder. "there", or the tables unmountedFor
// reads. Always exits 0: a missing folder is an answer, not an ssh failure.
const remoteFolderCheck = `[ -d "$1" ] && { echo there; exit 0; }
echo "@@fstab"; cat /etc/fstab 2>/dev/null
echo "@@mounts"; cat /proc/mounts 2>/dev/null
exit 0`

const folderGoneKey = "folder-gone"

// folderGoneNotice replaces any earlier one, so it sits under the message
// that just failed and never piles up.
func (rt *engine) folderGoneNotice(threadID string, e *folderGoneError) {
	rt.dropFolderGone(threadID)
	fix := "folder"
	if e.unmounted != "" {
		fix = "" // mounting it fixes this, not a move
	}
	rt.saveItem(threadID, agent.Item{Key: folderGoneKey, Kind: "notice", Level: "error", Fix: fix, Text: e.Error()})
}

// holdUnsent marks the latest message as not sent, with the reason.
func (rt *engine) holdUnsent(threadID, why string) {
	rows, err := rt.s.store.ListItems(rt.ctx, threadID)
	if err != nil {
		return
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind != "user" {
			continue
		}
		var it agent.Item
		if json.Unmarshal(rows[i].Data, &it) == nil && !it.Unsent {
			it.Unsent, it.Queued, it.UnsentWhy = true, false, why
			rt.saveItem(threadID, it)
		}
		return
	}
}

func (rt *engine) dropFolderGone(threadID string) {
	if _, err := rt.s.store.GetItem(rt.ctx, threadID, folderGoneKey); err != nil {
		return
	}
	_ = rt.s.store.DeleteItem(rt.ctx, threadID, folderGoneKey)
	rt.toThread(threadID, sseEvent{Name: "item_removed", Data: map[string]string{"key": folderGoneKey}})
}

// claudeProjectName is the folder name Claude Code files a cwd's sessions under.
var claudeProjectRe = regexp.MustCompile(`[^A-Za-z0-9]`)

func claudeProjectName(cwd string) string { return claudeProjectRe.ReplaceAllString(cwd, "-") }

// moveClaudeSession copies a session file (and its sub-agent logs) next to
// the new folder's other sessions, on this server.
func moveClaudeSession(home, sessionID, cwd string) error {
	src, err := agent.FindClaude(home, sessionID)
	if err != nil {
		return nil // nothing on disk yet: the CLI starts it fresh there
	}
	dstDir := filepath.Join(home, "projects", claudeProjectName(cwd))
	dst := filepath.Join(dstDir, sessionID+".jsonl")
	if src == dst {
		return nil
	}
	if err := copyPath(src, dst); err != nil {
		return err
	}
	side := strings.TrimSuffix(src, ".jsonl")
	if fi, err := os.Stat(side); err == nil && fi.IsDir() {
		return filepath.WalkDir(side, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(side, p)
			return copyPath(p, filepath.Join(dstDir, sessionID, rel))
		})
	}
	return nil
}

func copyPath(src, dst string) error {
	b, err := agent.ReadConfined(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return agent.WriteConfined(dst, b, 0o600)
}

// remoteMoveSession is moveClaudeSession on another machine (its own login).
const remoteMoveSession = `sid=$1; enc=$2
base="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects"
src=$(ls -1t "$base"/*/"$sid".jsonl 2>/dev/null | head -n 1)
[ -n "$src" ] || exit 0
dst="$base/$enc"
[ "$src" = "$dst/$sid.jsonl" ] && exit 0
mkdir -p "$dst" || exit 3
cp -p "$src" "$dst/$sid.jsonl" || exit 3
side="${src%.jsonl}"
[ -d "$side" ] && cp -Rp "$side" "$dst/"
exit 0
`

// handleMoveFolder: POST /threads/{id}/folder {cwd} — the owner moves the
// conversation; the agent starts there on the next message.
func (s *Server) handleMoveFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	var in struct {
		Cwd string `json:"cwd"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Cwd) == "" {
		writeErr(w, http.StatusBadRequest, "pick a folder")
		return
	}
	if t.Source == "ask" {
		writeErr(w, http.StatusBadRequest, "Ask conversations have no folder")
		return
	}
	busy := s.rt.isExternal(t.ID)
	if run := s.rt.get(t.ID); run != nil {
		run.mu.Lock()
		busy = busy || run.running || len(run.pending) > 0 || len(run.queue) > 0
		run.mu.Unlock()
	}
	if busy {
		writeErr(w, http.StatusConflict, "the agent is working: stop it or wait until it has finished")
		return
	}
	ctx := r.Context()
	h, err := s.host(ctx, t.HostID)
	if err != nil {
		s.fail(w, err, "move")
		return
	}
	cwd, err := s.moveSessionTo(ctx, t, h, strings.TrimSpace(in.Cwd))
	if err != nil {
		s.fail(w, err, "move")
		return
	}
	s.rt.stop(t.ID)
	from := t.Cwd
	t.Cwd = cwd
	if t.WorktreeID != 0 {
		if wt, err := s.store.GetWorktreeAny(ctx, t.WorktreeID); err != nil || !within(cwd, wt.Path) {
			t.WorktreeID = 0 // it left its worktree
		}
	}
	if t.Status == "error" {
		t.Status, t.Error = "idle", ""
	}
	if err := s.store.UpdateThread(ctx, t); err != nil {
		s.fail(w, err, "move")
		return
	}
	s.rt.dropFolderGone(t.ID)
	where := ""
	if h != nil {
		where = " on " + h.Name
	}
	s.rt.saveItem(t.ID, agent.Item{Key: "moved-" + time.Now().Format("150405.000"), Kind: "notice", Level: "info",
		Text: "Moved from " + shortHome(from) + " to " + shortHome(cwd) + where + ". The conversation carries on there."})
	s.log.Info("thread moved", "thread", t.ID, "from", from, "to", cwd, "host", t.HostID)
	t, _ = s.store.GetThreadAny(ctx, t.ID)
	s.rt.publishThread(t)
	writeJSON(w, http.StatusOK, s.rt.decorate(t))
}

// moveSessionTo checks the new folder and puts the CLI's session file where
// the CLI will look for it there. It returns the folder as resolved.
func (s *Server) moveSessionTo(ctx context.Context, t *store.Thread, h *store.Host, cwd string) (string, error) {
	sid := t.ProviderSessionID
	if h != nil {
		l, err := s.listRemoteDirs(ctx, h, cwd, false)
		if err != nil {
			return "", err
		}
		if l.Missing != "" {
			return "", badRequest(l.Missing + " does not exist on " + h.Name)
		}
		if t.Provider == "claude" && sid != "" {
			if _, errOut, err := remoteOf(h).Run(ctx, 2*time.Minute, inLogin("claude", t.HostHome, remoteMoveSession), sid, claudeProjectName(l.Path)); err != nil {
				return "", badRequest(h.Name + ": could not move the conversation's history: " + firstNonEmpty(remoteErr(errOut, err), err.Error()))
			}
		}
		return l.Path, nil
	}
	dir, err := s.checkDir(ctx, cwd)
	if err != nil {
		return "", err
	}
	if t.Provider == "claude" && sid != "" {
		home, _ := s.accountHome(ctx, t.Provider, t.AccountID)
		if err := s.ensureSession(ctx, t, sid); err != nil {
			return "", err
		}
		if err := moveClaudeSession(home, sid, dir); err != nil {
			return "", errors.New("could not move the conversation's history: " + err.Error())
		}
	}
	return dir, nil
}

func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
