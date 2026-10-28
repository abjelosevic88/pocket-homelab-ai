package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestJailE2E runs real bubblewrap: POCKET_JAIL_E2E=<people dir> (next to
// it the server's data; the Pocket Box VM test runs this under the same
// systemd hardening as the Pocket AI service). A person's process sees
// their own folder only, none of the server's files or environment, and no
// one else's processes.
func TestJailE2E(t *testing.T) {
	people := os.Getenv("POCKET_JAIL_E2E")
	if people == "" {
		t.Skip("set POCKET_JAIL_E2E to a people folder to run bubblewrap for real")
	}
	if err := os.MkdirAll(people, 0o700); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(filepath.Dir(people), "server-secret.db")
	if err := os.WriteFile(server, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_PASSWORD", "hunter2")
	SetJail(JailConfig{People: people})
	t.Cleanup(func() { SetJail(JailConfig{}) })
	if on, _, why := JailStatus(); !on {
		t.Fatalf("sandbox unavailable: %s", why)
	}
	a, b := filepath.Join(people, "1"), filepath.Join(people, "2")
	for _, d := range []string{filepath.Join(a, "projects"), filepath.Join(b, "projects"), filepath.Join(b, "home")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(b, "home", "theirs"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	// someone else's agent, running meanwhile
	other, err := LocalCmd(nil, filepath.Join(b, "projects"), nil, "sleep", "60")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	time.Sleep(300 * time.Millisecond)

	script := `set -e
echo ok > "$HOME/written"
test "$(pwd)" = "$3"
test ! -e "$1"                        # the server's data
test ! -e "$2/home/theirs"            # someone else's folder
test ! -e /home/abjelosevic88 && test ! -e /root/.ssh
if env | grep -q hunter2; then echo "server env leaked"; exit 1; fi
n=$(ls /proc | grep -c '^[0-9][0-9]*$')
if [ "$n" -gt 6 ]; then echo "sees $n processes"; ls /proc; exit 1; fi
if pgrep sleep >/dev/null 2>&1; then echo "sees the other agent"; exit 1; fi
echo inside-ok`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := LocalCmd(ctx, filepath.Join(a, "projects"), []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(a, "home", ".claude")},
		"sh", "-c", script, "sh", server, b, filepath.Join(a, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("inside:\n%s", out)
	if err != nil {
		t.Fatalf("jail check failed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(a, "home", "written")); err != nil || string(b) != "ok\n" {
		t.Fatalf("their home is not writable from inside: %q %v", b, err)
	}

	// a real CLI (POCKET_JAIL_E2E_CLI=codex|claude) starts in there on the
	// person's own login folder
	if cli := os.Getenv("POCKET_JAIL_E2E_CLI"); cli != "" {
		home := filepath.Join(a, "home", "."+cli)
		envKey := "CLAUDE_CONFIG_DIR="
		if cli == "codex" {
			envKey = "CODEX_HOME="
		}
		_ = os.MkdirAll(home, 0o700)
		cmd, err := LocalCmd(ctx, filepath.Join(a, "projects"), []string{envKey + home}, cli, "--version")
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		t.Logf("%s --version inside: %s", cli, out)
		if err != nil {
			t.Fatalf("%s does not start in the jail: %v", cli, err)
		}
		status := []string{"login", "status"}
		if cli == "claude" {
			status = []string{"auth", "status"}
		}
		cmd, _ = LocalCmd(ctx, filepath.Join(a, "projects"), []string{envKey + home}, cli, status...)
		out, _ = cmd.CombinedOutput()
		t.Logf("%s %v inside: %s", cli, status, out)
	}
}
