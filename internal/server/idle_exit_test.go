package server

import (
	"fmt"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// An idle agent whose machine went away is not a failure: the chat stays
// idle and where it was, out of Needs you. One cut off mid-turn is, and is
// unread until opened.
func TestIdleAgentExitIsNotAFailure(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "idle-exit@example.test", "admin", true)
	newRun := func(id string) *run {
		r := &run{threadID: id, userID: u.ID, sess: &rewindIdleSession{}, cancel: func() {}, live: map[string]*agent.Item{}, dirty: map[string]bool{}, pending: map[string]bool{}}
		s.rt.mu.Lock()
		s.rt.runs[id] = r
		s.rt.mu.Unlock()
		return r
	}

	idle := thread(t, s, u.ID, root)
	before, _ := s.store.GetThreadAny(t.Context(), idle.ID)
	(&runSink{rt: s.rt, r: newRun(idle.ID)}).Exited(fmt.Errorf("Lost the connection to arno@host (timed out). Send a message to continue."))
	after, _ := s.store.GetThreadAny(t.Context(), idle.ID)
	if after.Status != "idle" || after.Error != "" || after.ActivityAt != before.ActivityAt || after.Unread {
		t.Fatalf("idle exit marked the chat: %+v", after)
	}
	if s.rt.get(idle.ID) != nil {
		t.Fatal("the dead run is still attached")
	}

	busy := thread(t, s, u.ID, root)
	r := newRun(busy.ID)
	r.running = true
	(&runSink{rt: s.rt, r: r}).Exited(fmt.Errorf("Lost the connection to arno@host (timed out). Send a message to continue."))
	cut, _ := s.store.GetThreadAny(t.Context(), busy.ID)
	if cut.Status != "error" || cut.Error == "" || !cut.Unread {
		t.Fatalf("a turn cut off is not shown as failed and unseen: %+v", cut)
	}
}
