package server

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

var archiveCleanupMu sync.Mutex // one pass at a time (hourly + right after the setting changes)

// deleteOldArchived deletes a person's archived conversations that went to
// the archive more than their "Delete archived conversations after" days ago
// (Settings → General). Off (0) by default; favorites are kept.
func (s *Server) deleteOldArchived(ctx context.Context, uid int64) {
	days, _ := strconv.Atoi(s.store.UserSetting(ctx, uid, "archive_delete_days"))
	if days <= 0 {
		return
	}
	archiveCleanupMu.Lock()
	defer archiveCleanupMu.Unlock()
	cutoff := store.FormatTime(time.Now().Add(-time.Duration(days) * 24 * time.Hour))
	ids, err := s.store.ArchivedBefore(ctx, uid, cutoff)
	if err != nil {
		s.log.Warn("archived conversations cleanup", "user", uid, "error", err)
		return
	}
	n := 0
	for _, id := range ids {
		t, err := s.store.GetThread(ctx, uid, id)
		if err != nil || !t.Archived || t.Pinned {
			continue
		}
		if err := s.deleteThread(ctx, t); err != nil {
			s.log.Warn("delete archived conversation", "thread", id, "error", err)
			continue
		}
		n++
	}
	if n > 0 {
		s.log.Info("deleted archived conversations", "user", uid, "count", n, "older_than_days", days)
		s.touch()
	}
}
