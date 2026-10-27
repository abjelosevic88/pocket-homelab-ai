package store

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestDeploys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := &User{Email: "a@example.com", Name: "A", Role: "admin"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if d, err := s.LatestDeploy(ctx, "github.com/o/r"); err != nil || d != nil {
		t.Fatalf("none yet: %v %v", d, err)
	}
	d := &Deploy{UserID: u.ID, Project: "github.com/o/r", HostID: 2, Branch: "as-8", SHA: "abc", Source: "projects", Ref: "AS-8", Status: "running", Note: "Pushed", URL: "https://x"}
	if err := s.CreateDeploy(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status, d.Log, d.FinishedAt = "ok", "done\n", Now()
	if err := s.UpdateDeploy(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeploy(ctx, d.ID)
	if err != nil || got.Status != "ok" || got.Note != "Pushed" || got.Log != "done\n" || got.Ref != "AS-8" || got.UserID != u.ID {
		t.Fatalf("got %+v %v", got, err)
	}
	if g, _ := s.LastGoodDeploy(ctx, "github.com/o/r"); g == nil || g.ID != d.ID {
		t.Fatalf("last good: %+v", g)
	}
	if r, _ := s.RunningDeploys(ctx); len(r) != 0 {
		t.Fatalf("running: %+v", r)
	}
}
