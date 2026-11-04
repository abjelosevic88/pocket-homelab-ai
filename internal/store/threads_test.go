package store

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// Favorites lists a person's pinned threads, archived ones too, and nobody else's.
func TestListThreadsFavorites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := &User{Email: "a@example.com", Name: "A", Role: "admin"}
	b := &User{Email: "b@example.com", Name: "B", Role: "member"}
	for _, u := range []*User{a, b} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, th := range []*Thread{
		{ID: "fav", UserID: a.ID, Pinned: true},
		{ID: "fav-archived", UserID: a.ID, Pinned: true, Archived: true},
		{ID: "plain", UserID: a.ID},
		{ID: "theirs", UserID: b.ID, Pinned: true},
	} {
		pinned, archived := th.Pinned, th.Archived
		if err := s.CreateThread(ctx, th); err != nil {
			t.Fatal(err)
		}
		th.Pinned, th.Archived = pinned, archived
		if err := s.UpdateThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListThreads(ctx, a.ID, ThreadFilter{Favorites: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, th := range list {
		got[th.ID] = true
	}
	if len(list) != 2 || !got["fav"] || !got["fav-archived"] {
		t.Fatalf("favorites = %v", got)
	}
	list, _ = s.ListThreads(ctx, a.ID, ThreadFilter{})
	if len(list) != 2 { // fav + plain: the archived favorite stays out of the usual list
		t.Fatalf("usual list has %d threads", len(list))
	}
}
