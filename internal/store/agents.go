package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Agents started by agents: a thread with source "agent" and source_ref =
// the thread that started it.

// ChildThreads lists the agents a thread started, oldest first.
func (s *Store) ChildThreads(ctx context.Context, parentID string) ([]*Thread, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+threadCols+` FROM threads WHERE source = 'agent' AND source_ref = ? ORDER BY created_at, id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OweReport records that child reports its next answer to parent.
func (s *Store) OweReport(ctx context.Context, childID, parentID string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_reports (child_id, parent_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT(child_id) DO UPDATE SET parent_id = excluded.parent_id, created_at = excluded.created_at`, childID, parentID, Now())
	return err
}

// TakeReport removes the report child owes, returning its parent ("" = none owed).
func (s *Store) TakeReport(ctx context.Context, childID string) string {
	var parent string
	err := s.DB.QueryRowContext(ctx, `DELETE FROM agent_reports WHERE child_id = ? RETURNING parent_id`, childID).Scan(&parent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	return parent
}

// OwesReport reports whether child still owes its parent an answer.
func (s *Store) OwesReport(ctx context.Context, childID string) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_reports WHERE child_id = ?`, childID).Scan(&n)
	return n > 0
}

// LastReply is what the agent wrote since the person's (or the parent's)
// last message, and whether that turn is over (a turn footer follows it).
func (s *Store) LastReply(ctx context.Context, threadID string) (text string, ended bool, err error) {
	items, _, err := s.ListItemsTail(ctx, threadID, 0, 1, 400)
	if err != nil {
		return "", false, err
	}
	last := -1
	for i, it := range items {
		if it.Kind == "user" {
			last = i
		}
	}
	parts := []string{}
	for _, it := range items[last+1:] {
		switch it.Kind {
		case "assistant":
			if t := strings.TrimSpace(it.Body); t != "" {
				parts = append(parts, t)
			}
		case "turn":
			ended = true
		}
	}
	for i, p := range parts {
		if i > 0 {
			text += "\n\n"
		}
		text += p
	}
	return text, ended, nil
}

// TurnEnd is how a thread's last turn ended.
type TurnEnd struct {
	OK          bool   `json:"ok"`
	Interrupted bool   `json:"interrupted"`
	Error       string `json:"error"`
}

// LastTurn reads the footer of a thread's last turn.
func (s *Store) LastTurn(ctx context.Context, threadID string) (*TurnEnd, error) {
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT data FROM items WHERE thread_id = ? AND kind = 'turn' ORDER BY id DESC LIMIT 1`, threadID).Scan(&data)
	if err != nil {
		return nil, notFound(err)
	}
	var v struct {
		Turn TurnEnd `json:"turn"`
	}
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return nil, err
	}
	return &v.Turn, nil
}

// TakeReportFrom removes the report child owes parent ("" when it owes it
// to another conversation or nothing).
func (s *Store) TakeReportFrom(ctx context.Context, childID, parentID string) bool {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM agent_reports WHERE child_id = ? AND parent_id = ?`, childID, parentID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// Heartbeat is a prompt that comes back to its conversation on a schedule.
type Heartbeat struct {
	ID        int64  `json:"id"`
	ThreadID  string `json:"thread_id"`
	UserID    int64  `json:"-"`
	Prompt    string `json:"prompt"`
	Spec      string `json:"spec"`
	NextAt    string `json:"next_at"`
	LastAt    string `json:"last_at"`
	Runs      int    `json:"runs"`
	MaxRuns   int    `json:"max_runs"`
	EndsAt    string `json:"ends_at"`
	CreatedAt string `json:"created_at"`
}

const heartbeatCols = `id, thread_id, user_id, prompt, spec, next_at, last_at, runs, max_runs, ends_at, created_at`

func (s *Store) listHeartbeats(ctx context.Context, where string, args ...any) ([]*Heartbeat, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+heartbeatCols+` FROM heartbeats `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Heartbeat{}
	for rows.Next() {
		var h Heartbeat
		if err := rows.Scan(&h.ID, &h.ThreadID, &h.UserID, &h.Prompt, &h.Spec, &h.NextAt, &h.LastAt, &h.Runs, &h.MaxRuns, &h.EndsAt, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// AddHeartbeat stores a new heartbeat (ID and CreatedAt are set).
func (s *Store) AddHeartbeat(ctx context.Context, h *Heartbeat) error {
	h.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO heartbeats (thread_id, user_id, prompt, spec, next_at, max_runs, ends_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ThreadID, h.UserID, h.Prompt, h.Spec, h.NextAt, h.MaxRuns, h.EndsAt, h.CreatedAt)
	if err != nil {
		return err
	}
	h.ID, err = res.LastInsertId()
	return err
}

// ThreadHeartbeats lists a conversation's heartbeats, next first.
func (s *Store) ThreadHeartbeats(ctx context.Context, threadID string) ([]*Heartbeat, error) {
	return s.listHeartbeats(ctx, `WHERE thread_id = ? ORDER BY next_at, id`, threadID)
}

// DueHeartbeats lists the heartbeats whose time has come.
func (s *Store) DueHeartbeats(ctx context.Context, now string) ([]*Heartbeat, error) {
	return s.listHeartbeats(ctx, `WHERE next_at <= ? ORDER BY next_at`, now)
}

// BeatHeartbeat moves a heartbeat on (ran: one more run done).
func (s *Store) BeatHeartbeat(ctx context.Context, id int64, next string, ran bool) error {
	if ran {
		_, err := s.DB.ExecContext(ctx, `UPDATE heartbeats SET next_at = ?, last_at = ?, runs = runs + 1 WHERE id = ?`, next, FormatTime(time.Now()), id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE heartbeats SET next_at = ? WHERE id = ?`, next, id)
	return err
}

// DeleteHeartbeat removes one of a conversation's heartbeats.
func (s *Store) DeleteHeartbeat(ctx context.Context, threadID string, id int64) bool {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM heartbeats WHERE id = ? AND thread_id = ?`, id, threadID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}
