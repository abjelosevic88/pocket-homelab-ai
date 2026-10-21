package sdk

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Action is a durable request to another app. ID is also the receiver's
// idempotency key. Payload contains no credential; resolve current access for
// UserID on every attempt. Applications serialize dispatch within a process.
type Action struct {
	ID       string
	UserID   int64
	Kind     string
	Payload  json.RawMessage
	Attempts int
}

// QueueAction must share the domain transaction that requires this action.
func QueueAction(ctx context.Context, tx SQLExecutor, userID int64, kind string, payload any) (string, error) {
	if userID <= 0 || kind == "" {
		return "", fmt.Errorf("action requires user and kind")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var random [24]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	_, err = tx.ExecContext(ctx, `INSERT INTO pocket_actions(id,user_id,kind,payload,created_at) VALUES(?,?,?,?,?)`, id, userID, kind, b, time.Now().Unix())
	return id, err
}

// PendingActions preserves insertion order per person, including when an
// earlier request is waiting to retry. Other people's actions remain runnable.
func PendingActions(ctx context.Context, db *sql.DB, userID int64) ([]Action, error) {
	rows, err := db.QueryContext(ctx, `SELECT a.id,a.user_id,a.kind,a.payload,a.attempts FROM pocket_actions a WHERE a.done_at=0 AND a.next_at<=? AND (?=0 OR a.user_id=?) AND NOT EXISTS(SELECT 1 FROM pocket_actions b WHERE b.user_id=a.user_id AND b.done_at=0 AND b.rowid<a.rowid) ORDER BY a.rowid LIMIT 32`, time.Now().Unix(), userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		var a Action
		if err := rows.Scan(&a.ID, &a.UserID, &a.Kind, &a.Payload, &a.Attempts); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FinishAction records acknowledgement with any local projection of the result.
func FinishAction(ctx context.Context, db *sql.DB, a Action, hooks ...CommitHook) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE pocket_actions SET done_at=?,last_error='' WHERE id=? AND done_at=0`, time.Now().Unix(), a.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return Commit(tx, hooks...)
}
func RetryAction(ctx context.Context, db *sql.DB, a Action, cause error) error {
	delay := time.Second << min(a.Attempts, 8)
	_, err := db.ExecContext(ctx, `UPDATE pocket_actions SET attempts=attempts+1,next_at=?,last_error=? WHERE id=? AND done_at=0`, time.Now().Add(delay).Unix(), cause.Error(), a.ID)
	return err
}
