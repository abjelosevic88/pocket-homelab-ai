package agent

import (
	"os/exec"
	"strings"
	"testing"
)

// The remote command must reach the CLI byte for byte whatever login shell
// the remote account has (sshd runs it with `$SHELL -c`).
func TestRemoteCommandQuoting(t *testing.T) {
	args := []string{"it's", `a "quoted" \ back\slash`, "line\nbreak", "$HOME `id` $(id)", "", "--flag=x y", "ünï ✓"}
	dir := t.TempDir()
	script := remoteScript(dir, []string{"POCKET_AI=1", "X=a b'c"}, "sh", append([]string{"-c", `printf '%s|' "$PWD" "$POCKET_AI" "$X" "$@"`, "sh"}, args...))
	want := dir + "|1|a b'c|" + strings.Join(args, "|") + "|"
	for _, shell := range []string{"sh", "bash", "zsh", "fish"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		out, err := exec.Command(shell, "-c", remoteCommand(script)).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", shell, err, out)
		}
		if string(out) != want {
			t.Errorf("%s:\n got %q\nwant %q", shell, out, want)
		}
	}
}
