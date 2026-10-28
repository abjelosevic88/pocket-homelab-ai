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

// Everyone adds machines of their own: a member's are reached with a key
// Pocket AI makes in their folder, ssh running from there, and nobody sees
// or uses anyone else's.
func TestPeopleOwnServers(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	owner, ou := person(t, s, h, "owner@example.test", "admin", true)
	sani, su := person(t, s, h, "sani@example.test", "member", true)
	agent.SetJail(agent.JailConfig{People: s.cfg.PeopleDir, Off: true})
	t.Cleanup(func() { agent.SetJail(agent.JailConfig{}) })

	// fake ssh: logs its arguments and HOME, answers the check like a shell
	log := filepath.Join(t.TempDir(), "ssh.log")
	fake := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nprintf 'HOME=%s ARGS=%s\\n' \"$HOME\" \"$*\" >> " + log + "\necho home=/home/them\necho path=/home/them\necho version_claude=2.0\necho missing_codex=codex is not on the PATH\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))

	mine := &store.Host{Name: "owner-box", Target: "me@owner.invalid"}
	if err := s.store.CreateHost(ctx, mine); err != nil {
		t.Fatal(err)
	}

	// Sani adds one: theirs, checked over their own key
	var made store.Host
	sani.want("POST", "/api/v1/hosts", map[string]any{"target": "sani@box.invalid"}, 201, &made)
	if made.UserID != su.ID || !made.Info.OK {
		t.Fatalf("Sani's server: %+v", made)
	}
	home := s.personHome(su.ID)
	keyFile := filepath.Join(home, ".ssh", agent.PersonKey)
	if fi, err := os.Stat(keyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("Sani's key: %v %v", fi, err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"HOME=" + home + " ", "-F none", "-i " + keyFile, "IdentitiesOnly=yes", "IdentityAgent=none",
		"UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts"), "sani@box.invalid"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssh for Sani lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ControlMaster") {
		t.Fatalf("Sani's ssh shares no connection:\n%s", got)
	}

	// the key they paste on their machine is that key
	var key struct {
		Keys []struct{ Key string } `json:"keys"`
		Own  bool                   `json:"own"`
	}
	sani.want("GET", "/api/v1/hosts/key", nil, 200, &key)
	pub, _ := os.ReadFile(keyFile + ".pub")
	if !key.Own || len(key.Keys) != 1 || key.Keys[0].Key != strings.TrimSpace(string(pub)) || !strings.HasPrefix(key.Keys[0].Key, "ssh-ed25519 ") {
		t.Fatalf("Sani's key: %+v", key)
	}
	key.Own = false
	owner.want("GET", "/api/v1/hosts/key", nil, 200, &key)
	if key.Own {
		t.Fatal("the owner is shown the server's keys")
	}

	// each sees their own
	var list []store.Host
	sani.want("GET", "/api/v1/hosts", nil, 200, &list)
	if len(list) != 1 || list[0].ID != made.ID {
		t.Fatalf("Sani sees %+v", list)
	}
	owner.want("GET", "/api/v1/hosts", nil, 200, &list)
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("the owner sees %+v", list)
	}

	// and nobody touches or runs on anyone else's
	sani.want("PATCH", "/api/v1/hosts/"+itoa(mine.ID), map[string]any{"name": "x"}, 400, nil)
	sani.want("DELETE", "/api/v1/hosts/"+itoa(mine.ID), nil, 400, nil)
	sani.want("POST", "/api/v1/hosts/"+itoa(mine.ID)+"/check", nil, 400, nil)
	owner.want("PATCH", "/api/v1/hosts/"+itoa(made.ID), map[string]any{"name": "x"}, 400, nil)
	if _, err := s.checkHostUse(ctx, ou, made.ID); err == nil {
		t.Fatal("the owner may not run on Sani's server")
	}
	if _, err := s.checkHostUse(ctx, su, mine.ID); err == nil {
		t.Fatal("Sani may not run on the owner's server")
	}
	if hst, err := s.checkHostUse(ctx, su, made.ID); err != nil || remoteOf(hst).Home != home {
		t.Fatalf("Sani runs on theirs from their folder: %v %+v", err, hst)
	}
	// and starts a conversation there on that machine's own login, not the
	// server's (the fake ssh is no agent, so only the start is checked)
	if _, err := s.createThread(ctx, su, newThread{Provider: "claude", HostID: made.ID, Text: "hello"}, "web", ""); err != nil &&
		strings.Contains(err.Error(), "Sign in to") {
		t.Fatalf("Sani is sent to sign in for their own server: %v", err)
	}
	threads, _ := s.store.ListThreads(ctx, su.ID, store.ThreadFilter{Limit: 5})
	if len(threads) != 1 || threads[0].HostID != made.ID || threads[0].AccountID != 0 {
		t.Fatalf("Sani's conversation on their server: %+v", threads)
	}
	if hst, _ := s.host(ctx, mine.ID); remoteOf(hst).Home != "" {
		t.Fatal("the owner's server goes over the server's keys")
	}
	sani.want("PATCH", "/api/v1/hosts/"+itoa(made.ID), map[string]any{"name": "Sani's box"}, 200, nil)

	// a person who leaves takes their servers along
	if err := s.store.DeleteUser(ctx, su.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetHost(ctx, made.ID); err == nil {
		t.Fatal("Sani's server outlived Sani")
	}
}
