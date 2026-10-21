package sdk

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// SQLExecutor accepts either the host DB or its domain transaction.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// QueueEvent saves publication work inside the caller's transaction. A failed
// enqueue must abort that transaction. Delivery is at least once, deduplicated
// by Core using the persisted random operation key.
func QueueEvent(ctx context.Context, tx SQLExecutor, topic, subject string, payload any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"topic": topic, "subject": subject, "payload": payload})
	if err != nil {
		return err
	}
	var key [24]byte
	if _, err = rand.Read(key[:]); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pocket_outbox(id,body,created_at) VALUES(?,?,?)`, hex.EncodeToString(key[:]), b, time.Now().Unix())
	return err
}

type OutboxStatus struct {
	ActionsPending  int   `json:"actions_pending"`
	ActionsRetrying int   `json:"actions_retrying"`
	Pending         int   `json:"pending"`
	Retrying        int   `json:"retrying"`
	Oldest          int64 `json:"oldest,omitempty"`
}

func (c *Client) OutboxStatus(ctx context.Context) (OutboxStatus, error) {
	var s OutboxStatus
	if c.opts.DB == nil {
		return s, nil
	}
	err := c.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(attempts>0),0),COALESCE(MIN(created_at),0) FROM pocket_outbox`).Scan(&s.Pending, &s.Retrying, &s.Oldest)
	if err != nil {
		return s, err
	}
	err = c.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(attempts>0),0) FROM pocket_actions WHERE done_at=0`).Scan(&s.ActionsPending, &s.ActionsRetrying)
	return s, err
}

func (c *Client) runOutbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if c.opts.DB.PingContext(ctx) != nil {
			return
		}
		c.flushOutbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.outboxWake:
		}
	}
}

func (c *Client) flushOutbox(ctx context.Context) {
	for ctx.Err() == nil {
		var id string
		var body []byte
		var attempts int
		err := c.opts.DB.QueryRowContext(ctx, `SELECT id,body,attempts FROM pocket_outbox WHERE next_at<=? ORDER BY created_at,id LIMIT 1`, time.Now().Unix()).Scan(&id, &body, &attempts)
		if errors.Is(err, sql.ErrNoRows) || err != nil {
			return
		}
		_, err = c.doKey(ctx, "POST", "/api/v1/events", c.Token(), json.RawMessage(body), nil, id)
		if err == nil {
			if _, err = c.opts.DB.ExecContext(ctx, `DELETE FROM pocket_outbox WHERE id=?`, id); err != nil {
				return
			}
			continue
		}
		delay := time.Second << min(attempts, 8)
		if _, e := c.opts.DB.ExecContext(ctx, `UPDATE pocket_outbox SET attempts=attempts+1,next_at=?,last_error=? WHERE id=?`, time.Now().Add(delay).Unix(), err.Error(), id); e != nil {
			return
		}
	}
}
