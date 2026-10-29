package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A machine's own Claude login is shown, signed in over ssh with the code
// pasted in the browser, and only the owner reaches it.
func TestHostLogins(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	owner, _ := person(t, s, h, "host-login@example.test", "admin", true)
	box := &store.Host{Name: "vm", Target: "vm.invalid", ClaudeBin: "/opt/claude"}
	if err := s.store.CreateHost(ctx, box); err != nil {
		t.Fatal(err)
	}
	// Fake ssh: the status script answers "signed out" until the sign-in got
	// the right code; codex is not installed there.
	state := filepath.Join(t.TempDir(), "signed-in")
	script := `#!/usr/bin/env python3
import sys,base64,re,os
args=sys.argv[1:]
assert args[args.index('--')-1]=='vm.invalid'
cmd=base64.b64decode(re.search(r'echo ([A-Za-z0-9+/=]+)',args[-1]).group(1)).decode()
state=` + "'" + state + "'" + `
if 'auth login --claudeai' in cmd and 'CLAUDE_CONFIG_DIR=' in cmd:
  assert 'CLAUDE_CONFIG_DIR=/home/vmuser/.claude-max /opt/claude auth login' in cmd, cmd
  print('Open https://claude.ai/oauth/authorize?code=true&x=2 to sign in', flush=True)
  sys.stdout.write('Paste code here if prompted > '); sys.stdout.flush()
  sys.stdin.readline(); print('Login successful'); sys.exit(0)
if 'auth login --claudeai' in cmd:
  assert '/opt/claude' in cmd and 'BROWSER=true' in cmd
  print('Open https://claude.ai/oauth/authorize?code=true&x=1 to sign in', flush=True)
  sys.stdout.write('Paste code here if prompted > '); sys.stdout.flush()
  code=sys.stdin.readline().strip()
  if code!='good-code': print('Invalid code'); sys.exit(1)
  open(state,'w').write('1'); print('Login successful'); sys.exit(0)
if 'auth status' in cmd:
  assert "set -- /opt/claude codex" in cmd
  print('home=/home/vmuser')
  print('claude_home=/home/vmuser/.claude')
  if os.path.exists(state): print('claude_status={"loggedIn": true, "email": "me@vm.test", "subscriptionType": "max", "orgId": "o1"}')
  else: print('claude_status={"loggedIn": false}')
  print('claude_home=/home/vmuser/.claude-max')
  print('claude_status={"loggedIn": true, "email": "max@vm.test", "subscriptionType": "max", "orgId": "o2"}')
  print('codex_missing=1')
  sys.exit(0)
if 'echo "home=$HOME"' in cmd:  # the Servers check after a sign-in
  print('home=/home/vmuser'); sys.exit(0)
sys.exit(3)
`
	fake := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))

	var list []hostLoginView
	owner.want("GET", "/api/v1/hosts/logins", nil, 200, &list)
	if len(list) != 3 || list[0].Provider != "claude" || list[0].SignedIn || list[0].Home != "/home/vmuser/.claude" || list[0].Dir != "" || list[0].Error != "" {
		t.Fatalf("before: %+v", list)
	}
	// another login folder there (a script runs claude on it)
	if l := list[1]; l.Provider != "claude" || !l.SignedIn || l.Email != "max@vm.test" || l.Dir != "/home/vmuser/.claude-max" || l.Label != "claude-max" {
		t.Fatalf("second login: %+v", l)
	}
	if !list[2].Missing || list[2].Provider != "codex" {
		t.Fatalf("codex should be missing: %+v", list[2])
	}

	base := "/api/v1/hosts/" + strconv.FormatInt(box.ID, 10) + "/login/claude"
	var st map[string]any
	owner.want("POST", base, nil, 200, &st)
	if st["state"] != "waiting" || st["need_code"] != true || st["url"] != "https://claude.ai/oauth/authorize?code=true&x=1" {
		t.Fatalf("start: %+v", st)
	}
	owner.want("POST", base+"/code", map[string]any{"code": "good-code"}, 200, &st)
	if st["state"] != "done" {
		t.Fatalf("code: %+v", st)
	}
	owner.want("GET", "/api/v1/hosts/logins", nil, 200, &list)
	if !list[0].SignedIn || list[0].Email != "me@vm.test" || list[0].Plan != "max" {
		t.Fatalf("after: %+v", list[0])
	}
	// signing in on the other folder runs the CLI on that folder
	owner.want("POST", base+"?dir=/home/vmuser/.claude-max", nil, 200, &st)
	if st["url"] != "https://claude.ai/oauth/authorize?code=true&x=2" {
		t.Fatalf("folder sign-in: %+v", st)
	}
	owner.want("DELETE", base+"?dir=/home/vmuser/.claude-max", nil, 204, nil)
	if code, _ := owner.call("POST", base+"?dir=/home/vmuser/.ssh", nil); code != 400 {
		t.Fatalf("a folder that is no login was accepted: %d", code)
	}

	// Others never reach the owner's machines (their ssh key).
	other, _ := person(t, s, h, "member@example.test", "member", true)
	other.want("GET", "/api/v1/hosts/logins", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("member sees the owner's machines: %+v", list)
	}
	if code, _ := other.call("POST", base, nil); code != 400 {
		t.Fatalf("member started a sign-in there: %d", code)
	}
}
