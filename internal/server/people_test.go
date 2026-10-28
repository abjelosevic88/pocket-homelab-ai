package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Everyone but the owner works in a folder of their own, on their own login.
func TestPeopleOwnFolderAndLogin(t *testing.T) {
	s, h, root := testServer(t)
	ctx := context.Background()
	owner, ou := person(t, s, h, "owner@example.test", "admin", true)
	sani, su := person(t, s, h, "sani@example.test", "member", true)

	if s.jailed(ctx, ou.ID) || !s.jailed(ctx, su.ID) {
		t.Fatal("only people other than the owner get a folder of their own")
	}
	// the server's login is the owner's
	if err := s.checkAccount(ctx, ou, "claude", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.checkAccount(ctx, su, "claude", 0); err == nil || !strings.Contains(err.Error(), "Sign in to Claude Code") {
		t.Fatalf("server login for Sani: %v", err)
	}
	if _, err := s.usableAccount(ctx, su, "codex", 0); err == nil {
		t.Fatal("Sani has no login yet")
	}
	var accts []accountView
	sani.want("GET", "/api/v1/accounts", nil, 200, &accts)
	if len(accts) != 0 {
		t.Fatalf("Sani sees %+v", accts)
	}
	owner.want("GET", "/api/v1/accounts", nil, 200, &accts)
	if len(accts) != 2 || accts[0].Label != "Server login" {
		t.Fatalf("owner sees %+v", accts)
	}

	// a login of their own, in their own folder, without the owner's settings
	var made accountView
	sani.want("POST", "/api/v1/accounts", map[string]any{"provider": "claude", "label": "Mine", "user_id": ou.ID}, 201, &made)
	if made.UserID == nil || *made.UserID != su.ID {
		t.Fatalf("a member's login is theirs, whatever they ask: %+v", made)
	}
	if want := filepath.Join(s.personHome(su.ID), ".claude"); made.Home != want {
		t.Fatalf("home %s, want %s", made.Home, want)
	}
	if id, err := s.usableAccount(ctx, su, "claude", 0); err != nil || id != made.ID {
		t.Fatalf("Sani's default: %d %v", id, err)
	}
	sani.want("GET", "/api/v1/accounts", nil, 200, &accts)
	if len(accts) != 1 || accts[0].ID != made.ID {
		t.Fatalf("Sani sees %+v", accts)
	}
	// the owner cannot run on it, and it cannot be handed on
	a, _ := s.store.GetAccount(ctx, made.ID)
	if s.mayUseAccount(ctx, ou, a) {
		t.Fatal("the owner may not run on Sani's login")
	}
	owner.want("PATCH", "/api/v1/accounts/"+itoa(made.ID), map[string]any{"user_id": ou.ID}, 400, nil)
	sani.want("PATCH", "/api/v1/accounts/"+itoa(made.ID), map[string]any{"label": "Sani's"}, 200, nil)

	// an owner's account handed to Sani moves into Sani's folder
	other := filepath.Join(root, ".codex-spare")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".codex", "config.toml"), filepath.Join(other, "config.toml")); err != nil {
		t.Fatal(err)
	}
	spare := &store.Account{Provider: "codex", Label: "Spare", Home: other}
	if err := s.store.CreateAccount(ctx, spare); err != nil {
		t.Fatal(err)
	}
	if s.mayUseAccount(ctx, su, spare) {
		t.Fatal("an account nobody owns is not Sani's")
	}
	owner.want("PATCH", "/api/v1/accounts/"+itoa(spare.ID), map[string]any{"user_id": su.ID}, 200, nil)
	spare, _ = s.store.GetAccount(ctx, spare.ID)
	if !strings.HasPrefix(spare.Home, s.personHome(su.ID)+"/") || !s.mayUseAccount(ctx, su, spare) {
		t.Fatalf("handed-on login at %s", spare.Home)
	}
	if _, err := os.Lstat(filepath.Join(spare.Home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("the link to the owner's settings should be gone")
	}

	// folders: their projects only
	var dirs dirListing
	sani.want("GET", "/api/v1/fs/dirs", nil, 200, &dirs)
	if dirs.Path != s.personProjects(su.ID) || len(dirs.Roots) != 1 || dirs.Roots[0] != s.personProjects(su.ID) {
		t.Fatalf("Sani's picker: %+v", dirs)
	}
	sani.want("GET", "/api/v1/fs/dirs?path="+root, nil, 400, nil)
	if _, err := s.checkDirFor(ctx, su, root); err == nil {
		t.Fatal("the server's folders are not Sani's")
	}
	if _, err := s.checkDirFor(ctx, ou, root); err != nil {
		t.Fatal(err)
	}
	// a link out of their folder leads nowhere
	link := filepath.Join(s.personProjects(su.ID), "escape")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.checkDirFor(ctx, su, link); err == nil {
		t.Fatal("a symlink out of Sani's folder must not count as inside")
	}
	sani.want("GET", "/api/v1/fs/dirs", nil, 200, &dirs)
	for _, d := range dirs.Dirs {
		if d.Name == "escape" {
			t.Fatal("the link out should not be listed")
		}
	}

	// a thread of theirs outside their folder does not start
	th := &store.Thread{UserID: su.ID, Provider: "claude", Cwd: root, AccountID: made.ID}
	if err := s.checkRunFor(ctx, th); err == nil || !strings.Contains(err.Error(), "outside your own folder") {
		t.Fatalf("run outside: %v", err)
	}
	// on the server's login it moves to theirs
	th = &store.Thread{ID: "t-sani", UserID: su.ID, Provider: "claude", Mode: "default", Cwd: s.personProjects(su.ID)}
	if err := s.store.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	err := s.checkRunFor(ctx, th)
	if th.AccountID != made.ID {
		t.Fatalf("thread account %d, want %d", th.AccountID, made.ID)
	}
	if err != nil && !agent.IsJailUnavailable(err) {
		t.Fatal(err)
	}

	// a team's folder (the owner's checkout) maps to their own copy
	team := filepath.Join(root, "repo")
	_ = os.MkdirAll(team, 0o755)
	if _, err := s.ownCopy(ctx, su, team); err == nil || !strings.Contains(err.Error(), "Clone the repository into") {
		t.Fatalf("no copy yet: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(s.personProjects(su.ID), "repo"), 0o755)
	if got, err := s.ownCopy(ctx, su, team); err != nil || got != filepath.Join(s.personProjects(su.ID), "repo") {
		t.Fatalf("own copy %q %v", got, err)
	}
	if got, _ := s.ownCopy(ctx, ou, team); got != team {
		t.Fatalf("the owner keeps the folder: %q", got)
	}

	// remote servers are the owner's
	var hosts []store.Host
	sani.want("GET", "/api/v1/hosts", nil, 200, &hosts)
	if len(hosts) != 0 {
		t.Fatal(hosts)
	}
	if _, err := s.checkHostUse(ctx, su, 1); err == nil {
		t.Fatal("Sani may not use the owner's servers")
	}
}

// At start, people's saved folders and Ask chats move into their own folder
// and accounts nobody owned become the owner's.
func TestMigratePeople(t *testing.T) {
	s, h, root := testServer(t)
	ctx := context.Background()
	_, ou := person(t, s, h, "owner@example.test", "admin", true)
	_, su := person(t, s, h, "sani@example.test", "member", true)
	ws := &store.Workspace{UserID: su.ID, Name: "Home", Path: root}
	if err := s.store.SaveWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ask := &store.Thread{ID: "ask-1", UserID: su.ID, Provider: "claude", Mode: "default", Cwd: filepath.Join(s.cfg.DataDir, "ask-workspace"), Source: "ask", ProviderSessionID: "old"}
	if err := s.store.CreateThread(ctx, ask); err != nil {
		t.Fatal(err)
	}
	shared := &store.Account{Provider: "claude", Label: "Shared", Home: filepath.Join(root, ".claude-shared")}
	if err := s.store.CreateAccount(ctx, shared); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(root, ".claude-sani")
	_ = os.MkdirAll(theirs, 0o700)
	_ = os.WriteFile(filepath.Join(theirs, ".credentials.json"), []byte("{}"), 0o600)
	given := &store.Account{Provider: "claude", Label: "Sani", Home: theirs, UserID: &su.ID}
	if err := s.store.CreateAccount(ctx, given); err != nil {
		t.Fatal(err)
	}

	s.migratePeople(ctx)
	s.migratePeople(ctx) // twice: nothing more happens

	list, _ := s.store.ListWorkspaces(ctx, su.ID)
	if len(list) != 1 || list[0].Path != s.personProjects(su.ID) {
		t.Fatalf("workspaces %+v", list)
	}
	got, _ := s.store.GetThread(ctx, su.ID, "ask-1")
	if got.Cwd != filepath.Join(s.personHome(su.ID), ".pocket-ai", "ask-workspace") || got.ProviderSessionID != "" {
		t.Fatalf("ask thread %s %q", got.Cwd, got.ProviderSessionID)
	}
	a, _ := s.store.GetAccount(ctx, shared.ID)
	if a.UserID == nil || *a.UserID != ou.ID {
		t.Fatalf("shared account owner %v", a.UserID)
	}
	b, _ := s.store.GetAccount(ctx, given.ID)
	if b.Home != filepath.Join(s.personHome(su.ID), ".claude") {
		t.Fatalf("Sani's account at %s", b.Home)
	}
	if _, err := os.Stat(filepath.Join(b.Home, ".credentials.json")); err != nil {
		t.Fatal("the login came along: ", err)
	}
}

// The jail: a person's command gets none of the server's environment, and
// without bubblewrap it does not run (unless SANDBOX=off).
func TestJailCommand(t *testing.T) {
	people := t.TempDir()
	person := filepath.Join(people, "7")
	_ = os.MkdirAll(filepath.Join(person, "projects"), 0o700)
	t.Setenv("POCKET_SECRET", "hunter2")

	agent.SetJail(agent.JailConfig{People: people, Bin: filepath.Join(people, "no-bwrap")})
	t.Cleanup(func() { agent.SetJail(agent.JailConfig{}) })
	if _, err := agent.LocalCmd(nil, filepath.Join(person, "projects"), nil, "true"); !agent.IsJailUnavailable(err) {
		t.Fatalf("no bubblewrap: %v", err)
	}
	cmd, err := agent.LocalCmd(nil, "/", []string{"FOO=1"}, "true")
	if err != nil || cmd.Path == "" || !strings.HasSuffix(cmd.Path, "true") {
		t.Fatalf("the server's own commands run as before: %v %v", cmd, err)
	}
	// a login folder in env is enough to tell
	if _, err := agent.LocalCmd(nil, "/", []string{"CODEX_HOME=" + filepath.Join(person, "home", ".codex")}, "codex"); !agent.IsJailUnavailable(err) {
		t.Fatalf("by env: %v", err)
	}

	agent.SetJail(agent.JailConfig{People: people, Off: true})
	cmd, err = agent.LocalCmd(nil, "/srv", []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(person, "home", ".claude")}, "env")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	env := string(out)
	if strings.Contains(env, "hunter2") || !strings.Contains(env, "HOME="+filepath.Join(person, "home")) {
		t.Fatalf("env inside:\n%s", env)
	}
	if cmd.Dir != filepath.Join(person, "home") {
		t.Fatalf("a person's command starts in their folder, not %s", cmd.Dir)
	}

	// symlinks out of a person's folder are not followed by the server
	secret := filepath.Join(t.TempDir(), "secret.jsonl")
	_ = os.WriteFile(secret, []byte("private"), 0o600)
	link := filepath.Join(person, "home", "x.jsonl")
	_ = os.Symlink(secret, link)
	if agent.Confined(link) {
		t.Fatal("link out counted as inside")
	}
	if _, err := agent.ReadConfined(link); err == nil {
		t.Fatal("read through a link out")
	}
	if err := agent.WriteConfined(link, []byte("x"), 0o600); err == nil {
		t.Fatal("wrote through a link out")
	}
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatal("the file outside changed")
	}
	inside := filepath.Join(person, "home", "ok.jsonl")
	_ = os.WriteFile(inside, []byte("{}"), 0o600)
	if !agent.Confined(inside) || !agent.Confined(filepath.Join(person, "home", "new", "file")) || !agent.Confined(secret) {
		t.Fatal("ordinary paths")
	}
}
