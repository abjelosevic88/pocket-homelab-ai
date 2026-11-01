package server

import (
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
)

// A background shell dies with the agent, so an agent whose turn is over but
// whose shell still runs is not idle — neither for the reaper nor for a restart.
func TestIdleSkipsRunsWithBackgroundShells(t *testing.T) {
	rt := &engine{runs: map[string]*run{}}
	old := time.Now().Add(-time.Hour)
	rt.runs["quiet"] = &run{threadID: "quiet", lastUsed: old, pending: map[string]bool{}}
	rt.runs["building"] = &run{threadID: "building", lastUsed: old, tasks: []agent.Task{{ID: "b1", Kind: "shell"}}, pending: map[string]bool{}}
	rt.runs["stuck"] = &run{threadID: "stuck", lastUsed: time.Now().Add(-taskHold - time.Minute), tasks: []agent.Task{{ID: "b1", Kind: "shell"}}, pending: map[string]bool{}}

	got := rt.idle(20 * time.Minute)
	if len(got) != 2 || got[0] != "stuck" || got[1] != "quiet" {
		t.Fatalf("idle: %v", got)
	}
	if n := rt.busyCount(); n != 1 {
		t.Fatalf("busy: %d", n)
	}
}

// Over MAX_LIVE with every other agent remote-controlled, the agent just
// started (idle until its first message) used to be the only candidate and
// was stopped the moment it started.
func TestEnforceMaxKeepsTheAgentJustStarted(t *testing.T) {
	rt := &engine{s: &Server{cfg: &config.Config{MaxLive: 2}}, runs: map[string]*run{}}
	old := time.Now().Add(-time.Hour)
	rt.runs["rc1"] = &run{threadID: "rc1", lastUsed: old, remote: true, pending: map[string]bool{}}
	rt.runs["rc2"] = &run{threadID: "rc2", lastUsed: old, remote: true, pending: map[string]bool{}}
	rt.runs["new"] = &run{threadID: "new", lastUsed: time.Now(), pending: map[string]bool{}}

	rt.enforceMax("new")
	if rt.runs["new"] == nil {
		t.Fatal("the agent just started was stopped")
	}
}
