package server

import (
	"context"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestDeleteOldArchived(t *testing.T) {
	s, h, root := testServer(t)
	ctx := context.Background()
	alice, au := person(t, s, h, "alice@example.com", "admin", false)

	old := thread(t, s, au.ID, root)    // archived long ago: goes
	fav := thread(t, s, au.ID, root)    // archived long ago but a favorite: stays
	recent := thread(t, s, au.ID, root) // archived yesterday: stays
	active := thread(t, s, au.ID, root) // never archived: stays
	back := thread(t, s, au.ID, root)   // archived long ago, then moved out: stays
	for _, th := range []*store.Thread{old, fav, recent, back} {
		alice.want("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"archived": true}, 200, nil)
	}
	alice.want("PATCH", "/api/v1/threads/"+fav.ID, map[string]any{"pinned": true}, 200, nil)
	long := store.FormatTime(time.Now().Add(-40 * 24 * time.Hour))
	for _, id := range []string{old.ID, fav.ID, back.ID} {
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE threads SET archived_at=? WHERE id=?`, long, id); err != nil {
			t.Fatal(err)
		}
	}
	alice.want("PATCH", "/api/v1/threads/"+back.ID, map[string]any{"archived": false}, 200, nil)
	var at string
	_ = s.store.DB.QueryRowContext(ctx, `SELECT archived_at FROM threads WHERE id=?`, recent.ID).Scan(&at)
	if at == "" {
		t.Fatal("archiving did not record when")
	}
	_ = s.store.DB.QueryRowContext(ctx, `SELECT archived_at FROM threads WHERE id=?`, back.ID).Scan(&at)
	if at != "" {
		t.Fatalf("moved out of the archive but archived_at = %q", at)
	}

	// Off by default: nothing goes.
	var st store.Settings
	alice.want("GET", "/api/v1/settings", nil, 200, &st)
	if st.ArchiveDeleteDays != 0 {
		t.Fatalf("default archive_delete_days = %d, want 0 (never)", st.ArchiveDeleteDays)
	}
	s.deleteOldArchived(ctx, au.ID)
	if _, err := s.store.GetThread(ctx, au.ID, old.ID); err != nil {
		t.Fatal("deleted with the setting off")
	}

	alice.want("PUT", "/api/v1/settings", map[string]any{"archive_delete_days": -1}, 400, nil)
	alice.want("PUT", "/api/v1/settings", map[string]any{"archive_delete_days": 30}, 200, &st)
	if st.ArchiveDeleteDays != 30 {
		t.Fatalf("archive_delete_days = %d, want 30", st.ArchiveDeleteDays)
	}
	s.deleteOldArchived(ctx, au.ID)
	if _, err := s.store.GetThread(ctx, au.ID, old.ID); err == nil {
		t.Fatal("old archived conversation kept")
	}
	for _, th := range []*store.Thread{fav, recent, active, back} {
		if _, err := s.store.GetThread(ctx, au.ID, th.ID); err != nil {
			t.Fatalf("%s deleted: %v", th.ID, err)
		}
	}
}
