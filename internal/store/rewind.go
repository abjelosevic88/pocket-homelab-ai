package store

import "context"

// RewindThread atomically switches the agent context and removes the selected
// prompt and everything after it. Concurrent changes make the operation fail
// without deleting history. Identity, settings, links and sharing stay intact.
func (s *Store) RewindThread(ctx context.Context, t *Thread, key, previousSession string, lastID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetID, currentLast int64
	var session, status string
	if err := tx.QueryRowContext(ctx, `SELECT provider_session_id, status FROM threads WHERE id=? AND user_id=?`, t.ID, t.UserID).Scan(&session, &status); err != nil {
		return notFound(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM items WHERE thread_id=? AND key NOT LIKE 'acct-%'`, t.ID).Scan(&currentLast); err != nil {
		return err
	}
	if session != previousSession || currentLast != lastID || status == "running" || status == "waiting" {
		return ErrConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM items WHERE thread_id=? AND key=? AND kind='user'`, t.ID, key).Scan(&targetID); err != nil {
		return notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE thread_id=? AND id>=?`, t.ID, targetID); err != nil {
		return err
	}
	t.UpdatedAt = Now()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET provider_session_id=?, log_offset=?, status='idle', error='', unread=0, completed_unread=0, preview=?, last_prompt=?, prompt_at=?, usage=?, activity_at=?, updated_at=? WHERE id=?`, t.ProviderSessionID, t.LogOffset, t.Preview, t.LastPrompt, t.PromptAt, jsonMarshal(t.Usage), t.ActivityAt, t.UpdatedAt, t.ID); err != nil {
		return err
	}
	if t.ProviderSessionID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_owners(provider,session_id,user_id) VALUES(?,?,?)`, t.Provider, t.ProviderSessionID, t.UserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
