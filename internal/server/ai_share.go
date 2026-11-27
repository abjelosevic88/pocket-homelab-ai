package server

// Shared AI: the owner lends one of their Claude logins to someone else on
// the box, with an allowance — so many minutes of agent time a day, about
// so many percent of the plan's weekly limit, or both. Shared use also
// stops for everyone once the plan itself is nearly full ("keep for me").
//
// The person gets a login of their own: an accounts row in their folder
// (shared_from = the lent login, 0 = the server's own) that they pick per
// conversation like any other. Its Claude Code runs in their jail with an
// empty config folder, ANTHROPIC_BASE_URL pointing at a proxy here on a
// loopback port and ANTHROPIC_AUTH_TOKEN = the share's token. The proxy
// checks the allowance, swaps the token for the owner's sign-in (read from
// the lent login's .credentials.json, never shown to the jail), passes the
// request on to Anthropic and counts what came back: tokens, cost and
// active time. So the owner's sign-in never reaches the person, and a
// share stops working the moment it is paused or removed.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// shareUpstream is Anthropic's API, shareCodexUpstream the one Codex uses
// with a ChatGPT sign-in (vars for tests).
var (
	shareUpstream      = "https://api.anthropic.com"
	shareCodexUpstream = "https://chatgpt.com/backend-api/codex"
)

const (
	shareKeepKey     = "ai_share_keep"     // box setting: shared use stops at this % of the plan (default 80, "0" = never)
	sharePortKey     = "ai_share_port"     // box setting: the proxy's loopback port (kept, so held agents find it after a restart)
	shareCapKey      = "ai_share_capacity" // box setting prefix: what a full week of the lent login costs, as last estimated
	shareGap         = 3 * time.Minute     // a pause between requests up to this long is the agent at work (tools)
	shareDefaultKeep = 80
)

// shareState is the proxy's memory.
type shareState struct {
	mu   sync.Mutex
	addr string // 127.0.0.1:port once it listens
	// keyed by the lent login's limits key ("claude", "codex:3")
	plan     map[string]*sharePlan
	clock    map[int64]time.Time // lent account row → end of the last counted activity
	inflight map[int64]float64   // lent account row → cost since its last finished turn
	costs    map[string]*shareCosts
	stale    map[string]bool      // the lent login's token was refused: renew before the next use
	renewed  map[string]time.Time // the lent login was last renewed
	renewMu  sync.Mutex
}

// sharePlan is what Anthropic (or OpenAI) last said about the lent login's plan.
type sharePlan struct {
	five, week float64 // 0..1
	weekReset  time.Time
	at         time.Time
}

type shareCosts struct {
	at    time.Time
	since time.Time
	list  []store.LoginCost
}

func (st *shareState) init() {
	if st.plan == nil {
		st.plan, st.clock, st.inflight = map[string]*sharePlan{}, map[int64]time.Time{}, map[int64]float64{}
		st.costs, st.stale, st.renewed = map[string]*shareCosts{}, map[string]bool{}, map[string]time.Time{}
	}
}

// ---------- the proxy ----------

// shareAddr starts the proxy on a loopback port (the same one as before
// when it is free) and returns where it listens.
func (s *Server) shareAddr(ctx context.Context) (string, error) {
	s.share.mu.Lock()
	defer s.share.mu.Unlock()
	s.share.init()
	if s.share.addr != "" {
		return s.share.addr, nil
	}
	var ln net.Listener
	var err error
	if p, _ := strconv.Atoi(s.store.BoxSetting(ctx, sharePortKey)); p > 0 {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", err
		}
		_ = s.store.SetBoxSetting(ctx, sharePortKey, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.handleShareProxy), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	s.share.addr = ln.Addr().String()
	return s.share.addr, nil
}

// startShareProxy listens at start when anything is shared: agents still
// working from before a restart keep reaching it.
func (s *Server) startShareProxy(ctx context.Context) {
	if list, err := s.store.ListAIShares(ctx); err == nil && len(list) > 0 {
		if _, err := s.shareAddr(ctx); err != nil {
			s.log.Warn("shared AI proxy did not start", "err", err)
		}
	}
}

// homeEnv is the environment of the login whose folder is home: a lent
// login also gets the way to the proxy.
func (s *Server) homeEnv(ctx context.Context, provider, home string) []string {
	env := agent.AccountEnv(provider, home)
	if (provider != "claude" && provider != "codex") || home == "" || !agent.IsPersonPath(home) {
		return env
	}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom != nil && a.Home == home {
			return append(env, s.shareEnv(ctx, a)...)
		}
	}
	return env
}

// shareEnv is what a lent login's CLI needs to reach the proxy: Claude
// Code takes it from the environment; Codex from a model provider in its
// config.toml (written here, so a new port is picked up) whose key is in
// the environment.
func (s *Server) shareEnv(ctx context.Context, a *store.Account) []string {
	sh, err := s.store.GetAIShare(ctx, a.ID)
	if err != nil {
		return nil
	}
	addr, err := s.shareAddr(ctx)
	if err != nil {
		s.log.Warn("shared AI proxy", "err", err)
		return nil
	}
	if agent.IsACP(a.Provider) {
		return s.shareACPEnv(ctx, a, sh.Token, addr)
	}
	if a.Provider == "codex" {
		if err := writeCodexShareConfig(a.Home, addr); err != nil {
			s.log.Warn("shared AI: Codex config", "err", err)
		}
		return []string{shareCodexKeyEnv + "=" + sh.Token}
	}
	return []string{"ANTHROPIC_BASE_URL=http://" + addr, "ANTHROPIC_AUTH_TOKEN=" + sh.Token}
}

const shareCodexKeyEnv = "POCKET_SHARE_TOKEN"

// writeCodexShareConfig points a lent Codex login at the proxy.
func writeCodexShareConfig(home, addr string) error {
	want := "# Written by Pocket AI: this login is lent to you and reaches OpenAI through Pocket AI.\n" +
		"model_provider = \"pocket\"\n\n[model_providers.pocket]\nname = \"Pocket AI\"\n" +
		"base_url = \"http://" + addr + "/codex\"\nwire_api = \"responses\"\nenv_key = \"" + shareCodexKeyEnv + "\"\n"
	p := filepath.Join(home, "config.toml")
	if b, err := os.ReadFile(p); err == nil && string(b) == want {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(want), 0o600)
}

// codexErr answers the way OpenAI's API does (Codex shows the message).
func codexErr(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": kind, "code": kind, "message": msg}})
}

// shareErr answers the way Anthropic's API does, so Claude Code shows the
// message as it is (a 400 stops the turn at once; a 429 would be retried
// for minutes).
func shareErr(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": msg}})
}

var shareClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 10 * time.Minute}}

// shareRoute is where a lent agent's request goes.
type shareRoute struct {
	provider string
	sub      string // OpenCode's provider
	upURL    string
	metered  bool // a model call: counted and held to the allowance
	fail     func(http.ResponseWriter, int, string, string)
	meter    string // anthropic | responses | chat | google
}

// routeShare works out a relay request's route ("" provider = none).
func (s *Server) routeShare(ctx context.Context, r *http.Request) shareRoute {
	path, post := r.URL.Path, r.Method == http.MethodPost
	switch {
	case path == "/v1/messages" || path == "/v1/messages/count_tokens":
		if post {
			return shareRoute{provider: "claude", upURL: shareUpstream + path, metered: path == "/v1/messages", fail: shareErr, meter: "anthropic"}
		}
	case strings.HasPrefix(path, "/codex/"):
		rest := strings.TrimPrefix(path, "/codex")
		if (post && (rest == "/responses" || rest == "/responses/compact")) || (r.Method == http.MethodGet && rest == "/models") {
			return shareRoute{provider: "codex", upURL: shareCodexUpstream + rest, metered: post, fail: codexErr, meter: "responses"}
		}
	case strings.HasPrefix(path, "/gca/v1internal:"):
		rest := strings.TrimPrefix(path, "/gca")
		return shareRoute{provider: "antigravity", upURL: shareGCAUpstream + rest, metered: strings.Contains(rest, "enerateContent"), fail: googleErr, meter: "google"}
	case strings.HasPrefix(path, "/gemini/"):
		rest := strings.TrimPrefix(path, "/gemini")
		return shareRoute{provider: "gemini", upURL: shareGeminiUpstream + rest, metered: strings.Contains(rest, "enerateContent"), fail: googleErr, meter: "google"}
	case strings.HasPrefix(path, "/openai/qwen/"):
		base := strings.TrimRight(s.store.ACPEnv(ctx, "qwen")["OPENAI_BASE_URL"], "/")
		if base != "" {
			return shareRoute{provider: "qwen", upURL: base + strings.TrimPrefix(path, "/openai/qwen"), metered: post, fail: codexErr, meter: "chat"}
		}
	case strings.HasPrefix(path, "/oc/"):
		sub, rest, _ := strings.Cut(strings.TrimPrefix(path, "/oc/"), "/")
		if p, ok := ocProviders[sub]; ok {
			m := "chat"
			if p.auth == "google" {
				m = "google"
			}
			return shareRoute{provider: "opencode", sub: sub, upURL: p.base + "/" + rest, metered: post, fail: codexErr, meter: m}
		}
	}
	return shareRoute{}
}

// googleErr answers the way Google's APIs do.
func googleErr(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	status := "FAILED_PRECONDITION"
	if code == http.StatusUnauthorized {
		status = "UNAUTHENTICATED"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}})
}

// shareKeyOf finds the share key in a request, wherever its agent puts it.
func shareKeyOf(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(v, "psh_") {
		return v
	}
	for _, h := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		if v := r.Header.Get(h); strings.HasPrefix(v, "psh_") {
			return v
		}
	}
	if v := r.URL.Query().Get("key"); strings.HasPrefix(v, "psh_") {
		return v
	}
	return ""
}

func (s *Server) handleShareProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect { // a lent Antigravity login's HTTPS proxy
		s.handleShareConnect(w, r)
		return
	}
	if r.URL.Path == "/api/hello" { // Claude Code's "is it there" check
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	rt := s.routeShare(ctx, r)
	if rt.provider == "" {
		shareErr(w, http.StatusNotFound, "not_found_error", "Pocket AI does not pass this on for a shared login.")
		return
	}
	shareErr := rt.fail
	provider := rt.provider
	tok := shareKeyOf(r)
	sh, err := s.store.GetAIShareByToken(ctx, tok)
	if tok == "" || err != nil {
		shareErr(w, http.StatusUnauthorized, "authentication_error", "This shared login is no longer shared with you. Pick another login for the conversation.")
		return
	}
	a, err := s.store.GetAccount(ctx, sh.AccountID)
	if err != nil || a.SharedFrom == nil || a.UserID == nil || a.Provider != provider {
		shareErr(w, http.StatusUnauthorized, "authentication_error", "This shared login is no longer shared with you.")
		return
	}
	if u, err := s.store.GetUser(ctx, *a.UserID); err != nil || !u.MayRun() {
		shareErr(w, http.StatusForbidden, "permission_error", "An admin turned off running agents for you.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		shareErr(w, http.StatusBadRequest, "invalid_request_error", "The request is too big.")
		return
	}
	if rt.metered {
		if v := s.shareView(ctx, a, sh); v.Blocked != "" {
			shareErr(w, http.StatusBadRequest, "invalid_request_error", v.Blocked)
			return
		}
	}
	from := *a.SharedFrom
	lk := limitsKey(provider, from)
	var token, acct string
	if provider == "claude" || provider == "codex" {
		token, acct, err = s.lentToken(ctx, provider, from)
	} else {
		token, err = s.lentACPCred(ctx, provider, from, rt.sub)
	}
	if err != nil {
		shareErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)

	var rd io.Reader
	if r.Method == http.MethodPost {
		rd = bytes.NewReader(body)
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, rt.upURL, rd)
	if err != nil {
		shareErr(w, http.StatusInternalServerError, "api_error", "Pocket AI could not pass the request on.")
		return
	}
	q := r.URL.Query()
	q.Del("key") // a key in the query is the share key: the owner's goes in a header
	up.URL.RawQuery = q.Encode()
	copyShareHeaders(up.Header, r.Header)
	switch provider {
	case "claude":
		up.Header.Set("Authorization", "Bearer "+token)
		if beta := up.Header.Get("Anthropic-Beta"); !strings.Contains(beta, "oauth-2025-04-20") {
			if beta != "" {
				beta += ","
			}
			up.Header.Set("Anthropic-Beta", beta+"oauth-2025-04-20")
		}
	case "codex":
		up.Header.Set("Authorization", "Bearer "+token)
		up.Header.Set("Chatgpt-Account-Id", acct)
		if up.Header.Get("Originator") == "" {
			up.Header.Set("Originator", "codex_cli_rs")
		}
	case "gemini":
		up.Header.Set("X-Goog-Api-Key", token)
	case "opencode":
		switch ocProviders[rt.sub].auth {
		case "google":
			up.Header.Set("X-Goog-Api-Key", token)
		case "keep": // OpenCode's free models: its own public key
			up.Header.Set("Authorization", "Bearer public")
		default:
			up.Header.Set("Authorization", "Bearer "+token)
		}
	default: // antigravity, qwen
		up.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	res, err := shareClient.Do(up)
	if err != nil {
		if ctx.Err() == nil {
			shareErr(w, http.StatusBadGateway, "api_error", providerName(provider)+"'s service could not be reached: "+err.Error())
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		s.share.mu.Lock()
		s.share.init()
		s.share.stale[lk] = true
		s.share.mu.Unlock()
	}
	s.notePlan(lk, res.Header)
	for k, v := range res.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Connection", "Transfer-Encoding", "Content-Encoding", "Keep-Alive":
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(res.StatusCode)
	meter := &shareMeter{sse: strings.Contains(res.Header.Get("Content-Type"), "event-stream"), model: m.Model, kind: rt.meter, codex: rt.meter == "responses"}
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			meter.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	if rt.metered {
		s.shareRecord(a, start, time.Now(), meter.done())
	}
}

// shareMeter reads the usage out of a response as it passes.
type shareMeter struct {
	sse   bool
	codex bool   // OpenAI's Responses API, not Anthropic's
	kind  string // anthropic | responses | chat (OpenAI-style chat completions) | google
	model string
	line  []byte
	whole []byte // a plain JSON answer (up to 4 MB)
	u     store.ShareUsage
}

type apiUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
}

func (m *shareMeter) take(u *apiUsage, all bool) {
	if u == nil {
		return
	}
	if all || u.Input > 0 {
		m.u.InputTokens = u.Input
	}
	if all || u.CacheWrite > 0 {
		m.u.CacheWrite = u.CacheWrite
	}
	if all || u.CacheRead > 0 {
		m.u.CacheRead = u.CacheRead
	}
	if u.Output > 0 { // cumulative in message_delta
		m.u.OutputTokens = u.Output
	}
}

func (m *shareMeter) feed(b []byte) {
	if !m.sse && len(m.whole) == 0 {
		// a stream does not always say so (the Codex backend)
		t := bytes.TrimSpace(b)
		m.sse = bytes.HasPrefix(t, []byte("event:")) || bytes.HasPrefix(t, []byte("data:"))
	}
	if !m.sse {
		if len(m.whole)+len(b) <= 4<<20 {
			m.whole = append(m.whole, b...)
		}
		return
	}
	m.line = append(m.line, b...)
	for {
		i := bytes.IndexByte(m.line, '\n')
		if i < 0 {
			break
		}
		l := bytes.TrimSpace(m.line[:i])
		m.line = m.line[i+1:]
		if !bytes.HasPrefix(l, []byte("data:")) || !bytes.Contains(l, []byte(`usage`)) {
			continue
		}
		if m.kind == "chat" || m.kind == "google" {
			m.takeOther(bytes.TrimSpace(l[5:]))
			continue
		}
		if m.codex {
			var ev struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(bytes.TrimSpace(l[5:]), &ev) == nil && ev.Type == "response.completed" {
				m.takeOpenAI(ev.Response)
			}
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Model string    `json:"model"`
				Usage *apiUsage `json:"usage"`
			} `json:"message"`
			Usage *apiUsage `json:"usage"`
		}
		if json.Unmarshal(bytes.TrimSpace(l[5:]), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Model != "" {
				m.model = ev.Message.Model
			}
			m.take(ev.Message.Usage, true)
		case "message_delta":
			m.take(ev.Usage, false)
		}
	}
	if len(m.line) > 1<<20 { // no line breaks: not what we expect
		m.line = nil
	}
}

// takeOpenAI reads a Responses API answer's model and usage.
func (m *shareMeter) takeOpenAI(raw []byte) {
	var res struct {
		Model string `json:"model"`
		Usage *struct {
			Input   int64 `json:"input_tokens"`
			Output  int64 `json:"output_tokens"`
			Details struct {
				Cached int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &res) != nil || res.Usage == nil {
		return
	}
	if res.Model != "" {
		m.model = res.Model
	}
	m.u.InputTokens = max(0, res.Usage.Input-res.Usage.Details.Cached) // cached ones are counted in input
	m.u.CacheRead = res.Usage.Details.Cached
	m.u.OutputTokens = res.Usage.Output
}

// takeOther reads the usage of an OpenAI-style chat answer or a Google one
// (Gemini API, or Code Assist's wrapped {"response": …}); a later chunk
// replaces an earlier one (they are running totals).
func (m *shareMeter) takeOther(raw []byte) {
	if m.kind == "chat" {
		var c struct {
			Model string `json:"model"`
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Details    struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &c) != nil || c.Usage == nil {
			return
		}
		if c.Model != "" {
			m.model = c.Model
		}
		m.u.InputTokens = max(0, c.Usage.Prompt-c.Usage.Details.Cached)
		m.u.CacheRead, m.u.OutputTokens = c.Usage.Details.Cached, c.Usage.Completion
		return
	}
	type gUsage struct {
		Prompt     int64 `json:"promptTokenCount"`
		Candidates int64 `json:"candidatesTokenCount"`
		Thoughts   int64 `json:"thoughtsTokenCount"`
		Cached     int64 `json:"cachedContentTokenCount"`
	}
	type gResp struct {
		Usage   *gUsage `json:"usageMetadata"`
		Version string  `json:"modelVersion"`
	}
	var one struct {
		gResp
		Response *gResp `json:"response"`
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil { // a plain answer may be a list of chunks
		for _, it := range list {
			m.takeOther(it)
		}
		return
	}
	if json.Unmarshal(raw, &one) != nil {
		return
	}
	g := &one.gResp
	if one.Response != nil {
		g = one.Response
	}
	if g.Usage == nil {
		return
	}
	if g.Version != "" {
		m.model = g.Version
	}
	m.u.InputTokens = max(0, g.Usage.Prompt-g.Usage.Cached)
	m.u.CacheRead, m.u.OutputTokens = g.Usage.Cached, g.Usage.Candidates+g.Usage.Thoughts
}

func (m *shareMeter) done() store.ShareUsage {
	if (m.kind == "chat" || m.kind == "google") && !m.sse && len(m.whole) > 0 {
		m.takeOther(m.whole)
	} else if m.codex && !m.sse && len(m.whole) > 0 {
		m.takeOpenAI(m.whole)
	} else if !m.sse && len(m.whole) > 0 {
		var res struct {
			Model string    `json:"model"`
			Usage *apiUsage `json:"usage"`
		}
		if json.Unmarshal(m.whole, &res) == nil {
			if res.Model != "" {
				m.model = res.Model
			}
			m.take(res.Usage, true)
		}
	}
	m.u.Requests = 1
	m.u.CostUSD = shareCost(m.model, m.u)
	return m.u
}

// shareCost prices a request at list prices: only to weigh it against the
// rest of the week (the turns' own costs come from the CLI).
func shareCost(model string, u store.ShareUsage) float64 {
	in, out := 5.0, 25.0 // Opus
	switch m := strings.ToLower(model); {
	case strings.Contains(m, "gpt") || strings.Contains(m, "codex") || strings.HasPrefix(m, "o"):
		in, out = 1.25, 10
	case strings.Contains(m, "haiku"):
		in, out = 1, 5
	case strings.Contains(m, "sonnet"):
		in, out = 3, 15
	case strings.Contains(m, "fable"):
		in, out = 15, 75
	case strings.Contains(m, "gemini"):
		in, out = 1.25, 10
	}
	return (float64(u.InputTokens)*in + float64(u.CacheWrite)*in*1.25 + float64(u.CacheRead)*in*0.1 + float64(u.OutputTokens)*out) / 1e6
}

// shareRecord counts one request: its active time (requests close together
// are one stretch of work, the pauses between them the agent's tools) and
// what it used.
func (s *Server) shareRecord(a *store.Account, start, end time.Time, u store.ShareUsage) {
	s.share.mu.Lock()
	s.share.init()
	cursor := s.share.clock[a.ID]
	var d time.Duration
	if !cursor.IsZero() && start.Sub(cursor) < shareGap {
		if end.After(cursor) {
			d = end.Sub(cursor)
		}
	} else {
		d = end.Sub(start)
	}
	if end.After(cursor) {
		s.share.clock[a.ID] = end
	}
	s.share.inflight[a.ID] += u.CostUSD
	s.share.mu.Unlock()
	u.ActiveMS = d.Milliseconds()
	if err := s.store.AddShareUsage(context.Background(), a.ID, end, u); err != nil {
		s.log.Warn("shared AI usage not saved", "account", a.ID, "err", err)
	}
}

// shareTurnDone: the turn's cost is in the turns table now (runtime).
func (s *Server) shareTurnDone(accountID int64) {
	s.share.mu.Lock()
	if s.share.inflight != nil {
		delete(s.share.inflight, accountID)
	}
	s.share.mu.Unlock()
}

// notePlan keeps what Anthropic says about the plan with each answer.
func (s *Server) notePlan(lk string, h http.Header) {
	week, okW := headerFloat(h, "Anthropic-Ratelimit-Unified-7d-Utilization")
	five, okF := headerFloat(h, "Anthropic-Ratelimit-Unified-5h-Utilization")
	var reset time.Time
	if sec, err := strconv.ParseInt(h.Get("Anthropic-Ratelimit-Unified-7d-Reset"), 10, 64); err == nil && sec > 0 {
		reset = time.Unix(sec, 0)
	}
	// Codex: a primary and a secondary window, each with its length
	for _, w := range []string{"Primary", "Secondary"} {
		used, ok := headerFloat(h, "X-Codex-"+w+"-Used-Percent")
		mins, _ := headerFloat(h, "X-Codex-"+w+"-Window-Minutes")
		if !ok || mins <= 0 {
			continue
		}
		if mins >= 7*24*60 {
			week, okW = used/100, true
			if sec, err := strconv.ParseInt(h.Get("X-Codex-"+w+"-Reset-At"), 10, 64); err == nil && sec > 0 {
				reset = time.Unix(sec, 0)
			}
		} else {
			five, okF = used/100, true
		}
	}
	if !okW && !okF {
		return
	}
	s.share.mu.Lock()
	defer s.share.mu.Unlock()
	s.share.init()
	p := s.share.plan[lk]
	if p == nil {
		p = &sharePlan{}
		s.share.plan[lk] = p
	}
	if okW {
		p.week = week
	}
	if okF {
		p.five = five
	}
	if !reset.IsZero() {
		p.weekReset = reset
	}
	p.at = time.Now()
}

func headerFloat(h http.Header, k string) (float64, bool) {
	v := h.Get(k)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// ---------- the owner's sign-in ----------

// lentHome is the config folder of a login that may be lent: the server's
// own (0) or one of the owner's named Claude Code / Codex logins.
func (s *Server) lentHome(ctx context.Context, provider string, from int64) (string, error) {
	if provider != "claude" && provider != "codex" {
		return "", errors.New("only Claude Code and Codex logins can be shared")
	}
	if from == 0 {
		return s.defaultHome(provider), nil
	}
	a, err := s.store.GetAccount(ctx, from)
	if err != nil || a.Provider != provider || a.SharedFrom != nil || agent.IsPersonPath(a.Home) {
		return "", errors.New("that login cannot be shared")
	}
	return a.Home, nil
}

// readLentToken reads a login's access token (and, for Codex, its ChatGPT
// account id) and when it runs out.
func readLentToken(provider, home string) (tok, acct string, exp time.Time, err error) {
	if provider == "codex" {
		return readCodexToken(home)
	}
	tok, exp, err = readClaudeToken(home)
	return
}

func readClaudeToken(home string) (string, time.Time, error) {
	raw, err := os.ReadFile(filepath.Join(home, ".credentials.json"))
	if err != nil {
		return "", time.Time{}, err
	}
	var c struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.OAuth.AccessToken == "" {
		return "", time.Time{}, errors.New("no sign-in in it")
	}
	return c.OAuth.AccessToken, time.UnixMilli(c.OAuth.ExpiresAt), nil
}

// readCodexToken reads a ChatGPT sign-in from auth.json (an API key login
// cannot be lent: it has no plan to share).
func readCodexToken(home string) (string, string, time.Time, error) {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return "", "", time.Time{}, err
	}
	var c struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Tokens.AccessToken == "" {
		return "", "", time.Time{}, errors.New("no ChatGPT sign-in in it")
	}
	return c.Tokens.AccessToken, c.Tokens.AccountID, jwtExpiry(c.Tokens.AccessToken), nil
}

// jwtExpiry reads "exp" from a JWT without checking it (far future when
// there is none: the upstream's 401 says when it ran out).
func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) == 3 {
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			var c struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(b, &c) == nil && c.Exp > 0 {
				return time.Unix(c.Exp, 0)
			}
		}
	}
	return time.Now().Add(24 * time.Hour)
}

// lentToken is the owner's access token for the lent login, renewed first
// when it is about to run out (by the CLI itself: it writes the new one).
func (s *Server) lentToken(ctx context.Context, provider string, from int64) (string, string, error) {
	owner := s.ownerName(ctx)
	home, err := s.lentHome(ctx, provider, from)
	if err != nil {
		return "", "", errors.New(owner + " no longer shares this login.")
	}
	notHere := errors.New(owner + "'s login cannot be used right now: it is not signed in on this server.")
	tok, acct, exp, err := readLentToken(provider, home)
	if err != nil {
		return "", "", notHere
	}
	lk := limitsKey(provider, from)
	s.share.mu.Lock()
	s.share.init()
	stale := s.share.stale[lk]
	s.share.mu.Unlock()
	if stale || time.Until(exp) < 3*time.Minute {
		s.renewLent(ctx, provider, from, home)
		if tok, acct, exp, err = readLentToken(provider, home); err != nil {
			return "", "", notHere
		}
	}
	if time.Until(exp) <= 0 {
		return "", "", errors.New(owner + "'s sign-in on this server has run out. They need to sign in again (Settings → Machines).")
	}
	return tok, acct, nil
}

// renewLent lets the CLI renew the lent login's sign-in: a tiny question
// on it (at most once a minute).
func (s *Server) renewLent(ctx context.Context, provider string, from int64, home string) {
	s.share.renewMu.Lock()
	defer s.share.renewMu.Unlock()
	lk := limitsKey(provider, from)
	s.share.mu.Lock()
	last := s.share.renewed[lk]
	s.share.mu.Unlock()
	if time.Since(last) < time.Minute {
		return
	}
	var env []string
	if from != 0 {
		env = agent.AccountEnv(provider, home)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	var err error
	if provider == "codex" {
		dir, derr := os.MkdirTemp("", "pocket-share-renew-")
		if derr != nil {
			return
		}
		defer os.RemoveAll(dir)
		cmd := exec.CommandContext(cctx, s.cfg.CodexBin, "exec", "--skip-git-repo-check", "-C", dir, "Reply with the single word: ok")
		cmd.Env = append(os.Environ(), env...)
		_, err = cmd.CombinedOutput()
	} else {
		_, _, err = agent.CompleteClaude(cctx, agent.Options{Bin: s.cfg.ClaudeBin, Cwd: s.cfg.Home, Model: "haiku", Env: env, Log: s.log}, "Reply with the single word: ok")
	}
	s.share.mu.Lock()
	s.share.renewed[lk] = time.Now()
	if err == nil {
		delete(s.share.stale, lk)
	}
	s.share.mu.Unlock()
	if err != nil {
		s.log.Warn("shared AI: renewing the lent login failed", "login", lk, "err", err)
	}
}

func (s *Server) ownerName(ctx context.Context) string {
	if u, err := s.store.GetUser(ctx, s.store.OwnerID(ctx)); err == nil {
		if n := strings.Fields(displayName(u)); len(n) > 0 {
			return n[0]
		}
	}
	return "The owner"
}

// ---------- the allowance ----------

// shareView is a lent login's allowance and how much of it is used.
type shareView struct {
	From        int64   `json:"from"`     // the lent login (0 = the server's own)
	Provider    string  `json:"provider"` // claude | codex
	FromLabel   string  `json:"from_label"`
	By          string  `json:"by"` // whose it is
	MinutesDay  int     `json:"minutes_day"`
	UsedMinutes float64 `json:"used_minutes"` // today
	PercentWeek float64 `json:"percent_week"`
	UsedPercent float64 `json:"used_percent"` // of the plan's week, estimated
	// PercentKnown: the plan's weekly limit is known well enough to say.
	PercentKnown bool    `json:"percent_known"`
	WeekResets   string  `json:"week_resets,omitempty"`
	Paused       bool    `json:"paused"`
	PlanUsed     float64 `json:"plan_used"` // the lent login's fuller window, 0..1
	Keep         int     `json:"keep"`      // shared use stops at this % of the plan
	CostToday    float64 `json:"cost_today"`
	Requests     int64   `json:"requests_today"`
	Blocked      string  `json:"blocked,omitempty"` // why it cannot be used now
}

func localMidnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func (s *Server) shareKeep(ctx context.Context) int {
	v := s.store.BoxSetting(ctx, shareKeepKey)
	if v == "" {
		return shareDefaultKeep
	}
	n, _ := strconv.Atoi(v)
	return n
}

// lentPlan: the lent login's plan as last seen (the proxy's answers, else
// the limits Pocket AI reads for the Usage page).
func (s *Server) lentPlan(provider string, from int64) (five, week float64, reset time.Time, known bool) {
	lk := limitsKey(provider, from)
	s.share.mu.Lock()
	s.share.init()
	p := s.share.plan[lk]
	if p != nil && time.Since(p.at) < 30*time.Minute {
		five, week, reset, known = p.five, p.week, p.weekReset, true
	}
	s.share.mu.Unlock()
	s.rt.mu.Lock()
	l := s.rt.limits[lk]
	if l != nil {
		for _, w := range l.Windows {
			switch w.ID {
			case "five_hour", "codex:300":
				if !known {
					five = w.Used
				}
			case "seven_day", "codex:10080":
				if !known {
					week = w.Used
				}
				if reset.IsZero() {
					reset = store.ParseTime(w.ResetsAt)
				}
			}
		}
		known = known || len(l.Windows) > 0
	}
	s.rt.mu.Unlock()
	return
}

// loginGroup: the limits keys of every login on the same Anthropic account
// as the lent one (they all draw on its plan).
func (s *Server) loginGroup(provider string, from int64) map[string]bool {
	key := limitsKey(provider, from)
	out := map[string]bool{key: true}
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	if l := s.rt.limits[key]; l != nil && l.Ident != "" {
		for k, o := range s.rt.limits {
			if o.Ident == l.Ident {
				out[k] = true
			}
		}
	}
	return out
}

// weekCosts: what the turns since the week began cost per login (kept 30 s).
func (s *Server) weekCosts(ctx context.Context, provider string, from int64, since time.Time) []store.LoginCost {
	lk := limitsKey(provider, from)
	s.share.mu.Lock()
	c := s.share.costs[lk]
	s.share.mu.Unlock()
	if c != nil && time.Since(c.at) < 30*time.Second && c.since.Equal(since) {
		return c.list
	}
	list, err := s.store.TurnCostsSince(ctx, provider, since)
	if err != nil {
		return nil
	}
	s.share.mu.Lock()
	s.share.costs[lk] = &shareCosts{at: time.Now(), since: since, list: list}
	s.share.mu.Unlock()
	return list
}

// shareView works out a lent login's allowance now.
func (s *Server) shareView(ctx context.Context, a *store.Account, sh *store.AIShare) shareView {
	from := int64(0)
	if a.SharedFrom != nil {
		from = *a.SharedFrom
	}
	owner := s.ownerName(ctx)
	v := shareView{From: from, Provider: a.Provider, By: owner, MinutesDay: sh.MinutesDay, PercentWeek: sh.PercentWeek, Paused: sh.Paused, Keep: s.shareKeep(ctx)}
	v.FromLabel = "Server login"
	switch a.Provider {
	case "gemini", "qwen":
		v.FromLabel = "API key"
	case "opencode":
		v.FromLabel = "Free models and keys"
	}
	if from != 0 {
		if src, err := s.store.GetAccount(ctx, from); err == nil {
			v.FromLabel = src.Label
		}
	}
	now := time.Now()
	today, _ := s.store.ShareUsageSince(ctx, a.ID, localMidnight(now))
	v.UsedMinutes = float64(today.ActiveMS) / 60000
	v.CostToday, v.Requests = today.CostUSD, today.Requests

	five, week, reset, known := s.lentPlan(a.Provider, from)
	v.PlanUsed = max(five, week)
	if !reset.IsZero() {
		v.WeekResets = store.FormatTime(reset)
	}
	// About how much of the plan's week this person used: their cost in it
	// against what the whole week costs (what all its logins spent so far
	// over how full it is). Use of the plan outside Pocket AI is not seen,
	// which makes their share look bigger, never smaller.
	{
		begin := now.Add(-7 * 24 * time.Hour)
		if !reset.IsZero() && reset.After(now) {
			begin = reset.Add(-7 * 24 * time.Hour)
		}
		group := s.loginGroup(a.Provider, from)
		lent := s.lentRows(ctx, a.Provider, from)
		var total, mine float64
		for _, c := range s.weekCosts(ctx, a.Provider, from, begin) {
			key := limitsKey(c.Provider, c.AccountID)
			if c.HostID > 0 {
				key = hostLimitsKey(c.Provider, c.HostID, c.HostHome)
			}
			if group[key] || (c.HostID == 0 && lent[c.AccountID]) {
				total += c.CostUSD
			}
			if c.HostID == 0 && c.AccountID == a.ID {
				mine += c.CostUSD
			}
		}
		s.share.mu.Lock()
		for id, cost := range s.share.inflight {
			if lent[id] {
				total += cost
			}
		}
		mine += s.share.inflight[a.ID]
		s.share.mu.Unlock()
		capKey := shareCapKey + "." + strconv.FormatInt(from, 10)
		if a.Provider != "claude" {
			capKey = shareCapKey + "." + a.Provider + "." + strconv.FormatInt(from, 10)
		}
		var capacity float64
		if known && week >= 0.03 && total > 0 {
			capacity = total / week
			_ = s.store.SetBoxSetting(ctx, capKey, strconv.FormatFloat(capacity, 'f', 2, 64))
		} else {
			capacity, _ = strconv.ParseFloat(s.store.BoxSetting(ctx, capKey), 64)
		}
		if capacity > 0 {
			v.PercentKnown = true
			v.UsedPercent = mine / capacity * 100
		}
	}

	switch {
	case sh.Paused:
		v.Blocked = owner + " paused your use of their AI for now."
	case v.Keep > 0 && known && v.PlanUsed*100 >= float64(v.Keep):
		v.Blocked = fmt.Sprintf("%s's plan is %d%% used, and shared use stops at %d%% so they keep the rest. It frees up again when the plan resets.", owner, int(v.PlanUsed*100), v.Keep)
	case sh.MinutesDay > 0 && v.UsedMinutes >= float64(sh.MinutesDay):
		v.Blocked = fmt.Sprintf("You have used today's %s of AI from %s. It comes back at midnight. You can switch this conversation to a login of your own.", minutesText(sh.MinutesDay), owner)
	case sh.PercentWeek > 0 && v.PercentKnown && v.UsedPercent >= sh.PercentWeek:
		when := "when their plan's week starts again"
		if !reset.IsZero() {
			when = "on " + reset.In(time.Local).Format("Monday at 15:04")
		}
		v.Blocked = fmt.Sprintf("You have used your %s%% of %s's weekly plan. It comes back %s. You can switch this conversation to a login of your own.", trimFloat(sh.PercentWeek), owner, when)
	}
	return v
}

func minutesText(m int) string {
	switch {
	case m%60 == 0 && m == 60:
		return "hour"
	case m%60 == 0:
		return strconv.Itoa(m/60) + " hours"
	}
	return strconv.Itoa(m) + " minutes"
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// lentRows: the lent account rows made from one login.
func (s *Server) lentRows(ctx context.Context, provider string, from int64) map[int64]bool {
	out := map[int64]bool{}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom != nil && *a.SharedFrom == from && a.Provider == provider {
			out[a.ID] = true
		}
	}
	return out
}

// shareBlocked says why a conversation on this account cannot go on now
// ("" = it can, or it is not a lent login).
func (s *Server) shareBlocked(ctx context.Context, accountID int64) string {
	if accountID == 0 {
		return ""
	}
	a, err := s.store.GetAccount(ctx, accountID)
	if err != nil || a.SharedFrom == nil {
		return ""
	}
	sh, err := s.store.GetAIShare(ctx, a.ID)
	if err != nil {
		return "This login is no longer shared with you. Pick another login for the conversation."
	}
	return s.shareView(ctx, a, sh).Blocked
}

// ---------- admin API ----------

func (s *Server) aiShareRoutes(r chi.Router) {
	r.Get("/ai-shares", s.handleListShares)
	r.Post("/ai-shares", s.handleCreateShare)
	r.Put("/ai-shares/settings", s.handleShareSettings)
	r.Patch("/ai-shares/{id}", s.handlePatchShare)
	r.Delete("/ai-shares/{id}", s.handleDeleteShare)
}

type lendable struct {
	ID       int64   `json:"id"` // 0 = the server's own login
	Provider string  `json:"provider"`
	Label    string  `json:"label"`
	Email    string  `json:"email,omitempty"`
	Plan     string  `json:"plan,omitempty"`
	SignedIn bool    `json:"signed_in"`
	PlanUsed float64 `json:"plan_used"`
	// PlanKnown: its provider says how full the plan is (a share of the week
	// can be set); Why: why it cannot be lent now.
	PlanKnown bool   `json:"plan_known"`
	Why       string `json:"why,omitempty"`
}

type aiSharePerson struct {
	ID     int64      `json:"id"`
	Name   string     `json:"name"`
	Email  string     `json:"email"`
	Role   string     `json:"role"`
	CanRun bool       `json:"can_run"`
	Own    bool       `json:"own_folder"` // works in a folder of their own (needed to share)
	Shares []shareRow `json:"shares"`
}

type shareRow struct {
	AccountID int64  `json:"account_id"`
	Label     string `json:"label"`
	shareView
}

// lendables lists the logins the owner may lend.
func (s *Server) lendables(ctx context.Context) []lendable {
	out := []lendable{{ID: 0, Provider: "claude", Label: "Server login"}, {ID: 0, Provider: "codex", Label: "Server login"}}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom == nil && !agent.IsPersonPath(a.Home) {
			out = append(out, lendable{ID: a.ID, Provider: a.Provider, Label: a.Label})
		}
	}
	for _, ag := range agent.ACPAgents {
		if !shareable(ag.ID) {
			continue
		}
		label := "Server login"
		switch ag.ID {
		case "gemini", "qwen":
			label = "Your API key"
		case "opencode":
			label = "Free models and your keys"
		}
		out = append(out, lendable{ID: 0, Provider: ag.ID, Label: label})
		if ag.Logins {
			more, _ := s.store.ListAgentAccounts(ctx)
			for _, a := range more {
				if a.Provider == ag.ID && a.SharedFrom == nil && !agent.IsPersonPath(a.Home) {
					out = append(out, lendable{ID: a.ID, Provider: ag.ID, Label: a.Label})
				}
			}
		}
	}
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(l *lendable) {
			defer wg.Done()
			l.PlanKnown = sharePlanKnown(l.Provider)
			if agent.IsACP(l.Provider) {
				l.Why = s.lentACPReady(ctx, l.Provider, l.ID)
				l.SignedIn = l.Why == ""
				if home, _ := s.lentACPHome(ctx, l.Provider, l.ID); home != "" && l.SignedIn {
					if ag, ok := agent.ACPAgentByID(l.Provider); ok {
						l.Email, _ = ag.LoginStatus(home)
					}
				}
				return
			}
			home, _ := s.lentHome(ctx, l.Provider, l.ID)
			st := s.accountStatus(ctx, l.Provider, l.ID, home, false)
			_, _, _, terr := readLentToken(l.Provider, home) // what the proxy needs
			l.SignedIn, l.Email, l.Plan = terr == nil, st.Email, st.Plan
			five, week, _, _ := s.lentPlan(l.Provider, l.ID)
			l.PlanUsed = max(five, week)
		}(&out[i])
	}
	wg.Wait()
	return out
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		s.fail(w, err, "people")
		return
	}
	accts, _ := s.store.ListAccounts(ctx)
	people := []aiSharePerson{}
	for _, u := range users {
		if s.isOwner(ctx, u.ID) {
			continue
		}
		p := aiSharePerson{ID: u.ID, Name: displayName(&u), Email: u.Email, Role: u.Role, CanRun: u.MayRun(), Own: s.jailed(ctx, u.ID), Shares: []shareRow{}}
		for _, a := range accts {
			if a.SharedFrom == nil || a.UserID == nil || *a.UserID != u.ID {
				continue
			}
			sh, err := s.store.GetAIShare(ctx, a.ID)
			if err != nil {
				continue
			}
			p.Shares = append(p.Shares, shareRow{AccountID: a.ID, Label: a.Label, shareView: s.shareView(ctx, a, sh)})
		}
		people = append(people, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keep": s.shareKeep(ctx), "logins": s.lendables(ctx), "people": people})
}

type shareInput struct {
	UserID      int64    `json:"user_id"`
	Provider    string   `json:"provider"` // claude (default) | codex
	From        *int64   `json:"from"`
	MinutesDay  *int     `json:"minutes_day"`
	PercentWeek *float64 `json:"percent_week"`
	Paused      *bool    `json:"paused"`
}

func (in shareInput) apply(sh *store.AIShare) error {
	if in.MinutesDay != nil {
		if *in.MinutesDay < 0 || *in.MinutesDay > 24*60 {
			return badRequest("minutes a day must be between 0 (no limit) and 1440")
		}
		sh.MinutesDay = *in.MinutesDay
	}
	if in.PercentWeek != nil {
		if *in.PercentWeek < 0 || *in.PercentWeek > 100 {
			return badRequest("the share of the week must be between 0 (no limit) and 100")
		}
		sh.PercentWeek = *in.PercentWeek
	}
	if in.Paused != nil {
		sh.Paused = *in.Paused
	}
	return nil
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	var in shareInput
	if err := decode(r, &in); err != nil || in.From == nil {
		writeErr(w, http.StatusBadRequest, "say whom to share which login with")
		return
	}
	person, err := s.store.GetUser(ctx, in.UserID)
	if err != nil {
		s.fail(w, err, "person")
		return
	}
	if s.isOwner(ctx, person.ID) || !s.jailed(ctx, person.ID) {
		writeErr(w, http.StatusBadRequest, displayName(person)+" already runs on this server's own logins, so there is nothing to share with them.")
		return
	}
	if in.Provider == "" {
		in.Provider = "claude"
	}
	if !shareable(in.Provider) {
		writeErr(w, http.StatusBadRequest, providerName(in.Provider)+" cannot be shared")
		return
	}
	if agent.IsACP(in.Provider) {
		if why := s.lentACPReady(ctx, in.Provider, *in.From); why != "" {
			writeErr(w, http.StatusBadRequest, providerName(in.Provider)+": "+why+".")
			return
		}
		if !sharePlanKnown(in.Provider) {
			z := 0.0
			in.PercentWeek = &z // its provider does not say how full the plan is
		}
	} else if srcHome, err := s.lentHome(ctx, in.Provider, *in.From); err != nil {
		s.fail(w, badRequest(err.Error()), "share")
		return
	} else if _, _, _, err := readLentToken(in.Provider, srcHome); err != nil {
		msg := "That login is not signed in on this server (or keeps its sign-in in a keychain), so it cannot be shared."
		if in.Provider == "codex" {
			msg = "That Codex login is not signed in with ChatGPT on this server (an API key login has no plan to share)."
		}
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// one share per person and login: change it instead
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom != nil && *a.SharedFrom == *in.From && a.Provider == in.Provider && a.UserID != nil && *a.UserID == person.ID {
			sh, err := s.store.GetAIShare(ctx, a.ID)
			if err != nil {
				sh = &store.AIShare{AccountID: a.ID, Token: "psh_" + store.RandomToken(24)}
			}
			if err := in.apply(sh); err != nil {
				s.fail(w, err, "share")
				return
			}
			if err := s.store.SaveAIShare(ctx, sh); err != nil {
				s.fail(w, err, "share")
				return
			}
			s.rt.publishAccounts()
			writeJSON(w, http.StatusOK, shareRow{AccountID: a.ID, Label: a.Label, shareView: s.shareView(ctx, a, sh)})
			return
		}
	}
	if err := s.ensurePerson(person.ID); err != nil {
		s.fail(w, err, "person folder")
		return
	}
	srcLabel := providerName(in.Provider)
	if *in.From != 0 && !agent.IsACP(in.Provider) {
		if src, err := s.store.GetAccount(ctx, *in.From); err == nil {
			srcLabel = src.Label
		}
	}
	if *in.From != 0 && agent.IsACP(in.Provider) {
		if src, err := s.store.GetAccount(ctx, *in.From); err == nil {
			srcLabel = providerName(in.Provider) + " " + src.Label
		}
	}
	base := filepath.Join(s.personHome(person.ID), "."+in.Provider+"-shared")
	home := base
	for i := 2; exists(home); i++ {
		home = base + "-" + strconv.Itoa(i)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		s.fail(w, err, "shared login folder")
		return
	}
	from := *in.From
	uid := person.ID
	a := &store.Account{Provider: in.Provider, Label: oneLine(s.ownerName(ctx)+"'s "+srcLabel, 60), Home: home, UserID: &uid, SharedFrom: &from}
	if err := s.store.CreateAccount(ctx, a); err != nil {
		s.fail(w, err, "shared login")
		return
	}
	sh := &store.AIShare{AccountID: a.ID, Token: "psh_" + store.RandomToken(24)}
	if err := in.apply(sh); err != nil {
		_ = s.store.DeleteAccount(ctx, a.ID)
		s.fail(w, err, "share")
		return
	}
	if err := s.store.SaveAIShare(ctx, sh); err != nil {
		_ = s.store.DeleteAccount(ctx, a.ID)
		s.fail(w, err, "share")
		return
	}
	if !person.CanRun && person.Role != "admin" { // a share is no use without it
		person.CanRun = true
		_ = s.store.UpdateUser(ctx, person)
	}
	_, _ = s.shareAddr(ctx)
	s.rt.publishAccounts()
	writeJSON(w, http.StatusCreated, shareRow{AccountID: a.ID, Label: a.Label, shareView: s.shareView(ctx, a, sh)})
}

// sharedAccount is the lent login of the request (admins only).
func (s *Server) sharedAccount(w http.ResponseWriter, r *http.Request) (*store.Account, *store.AIShare) {
	if !s.requireAdmin(w, r) {
		return nil, nil
	}
	a, err := s.store.GetAccount(r.Context(), idParam(r, "id"))
	if err != nil || a.SharedFrom == nil {
		writeErr(w, http.StatusNotFound, "that shared login is gone")
		return nil, nil
	}
	sh, err := s.store.GetAIShare(r.Context(), a.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "that shared login is gone")
		return nil, nil
	}
	return a, sh
}

func (s *Server) handlePatchShare(w http.ResponseWriter, r *http.Request) {
	a, sh := s.sharedAccount(w, r)
	if a == nil {
		return
	}
	var in shareInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := in.apply(sh); err != nil {
		s.fail(w, err, "share")
		return
	}
	if err := s.store.SaveAIShare(r.Context(), sh); err != nil {
		s.fail(w, err, "share")
		return
	}
	s.rt.publishAccounts()
	writeJSON(w, http.StatusOK, shareRow{AccountID: a.ID, Label: a.Label, shareView: s.shareView(r.Context(), a, sh)})
}

// handleDeleteShare stops sharing: the login is gone from their list at
// once and its token stops working (their conversations stay, and move to
// a login of their own when they go on).
func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	a, _ := s.sharedAccount(w, r)
	if a == nil {
		return
	}
	if err := s.store.DeleteAccount(r.Context(), a.ID); err != nil {
		s.fail(w, err, "stop sharing")
		return
	}
	s.rt.stopAccount(a.ID)
	s.rt.dropLimits(limitsKey(a.Provider, a.ID))
	s.shareTurnDone(a.ID)
	s.rt.publishAccounts()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShareSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Keep *int `json:"keep"`
	}
	if err := decode(r, &in); err != nil || in.Keep == nil || *in.Keep < 0 || *in.Keep > 100 {
		writeErr(w, http.StatusBadRequest, "keep must be a percentage (0 = never stop)")
		return
	}
	if err := s.store.SetBoxSetting(r.Context(), shareKeepKey, strconv.Itoa(*in.Keep)); err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"keep": *in.Keep})
}

// sharedStatus is how a lent login looks in the person's list: signed in
// when the owner's is, without saying who that is.
func (s *Server) sharedStatus(ctx context.Context, a *store.Account) acctStatus {
	from := *a.SharedFrom
	if agent.IsACP(a.Provider) {
		st := acctStatus{CheckedAt: store.Now(), SignedIn: true}
		if why := s.lentACPReady(ctx, a.Provider, from); why != "" {
			st.SignedIn, st.Error = false, s.ownerName(ctx)+"'s "+providerName(a.Provider)+" cannot be used right now: "+why+"."
		}
		return st
	}
	home, err := s.lentHome(ctx, a.Provider, from)
	st := acctStatus{CheckedAt: store.Now()}
	if err != nil {
		st.Error = s.ownerName(ctx) + " no longer shares this login."
		return st
	}
	// signed in = its sign-in is in the file the proxy reads (the CLI's
	// own answer only adds the plan)
	_, _, _, terr := readLentToken(a.Provider, home)
	src := s.accountStatus(ctx, a.Provider, from, home, false)
	st.SignedIn, st.Plan = terr == nil, src.Plan
	if !st.SignedIn {
		st.Error = s.ownerName(ctx) + "'s login is signed out on this server for now."
	}
	return st
}
