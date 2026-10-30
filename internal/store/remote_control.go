package store

import "context"

// ThreadRemote is a thread with Claude Remote Control turned on.
type ThreadRemote struct {
	ThreadID        string
	BridgeSessionID string // the claude.ai session it was last attached to
	SessionURL      string
}

// ListThreadRemotes lists every thread with Remote Control on.
func (s *Store) ListThreadRemotes(ctx context.Context) ([]ThreadRemote, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT thread_id, bridge_session_id, session_url FROM thread_remote`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadRemote
	for rows.Next() {
		var r ThreadRemote
		if err := rows.Scan(&r.ThreadID, &r.BridgeSessionID, &r.SessionURL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetThreadRemote turns Remote Control on for a thread (or updates its session).
func (s *Store) SetThreadRemote(ctx context.Context, r ThreadRemote) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO thread_remote (thread_id, bridge_session_id, session_url, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET bridge_session_id = excluded.bridge_session_id, session_url = excluded.session_url, updated_at = excluded.updated_at`,
		r.ThreadID, r.BridgeSessionID, r.SessionURL, Now())
	return err
}

// DeleteThreadRemote turns Remote Control off for a thread.
func (s *Store) DeleteThreadRemote(ctx context.Context, threadID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM thread_remote WHERE thread_id = ?`, threadID)
	return err
}
