package server

import (
	"testing"
	"time"
)

// A background shell dies with the agent, so an agent whose turn is over but
// whose shell still runs is not idle — neither for the reaper nor for a restart.
func TestIdleSkipsRunsWithBackgroundShells(t *testing.T) {
	rt := &engine{runs: map[string]*run{}}
	old := time.Now().Add(-time.Hour)
	rt.runs["quiet"] = &run{threadID: "quiet", lastUsed: old, pending: map[string]bool{}}
	rt.runs["building"] = &run{threadID: "building", lastUsed: old, shells: 1, pending: map[string]bool{}}
	rt.runs["stuck"] = &run{threadID: "stuck", lastUsed: time.Now().Add(-shellHold - time.Minute), shells: 1, pending: map[string]bool{}}

	got := rt.idle(20 * time.Minute)
	if len(got) != 2 || got[0] != "stuck" || got[1] != "quiet" {
		t.Fatalf("idle: %v", got)
	}
	if n := rt.busyCount(); n != 1 {
		t.Fatalf("busy: %d", n)
	}
}
