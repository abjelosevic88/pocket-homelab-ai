package store

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// A start that created its thread but never recorded it leaves the draft
// claimed with no thread: reading it links the thread; a claim with no thread
// at all is released once stale.
func TestHealLinkDraft(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := &User{Email: "a@example.com", Name: "A", Role: "admin"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	d := &LinkDraft{ID: "d1", UserID: u.ID, Source: "projects", Ref: "p/ATH-49", UploadIDs: []string{}}
	if err := s.CreateLinkDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimLinkDraft(ctx, u.ID, d.ID); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	// another issue's thread and an older one of this issue are not it
	for _, th := range []*Thread{{ID: "other", Source: "link:projects", SourceRef: "p/ATH-1"}, {ID: "t1", Source: "link:projects", SourceRef: "p/ATH-49"}} {
		th.UserID = u.ID
		if err := s.CreateThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetLinkDraft(ctx, u.ID, d.ID)
	if err != nil || got.ThreadID == nil || *got.ThreadID != "t1" {
		t.Fatalf("not linked: %+v %v", got, err)
	}
	if list, _ := s.LinkDrafts(ctx, u.ID, "projects", "p/ATH-49"); len(list) != 1 || list[0].ThreadID == nil || *list[0].ThreadID != "t1" {
		t.Fatalf("list: %+v", list)
	}

	// claimed, no thread made: kept while a start may still be running, then released
	d2 := &LinkDraft{ID: "d2", UserID: u.ID, Source: "projects", Ref: "p/ATH-50", UploadIDs: []string{}}
	if err := s.CreateLinkDraft(ctx, d2); err != nil {
		t.Fatal(err)
	}
	_, _ = s.ClaimLinkDraft(ctx, u.ID, d2.ID)
	if got, _ := s.GetLinkDraft(ctx, u.ID, d2.ID); got.UsedAt == nil {
		t.Fatal("fresh claim released")
	}
	old := time.Now().Add(-staleDraftClaim - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB.Exec(`UPDATE link_drafts SET used_at = ? WHERE id = ?`, old, d2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLinkDraft(ctx, u.ID, d2.ID); got.UsedAt != nil {
		t.Fatal("stale claim kept")
	}
	if ok, _ := s.ClaimLinkDraft(ctx, u.ID, d2.ID); !ok {
		t.Fatal("released draft cannot be sent")
	}
}
