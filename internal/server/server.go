// Package server wires the HTTP API, the live agent processes and their
// event streams, recipes and schedules, auth and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	webFS fs.FS
	http  *http.Client

	started  time.Time
	requests atomic.Int64
	errors   atomic.Int64
	changed  atomic.Int64 // bumps on every write; GET /changes

	core       *core.Client
	coreMu     sync.Mutex
	workflowMu sync.Mutex
	coreOK     bool
	coreErr    string

	oidc   *oidcProvider
	oidcMu sync.Mutex

	rt *engine

	wtOnce sync.Once
	wtHold *wtHolds // messages waiting for a worktree's setup: worktrees.go

	provMu   sync.Mutex
	provList []agent.Provider
	provAt   time.Time

	inferenceSlots      chan struct{}
	inferenceModelsMu   sync.Mutex
	inferenceModelCache map[string]inferenceModelEntry

	accts accountState // logins: status cache, sign-ins in progress (accounts.go)
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	ua := "PocketAI/" + Version + " (+https://github.com/abjelosevic88/pocket-homelab-ai)"
	s := &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(),
		accts: accountState{status: map[string]acctStatus{}, logins: map[int64]*loginRun{}}}
	s.inferenceSlots = make(chan struct{}, 4)
	s.rt = newRuntime(s)
	agent.Version = Version
	s.http = &http.Client{Timeout: 60 * time.Second}
	s.core = core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: ua, ServiceCache: time.Minute, ServiceStale: 15 * time.Minute})
	s.oidc = &oidcProvider{issuerURL: cfg.OIDCIssuer}
	return s
}

// isOwner reports whether uid is the account the box was set up with.
func (s *Server) isOwner(ctx context.Context, uid int64) bool {
	return uid > 0 && uid == s.store.OwnerID(ctx)
}

// Bootstrap seeds the first admin (if configured), demo data and starts the workers.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		s.seedUser(ctx, u)
		st, _ := s.store.GetSettings(ctx, 0)
		st.SetupComplete = true
		if err := s.store.SaveBoxSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	_ = s.store.PurgeSessions(ctx)
	cut := s.store.ResetRunning(ctx)
	_ = s.store.FailRunningSetups(ctx) // their output is in the thread; "Run setup again" is in the panel
	s.changed.Store(time.Now().UnixNano())
	if err := s.loadSecretKey(); err != nil {
		return err
	}
	s.rt.start(ctx)
	// say so in the thread, or a turn cut off by a crash or a manual restart
	// just looks stuck
	for _, id := range cut {
		s.rt.saveItem(id, agent.Item{Key: fmt.Sprintf("restart-%d", time.Now().UnixNano()), Kind: "notice", Level: "warn",
			Text: "Pocket AI restarted while the agent was working, so this turn stopped. Send a message (e.g. “continue”) to pick up where it left off."})
	}
	go s.housekeeping(ctx)
	go s.scheduleLoop(ctx)
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET last_status='interrupted' WHERE last_status='running'`)
	go s.workflowLoop(ctx)
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE app_calls SET status=CASE WHEN status='pending' THEN 'denied' WHEN is_write=1 THEN 'unknown' ELSE 'interrupted' END WHERE status IN ('running','pending')`)
	go agent.WarmSessions(s.homes(ctx))    // first parse of every session file takes a while
	go func() { s.providers(ctx, true) }() // warm the provider cache
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// housekeeping prunes expired sessions and images never sent, once an hour.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		_ = s.store.PurgeSessions(ctx)
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM app_calls WHERE is_write=0 AND created_at<?`, store.FormatTime(time.Now().Add(-30*24*time.Hour)))
		if ups, err := s.store.OrphanUploads(ctx, store.FormatTime(time.Now().Add(-24*time.Hour))); err == nil {
			for _, u := range ups {
				s.removeUploadFile(u.Path)
				_ = s.store.DeleteUpload(ctx, u.UserID, u.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// touch records that something changed (the UI polls /changes).
func (s *Server) touch() { s.changed.Store(time.Now().UnixNano()) }

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5, "application/json", "text/html", "text/css", "application/javascript", "text/plain", "text/markdown"))

	r.Get("/pocket-ask.js", s.core.AskWidget("ai"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	// Event streams live outside the request timeout.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/api/v1/events", s.handleUserEvents)
		r.Get("/api/v1/threads/{id}/events", s.handleThreadEvents)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(180 * time.Second))
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)
		r.Post("/client-error", s.handleClientError)
		r.Post("/inference", s.handleInference)
		r.Get("/inference/models", s.handleInference)
		// Conversations other Pocket apps prepare for a person: link_threads.go.
		r.Get("/link/agent-options", s.handleLinkAgentOptions)
		r.Post("/link/drafts", s.handleLinkCreateDraft)
		r.Get("/link/threads", s.handleLinkThreads)
		r.Post("/link/threads/{draft}/unlink", s.handleLinkUnlink)
		r.Delete("/link/threads/{draft}", s.handleLinkDelete)
		r.Get("/link/dirs", s.handleLinkDirs)
		r.Get("/link/active", s.handleLinkActive)
		r.Get("/link/prs", s.handleLinkPRs)
		r.Get("/link/worktrees", s.handleLinkWorktrees)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)
			r.Get("/changes", s.handleChanges)

			// Threads, messages, requests, search, usage: threads_api.go.
			s.threadRoutes(r)
			s.askRoutes(r)
			s.assistantRoutes(r)
			// Images for prompts: uploads.go.
			s.uploadRoutes(r)
			// Workspaces, folder picker, recipes: library_api.go.
			s.libraryRoutes(r)
			// Schedules: schedules.go. CLI history: history.go.
			s.scheduleRoutes(r)
			s.historyRoutes(r)
			// More Claude Code / Codex logins: accounts.go.
			s.accountRoutes(r)
			// Other machines agents run on over ssh: hosts.go.
			s.hostRoutes(r)
			// Git worktrees for threads: worktrees.go (+ worktree_git.go).
			s.worktreeRoutes(r)
			// Drafts other Pocket apps prepared: link_threads.go.
			s.linkRoutes(r)

			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Put("/users/{id}", s.handleUpdateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)

			r.Get("/settings", s.handleGetSettings)
			r.Get("/remembered-folders", s.handleRememberedFolders)
			r.Post("/remembered-folders", s.handleRememberedFolders)
			r.Delete("/remembered-folders", s.handleRememberedFolders)
			r.Get("/settings/app-models", s.handleAppModels)
			r.Put("/settings", s.handlePutSettings)
			r.Get("/settings/system", s.handleSystemInfo)
			r.Post("/notify/test", s.handleTestNotify)

			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)

			r.Get("/backup", s.handleBackup)
		})
	})

	r.NotFound(s.handleSPA)
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

type userError struct{ msg string }

// forbidden is a request the person may not make.
type forbidden struct{ msg string }

func (e forbidden) Error() string { return e.msg }

func errForbidden(msg string) error { return forbidden{msg} }

func (e userError) Error() string { return e.msg }

func badRequest(msg string) error { return userError{msg} }

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	var ue userError
	var fb forbidden
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already there")
		return
	case errors.As(err, &ue):
		writeErr(w, http.StatusBadRequest, ue.msg)
		return
	case errors.As(err, &fb):
		writeErr(w, http.StatusForbidden, fb.msg)
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<20))
	return dec.Decode(v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryInt64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func me(r *http.Request) int64 { return actorID(r.Context()) }

// ---------- Pocket Core ----------

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh.
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "ai", Name: "Pocket AI",
		Description: "Claude Code and Codex on your server, from any browser or phone: start and continue agent sessions, send screenshots, approve what they want to do, one-tap recipes and scheduled checks that tell you when something needs attention.",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "AI",
		Publishes: []string{"ai.turn_finished", "ai.needs_input", "ai.schedule_attention"},
		Offers: []core.Offer{{Scope: core.InferenceScope, Name: "Generate text", Description: "Text-only generation with your Pocket AI account; no tools or conversation access"},
			{Scope: ThreadsScope, Name: "Start coding conversations", Description: "Prepare a conversation with Claude Code or Codex in a folder on this server; you read the prompt and press send yourself"}},
	}
	if s.cfg.BaseURL != "" {
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.cfg.BaseURL + "/api/v1/auth/oidc/callback"}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

// publish persists an event locally; the SDK retries network delivery.

// coreStatus summarises the Pocket Core link for the About page.
func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// handleClientError writes a crash of the web app into the server's log.
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message, Stack, Component, URL, Where, Agent string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	who := ""
	if u, err := s.resolveUser(r); err == nil && u != nil {
		who = u.Email
	}
	s.log.Warn("web app crashed", "user", who, "where", in.Where, "url", in.URL, "message", in.Message, "stack", in.Stack, "component", in.Component, "agent", in.Agent)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"stamp": s.changed.Load()})
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String(),
		"agents_working": s.rt.busyCount() + len(s.inferenceSlots)})
}

// WaitIdle returns once no thread has a turn running or messages queued (or
// after max): a restart then cuts nobody off.
func (s *Server) WaitIdle(ctx context.Context, max time.Duration) {
	start := time.Now()
	nextLog := start.Add(10 * time.Minute)
	for s.rt.busyCount() > 0 || len(s.inferenceSlots) > 0 {
		if max > 0 && time.Since(start) > max {
			return
		}
		if time.Now().After(nextLog) {
			s.log.Info("restart still waiting for agents to finish", "working", s.rt.busyCount()+len(s.inferenceSlots), "waited", time.Since(start).Round(time.Minute))
			nextLog = nextLog.Add(10 * time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_ai_info Build info.\n# TYPE pocket_ai_info gauge\npocket_ai_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_ai_uptime_seconds Process uptime.\n# TYPE pocket_ai_uptime_seconds gauge\npocket_ai_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_ai_http_requests_total HTTP requests served.\n# TYPE pocket_ai_http_requests_total counter\npocket_ai_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_ai_http_errors_total HTTP 5xx responses.\n# TYPE pocket_ai_http_errors_total counter\npocket_ai_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_ai_rows Rows per table.\n# TYPE pocket_ai_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_ai_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/.well-known/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}

// loc returns the person's time zone (their choice, else the box's).
func (s *Server) loc(ctx context.Context, uid int64) *time.Location {
	if tz := s.store.UserSetting(ctx, uid, "timezone"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	l, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		return time.UTC
	}
	return l
}
