package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// linkCore fakes Pocket Core's token introspection: pcx_valid acts for
// email with threads:write; the others are each wrong in one way.
func linkCore(t *testing.T, s *Server, emails map[string]string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		scope, audience, kind := ThreadsScope, "ai", "delegated"
		switch input.Token {
		case "pcx_wrong_scope":
			scope = core.InferenceScope
		case "pcx_wrong_audience":
			audience = "projects"
		case "pcx_not_delegated":
			kind = "user"
		}
		email := emails[input.Token]
		if email == "" {
			email = emails["pcx_valid"]
		}
		json.NewEncoder(w).Encode(map[string]any{"active": input.Token != "pcx_revoked", "kind": kind, "scope": scope, "audience": audience,
			"user": map[string]any{"id": 7, "sub": "sub-" + email, "email": email, "name": email}})
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
}

// fakeClaude makes Claude look available and records each start's arguments.
func fakeClaude(t *testing.T, s *Server, root string) string {
	t.Helper()
	argsFile := filepath.Join(root, "claude-args.json")
	bin := filepath.Join(root, "fake-claude")
	script := "#!/usr/bin/env python3\nimport sys,json\nif sys.argv[1:2] == ['auth']:\n    print('{}')\n    sys.exit(0)\njson.dump(sys.argv[1:],open(" + "'" + argsFile + "'" + ",'w'))\nfor line in sys.stdin:\n    pass\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.cfg.ClaudeBin = bin
	s.provList = []agent.Provider{
		{ID: "claude", Name: "Claude Code", Available: true, Models: []agent.Model{{ID: "default", Name: "Default", Efforts: []string{"low", "high"}}},
			Efforts: []string{"low", "high"}, Modes: agent.ClaudeModes, Speeds: []agent.Speed{}, DefaultModel: "default", DefaultEffort: "high", DefaultMode: "default"},
		{ID: "codex", Name: "Codex", Error: "not installed", Models: []agent.Model{}, Efforts: []string{}, Modes: agent.CodexModes, Speeds: []agent.Speed{}},
	}
	s.provAt = time.Now()
	return argsFile
}

func TestLinkDraftAuth(t *testing.T) {
	s, h, root := testServer(t)
	personal, _ := person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	body := []byte(`{"cwd":"` + root + `","text":"look","source":"projects","ref":"AS-2"}`)
	for _, token := range []string{"", personal.token, "pcx_wrong_scope", "pcx_wrong_audience", "pcx_not_delegated", "pcx_revoked"} {
		c := &client{t: t, h: h, token: token}
		for _, rq := range []struct{ m, p string }{{"POST", "/api/v1/link/drafts"}, {"GET", "/api/v1/link/agent-options"}, {"GET", "/api/v1/link/threads?source=projects&ref=AS-2"}, {"GET", "/api/v1/link/dirs"}, {"GET", "/api/v1/link/active?source=projects"}} {
			code, b := c.do(rq.m, rq.p, "application/json", body)
			if code != 401 && code != 403 {
				t.Fatalf("%q bypassed %s: %d %s", token, rq.p, code, b)
			}
		}
	}
	// A person who may not run agents cannot be handed a draft either.
	person(t, s, h, "norun@example.test", "member", false)
	linkCore(t, s, map[string]string{"pcx_valid": "norun@example.test"})
	c := &client{t: t, h: h, token: "pcx_valid"}
	if code, b := c.do("POST", "/api/v1/link/drafts", "application/json", body); code != 403 || !strings.Contains(string(b), "May run agents") {
		t.Fatalf("no can_run: %d %s", code, b)
	}
}

func TestLinkDraftLifecycle(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "link@example.test", "member", true)
	other, _ := person(t, s, h, "other@example.test", "admin", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	argsFile := fakeClaude(t, s, root)
	app := &client{t: t, h: h, token: "pcx_valid"}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}

	var opts struct {
		Providers []struct {
			ID           string `json:"id"`
			ReadonlyMode string `json:"readonly_mode"`
		} `json:"providers"`
		Accounts []linkAccount `json:"accounts"`
		Home     string        `json:"home"`
	}
	app.want("GET", "/api/v1/link/agent-options", nil, 200, &opts)
	if len(opts.Providers) != 2 || opts.Providers[0].ReadonlyMode != "plan" || opts.Providers[1].ReadonlyMode != "read-only" || len(opts.Accounts) < 2 || opts.Home != root {
		t.Fatalf("agent options: %+v", opts)
	}

	for name, in := range map[string]map[string]any{
		"outside roots": {"cwd": "/etc", "text": "x"},
		"missing":       {"cwd": filepath.Join(root, "nope"), "text": "x"},
		"no text":       {"cwd": repo},
		"provider":      {"cwd": repo, "text": "x", "provider": "gpt"},
		"mode":          {"cwd": repo, "text": "x", "provider": "claude", "mode": "read-only"},
		"ref url":       {"cwd": repo, "text": "x", "ref_url": "javascript:alert(1)"},
	} {
		if code, b := app.call("POST", "/api/v1/link/drafts", in); code != 400 {
			t.Fatalf("%s accepted: %d %s", name, code, b)
		}
	}

	var made struct{ ID, URL string }
	app.want("POST", "/api/v1/link/drafts", map[string]any{"provider": "claude", "mode": "plan", "effort": "low", "cwd": repo, "title": "AS-2 · Fix it",
		"text": "Investigate AS-2", "system": "TEAM-RULES: never push", "source": "projects", "ref": "AS-2", "ref_url": "https://projects.example/issue/AS-2", "ref_label": "AS-2 · Fix it"}, 201, &made)
	if made.URL != "https://ai.example/?draft="+made.ID {
		t.Fatalf("draft url %q", made.URL)
	}
	var list []linkThread
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-2", nil, 200, &list)
	if len(list) != 1 || list[0].Status != "draft" || list[0].ThreadID != nil {
		t.Fatalf("draft list: %+v", list)
	}

	// Only the person it was prepared for sees it, admins included.
	other.want("GET", "/api/v1/drafts/"+made.ID, nil, 404, nil)
	other.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{}, 404, nil)
	var d struct {
		Text, System, Mode string
		ThreadID           *string `json:"thread_id"`
	}
	owner.want("GET", "/api/v1/drafts/"+made.ID, nil, 200, &d)
	if d.Text != "Investigate AS-2" || d.Mode != "plan" || d.ThreadID != nil {
		t.Fatalf("draft: %+v", d)
	}

	var th struct{ ID, Source, Title, Mode string }
	owner.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{"text": "Investigate AS-2, edited"}, 201, &th)
	if th.Source != "link:projects" || th.Title != "AS-2 · Fix it" || th.Mode != "plan" {
		t.Fatalf("thread: %+v", th)
	}
	c, err := s.store.GetThreadContext(t.Context(), th.ID)
	if err != nil || c.System != "TEAM-RULES: never push" || c.Ref != "AS-2" {
		t.Fatalf("context: %+v %v", c, err)
	}
	// The hidden context reached the CLI's system prompt.
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(argsFile)
		if err == nil {
			var args []string
			_ = json.Unmarshal(raw, &args)
			joined := strings.Join(args, "\n")
			if !strings.Contains(joined, "TEAM-RULES: never push") || !strings.Contains(joined, "Pocket Projects") {
				t.Fatalf("system prompt missing context: %s", joined)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p := s.threadContextPrompt(t.Context(), mustThread(t, s, user.ID, th.ID)); !strings.Contains(p, "TEAM-RULES") {
		t.Fatalf("resume prompt: %q", p)
	}

	code, b := owner.call("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{})
	if code != 409 || !strings.Contains(string(b), th.ID) {
		t.Fatalf("second start: %d %s", code, b)
	}
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-2", nil, 200, &list)
	if len(list) != 1 || list[0].ThreadID == nil || *list[0].ThreadID != th.ID || list[0].URL != "https://ai.example/t/"+th.ID || list[0].Status == "draft" {
		t.Fatalf("started list: %+v", list)
	}
	owner.want("GET", "/api/v1/threads/"+th.ID+"/context", nil, 200, nil)
	other.want("GET", "/api/v1/threads/"+th.ID+"/context", nil, 404, nil)

	// link/active: the started conversation by ref, only while it works or waits
	var active []struct {
		Ref, Status string
		ThreadID    string `json:"thread_id"`
	}
	app.want("GET", "/api/v1/link/active?source=projects", nil, 200, &active)
	busy := list[0].Status == "running" || list[0].Status == "waiting"
	if busy && (len(active) != 1 || active[0].Ref != "AS-2" || active[0].ThreadID != th.ID) || !busy && len(active) != 0 {
		t.Fatalf("active (thread %s): %+v", list[0].Status, active)
	}
	app.want("GET", "/api/v1/link/active?source=other", nil, 200, &active)
	if len(active) != 0 {
		t.Fatalf("other source: %+v", active)
	}
}

func mustThread(t *testing.T, s *Server, uid int64, id string) *store.Thread {
	t.Helper()
	th, err := s.store.GetThread(t.Context(), uid, id)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestLinkDirs(t *testing.T) {
	s, h, root := testServer(t)
	person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	for _, d := range []string{"repo/.git", "notes", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	app := &client{t: t, h: h, token: "pcx_valid"}
	var out dirListing
	app.want("GET", "/api/v1/link/dirs", nil, 200, &out)
	names := map[string]bool{}
	for _, d := range out.Dirs {
		names[d.Name] = d.Git
	}
	if out.Path != root || !names["repo"] {
		t.Fatalf("home listing: %+v", out)
	}
	if _, ok := names[".hidden"]; ok {
		t.Fatalf("hidden folder listed: %+v", out)
	}
	if code, b := app.call("GET", "/api/v1/link/dirs?path=/etc", nil); code != 400 {
		t.Fatalf("outside roots listed: %d %s", code, b)
	}
}
