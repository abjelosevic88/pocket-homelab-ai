package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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

func TestListItemsTail(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := &User{Email: "a@example.com", Name: "A", Role: "admin"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	th := &Thread{UserID: u.ID, Title: "t", Provider: "claude", Cwd: "/"}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	// 5 turns: a message and 3 replies each = 20 items
	for i := 0; i < 5; i++ {
		for j, kind := range []string{"user", "assistant", "tool", "turn"} {
			if _, err := s.UpsertItem(ctx, th.ID, fmt.Sprintf("k%d-%d", i, j), kind, []byte(`{}`), ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	keys := func(items []*Item) string {
		var k []string
		for _, it := range items {
			k = append(k, it.Key)
		}
		return strings.Join(k, " ")
	}
	// the last 2 turns
	got, earlier, err := s.ListItemsTail(ctx, th.ID, 0, 2, 100)
	if err != nil || earlier != 12 || keys(got) != "k3-0 k3-1 k3-2 k3-3 k4-0 k4-1 k4-2 k4-3" {
		t.Fatalf("tail: %s, %d earlier, %v", keys(got), earlier, err)
	}
	// the item cap wins when it is the tighter one
	got, earlier, _ = s.ListItemsTail(ctx, th.ID, 0, 2, 3)
	if earlier != 17 || keys(got) != "k4-1 k4-2 k4-3" {
		t.Fatalf("capped: %s, %d earlier", keys(got), earlier)
	}
	// the page before the first one, down to the start
	got, earlier, _ = s.ListItemsTail(ctx, th.ID, got[0].ID, 10, 100)
	if earlier != 0 || len(got) != 17 || got[0].Key != "k0-0" || got[16].Key != "k4-0" {
		t.Fatalf("earlier page: %s, %d earlier", keys(got), earlier)
	}
}
