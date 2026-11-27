package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every Codex login folder is asked inside the one script (one ssh
// connection): the default without CODEX_HOME, the others with it; one whose
// app-server dies gives its last stderr lines, not a dead "process exited".
func TestRemoteCodexSh(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not here")
	}
	home := t.TempDir()
	for _, d := range []string{".codex", ".codex-pro", ".codex-bad", ".codex-mute"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, d, "auth.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A stand-in app-server: answers in order, and only once initialized.
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte(`#!/usr/bin/env python3
import sys, os, json, time
assert sys.argv[1:] == ['app-server'], sys.argv
h = os.environ.get('CODEX_HOME', '')
if h.endswith('.codex-bad'):
    sys.stderr.write('\x1b[31mERROR\x1b[0m could not read auth.json\n'); sys.exit(1)
ready = False
for line in sys.stdin:
    m = json.loads(line)
    if m.get('method') == 'initialize':
        print(json.dumps({'id': m['id'], 'result': {'codexHome': h}}), flush=True)
    elif m.get('method') == 'initialized':
        ready = True
    elif m.get('method') == 'account/read':
        assert ready
        if h.endswith('.codex-mute'):
            continue
        print(json.dumps({'method': 'account/updated', 'params': {'id': 2}}), flush=True)
        acct = {'type': 'chatgpt', 'email': 'pro@x.test', 'planType': 'pro'} if h.endswith('.codex-pro') else None
        assert h == '' or h.endswith('.codex-pro'), h
        print(json.dumps({'id': m['id'], 'result': {'account': acct}}, separators=(',', ':')), flush=True)
time.sleep(30)  # stdin closed: it is ended, not waited for
`), 0o700); err != nil {
		t.Fatal(err)
	}
	// the wait for a silent app-server is 40 half seconds: 3 here
	script := strings.Replace(RemoteHomesSh+RemoteCodexSh(), "[ $i -lt 40 ]", "[ $i -lt 3 ]", 1) + `codex_ask "$1"; codex_tell; [ ! -e "$ct" ] || echo "left $ct"`
	cmd := exec.Command("sh", "-c", script, "sh", fake)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("took %s: an answered app-server was waited for", d)
	}
	want := []string{
		"codex_home=" + home + "/.codex",
		`codex_status={"id":2,"result":{"account":null}}`,
		"codex_home=" + home + "/.codex-bad",
		"codex_error=\x1b[31mERROR\x1b[0m could not read auth.json",
		"codex_home=" + home + "/.codex-mute",
		"codex_error=",
		"codex_home=" + home + "/.codex-pro",
		`codex_status={"id":2,"result":{"account":{"type":"chatgpt","email":"pro@x.test","planType":"pro"}}}`,
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := range got {
		got[i] = strings.TrimRight(got[i], " ")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	email, plan, in, err := ParseCodexAccount([]byte(`{"account":{"type":"chatgpt","email":"pro@x.test","planType":"pro"}}`))
	if err != nil || !in || email != "pro@x.test" || plan != "PRO" {
		t.Fatalf("account: %q %q %v %v", email, plan, in, err)
	}
	if _, _, in, err := ParseCodexAccount([]byte(`{"account":null}`)); err != nil || in {
		t.Fatalf("signed out: %v %v", in, err)
	}
}
