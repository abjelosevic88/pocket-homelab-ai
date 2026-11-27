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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// shareUpstream is Anthropic's API (a var for tests).
var shareUpstream = "https://api.anthropic.com"

const (
	shareKeepKey     = "ai_share_keep"     // box setting: shared use stops at this % of the plan (default 80, "0" = never)
	sharePortKey     = "ai_share_port"     // box setting: the proxy's loopback port (kept, so held agents find it after a restart)
	shareCapKey      = "ai_share_capacity" // box setting prefix: what a full week of the lent login costs, as last estimated
	shareGap         = 3 * time.Minute     // a pause between requests up to this long is the agent at work (tools)
	shareDefaultKeep = 80
)

// shareState is the proxy's memory.
type shareState struct {
	mu       sync.Mutex
	addr     string // 127.0.0.1:port once it listens
	plan     map[int64]*sharePlan
	clock    map[int64]time.Time // lent account row → end of the last counted activity
	inflight map[int64]float64   // lent account row → cost since its last finished turn
	costs    map[int64]*shareCosts
	stale    map[int64]bool      // the lent login's token was refused: renew before the next use
	renewed  map[int64]time.Time // the lent login was last renewed
	renewMu  sync.Mutex
}

// sharePlan is what Anthropic last said about the lent login's plan.
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
		st.plan, st.clock, st.inflight = map[int64]*sharePlan{}, map[int64]time.Time{}, map[int64]float64{}
		st.costs, st.stale, st.renewed = map[int64]*shareCosts{}, map[int64]bool{}, map[int64]time.Time{}
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
	if provider != "claude" || home == "" || !agent.IsPersonPath(home) {
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

// shareEnv is what a lent login's Claude Code needs to reach the proxy.
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
	return []string{"ANTHROPIC_BASE_URL=http://" + addr, "ANTHROPIC_AUTH_TOKEN=" + sh.Token}
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

func (s *Server) handleShareProxy(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/hello" { // Claude Code's "is it there" check
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost || (path != "/v1/messages" && path != "/v1/messages/count_tokens") {
		shareErr(w, http.StatusNotFound, "not_found_error", "Pocket AI passes on Claude Code's messages only.")
		return
	}
	ctx := r.Context()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.Header.Get("X-Api-Key")
	}
	sh, err := s.store.GetAIShareByToken(ctx, tok)
	if tok == "" || err != nil {
		shareErr(w, http.StatusUnauthorized, "authentication_error", "This shared login is no longer shared with you. Pick another login for the conversation.")
		return
	}
	a, err := s.store.GetAccount(ctx, sh.AccountID)
	if err != nil || a.SharedFrom == nil || a.UserID == nil {
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
	counting := path == "/v1/messages/count_tokens"
	if !counting {
		if v := s.shareView(ctx, a, sh); v.Blocked != "" {
			shareErr(w, http.StatusBadRequest, "invalid_request_error", v.Blocked)
			return
		}
	}
	from := *a.SharedFrom
	token, err := s.lentToken(ctx, from)
	if err != nil {
		shareErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)

	up, err := http.NewRequestWithContext(ctx, http.MethodPost, shareUpstream+path, bytes.NewReader(body))
	if err != nil {
		shareErr(w, http.StatusInternalServerError, "api_error", "Pocket AI could not pass the request on.")
		return
	}
	up.URL.RawQuery = r.URL.RawQuery
	for k, v := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "X-Api-Key", "Host", "Content-Length", "Connection", "Accept-Encoding", "Proxy-Connection", "Keep-Alive", "Te", "Upgrade":
			continue
		}
		up.Header[k] = v
	}
	up.Header.Set("Authorization", "Bearer "+token)
	if beta := up.Header.Get("Anthropic-Beta"); !strings.Contains(beta, "oauth-2025-04-20") {
		if beta != "" {
			beta += ","
		}
		up.Header.Set("Anthropic-Beta", beta+"oauth-2025-04-20")
	}
	start := time.Now()
	res, err := shareClient.Do(up)
	if err != nil {
		if ctx.Err() == nil {
			shareErr(w, http.StatusBadGateway, "api_error", "Anthropic could not be reached: "+err.Error())
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		s.share.mu.Lock()
		s.share.stale[from] = true
		s.share.mu.Unlock()
	}
	s.notePlan(from, res.Header)
	for k, v := range res.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Connection", "Transfer-Encoding", "Content-Encoding", "Keep-Alive":
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(res.StatusCode)
	meter := &shareMeter{sse: strings.Contains(res.Header.Get("Content-Type"), "event-stream"), model: m.Model}
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
	if !counting {
		s.shareRecord(a, start, time.Now(), meter.done())
	}
}

// shareMeter reads the usage out of a response as it passes.
type shareMeter struct {
	sse   bool
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

func (m *shareMeter) done() store.ShareUsage {
	if !m.sse && len(m.whole) > 0 {
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
	case strings.Contains(m, "haiku"):
		in, out = 1, 5
	case strings.Contains(m, "sonnet"):
		in, out = 3, 15
	case strings.Contains(m, "fable"):
		in, out = 15, 75
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
func (s *Server) notePlan(from int64, h http.Header) {
	week, okW := headerFloat(h, "Anthropic-Ratelimit-Unified-7d-Utilization")
	five, okF := headerFloat(h, "Anthropic-Ratelimit-Unified-5h-Utilization")
	if !okW && !okF {
		return
	}
	s.share.mu.Lock()
	defer s.share.mu.Unlock()
	s.share.init()
	p := s.share.plan[from]
	if p == nil {
		p = &sharePlan{}
		s.share.plan[from] = p
	}
	if okW {
		p.week = week
	}
	if okF {
		p.five = five
	}
	if sec, err := strconv.ParseInt(h.Get("Anthropic-Ratelimit-Unified-7d-Reset"), 10, 64); err == nil && sec > 0 {
		p.weekReset = time.Unix(sec, 0)
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
// own (0) or one of the owner's named Claude logins.
func (s *Server) lentHome(ctx context.Context, from int64) (string, error) {
	if from == 0 {
		return s.cfg.ClaudeHome, nil
	}
	a, err := s.store.GetAccount(ctx, from)
	if err != nil || a.Provider != "claude" || a.SharedFrom != nil || agent.IsPersonPath(a.Home) {
		return "", errors.New("that login cannot be shared")
	}
	return a.Home, nil
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

// lentToken is the owner's access token for the lent login, renewed first
// when it is about to run out (by the CLI itself: it writes the new one).
func (s *Server) lentToken(ctx context.Context, from int64) (string, error) {
	owner := s.ownerName(ctx)
	home, err := s.lentHome(ctx, from)
	if err != nil {
		return "", errors.New(owner + " no longer shares this login.")
	}
	tok, exp, err := readClaudeToken(home)
	if err != nil {
		return "", errors.New(owner + "'s login cannot be used right now: it is not signed in on this server.")
	}
	s.share.mu.Lock()
	s.share.init()
	stale := s.share.stale[from]
	s.share.mu.Unlock()
	if stale || time.Until(exp) < 3*time.Minute {
		s.renewLent(ctx, from, home)
		if tok, exp, err = readClaudeToken(home); err != nil {
			return "", errors.New(owner + "'s login cannot be used right now: it is not signed in on this server.")
		}
	}
	if time.Until(exp) <= 0 {
		return "", errors.New(owner + "'s sign-in on this server has run out. They need to sign in again (Settings → Machines).")
	}
	return tok, nil
}

// renewLent lets Claude Code renew the lent login's sign-in: a tiny
// question on it (at most once a minute).
func (s *Server) renewLent(ctx context.Context, from int64, home string) {
	s.share.renewMu.Lock()
	defer s.share.renewMu.Unlock()
	s.share.mu.Lock()
	last := s.share.renewed[from]
	s.share.mu.Unlock()
	if time.Since(last) < time.Minute {
		return
	}
	var env []string
	if from != 0 {
		env = agent.AccountEnv("claude", home)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	_, _, err := agent.CompleteClaude(cctx, agent.Options{Bin: s.cfg.ClaudeBin, Cwd: s.cfg.Home, Model: "haiku", Env: env, Log: s.log}, "Reply with the single word: ok")
	s.share.mu.Lock()
	s.share.renewed[from] = time.Now()
	if err == nil {
		delete(s.share.stale, from)
	}
	s.share.mu.Unlock()
	if err != nil {
		s.log.Warn("shared AI: renewing the lent login failed", "from", from, "err", err)
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
	From        int64   `json:"from"` // the lent login (0 = the server's own)
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
func (s *Server) lentPlan(from int64) (five, week float64, reset time.Time, known bool) {
	s.share.mu.Lock()
	s.share.init()
	p := s.share.plan[from]
	if p != nil && time.Since(p.at) < 30*time.Minute {
		five, week, reset, known = p.five, p.week, p.weekReset, true
	}
	s.share.mu.Unlock()
	s.rt.mu.Lock()
	l := s.rt.limits[limitsKey("claude", from)]
	if l != nil {
		for _, w := range l.Windows {
			switch w.ID {
			case "five_hour":
				if !known {
					five = w.Used
				}
			case "seven_day":
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
func (s *Server) loginGroup(from int64) map[string]bool {
	key := limitsKey("claude", from)
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
func (s *Server) weekCosts(ctx context.Context, from int64, since time.Time) []store.LoginCost {
	s.share.mu.Lock()
	c := s.share.costs[from]
	s.share.mu.Unlock()
	if c != nil && time.Since(c.at) < 30*time.Second && c.since.Equal(since) {
		return c.list
	}
	list, err := s.store.TurnCostsSince(ctx, "claude", since)
	if err != nil {
		return nil
	}
	s.share.mu.Lock()
	s.share.costs[from] = &shareCosts{at: time.Now(), since: since, list: list}
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
	v := shareView{From: from, By: owner, MinutesDay: sh.MinutesDay, PercentWeek: sh.PercentWeek, Paused: sh.Paused, Keep: s.shareKeep(ctx)}
	v.FromLabel = "Server login"
	if from != 0 {
		if src, err := s.store.GetAccount(ctx, from); err == nil {
			v.FromLabel = src.Label
		}
	}
	now := time.Now()
	today, _ := s.store.ShareUsageSince(ctx, a.ID, localMidnight(now))
	v.UsedMinutes = float64(today.ActiveMS) / 60000
	v.CostToday, v.Requests = today.CostUSD, today.Requests

	five, week, reset, known := s.lentPlan(from)
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
		group := s.loginGroup(from)
		lent := s.lentRows(ctx, from)
		var total, mine float64
		for _, c := range s.weekCosts(ctx, from, begin) {
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
func (s *Server) lentRows(ctx context.Context, from int64) map[int64]bool {
	out := map[int64]bool{}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom != nil && *a.SharedFrom == from {
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
	Label    string  `json:"label"`
	Email    string  `json:"email,omitempty"`
	Plan     string  `json:"plan,omitempty"`
	SignedIn bool    `json:"signed_in"`
	PlanUsed float64 `json:"plan_used"`
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
	out := []lendable{{ID: 0, Label: "Server login"}}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.Provider == "claude" && a.SharedFrom == nil && !agent.IsPersonPath(a.Home) {
			out = append(out, lendable{ID: a.ID, Label: a.Label})
		}
	}
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(l *lendable) {
			defer wg.Done()
			home, _ := s.lentHome(ctx, l.ID)
			st := s.accountStatus(ctx, "claude", l.ID, home, false)
			_, _, terr := readClaudeToken(home) // what the proxy needs
			l.SignedIn, l.Email, l.Plan = terr == nil, st.Email, st.Plan
			five, week, _, _ := s.lentPlan(l.ID)
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
	srcHome, err := s.lentHome(ctx, *in.From)
	if err != nil {
		s.fail(w, badRequest(err.Error()), "share")
		return
	}
	if _, _, err := readClaudeToken(srcHome); err != nil {
		writeErr(w, http.StatusBadRequest, "That login is not signed in on this server (or keeps its sign-in in a keychain), so it cannot be shared.")
		return
	}
	// one share per person and login: change it instead
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.SharedFrom != nil && *a.SharedFrom == *in.From && a.UserID != nil && *a.UserID == person.ID {
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
	srcLabel := "Claude"
	if *in.From != 0 {
		if src, err := s.store.GetAccount(ctx, *in.From); err == nil {
			srcLabel = src.Label
		}
	}
	base := filepath.Join(s.personHome(person.ID), ".claude-shared")
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
	a := &store.Account{Provider: "claude", Label: oneLine(s.ownerName(ctx)+"'s "+srcLabel, 60), Home: home, UserID: &uid, SharedFrom: &from}
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
	home, err := s.lentHome(ctx, from)
	st := acctStatus{CheckedAt: store.Now()}
	if err != nil {
		st.Error = s.ownerName(ctx) + " no longer shares this login."
		return st
	}
	// signed in = its sign-in is in the file the proxy reads (the CLI's
	// own answer only adds the plan)
	_, _, terr := readClaudeToken(home)
	src := s.accountStatus(ctx, "claude", from, home, false)
	st.SignedIn, st.Plan = terr == nil, src.Plan
	if !st.SignedIn {
		st.Error = s.ownerName(ctx) + "'s login is signed out on this server for now."
	}
	return st
}
