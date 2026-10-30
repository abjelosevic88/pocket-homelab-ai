package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// "On this server" and Today also cover the person's other machines: their
// CLI sessions are copied into DATA_DIR/remote-sessions/<host>/mirror
// (agent.MirrorSessions) and read from there. A machine with a copy already
// shows it at once while a fresh one is made in the background for the
// next look; only a machine never copied before is waited for, a moment.

const (
	remoteMirrorTTL  = 20 * time.Second
	remoteMirrorWait = 4 * time.Second
)

type mirrorState struct {
	mu      sync.Mutex
	at      time.Time     // last finished copy
	running chan struct{} // closed when the copy in progress ends
}

var (
	mirrorsMu sync.Mutex
	mirrors   = map[int64]*mirrorState{}
)

func (s *Server) mirrorDir(h *store.Host) string {
	return filepath.Join(s.cfg.DataDir, "remote-sessions", strconv.FormatInt(h.ID, 10), "mirror")
}

// syncMirror starts a copy of the machine's sessions unless one is running
// or the last is recent, and returns a channel that is closed when the
// copy is done.
func (s *Server) syncMirror(h *store.Host, force bool) <-chan struct{} {
	mirrorsMu.Lock()
	m := mirrors[h.ID]
	if m == nil {
		m = &mirrorState{}
		mirrors[h.ID] = m
	}
	mirrorsMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil {
		return m.running
	}
	done := make(chan struct{})
	if !force && time.Since(m.at) < remoteMirrorTTL {
		close(done)
		return done
	}
	m.running = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := agent.MirrorSessions(ctx, remoteOf(h), s.mirrorDir(h), ""); err != nil {
			s.log.Info("sessions of another machine not copied", "host", h.Name, "err", err)
		}
		m.mu.Lock()
		m.at, m.running = time.Now(), nil
		m.mu.Unlock()
		close(done)
	}()
	return done
}

// remoteHomes are the mirrors of the machines u may use, after waiting a
// moment for fresh copies.
func (s *Server) remoteHomes(ctx context.Context, u *store.User) []hostHomes {
	hosts, _ := s.hostsFor(ctx, u)
	if len(hosts) == 0 {
		return nil
	}
	var wait []<-chan struct{}
	for _, h := range hosts {
		done := s.syncMirror(h, false)
		if _, err := os.Stat(s.mirrorDir(h)); err != nil {
			wait = append(wait, done) // nothing to show yet
		}
	}
	timer := time.NewTimer(remoteMirrorWait)
	defer timer.Stop()
	// one deadline for all of them: the timer fires once, so after it the
	// rest are not waited for (a second slow machine would otherwise block
	// until its copy finished, up to 10 minutes)
wait:
	for _, ch := range wait {
		select {
		case <-ch:
		case <-timer.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	out := make([]hostHomes, 0, len(hosts))
	for _, h := range hosts {
		homes, dirs := agent.MirrorHomes(s.mirrorDir(h), h.Info.Home)
		out = append(out, hostHomes{host: h, homes: homes, dirs: dirs})
	}
	return out
}

type hostHomes struct {
	host  *store.Host
	homes agent.Homes
	dirs  map[int64]string // home id → its login folder there ("" = the default)
}

// remoteFilter: a session one of Pocket AI's threads used is that thread's
// person's alone; the rest are the machine's own work, seen by whoever the
// machine belongs to (and admins, like this server's own sessions).
func (s *Server) remoteFilter(ctx context.Context, u *store.User) func(h *store.Host, provider, sessionID string) bool {
	if u == nil {
		return func(*store.Host, string, string) bool { return false }
	}
	owners := s.store.SessionOwners(ctx)
	return func(h *store.Host, provider, sessionID string) bool {
		if uid, ok := owners[provider+":"+sessionID]; ok {
			return uid == u.ID
		}
		return u.Role == "admin" || s.hostUser(ctx, h) == u.ID
	}
}

func (s *Server) remoteSessions(ctx context.Context, u *store.User) []agent.ServerSession {
	visible := s.remoteFilter(ctx, u)
	var out []agent.ServerSession
	for _, hh := range s.remoteHomes(ctx, u) {
		for _, x := range agent.Sessions(hh.homes, "") {
			if visible(hh.host, x.Provider, x.SessionID) {
				x.HostID, x.Host, x.HostHome, x.Account = hh.host.ID, hh.host.Name, hh.dirs[x.Account], 0
				out = append(out, x)
			}
		}
	}
	return out
}

func (s *Server) remoteRuns(ctx context.Context, u *store.User, from, to time.Time) []agent.Run {
	visible := s.remoteFilter(ctx, u)
	var out []agent.Run
	for _, hh := range s.remoteHomes(ctx, u) {
		for _, x := range agent.Runs(hh.homes, "", from, to) {
			if visible(hh.host, x.Provider, x.SessionID) {
				x.HostID, x.Host, x.HostHome, x.Account = hh.host.ID, hh.host.Name, hh.dirs[x.Account], 0
				out = append(out, x)
			}
		}
	}
	return out
}

// importRemoteSession makes a thread of a session on another machine; it
// runs there again on the next message.
func (s *Server) importRemoteSession(ctx context.Context, u *store.User, h *store.Host, provider, sessionID string) (*store.Thread, error) {
	if !s.remoteFilter(ctx, u)(h, provider, sessionID) {
		return nil, errSessionGone
	}
	if provider != "claude" && provider != "codex" {
		return nil, badRequest("provider must be claude or codex")
	}
	if !sessionIDRe.MatchString(sessionID) {
		return nil, badRequest("bad session id")
	}
	// the latest of this one session (whichever login folder it is in),
	// whatever the last full copy missed
	dir := s.mirrorDir(h)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err := agent.MirrorSessions(cctx, remoteOf(h), dir, sessionID)
	cancel()
	homes, dirs := agent.MirrorHomes(dir, h.Info.Home)
	var found *agent.ServerSession
	for _, x := range agent.Sessions(homes, "") {
		if x.Provider == provider && x.SessionID == sessionID {
			found = &x
			break
		}
	}
	if found == nil {
		if err != nil {
			return nil, badRequest(h.Name + ": " + err.Error())
		}
		return nil, badRequest("that session is not on " + h.Name)
	}
	home := dirs[found.Account]
	var items []agent.Item
	var cwd, title string
	switch provider {
	case "claude":
		if items, cwd, _, err = agent.ReadClaude(found.Path); err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Kind == "user" {
				title = it.Text
				break
			}
		}
	case "codex":
		items, cwd, title, err = agent.ReadCodex(ctx, hostBin(h, "codex"), sessionID, agent.HomeEnv("codex", home), remoteOf(h))
		if err != nil {
			return nil, badRequest(h.Name + ": " + err.Error())
		}
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	def := st.Claude
	if provider == "codex" {
		def = st.Codex
	}
	// folders are the other machine's (one that is gone is caught on send)
	t := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: provider, HostID: h.ID, HostHome: home, ProviderSessionID: sessionID, Cwd: cwd,
		Model: def.Model, Effort: def.Effort, Mode: def.Mode, Source: "import", Title: titleFrom(title), LogOffset: -1}
	return s.storeImported(ctx, t, items)
}

// inLogin runs a remote script on the login folder home ("" = the default
// one): scripts find the CLI's files under ${CLAUDE_CONFIG_DIR:-…}.
func inLogin(provider, home, script string) string {
	if home == "" {
		return script
	}
	v := "CLAUDE_CONFIG_DIR"
	if provider == "codex" {
		v = "CODEX_HOME"
	}
	return v + "=" + agent.ShellQuote(home) + "; export " + v + "\n" + script
}

// remoteEnsureSession copies a session into a login folder on the machine
// when it is only in another one ($1 claude|codex, $2 folder or "" for the
// default, $3 session id), like ensureSession does here.
const remoteEnsureSession = agent.RemoteHomesSh + `kind=$1; to=$2; sid=$3
if [ "$kind" = claude ]; then d0="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; else d0="${CODEX_HOME:-$HOME/.codex}"; fi
[ -n "$to" ] || to=$d0
find1() {
  if [ "$kind" = claude ]; then ls -1t "$1"/projects/*/"$sid".jsonl 2>/dev/null | head -n 1
  else find "$1/sessions" -type f -name "*$sid.jsonl" 2>/dev/null | head -n 1; fi
}
[ -n "$(find1 "$to")" ] && exit 0
found=$(homes "$kind" | while IFS= read -r d; do [ "$d" = "$to" ] && continue; f=$(find1 "$d"); [ -n "$f" ] && { printf '%s\n%s\n' "$d" "$f"; break; }; done)
[ -n "$found" ] || exit 0
from=$(printf '%s\n' "$found" | sed -n 1p); src=$(printf '%s\n' "$found" | sed -n 2p)
rel=${src#"$from"/}
mkdir -p "$to/${rel%/*}" && cp -p "$src" "$to/$rel" || exit 3
side=${src%.jsonl}
[ -d "$side" ] && cp -Rp "$side" "$to/${rel%/*}/"
exit 0
`

// hostMissing is why the CLI cannot run on the machine ("" = it can). The
// check looks at the default login only, so on another login folder only
// "not installed" counts: that login's own state shows when it runs.
func hostMissing(h *store.Host, provider, home string) string {
	why := h.Info.Missing[provider]
	if home != "" && !strings.Contains(why, "not on the PATH") {
		return ""
	}
	return why
}
