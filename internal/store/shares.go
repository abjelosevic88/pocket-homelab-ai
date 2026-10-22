package store

import "context"

// ThreadShare is one person a thread is shared with.
type ThreadShare struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"` // view | chat
}

// SharedThread is who a shared thread belongs to and who else may open it.
type SharedThread struct {
	OwnerName string
	Shares    []ThreadShare
}

// AllShares maps every shared thread's id to its owner and shares.
func (s *Store) AllShares(ctx context.Context) (map[string]SharedThread, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT sh.thread_id, sh.user_id, COALESCE(NULLIF(u.name, ''), u.email), sh.role, COALESCE(NULLIF(o.name, ''), o.email)
		FROM thread_shares sh JOIN users u ON u.id = sh.user_id JOIN threads t ON t.id = sh.thread_id JOIN users o ON o.id = t.user_id
		ORDER BY sh.created_at, sh.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SharedThread{}
	for rows.Next() {
		var tid, owner string
		var sh ThreadShare
		if err := rows.Scan(&tid, &sh.UserID, &sh.Name, &sh.Role, &owner); err != nil {
			return nil, err
		}
		st := out[tid]
		st.OwnerName = owner
		st.Shares = append(st.Shares, sh)
		out[tid] = st
	}
	return out, rows.Err()
}

// SetShares replaces who a thread is shared with.
func (s *Store) SetShares(ctx context.Context, threadID string, list []ThreadShare) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[int64]bool{}
	for _, sh := range list {
		keep[sh.UserID] = true
		// an upsert keeps created_at, so the list keeps its order
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_shares (thread_id, user_id, role) VALUES (?, ?, ?)
			ON CONFLICT (thread_id, user_id) DO UPDATE SET role = excluded.role`, threadID, sh.UserID, sh.Role); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM thread_shares WHERE thread_id = ?`, threadID)
	if err != nil {
		return err
	}
	var drop []int64
	for rows.Next() {
		var uid int64
		if rows.Scan(&uid) == nil && !keep[uid] {
			drop = append(drop, uid)
		}
	}
	rows.Close()
	for _, uid := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM thread_shares WHERE thread_id = ? AND user_id = ?`, threadID, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetUploadAny loads an upload whoever sent it (callers check access to its thread).
func (s *Store) GetUploadAny(ctx context.Context, id string) (*Upload, error) {
	var uid int64
	if err := s.DB.QueryRowContext(ctx, `SELECT user_id FROM uploads WHERE id = ?`, id).Scan(&uid); err != nil {
		return nil, notFound(err)
	}
	return s.GetUpload(ctx, uid, id)
}
