package agent

import (
	"os/exec"
	"testing"
)

// Only a connection that was never made is tried again: the script did not
// run, so running it again cannot do anything twice.
func TestNotConnected(t *testing.T) {
	exit := func(code string) error { return exec.Command("sh", "-c", "exit "+code).Run() }
	for _, c := range []struct {
		code, stderr string
		want         bool
	}{
		{"255", "ssh: connect to host 51.161.119.116 port 22: No route to host", true},
		{"255", "ssh: Could not resolve hostname athena-box: Temporary failure in name resolution", true},
		{"255", "ssh: connect to host x port 22: Connection timed out", true},
		{"255", "ubuntu@x: Permission denied (publickey).", false},
		{"255", "Connection to x closed by remote host.", false}, // may have run already
		{"1", "No route to host", false},                         // the script's own failure
	} {
		if got := notConnected(exit(c.code), c.stderr); got != c.want {
			t.Errorf("exit %s %q: got %v", c.code, c.stderr, got)
		}
	}
}
