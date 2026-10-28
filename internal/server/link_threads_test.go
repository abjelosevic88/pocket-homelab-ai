package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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
		app := "projects"
		if input.Token == "pcx_other_app" {
			app = "notes"
		}
		json.NewEncoder(w).Encode(map[string]any{"active": input.Token != "pcx_revoked", "kind": kind, "scope": scope, "audience": audience,
			"app":  map[string]any{"id": 3, "slug": app, "name": app},
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
	// Bootstrap warms this cache asynchronously. Wait for its first result before
	// replacing it, or a late probe of the deliberately missing CLI overwrites
	// the fixture and makes requests fail depending on scheduling.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.provMu.Lock()
		if !s.provAt.IsZero() {
			break
		}
		s.provMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("provider warmup did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	defer s.provMu.Unlock()
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

func TestLinkedAppToolsSurviveShortPrompts(t *testing.T) {
	all := []askTool{}
	for i := 0; i < 40; i++ { // a large catalog, so the prompt's words pick the tools
		all = append(all, askTool{Name: fmt.Sprintf("pocket_money_tool_%d", i), Description: "money"})
	}
	all = append(all, askTool{Name: "pocket_projects_comment", Description: "Comment on an issue"}, askTool{Name: "pocket_projects_link_pr", Description: "Link a pull request"})
	names := func(v any) map[string]bool {
		out := map[string]bool{}
		for _, x := range v.(map[string]any)["tools"].([]askTool) {
			out[x.Name] = true
		}
		return out
	}
	linked := names(withLinkedAppTools(scopedCatalog(all, "ye"), all, &store.Thread{Source: "link:projects"}))
	if !linked["pocket_projects_comment"] || !linked["pocket_projects_link_pr"] {
		t.Fatalf("link:projects thread lost its ticket tools: %v", linked)
	}
	plain := names(withLinkedAppTools(scopedCatalog(all, "ye"), all, &store.Thread{Source: "web"}))
	if plain["pocket_projects_link_pr"] {
		t.Fatalf("ordinary thread got app tools forced in: %v", plain)
	}
}

func TestThreadPRsFromCreateCommands(t *testing.T) {
	s, h, _ := testServer(t)
	_, user := person(t, s, h, "link@example.test", "member", true)
	th := &store.Thread{ID: "t-prs", UserID: user.ID, Provider: "claude", Source: "link:projects", SourceRef: "AS-4", Cwd: t.TempDir()}
	if err := s.store.CreateThread(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	created := `{"tool":{"name":"Bash","input":{"command":"git commit -q -am \"fix\" && gh pr create --base development --fill"}},"output":"https://github.com/athenastudioAI/athena/pull/3980\n"}`
	listed := `{"tool":{"name":"Bash","input":{"command":"gh pr list"}},"output":"https://github.com/athenastudioAI/athena/pull/3947 open"}`
	forgejo := `{"tool":{"name":"Bash","input":{"command":"tea pr create --title x"}},"output":"https://git.bjelke.org/me/app/pulls/12"}`
	for i, d := range []string{created, listed, forgejo} {
		if _, err := s.store.UpsertItem(t.Context(), th.ID, fmt.Sprintf("k%d", i), "tool", []byte(d), ""); err != nil {
			t.Fatal(err)
		}
	}
	got := s.threadPRs(t.Context(), th.ID)
	if len(got) != 2 || got[0] != "https://github.com/athenastudioAI/athena/pull/3980" || got[1] != "https://git.bjelke.org/me/app/pulls/12" {
		t.Fatalf("prs: %v", got)
	}
}

// An app's files (an issue's screenshots and documents) go with the first
// message: pictures as pictures, documents as text; unreadable ones are left
// out and reported; the person can drop some before sending.
func TestLinkDraftFiles(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	fakeClaude(t, s, root)
	app := &client{t: t, h: h, token: "pcx_valid"}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	var made struct {
		ID    string
		Files []struct {
			Name  string
			OK    bool
			Error string
		}
	}
	app.want("POST", "/api/v1/link/drafts", map[string]any{"cwd": repo, "text": "Fix AS-6", "source": "projects", "ref": "AS-6",
		"files": []map[string]any{
			{"name": "CleanShot 15.04.43@2x.png", "data": png},
			{"name": "steps.md", "data": []byte("1. Create a rule in Teams\n2. It lands on a workflow")},
			{"name": "tool.exe", "data": []byte("MZ\x90\x00binary")},
		}}, 201, &made)
	if len(made.Files) != 3 || !made.Files[0].OK || !made.Files[1].OK || made.Files[2].OK || made.Files[2].Error == "" {
		t.Fatalf("files: %+v", made.Files)
	}
	var d struct {
		Uploads []struct{ ID, Name, Mime, URL string }
	}
	owner.want("GET", "/api/v1/drafts/"+made.ID, nil, 200, &d)
	if len(d.Uploads) != 2 || d.Uploads[0].Mime != "image/png" || d.Uploads[1].Name != "steps.md" {
		t.Fatalf("draft uploads: %+v", d.Uploads)
	}
	owner.want("GET", d.Uploads[0].URL, nil, 200, nil)

	// all of them by default
	var th struct{ ID string }
	owner.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{}, 201, &th)
	items, _ := s.store.ListItems(t.Context(), th.ID)
	var first agent.Item
	for _, it := range items {
		if it.Kind == "user" {
			_ = json.Unmarshal(it.Data, &first)
			break
		}
	}
	if len(first.Images) != 1 || len(first.Files) != 1 || first.Files[0].Name != "steps.md" {
		t.Fatalf("first message: %+v", first)
	}
	_ = user

	// the person removed the document before sending
	app.want("POST", "/api/v1/link/drafts", map[string]any{"cwd": repo, "text": "Fix AS-7", "source": "projects", "ref": "AS-7",
		"files": []map[string]any{{"name": "a.png", "data": png}, {"name": "b.md", "data": []byte("notes")}}}, 201, &made)
	owner.want("GET", "/api/v1/drafts/"+made.ID, nil, 200, &d)
	owner.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{"draft_uploads": []string{d.Uploads[0].ID}}, 201, &th)
	items, _ = s.store.ListItems(t.Context(), th.ID)
	for _, it := range items {
		if it.Kind == "user" {
			first = agent.Item{}
			_ = json.Unmarshal(it.Data, &first)
			break
		}
	}
	if len(first.Images) != 1 || len(first.Files) != 0 {
		t.Fatalf("after removing one: %+v", first)
	}
}

// The app that prepared a conversation can take it off its item (it stays in
// Pocket AI) or delete it; other apps cannot touch it.
func TestLinkUnlinkAndDelete(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test", "pcx_other_app": "link@example.test"})
	fakeClaude(t, s, root)
	app := &client{t: t, h: h, token: "pcx_valid"}
	other := &client{t: t, h: h, token: "pcx_other_app"}
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0700)
	draft := func(ref string, files ...map[string]any) string {
		var made struct{ ID string }
		body := map[string]any{"cwd": repo, "text": "Fix " + ref, "source": "projects", "ref": ref}
		if len(files) > 0 {
			body["files"] = files
		}
		app.want("POST", "/api/v1/link/drafts", body, 201, &made)
		return made.ID
	}
	list := func(ref string) []linkThread {
		var l []linkThread
		app.want("GET", "/api/v1/link/threads?source=projects&ref="+ref, nil, 200, &l)
		return l
	}

	// unlink: gone from the issue, the conversation stays
	d1 := draft("AS-1")
	var th struct{ ID string }
	owner.want("POST", "/api/v1/drafts/"+d1+"/start", map[string]any{}, 201, &th)
	other.want("POST", "/api/v1/link/threads/"+d1+"/unlink", nil, 404, nil)
	other.want("DELETE", "/api/v1/link/threads/"+d1, nil, 404, nil)
	app.want("POST", "/api/v1/link/threads/"+d1+"/unlink", nil, 204, nil)
	if l := list("AS-1"); len(l) != 0 {
		t.Fatalf("still listed: %+v", l)
	}
	mustThread(t, s, user.ID, th.ID)

	// delete: the conversation goes too
	d2 := draft("AS-2")
	owner.want("POST", "/api/v1/drafts/"+d2+"/start", map[string]any{}, 201, &th)
	app.want("DELETE", "/api/v1/link/threads/"+d2, nil, 204, nil)
	if _, err := s.store.GetThread(t.Context(), user.ID, th.ID); err == nil {
		t.Fatal("thread not deleted")
	}
	if l := list("AS-2"); len(l) != 0 {
		t.Fatalf("still listed: %+v", l)
	}

	// an unsent draft's files go with it
	d3 := draft("AS-3", map[string]any{"name": "notes.md", "data": []byte("steps")})
	var d struct{ Uploads []struct{ ID string } }
	owner.want("GET", "/api/v1/drafts/"+d3, nil, 200, &d)
	app.want("DELETE", "/api/v1/link/threads/"+d3, nil, 204, nil)
	if _, err := s.store.GetUpload(t.Context(), user.ID, d.Uploads[0].ID); err == nil {
		t.Fatal("draft upload left behind")
	}
	app.want("DELETE", "/api/v1/link/threads/"+d3, nil, 404, nil)
}

// An app can ask for a worktree of the ticket's own: made on send, reused by
// the next conversation about the same ticket, the branch picked up when the
// repo already has it.
func TestLinkDraftWorktree(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	s, h, root := testServer(t)
	owner, _ := person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	fakeClaude(t, s, root)
	app := &client{t: t, h: h, token: "pcx_valid"}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "init")
	gitT(t, repo, "branch", "as-3-old")
	// the main checkout is on another ticket's branch: new worktrees still start from main
	gitT(t, repo, "switch", "-q", "-c", "as-9-other")
	_ = os.WriteFile(filepath.Join(repo, "b.txt"), []byte("other\n"), 0o644)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "other")
	// origin is ahead of the local main: the worktree starts from origin's
	origin := filepath.Join(root, "origin.git")
	gitT(t, root, "clone", "-q", "--bare", repo, origin)
	gitT(t, repo, "remote", "add", "origin", origin)
	up := filepath.Join(root, "up")
	gitT(t, root, "clone", "-q", "-b", "main", origin, up)
	_ = os.WriteFile(filepath.Join(up, "c.txt"), []byte("new on origin\n"), 0o644)
	gitT(t, up, "add", "-A")
	gitT(t, up, "commit", "-q", "-m", "origin moved on")
	gitT(t, up, "push", "-q", "origin", "main")
	gitT(t, repo, "fetch", "-q", "origin")
	gitT(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	var started []string
	// before the folders go: worktree setups done, the (fake) agents stopped
	t.Cleanup(func() {
		for _, id := range started {
			for i := 0; i < 100 && s.holds().held(id); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			s.rt.stop(id)
		}
		time.Sleep(200 * time.Millisecond)
	})

	if code, b := app.call("POST", "/api/v1/link/drafts", map[string]any{"cwd": repo, "text": "x", "worktree": map[string]any{"name": "bad..name"}}); code != 400 {
		t.Fatalf("bad branch name accepted: %d %s", code, b)
	}
	start := func(name string, rename ...string) (thread struct {
		ID         string `json:"id"`
		Cwd        string `json:"cwd"`
		WorktreeID int64  `json:"worktree_id"`
	}) {
		var made struct{ ID string }
		app.want("POST", "/api/v1/link/drafts", map[string]any{"cwd": repo, "text": "Fix " + name, "source": "projects", "ref": name,
			"worktree": map[string]any{"name": name}}, 201, &made)
		var d struct {
			Worktree *store.DraftWorktree `json:"worktree"`
		}
		owner.want("GET", "/api/v1/drafts/"+made.ID, nil, 200, &d)
		if d.Worktree == nil || d.Worktree.Name != name {
			t.Fatalf("draft worktree: %+v", d.Worktree)
		}
		send := name
		if len(rename) > 0 {
			send = rename[0]
		}
		owner.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{"worktree": map[string]any{"mode": "new", "name": send}}, 201, &thread)
		started = append(started, thread.ID)
		return
	}
	branchOf := func(dir string) string { return gitT(t, dir, "rev-parse", "--abbrev-ref", "HEAD") }

	first := start("you/as-2-login")
	if first.WorktreeID == 0 || first.Cwd == repo || branchOf(first.Cwd) != "you/as-2-login" {
		t.Fatalf("first: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(first.Cwd, "b.txt")); err == nil {
		t.Fatal("the worktree started from the main checkout's branch, not origin's default")
	}
	if _, err := os.Stat(filepath.Join(first.Cwd, "c.txt")); err != nil {
		t.Fatal("the worktree started from a stale local main, not origin's")
	}
	if again := start("you/as-2-login"); again.WorktreeID != first.WorktreeID || again.Cwd != first.Cwd {
		t.Fatalf("second conversation did not reuse the worktree: %+v vs %+v", again, first)
	}
	// renamed before sending: the new name, still from origin's up-to-date main
	if ren := start("you/as-4-long-title-cut-o", "fix/as-4-tidy"); branchOf(ren.Cwd) != "fix/as-4-tidy" {
		t.Fatalf("renamed: %+v", ren)
	} else if _, err := os.Stat(filepath.Join(ren.Cwd, "c.txt")); err != nil {
		t.Fatal("the renamed worktree did not start from origin's main")
	}
	if old := start("as-3-old"); old.WorktreeID == 0 || old.WorktreeID == first.WorktreeID || branchOf(old.Cwd) != "as-3-old" {
		t.Fatalf("existing branch: %+v", old)
	}

	// the item's worktrees: the conversations' own, plus branches named after it
	var wts []linkWorktree
	app.want("GET", "/api/v1/link/worktrees?source=projects&ref=AS-3", nil, 200, &wts)
	if len(wts) != 1 || wts[0].Branch != "as-3-old" || wts[0].Status != "active" || len(wts[0].Threads) != 1 || !strings.Contains(wts[0].URL, "/worktrees?open=") {
		t.Fatalf("AS-3 worktrees: %+v", wts)
	}
	app.want("GET", "/api/v1/link/worktrees?source=projects&ref=AS-2", nil, 200, &wts)
	if len(wts) != 1 || wts[0].Branch != "you/as-2-login" || len(wts[0].Threads) != 2 {
		t.Fatalf("AS-2 worktrees (named by branch, not by ref): %+v", wts)
	}
	var threads []linkThread
	app.want("GET", "/api/v1/link/threads?source=projects&ref=as-3-old", nil, 200, &threads)
	if len(threads) != 1 || threads[0].Worktree != "as-3-old" || threads[0].WorktreeID == 0 {
		t.Fatalf("thread worktree: %+v", threads)
	}
}

// A draft can run on another machine: its folder is that machine's (not
// checked here), the machine's own login is used, and the app sees the
// machines in agent-options.
func TestLinkDraftHost(t *testing.T) {
	s, h, _ := testServer(t)
	owner, _ := person(t, s, h, "link@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	app := &client{t: t, h: h, token: "pcx_valid"}
	box := &store.Host{Name: "athena-box", Target: "ubuntu@example.invalid"}
	if err := s.store.CreateHost(t.Context(), box); err != nil {
		t.Fatal(err)
	}
	var opts struct {
		Hosts []linkHost `json:"hosts"`
	}
	app.want("GET", "/api/v1/link/agent-options", nil, 200, &opts)
	if len(opts.Hosts) != 1 || opts.Hosts[0].Name != "athena-box" || opts.Hosts[0].ID != box.ID {
		t.Fatalf("hosts: %+v", opts.Hosts)
	}
	for name, in := range map[string]map[string]any{
		"unknown host":  {"cwd": "/work/athena", "text": "x", "host_id": box.ID + 100},
		"relative path": {"cwd": "work/athena", "text": "x", "host_id": box.ID},
	} {
		if code, b := app.call("POST", "/api/v1/link/drafts", in); code < 400 {
			t.Fatalf("%s accepted: %d %s", name, code, b)
		}
	}
	var made struct{ ID string }
	app.want("POST", "/api/v1/link/drafts", map[string]any{"cwd": "/work/Projects/athena", "text": "Fix AS-7", "host_id": box.ID, "account_id": 0}, 201, &made)
	var d struct {
		HostID    int64  `json:"host_id"`
		Cwd       string `json:"cwd"`
		AccountID *int64 `json:"account_id"`
	}
	owner.want("GET", "/api/v1/drafts/"+made.ID, nil, 200, &d)
	if d.HostID != box.ID || d.Cwd != "/work/Projects/athena" || d.AccountID != nil {
		t.Fatalf("draft: %+v", d)
	}
}

// A fork of a conversation an app prepared belongs to the same item and keeps
// the app's hidden context.
func TestForkInheritsLink(t *testing.T) {
	s, h, root := testServer(t)
	owner, user := person(t, s, h, "fork@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "fork@example.test"})
	fakeClaude(t, s, root)
	app := &client{t: t, h: h, token: "pcx_valid"}
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0700)
	var made struct{ ID string }
	app.want("POST", "/api/v1/link/drafts", map[string]any{"cwd": repo, "text": "Fix AS-9", "source": "projects", "ref": "AS-9", "system": "Team instructions: be brief."}, 201, &made)
	var th struct{ ID string }
	owner.want("POST", "/api/v1/drafts/"+made.ID+"/start", map[string]any{}, 201, &th)
	ctx := context.Background()
	from, err := s.store.GetThread(ctx, user.ID, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.threadContextPrompt(ctx, from) == "" {
		t.Fatal("the original has no context")
	}
	n := &store.Thread{ID: uuid.NewString(), UserID: user.ID, Title: "Fork of " + from.Title, Provider: from.Provider, Cwd: from.Cwd, Source: "fork"}
	if err := s.store.CreateThread(ctx, n); err != nil {
		t.Fatal(err)
	}
	s.inheritLink(ctx, from, n)
	var l []linkThread
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-9", nil, 200, &l)
	if len(l) != 2 || l[0].ThreadID == nil || *l[0].ThreadID != n.ID || l[0].Status == "draft" {
		t.Fatalf("issue conversations: %+v", l)
	}
	if got := s.threadContextPrompt(ctx, n); !strings.Contains(got, "be brief") {
		t.Fatalf("fork context: %q", got)
	}
	if got := s.rt.baseOptions(ctx, n).SystemPrompt; !strings.Contains(got, "be brief") || !strings.Contains(got, forkScopePrompt) {
		t.Fatal("fork must keep both linked app context and its independent work scope")
	}
	// a thread no app prepared: nothing happens
	plain := &store.Thread{ID: uuid.NewString(), UserID: user.ID, Title: "x", Provider: "claude", Cwd: repo}
	_ = s.store.CreateThread(ctx, plain)
	s.inheritLink(ctx, plain, &store.Thread{ID: uuid.NewString(), UserID: user.ID})
	app.want("GET", "/api/v1/link/threads?source=projects&ref=AS-9", nil, 200, &l)
	if len(l) != 2 {
		t.Fatalf("plain fork changed the issue: %d", len(l))
	}
}
