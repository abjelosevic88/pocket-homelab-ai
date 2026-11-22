package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// analyticsFake is Pocket Core (the person's link, link tokens, and its MCP
// catalog when ask is set) with Pocket Analytics behind it.
type analyticsFake struct {
	state string // the link's state in Core: on, off_person, …
	sites int    // how many websites the person has
	ask   bool   // Core's own catalog has Analytics tools too
	mu    sync.Mutex
	asked []string // what Analytics was asked: "path?query"
	auths map[string]bool
	mcp   int // tools/call that reached Core's catalog
}

func (f *analyticsFake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.asked...)
}

func (f *analyticsFake) saw(part string) bool {
	for _, p := range f.paths() {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

func analyticsCore(t *testing.T, s *Server, f *analyticsFake) {
	t.Helper()
	f.auths = map[string]bool{}
	site := func(id int, domain string) map[string]any {
		return map[string]any{"id": id, "domain": domain, "name": "Site " + domain, "tz": "Europe/Sarajevo", "also": []string{}, "installed": true,
			"proxy_key": "secret-proxy-key", "snippet": `<script src="x"></script>`, "script_url": "http://x/js/script.js", "event_url": "http://x/api/event", "ignore_url": "https://x/?pocket_ignore=1",
			"first_hit": 1794783600, "last_hit": 1795363200, "today": 12, "live": 2, "visitors_30d": 340, "visitors_prev_30d": 290, "spark": []int{1, 2, 3}}
	}
	row := func(value string, visitors int) map[string]any {
		return map[string]any{"value": value, "visitors": visitors, "visits": visitors + 3, "pageviews": visitors * 2, "bounce_rate": 41.5, "duration": 81.23456789}
	}
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, strings.TrimPrefix(r.URL.Path, "/api/v1")+"?"+r.URL.Query().Encode())
		f.auths[r.Header.Get("Authorization")] = true
		f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		var out any
		switch {
		case path == "/sites":
			list := []any{site(1, "first.example")}
			if f.sites > 1 {
				list = append(list, site(2, "second.example"))
			}
			out = map[string]any{"sites": list, "geo": "ok"}
		case path == "/sites/99":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		case path == "/sites/1" || path == "/sites/2":
			m := site(int(path[len(path)-1]-'0'), map[string]string{"/sites/1": "first.example", "/sites/2": "second.example"}[path])
			m["goals"] = []any{map[string]any{"id": 3, "name": "Order placed", "kind": "event", "match": "Order placed"}}
			m["funnels"] = []any{map[string]any{"id": 7, "name": "Ordering", "steps": []any{}}}
			out = m
		case strings.HasSuffix(path, "/stats"):
			out = map[string]any{"site": site(1, "first.example"), "now": 1795364863, "live": 2, "robots": true, "filters": []any{}, "notes": []any{},
				"range":           map[string]any{"key": "7d", "interval": "day", "from": "2026-11-16", "to": "2026-11-22", "label": "Last 7 days"},
				"totals":          map[string]any{"visitors": 104, "visits": 133, "pageviews": 275, "views_per_visit": 2.1, "bounce_rate": 29.3, "duration": 166.53383458646616, "events": 40},
				"previous":        map[string]any{"visitors": 93, "visits": 119, "pageviews": 229, "views_per_visit": 1.9, "bounce_rate": 32.8, "duration": 148.14285714285714, "events": 31},
				"series":          []any{map[string]any{"start": 1794783600, "visitors": 14, "visits": 17, "pageviews": 30, "bounce_rate": 30, "duration": 120.5}},
				"previous_series": []any{map[string]any{"start": 1794178800, "visitors": 9}}}
		case strings.HasSuffix(path, "/breakdown"):
			if r.URL.Query().Get("dim") == "download" {
				out = map[string]any{"dim": "download", "rows": []any{}} // nothing downloaded: left out of the report
			} else if r.URL.Query().Get("dim") == "utm_campaign" {
				out = map[string]any{"dim": "utm_campaign", "rows": []any{row("", 90), row("november", 14)}} // "" = came with no campaign
			} else {
				out = map[string]any{"dim": r.URL.Query().Get("dim"), "rows": []any{row("top "+r.URL.Query().Get("dim"), 50), row("next", 20)}}
			}
		case strings.HasSuffix(path, "/conversions"):
			out = map[string]any{"visitors": 104, "goals": []any{map[string]any{"goal": map[string]any{"id": 3, "site_id": 1, "name": "Order placed", "created_at": "2026-11-01T10:00:00Z"}, "visitors": 9, "conversions": 11, "rate": 8.7}}}
		case strings.HasSuffix(path, "/funnels/7/counts"):
			out = map[string]any{"funnel": map[string]any{"id": 7}, "steps": []any{map[string]any{"step": map[string]any{"kind": "page", "match": "/order"}, "visitors": 30, "dropped": 0, "rate": 100, "next": 40}}}
		case strings.HasSuffix(path, "/landing"):
			out = map[string]any{"goals": true, "rows": []any{map[string]any{"page": "/", "visits": 90, "visitors": 80, "bounce": 28.5, "engaged": 109.8, "pages": 2.1, "converted": 7, "rate": 7.8}}}
		case strings.HasSuffix(path, "/robots"):
			out = map[string]any{"seen": true, "total": 5296, "rows": []any{map[string]any{"value": "Google", "kind": "search", "hits": 1301, "pages": 13, "last": 1795363200}}}
		case strings.HasSuffix(path, "/ai"):
			out = map[string]any{"reads": 1716, "visits": 158, "pages": []any{}, "robots": []any{}, "sources": []any{map[string]any{"name": "ChatGPT", "n": 124}}}
		case strings.HasSuffix(path, "/speed"):
			m := map[string]any{"p75": 2541, "n": 4157}
			out = map[string]any{"dim": r.URL.Query().Get("dim"), "on": true, "all": map[string]any{"value": "", "loads": 4157, "lcp": m, "cls": map[string]any{"p75": 0.0284, "n": 4157}}, "rows": []any{map[string]any{"value": "/", "loads": 1875, "lcp": m}}}
		case strings.HasSuffix(path, "/keywords"):
			out = map[string]any{"state": "ok", "by": r.URL.Query().Get("by"), "from": "2026-10-24", "to": "2026-11-22", "totals": map[string]any{"clicks": 1679, "impressions": 39990},
				"rows": []any{map[string]any{"words": "maple bakery", "clicks": 413, "impressions": 761, "ctr": 54.3, "position": 1.1}}}
		case strings.HasSuffix(path, "/searches"):
			out = map[string]any{"nothing_found": 0, "rows": []any{}}
		case strings.HasSuffix(path, "/when"):
			cells := make([][]int, 7)
			for d := range cells {
				cells[d] = make([]int, 24)
			}
			cells[2][14] = 60 // Tuesday 14:00
			cells[5][9] = 20
			out = map[string]any{"cells": cells, "max": 60, "visits": 80, "week_start": 1, "tz": "Europe/Sarajevo"}
		case strings.HasSuffix(path, "/paths"):
			out = map[string]any{"page": r.URL.Query().Get("page"), "views": 40, "before": []any{map[string]any{"value": "", "times": 30}}, "after": []any{map[string]any{"value": "/menu", "times": 12}}}
		case strings.HasSuffix(path, "/live"):
			out = map[string]any{"now": 5, "minutes": []int{0, 1}, "recent": []any{map[string]any{"ts": 1795364843, "name": "pageview", "path": "/order", "country": "BA"}}}
		default:
			w.WriteHeader(http.StatusForbidden) // what Analytics answers a link outside its scope
			json.NewEncoder(w).Encode(map[string]string{"error": "this Pocket Core link does not cover GET " + r.URL.Path})
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(analytics.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "analytics", Scope: analyticsScope, On: f.state == "on", State: f.state}})
		case "/api/v1/links/token":
			if f.state != "on" {
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "the link is off", "reason": f.state})
				return
			}
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_analytics", URL: analytics.URL, PublicURL: "https://analytics.example.test", App: "analytics"})
		case "/mcp":
			var rpc struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&rpc)
			w.Header().Set("Content-Type", "application/json")
			if rpc.Method == "tools/list" {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"pocket_notes_get","description":"Read a note","annotations":{"readOnlyHint":true}},{"name":"pocket_analytics_stats","description":"Core's own","annotations":{"readOnlyHint":true}},{"name":"pocket_analytics_summary","description":"Core's own","annotations":{"readOnlyHint":true}}]}}`))
				return
			}
			f.mu.Lock()
			f.mcp++
			f.mu.Unlock()
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
}

// analyticsSession starts a coding conversation's Pocket tool endpoint and
// gives what the agent was told and a client for its tools.
func analyticsSession(t *testing.T, s *Server, u *store.User, root string) (string, agentToolClient) {
	t.Helper()
	th := thread(t, s, u.ID, root)
	r := &run{threadID: th.ID, userID: u.ID, provider: "claude", lastUsed: time.Now(), pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}, cancel: func() {}}
	s.rt.mu.Lock()
	s.rt.runs[th.ID] = r
	s.rt.mu.Unlock()
	t.Cleanup(func() { s.rt.mu.Lock(); delete(s.rt.runs, th.ID); s.rt.mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := agent.Options{SystemPrompt: "Base"}
	if err := s.preparePocketTools(ctx, th, &runSink{rt: s.rt, r: r}, &opts); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(opts.MCPConfig)
	var conf struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal(raw, &conf)
	p := conf.Servers["pocket"]
	return opts.SystemPrompt, agentToolClient{t: t, url: p.URL, key: p.Headers["Authorization"]}
}

// toolNames is a tools/list answer as name → description.
func toolNames(c agentToolClient) map[string]string {
	out := map[string]string{}
	list, _ := c.rpc("tools/list", map[string]any{})["tools"].([]any)
	for _, x := range list {
		m := x.(map[string]any)
		out[m["name"].(string)], _ = m["description"].(string)
	}
	return out
}

// The link Pocket AI → Pocket Analytics gives a conversation the
// pocket_analytics_* tools without the Ask connection: off, they are not
// there and the agent is told how the person switches the link on; on, the
// agent reads the numbers as the person, and never a site's keys.
func TestAnalyticsLinkTools(t *testing.T) {
	s, h, root := testServer(t)
	_, alice := person(t, s, h, "sites@example.test", "member", true)
	count := func(names map[string]string) int {
		n := 0
		for name := range names {
			if strings.HasPrefix(name, "pocket_analytics_") {
				n++
			}
		}
		return n
	}

	// no Core: no tools, nothing said
	prompt, c := analyticsSession(t, s, alice, root)
	if count(toolNames(c)) != 0 || strings.Contains(prompt, "Pocket Analytics") {
		t.Fatalf("without core: %v %q", toolNames(c), prompt)
	}

	// the link switched off: no tools, and the agent knows what to tell the person
	f := &analyticsFake{state: "off_person", sites: 1}
	analyticsCore(t, s, f)
	prompt, c = analyticsSession(t, s, alice, root)
	if count(toolNames(c)) != 0 || !strings.Contains(prompt, "switch on Pocket AI → Pocket Analytics") || len(f.paths()) != 0 {
		t.Fatalf("link off: %v %q %v", toolNames(c), prompt, f.paths())
	}

	// no Analytics on the box: nothing said at all
	f.state = "target_missing"
	if prompt, c = analyticsSession(t, s, alice, root); count(toolNames(c)) != 0 || strings.Contains(prompt, "Pocket Analytics") {
		t.Fatalf("no analytics: %q", prompt)
	}

	// the link on, no Ask connection: the five tools beside the agent tools
	f.state = "on"
	prompt, c = analyticsSession(t, s, alice, root)
	names := toolNames(c)
	if count(names) != 5 || names["pocket_agent_start"] == "" || !strings.Contains(prompt, "pocket_analytics_report") || !strings.Contains(prompt, "enable the Ask connection") {
		t.Fatalf("link on: %v %q", names, prompt)
	}

	// the websites: what the list shows, dates in the site's zone, no keys
	out := c.call("pocket_analytics_sites", map[string]any{})
	first := out["result"].(map[string]any)["sites"].([]any)[0].(map[string]any)
	if first["domain"] != "first.example" || first["first_hit"] != "2026-11-16 00:00" || first["visitors_30d"] != float64(340) || out["app_url"] != "https://analytics.example.test/" {
		t.Fatalf("sites: %+v", out)
	}

	// the report: one website, so no need to name it; every part on one page
	out = c.call("pocket_analytics_report", map[string]any{"range": "7d"})
	res := out["result"].(map[string]any)
	for _, part := range []string{"range", "totals", "previous", "series", "channels", "sources", "pages", "entry_pages", "countries", "devices", "campaigns", "events",
		"outbound_links", "missing_pages", "goals", "funnels", "landing_pages", "robots", "robot_kinds", "ai", "speed", "google_words", "google_pages", "busy"} {
		if res[part] == nil {
			t.Fatalf("report without %s: %v", part, res)
		}
	}
	// what has nothing in it is left out; nothing failed
	if res["downloads"] != nil || res["site_searches"] != nil || res["incomplete"] != nil || res["previous_series"] != nil {
		t.Fatalf("report extras: %v", res)
	}
	if got := res["series"].([]any)[0].(map[string]any)["start"]; got != "2026-11-16" {
		t.Fatalf("series start: %v", got)
	}
	if got := res["totals"].(map[string]any)["duration"]; got != 166.534 {
		t.Fatalf("duration: %v", got)
	}
	busy := res["busy"].(map[string]any)["busiest"].(map[string]any)
	if busy["day"] != "Tuesday" || busy["hour"] != float64(14) || busy["visits"] != float64(60) {
		t.Fatalf("busy: %v", busy)
	}
	if steps := res["funnels"].([]any)[0].(map[string]any); steps["name"] != "Ordering" || len(steps["steps"].([]any)) != 1 {
		t.Fatalf("funnels: %v", steps)
	}
	if camps := res["campaigns"].([]any); len(camps) != 1 || camps[0].(map[string]any)["value"] != "november" {
		t.Fatalf("campaigns: %v", camps)
	}
	if res["on_site_now"] != float64(2) {
		t.Fatalf("on the site now: %v", res["on_site_now"])
	}
	if out["source_url"] != "https://analytics.example.test/sites/1" || out["site"].(map[string]any)["domain"] != "first.example" {
		t.Fatalf("report links: %v %v", out["source_url"], out["site"])
	}
	text, _ := json.Marshal(out)
	for _, secret := range []string{"secret-proxy-key", "proxy_key", "snippet", "pocket_ignore", "site_id", "created_at"} {
		if strings.Contains(string(text), secret) {
			t.Fatalf("the report holds %s", secret)
		}
	}
	if !f.saw("/sites/1/stats?range=7d") || !f.saw("/sites/1/funnels/7/counts?range=7d") || !f.saw("dim=source&limit=10&range=7d") {
		t.Fatalf("asked: %v", f.paths())
	}
	// read as the person, with the link token and nothing else
	if len(f.auths) != 1 || !f.auths["Bearer pcx_analytics"] {
		t.Fatalf("asked with %v", f.auths)
	}

	// totals narrowed by filters, a custom period
	c.call("pocket_analytics_stats", map[string]any{"site": 1, "from": "2026-11-01", "to": "2026-11-07", "interval": "day", "filters": []string{"country:DE", "!source:Google"}})
	if !f.saw("/sites/1/stats?f=country%3ADE&f=%21source%3AGoogle&from=2026-11-01&interval=day&range=custom&to=2026-11-07") {
		t.Fatalf("stats asked: %v", f.paths())
	}
	// a top list, and the other views with their own arguments
	out = c.call("pocket_analytics_breakdown", map[string]any{"dim": "country", "limit": 3})
	if rows := out["result"].(map[string]any)["rows"].([]any); len(rows) != 2 || rows[0].(map[string]any)["duration"] != 81.235 {
		t.Fatalf("breakdown: %v", out)
	}
	c.call("pocket_analytics_view", map[string]any{"view": "speed", "by": "device"})
	c.call("pocket_analytics_view", map[string]any{"view": "paths", "page": "/menu", "range": "30d"})
	if !f.saw("/sites/1/speed?dim=device") || !f.saw("/sites/1/paths?page=%2Fmenu&range=30d") {
		t.Fatalf("views asked: %v", f.paths())
	}
	out = c.call("pocket_analytics_view", map[string]any{"view": "live"})
	if out["source_url"] != "https://analytics.example.test/sites/1/live" || out["result"].(map[string]any)["now"] != float64(5) ||
		out["result"].(map[string]any)["recent"].([]any)[0].(map[string]any)["ts"] != "2026-11-22 17:27" {
		t.Fatalf("live: %v", out)
	}
	if out = c.call("pocket_analytics_view", map[string]any{"view": "funnels"}); len(out["result"].(map[string]any)["funnels"].([]any)) != 1 {
		t.Fatalf("funnels view: %v", out)
	}

	// what cannot be answered says why, and asks Analytics nothing wrong
	for _, bad := range []map[string]any{
		{"dim": "password"}, {"dim": "prop"}, {"dim": "page", "range": "decade"}, {"dim": "page", "from": "2026-11-01"}, {"dim": "page", "filters": []string{"no colon"}},
	} {
		if out = c.call("pocket_analytics_breakdown", bad, true); out["error"] == nil {
			t.Fatalf("breakdown %v went through: %v", bad, out)
		}
	}
	if out = c.call("pocket_analytics_view", map[string]any{"view": "paths"}, true); out["error"] != "view paths needs page" {
		t.Fatalf("paths without a page: %v", out)
	}
	if out = c.call("pocket_analytics_view", map[string]any{"view": "visits"}, true); out["error"] == nil {
		t.Fatalf("visits one by one: %v", out)
	}
	if f.saw("visits") || f.saw("password") {
		t.Fatalf("asked: %v", f.paths())
	}

	// two websites: the agent has to say which, by domain as people write it
	f.sites = 2
	if out = c.call("pocket_analytics_stats", map[string]any{}, true); !strings.Contains(out["error"].(string), "first.example (id 1), second.example (id 2)") {
		t.Fatalf("which website: %v", out)
	}
	if out = c.call("pocket_analytics_stats", map[string]any{"site": "https://www.Second.example/"}); out["site"].(map[string]any)["id"] != float64(2) || !f.saw("/sites/2/stats") {
		t.Fatalf("by domain: %v", out)
	}
	if out = c.call("pocket_analytics_stats", map[string]any{"site": "third.example"}, true); !strings.Contains(out["error"].(string), "No website like that") {
		t.Fatalf("unknown website: %v", out)
	}

	if out = c.call("pocket_analytics_stats", map[string]any{"site": 99}, true); !strings.Contains(out["error"].(string), "no website with id 99") {
		t.Fatalf("someone else's website: %v", out)
	}

	// each read is in the person's app activity like any other
	var reads int
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM app_calls WHERE user_id=? AND tool LIKE 'pocket_analytics_%' AND is_write=0`, alice.ID).Scan(&reads)
	if reads < 10 {
		t.Fatalf("recorded reads: %d", reads)
	}

	// switched off in Core mid-conversation: the next call is refused, in plain words
	f.state = "off_person"
	asked := len(f.paths())
	if out = c.call("pocket_analytics_sites", map[string]any{}, true); !strings.Contains(out["error"].(string), "switched off") || len(f.paths()) != asked {
		t.Fatalf("after switching off: %v", out)
	}
}

// With the Ask connection there too, the linked tools are the one way to ask
// about websites: Core's own Analytics tools are not listed beside them, and
// a call goes to Analytics through the link, not through Core's catalog.
func TestAnalyticsLinkBesideAskConnection(t *testing.T) {
	s, h, root := testServer(t)
	_, alice := person(t, s, h, "both@example.test", "member", true)
	sealed, _ := s.seal("pcma_test_connection")
	if _, err := s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, alice.ID, sealed); err != nil {
		t.Fatal(err)
	}
	f := &analyticsFake{state: "on", sites: 1, ask: true}
	analyticsCore(t, s, f)
	prompt, c := analyticsSession(t, s, alice, root)
	names := toolNames(c)
	if names["pocket_notes_get"] == "" || names["pocket_analytics_summary"] != "" || !strings.Contains(names["pocket_analytics_stats"], "Pocket AI → Pocket Analytics link") {
		t.Fatalf("tools: %v", names)
	}
	if !strings.Contains(prompt, "pocket_analytics_report") || strings.Contains(prompt, "currently unavailable") {
		t.Fatalf("prompt: %q", prompt)
	}
	out := c.call("pocket_analytics_stats", map[string]any{})
	if out["result"].(map[string]any)["totals"] == nil || !f.saw("/sites/1/stats") || f.mcp != 0 {
		t.Fatalf("stats through the link: %v (core calls %d)", out, f.mcp)
	}
	// found by pocket_find_tools as well, as the linked one
	found := c.call("pocket_find_tools", map[string]any{"query": "analytics report"})
	text, _ := json.Marshal(found)
	if !strings.Contains(string(text), "pocket_analytics_report") {
		t.Fatalf("find: %s", text)
	}

	// the link off: Core's own tools are listed again, untouched
	f.state = "off_admin"
	_, c = analyticsSession(t, s, alice, root)
	if names = toolNames(c); names["pocket_analytics_summary"] != "Core's own" || names["pocket_analytics_report"] != "" {
		t.Fatalf("link off beside ask: %v", names)
	}
}
