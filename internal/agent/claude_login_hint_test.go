package agent

import (
	"strings"
	"testing"
)

func TestLoginEndedHint(t *testing.T) {
	msg := "Failed to authenticate: this login reached its time limit and cannot be renewed. Run claude auth login to sign in again"
	if got := loginEndedHint(msg); !strings.HasPrefix(got, "This Claude login has ended. Sign in again in Settings → Machines") || !strings.Contains(got, msg) {
		t.Fatal(got)
	}
	if got := loginEndedHint("Stopped: too many turns."); got != "Stopped: too many turns." {
		t.Fatal(got)
	}
}
