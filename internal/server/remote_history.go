package server

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// "On this server" and Today also cover the person's other machines: their
// CLI sessions are copied into DATA_DIR/remote-sessions/<host>/mirror
// (agent.MirrorSessions) and read from there. A page waits a moment for a
// fresh copy; a slow or unreachable machine shows its last copy, and the
// copy finishes in the background for the next look.

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
	wait := make([]<-chan struct{}, len(hosts))
	for i, h := range hosts {
		wait[i] = s.syncMirror(h, false)
	}
	timer := time.NewTimer(remoteMirrorWait)
	defer timer.Stop()
	for _, ch := range wait {
		select {
		case <-ch:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	out := make([]hostHomes, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, hostHomes{host: h, homes: agent.MirrorHomes(s.mirrorDir(h))})
	}
	return out
}

type hostHomes struct {
	host  *store.Host
	homes agent.Homes
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
				x.HostID, x.Host, x.Account = hh.host.ID, hh.host.Name, 0
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
				x.HostID, x.Host, x.Account = hh.host.ID, hh.host.Name, 0
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
	var items []agent.Item
	var cwd, title string
	switch provider {
	case "claude":
		if !sessionIDRe.MatchString(sessionID) {
			return nil, badRequest("bad session id")
		}
		dir := s.mirrorDir(h)
		// the latest of this one session, whatever the last full copy missed
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := agent.MirrorSessions(cctx, remoteOf(h), dir, "/"+sessionID+".jsonl")
		cancel()
		path, ferr := agent.FindClaude(filepath.Join(dir, "claude"), sessionID)
		if ferr != nil {
			if err != nil {
				return nil, badRequest(h.Name + ": " + err.Error())
			}
			return nil, badRequest("that Claude Code session is not on " + h.Name)
		}
		if items, cwd, _, err = agent.ReadClaude(path); err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Kind == "user" {
				title = it.Text
				break
			}
		}
	case "codex":
		var err error
		items, cwd, title, err = agent.ReadCodex(ctx, hostBin(h, "codex"), sessionID, nil, remoteOf(h))
		if err != nil {
			return nil, badRequest(h.Name + ": " + err.Error())
		}
	default:
		return nil, badRequest("provider must be claude or codex")
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	def := st.Claude
	if provider == "codex" {
		def = st.Codex
	}
	// folders are the other machine's (one that is gone is caught on send)
	t := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: provider, HostID: h.ID, ProviderSessionID: sessionID, Cwd: cwd,
		Model: def.Model, Effort: def.Effort, Mode: def.Mode, Source: "import", Title: titleFrom(title), LogOffset: -1}
	return s.storeImported(ctx, t, items)
}
