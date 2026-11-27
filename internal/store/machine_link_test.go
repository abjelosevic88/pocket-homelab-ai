package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInstallationIDMadeOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, err := s.InstallationID(ctx)
	if err != nil || a == "" {
		t.Fatalf("first: %q %v", a, err)
	}
	b, _ := s.InstallationID(ctx)
	if a != b {
		t.Fatalf("changed: %q then %q", a, b)
	}
}

func TestControllerLease(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	ttl := 30 * time.Second
	if ok, err := s.TakeLease(ctx, "a", now, ttl); !ok || err != nil {
		t.Fatalf("a takes: %v %v", ok, err)
	}
	if ok, _ := s.TakeLease(ctx, "b", now.Add(10*time.Second), ttl); ok {
		t.Fatal("b took a lease a still holds")
	}
	if ok, _ := s.TakeLease(ctx, "a", now.Add(10*time.Second), ttl); !ok {
		t.Fatal("a could not renew")
	}
	// a's renewal ran to now+40s: b gets it only after that
	if ok, _ := s.TakeLease(ctx, "b", now.Add(39*time.Second), ttl); ok {
		t.Fatal("b took it before it ran out")
	}
	if ok, _ := s.TakeLease(ctx, "b", now.Add(40*time.Second), ttl); !ok {
		t.Fatal("b could not take a lease that ran out")
	}
	if h, _, _ := s.LeaseHolder(ctx); h != "b" {
		t.Fatalf("holder %q", h)
	}
	// release: only the holder's release counts
	_ = s.ReleaseLease(ctx, "a")
	if h, _, _ := s.LeaseHolder(ctx); h != "b" {
		t.Fatalf("a released b's lease")
	}
	_ = s.ReleaseLease(ctx, "b")
	if ok, _ := s.TakeLease(ctx, "a", now.Add(41*time.Second), ttl); !ok {
		t.Fatal("a could not take a released lease")
	}
}

func TestNextLinkGenerationOnlyForHolder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	h := &Host{Name: "box", Target: "me@box"}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	if _, err := s.NextLinkGeneration(ctx, h.ID, "a", now); !errors.Is(err, ErrNotLeaseHolder) {
		t.Fatalf("no lease: %v", err)
	}
	_, _ = s.TakeLease(ctx, "a", now, 30*time.Second)
	g1, err := s.NextLinkGeneration(ctx, h.ID, "a", now)
	if err != nil || g1 != 1 {
		t.Fatalf("g1 %d %v", g1, err)
	}
	g2, _ := s.NextLinkGeneration(ctx, h.ID, "a", now)
	if g2 != 2 {
		t.Fatalf("g2 %d", g2)
	}
	if _, err := s.NextLinkGeneration(ctx, h.ID, "b", now); !errors.Is(err, ErrNotLeaseHolder) {
		t.Fatalf("b allocated: %v", err)
	}
	// a's lease ran out: not even a may allocate
	if _, err := s.NextLinkGeneration(ctx, h.ID, "a", now.Add(31*time.Second)); !errors.Is(err, ErrNotLeaseHolder) {
		t.Fatalf("expired a allocated: %v", err)
	}
	got, _ := s.GetHost(ctx, h.ID)
	if got.LinkGeneration != 2 {
		t.Fatalf("stored generation %d", got.LinkGeneration)
	}
}

func TestHostLinkModeAndFallback(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	h := &Host{Name: "box", Target: "me@box"}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetHost(ctx, h.ID)
	if got.LinkMode != LinkOff || got.LinkFallback != nil {
		t.Fatalf("defaults: %q %+v", got.LinkMode, got.LinkFallback)
	}
	if err := s.SetHostLinkMode(ctx, h.ID, "bogus"); err == nil {
		t.Fatal("bogus mode stored")
	}
	_ = s.SetHostLinkMode(ctx, h.ID, LinkStatus)
	_ = s.SetHostLinkFallback(ctx, h.ID, &LinkFallback{At: "2026-11-27T13:02:00Z", Why: "the relay is another version"})
	// an edit of the machine keeps them
	got, _ = s.GetHost(ctx, h.ID)
	got.Name = "Box"
	_ = s.UpdateHost(ctx, got)
	got, _ = s.GetHost(ctx, h.ID)
	if got.LinkMode != LinkStatus || got.LinkFallback == nil || got.LinkFallback.Why != "the relay is another version" {
		t.Fatalf("after edit: %q %+v", got.LinkMode, got.LinkFallback)
	}
	_ = s.SetHostLinkFallback(ctx, h.ID, nil)
	got, _ = s.GetHost(ctx, h.ID)
	if got.LinkFallback != nil {
		t.Fatal("fallback not cleared")
	}
}

func TestAgentRoutes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	h := &Host{Name: "box", Target: "me@box"}
	_ = s.CreateHost(ctx, h)
	if _, err := s.GetAgentRoute(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := s.SetAgentRoute(ctx, &AgentRoute{ThreadID: "t1", HostID: h.ID, Port: 41234, Route: "sideways"}); err == nil {
		t.Fatal("bad route stored")
	}
	_ = s.SetAgentRoute(ctx, &AgentRoute{ThreadID: "t1", HostID: h.ID, Port: 41234, Route: RouteToLink})
	_ = s.SetAgentRoute(ctx, &AgentRoute{ThreadID: "t2", HostID: h.ID, Port: 41235, Route: RouteSSH})
	_ = s.SetAgentRoute(ctx, &AgentRoute{ThreadID: "t1", HostID: h.ID, Port: 41234, Route: RouteLink})
	r, err := s.GetAgentRoute(ctx, "t1")
	if err != nil || r.Route != RouteLink || r.Port != 41234 || r.UpdatedAt == "" {
		t.Fatalf("t1: %+v %v", r, err)
	}
	list, _ := s.ListAgentRoutes(ctx, h.ID)
	if len(list) != 2 {
		t.Fatalf("list %d", len(list))
	}
	_ = s.DeleteAgentRoute(ctx, "t2")
	if list, _ := s.ListAgentRoutes(ctx, h.ID); len(list) != 1 {
		t.Fatalf("after delete %d", len(list))
	}
	_ = s.DeleteHost(ctx, h.ID)
	if list, _ := s.ListAgentRoutes(ctx, h.ID); len(list) != 0 {
		t.Fatalf("routes outlived their machine: %d", len(list))
	}
}

// A machine with conversations is never taken for a new one (the link's
// default for new machines would move its agents without a Confirm).
func TestHostHasThreads(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := &User{Email: "a@example.com", Name: "A", Role: "admin"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	h := &Host{Name: "box", Target: "box"}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if s.HostHasThreads(ctx, h.ID) {
		t.Fatal("a new machine has conversations")
	}
	th := &Thread{ID: "t1", UserID: u.ID, HostID: h.ID}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if !s.HostHasThreads(ctx, h.ID) {
		t.Fatal("its conversation was not seen")
	}
}
