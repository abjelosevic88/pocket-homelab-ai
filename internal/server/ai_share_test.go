package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A lent login: the person's agent reaches the fake Anthropic through the
// proxy with the share's token; the owner's token goes upstream instead,
// usage is counted, and pausing, the daily minutes and stopping sharing
// take effect at once.
func TestSharedLoginProxy(t *testing.T) {
	var gotAuth, gotBeta, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta, gotPath = r.Header.Get("Authorization"), r.Header.Get("Anthropic-Beta"), r.URL.Path
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.40")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.10")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5-5\",\"usage\":{\"input_tokens\":1000,\"cache_read_input_tokens\":5000,\"output_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":200}}\n\n")
	}))
	defer up.Close()
	old := shareUpstream
	shareUpstream = up.URL
	defer func() { shareUpstream = old }()

	s, h, _ := testServer(t)
	admin, _ := person(t, s, h, "owner@example.com", "admin", false)
	member, mu := person(t, s, h, "kid@example.com", "member", false)
	ctx := context.Background()
	if !s.jailed(ctx, mu.ID) {
		t.Fatal("the member should have a folder of their own")
	}
	// the owner's server login
	if err := os.MkdirAll(s.cfg.ClaudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"owner-secret","refreshToken":"r","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(s.cfg.ClaudeHome, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}

	// members cannot share
	member.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "from": 0}, 403, nil)
	var row shareRow
	admin.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "from": 0, "minutes_day": 60, "percent_week": 5}, 201, &row)
	if row.MinutesDay != 60 || row.PercentWeek != 5 || row.By == "" {
		t.Fatalf("share = %+v", row)
	}
	if u, _ := s.store.GetUser(ctx, mu.ID); !u.CanRun {
		t.Fatal("sharing should let them run agents")
	}
	a, err := s.store.GetAccount(ctx, row.AccountID)
	if err != nil || a.SharedFrom == nil || *a.SharedFrom != 0 || !strings.HasPrefix(a.Home, s.personHome(mu.ID)) {
		t.Fatalf("lent account = %+v, %v", a, err)
	}
	// it is theirs to pick, and its environment leads to the proxy
	var mine []accountView
	member.want("GET", "/api/v1/accounts", nil, 200, &mine)
	found := false
	for _, v := range mine {
		if v.ID == a.ID {
			found = v.Shared != nil && v.SignedIn && v.Email == ""
		}
		if v.ID == 0 {
			t.Fatal("the member must not see the server login itself")
		}
	}
	if !found {
		t.Fatalf("lent login not usable: %+v", mine)
	}
	if err := s.checkAccount(ctx, mu, "claude", a.ID); err != nil {
		t.Fatal(err)
	}
	_, env := s.accountHome(ctx, "claude", a.ID)
	var base, tok string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "ANTHROPIC_BASE_URL="); ok {
			base = v
		}
		if v, ok := strings.CutPrefix(e, "ANTHROPIC_AUTH_TOKEN="); ok {
			tok = v
		}
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") || !strings.HasPrefix(tok, "psh_") || strings.Contains(strings.Join(env, " "), "owner-secret") {
		t.Fatalf("env = %v", env)
	}
	ask := func(token string) (int, string) {
		req, _ := http.NewRequest("POST", base+"/v1/messages?beta=true", bytes.NewReader([]byte(`{"model":"claude-sonnet-5-5","stream":true}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Beta", "claude-code-20250219")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := ask(tok); code != 200 || !strings.Contains(body, "message_delta") {
		t.Fatalf("proxied answer %d %s", code, body)
	}
	if gotAuth != "Bearer owner-secret" || !strings.Contains(gotBeta, "oauth-2025-04-20") || !strings.Contains(gotBeta, "claude-code") || gotPath != "/v1/messages" {
		t.Fatalf("upstream saw auth=%q beta=%q path=%q", gotAuth, gotBeta, gotPath)
	}
	if code, _ := ask("psh_wrong"); code != 401 {
		t.Fatalf("a wrong token got %d", code)
	}
	u, _ := s.store.ShareUsageSince(ctx, a.ID, time.Now().Add(-time.Hour))
	if u.Requests != 1 || u.InputTokens != 1000 || u.CacheRead != 5000 || u.OutputTokens != 200 || u.CostUSD <= 0 {
		t.Fatalf("usage = %+v", u)
	}
	if five, week, _, known := s.lentPlan("claude", 0); !known || week != 0.40 || five != 0.10 {
		t.Fatalf("plan = %v %v %v", five, week, known)
	}

	// today's minutes used up: refused with Anthropic's error shape, and
	// the conversation says so before it starts
	if err := s.store.AddShareUsage(ctx, a.ID, time.Now(), storeUsage(61*60*1000)); err != nil {
		t.Fatal(err)
	}
	code, body := ask(tok)
	var apiErr struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &apiErr)
	if code != 400 || !strings.Contains(apiErr.Error.Message, "today's hour") {
		t.Fatalf("over the minutes: %d %s", code, body)
	}
	if why := s.shareBlocked(ctx, a.ID); !strings.Contains(why, "today's hour") {
		t.Fatalf("shareBlocked = %q", why)
	}
	// no time limit any more, but the week's share: all Pocket AI saw this
	// week was their request while the plan is 40% full, so their share
	// looks like 40% — over 5%
	admin.want("PATCH", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), map[string]any{"minutes_day": 0}, 200, nil)
	if code, body := ask(tok); code != 400 || !strings.Contains(body, "your 5% of") {
		t.Fatalf("over the week's share: %d %s", code, body)
	}
	admin.want("PATCH", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), map[string]any{"percent_week": 0}, 200, nil)
	if code, _ := ask(tok); code != 200 {
		t.Fatalf("no limits any more: %d", code)
	}
	// paused
	admin.want("PATCH", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), map[string]any{"paused": true}, 200, nil)
	if code, body := ask(tok); code != 400 || !strings.Contains(body, "paused") {
		t.Fatalf("paused: %d %s", code, body)
	}
	admin.want("PATCH", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), map[string]any{"paused": false}, 200, nil)
	// the plan nearly full: shared use stops at "keep"
	admin.want("PUT", "/api/v1/ai-shares/settings", map[string]any{"keep": 30}, 200, nil)
	if code, body := ask(tok); code != 400 || !strings.Contains(body, "so they keep the rest") {
		t.Fatalf("keep: %d %s", code, body)
	}
	admin.want("PUT", "/api/v1/ai-shares/settings", map[string]any{"keep": 0}, 200, nil)

	// the member can neither sign it in/out nor remove it
	member.want("DELETE", fmt.Sprintf("/api/v1/accounts/%d", a.ID), nil, 403, nil)
	member.want("POST", fmt.Sprintf("/api/v1/accounts/%d/logout", a.ID), nil, 403, nil)
	var list struct {
		People []aiSharePerson `json:"people"`
		Logins []lendable      `json:"logins"`
	}
	admin.want("GET", "/api/v1/ai-shares", nil, 200, &list)
	if len(list.People) != 1 || len(list.People[0].Shares) != 1 || len(list.Logins) == 0 || !list.Logins[0].SignedIn {
		t.Fatalf("list = %+v", list)
	}
	// stop sharing: the token is dead at once
	admin.want("DELETE", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), nil, 204, nil)
	if code, _ := ask(tok); code != 401 {
		t.Fatalf("after stop sharing: %d", code)
	}
}

func TestShareMeterPlainJSON(t *testing.T) {
	m := &shareMeter{model: "claude-haiku-5-5"}
	m.feed([]byte(`{"model":"claude-haiku-5-5","usage":{"input_tokens":10,`))
	m.feed([]byte(`"output_tokens":20,"cache_creation_input_tokens":30}}`))
	u := m.done()
	if u.InputTokens != 10 || u.OutputTokens != 20 || u.CacheWrite != 30 || u.CostUSD <= 0 {
		t.Fatalf("usage = %+v", u)
	}
}

func storeUsage(ms int64) store.ShareUsage { return store.ShareUsage{ActiveMS: ms} }

// A lent Codex login: Codex is pointed at the proxy by a model provider in
// its config.toml; the proxy sends the owner's ChatGPT token and account
// to the Codex backend and reads the usage of response.completed.
func TestSharedCodexLogin(t *testing.T) {
	var gotAuth, gotAcct, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAcct, gotPath = r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id"), r.URL.Path
		w.Header().Set("X-Codex-Primary-Used-Percent", "12")
		w.Header().Set("X-Codex-Primary-Window-Minutes", "10080")
		w.Header().Set("X-Codex-Primary-Reset-At", fmt.Sprint(time.Now().Add(48*time.Hour).Unix()))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6.2-sol\",\"usage\":{\"input_tokens\":3000,\"input_tokens_details\":{\"cached_tokens\":2000},\"output_tokens\":50}}}\n\n")
	}))
	defer up.Close()
	old := shareCodexUpstream
	shareCodexUpstream = up.URL + "/backend-api/codex"
	defer func() { shareCodexUpstream = old }()

	s, h, _ := testServer(t)
	admin, _ := person(t, s, h, "owner@example.com", "admin", false)
	member, mu := person(t, s, h, "kid@example.com", "member", false)
	ctx := context.Background()
	_ = os.MkdirAll(s.cfg.CodexHome, 0o700)
	// a JWT whose exp is an hour away
	payload := base64URL(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":"h.%s.s","account_id":"acct-1","refresh_token":"r"}}`, payload)
	if err := os.WriteFile(filepath.Join(s.cfg.CodexHome, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Logins []lendable `json:"logins"`
	}
	admin.want("GET", "/api/v1/ai-shares", nil, 200, &list)
	codexOK := false
	for _, l := range list.Logins {
		codexOK = codexOK || (l.Provider == "codex" && l.ID == 0 && l.SignedIn)
	}
	if !codexOK {
		t.Fatalf("Codex server login not lendable: %+v", list.Logins)
	}
	var row shareRow
	admin.want("POST", "/api/v1/ai-shares", map[string]any{"user_id": mu.ID, "provider": "codex", "from": 0, "percent_week": 50}, 201, &row)
	a, _ := s.store.GetAccount(ctx, row.AccountID)
	if a.Provider != "codex" || !strings.Contains(a.Home, ".codex-shared") {
		t.Fatalf("account = %+v", a)
	}
	var mine []accountView
	member.want("GET", "/api/v1/accounts", nil, 200, &mine)
	seen := false
	for _, v := range mine {
		seen = seen || (v.ID == a.ID && v.Provider == "codex" && v.SignedIn && v.Shared != nil)
	}
	if !seen {
		t.Fatalf("member's logins: %+v", mine)
	}
	_, env := s.accountHome(ctx, "codex", a.ID)
	tok := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, shareCodexKeyEnv+"="); ok {
			tok = v
		}
	}
	cfg, err := os.ReadFile(filepath.Join(a.Home, "config.toml"))
	if tok == "" || err != nil || !strings.Contains(string(cfg), `env_key = "POCKET_SHARE_TOKEN"`) {
		t.Fatalf("env %v config %s %v", env, cfg, err)
	}
	base := strings.Split(strings.Split(string(cfg), `base_url = "`)[1], `"`)[0]
	req, _ := http.NewRequest("POST", base+"/responses", strings.NewReader(`{"model":"gpt-6.2-sol","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "response.completed") {
		t.Fatalf("answer %d %s", res.StatusCode, b)
	}
	if gotAuth != "Bearer h."+payload+".s" || gotAcct != "acct-1" || gotPath != "/backend-api/codex/responses" {
		t.Fatalf("upstream saw %q %q %q", gotAuth, gotAcct, gotPath)
	}
	u, _ := s.store.ShareUsageSince(ctx, a.ID, time.Now().Add(-time.Hour))
	if u.InputTokens != 1000 || u.CacheRead != 2000 || u.OutputTokens != 50 {
		t.Fatalf("usage %+v", u)
	}
	if _, week, _, known := s.lentPlan("codex", 0); !known || week != 0.12 {
		t.Fatalf("plan week %v", week)
	}
	// a Claude Code token does not open Codex's door
	req, _ = http.NewRequest("POST", strings.TrimSuffix(base, "/codex")+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Fatalf("wrong door: %v %v", res.StatusCode, err)
	}
	// paused: refused the way OpenAI refuses
	admin.want("PATCH", fmt.Sprintf("/api/v1/ai-shares/%d", a.ID), map[string]any{"paused": true}, 200, nil)
	req, _ = http.NewRequest("POST", base+"/responses", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	res, _ = http.DefaultClient.Do(req)
	b, _ = io.ReadAll(res.Body)
	if res.StatusCode != 400 || !strings.Contains(string(b), `"message":`) || !strings.Contains(string(b), "paused") {
		t.Fatalf("paused: %d %s", res.StatusCode, b)
	}
}

func base64URL(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
