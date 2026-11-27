package store

import (
	"context"
	"database/sql"
	"time"
)

// AIShare is the allowance of a lent login (see migration 0041).
type AIShare struct {
	AccountID   int64   `json:"account_id"`
	Token       string  `json:"-"`
	MinutesDay  int     `json:"minutes_day"`
	PercentWeek float64 `json:"percent_week"`
	Paused      bool    `json:"paused"`
	CreatedAt   string  `json:"created_at"`
}

// nullZero stores a pointer as NULL only when it is nil (0 is a value).
func nullZero(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

const aiShareCols = `account_id, token, minutes_day, percent_week, paused, created_at`

func scanAIShare(row interface{ Scan(...any) error }) (*AIShare, error) {
	var a AIShare
	if err := row.Scan(&a.AccountID, &a.Token, &a.MinutesDay, &a.PercentWeek, &a.Paused, &a.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// GetAIShare loads the allowance of a lent login.
func (s *Store) GetAIShare(ctx context.Context, accountID int64) (*AIShare, error) {
	return scanAIShare(s.DB.QueryRowContext(ctx, `SELECT `+aiShareCols+` FROM ai_shares WHERE account_id = ?`, accountID))
}

// GetAIShareByToken finds the allowance a proxy token stands for.
func (s *Store) GetAIShareByToken(ctx context.Context, token string) (*AIShare, error) {
	return scanAIShare(s.DB.QueryRowContext(ctx, `SELECT `+aiShareCols+` FROM ai_shares WHERE token = ?`, token))
}

// ListAIShares lists every allowance.
func (s *Store) ListAIShares(ctx context.Context) ([]*AIShare, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+aiShareCols+` FROM ai_shares ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AIShare
	for rows.Next() {
		a, err := scanAIShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAIShare inserts or updates an allowance (the token is kept once set).
func (s *Store) SaveAIShare(ctx context.Context, a *AIShare) error {
	if a.CreatedAt == "" {
		a.CreatedAt = Now()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO ai_shares (account_id, token, minutes_day, percent_week, paused, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET minutes_day = excluded.minutes_day, percent_week = excluded.percent_week, paused = excluded.paused`,
		a.AccountID, a.Token, a.MinutesDay, a.PercentWeek, boolInt(a.Paused), a.CreatedAt)
	return err
}

// ShareUsage is what went through the proxy for one lent login.
type ShareUsage struct {
	ActiveMS     int64
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	CostUSD      float64
}

// usageHour is the bucket of t: its UTC hour.
func usageHour(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// AddShareUsage adds to the hour bucket of at.
func (s *Store) AddShareUsage(ctx context.Context, accountID int64, at time.Time, u ShareUsage) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO ai_share_usage (account_id, hour, active_ms, requests, input_tokens, output_tokens, cache_read, cache_write, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, hour) DO UPDATE SET active_ms = active_ms + excluded.active_ms, requests = requests + excluded.requests,
		input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
		cache_read = cache_read + excluded.cache_read, cache_write = cache_write + excluded.cache_write, cost_usd = cost_usd + excluded.cost_usd`,
		accountID, usageHour(at), u.ActiveMS, u.Requests, u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, u.CostUSD)
	return err
}

// ShareUsageSince sums the buckets from the hour of since on.
func (s *Store) ShareUsageSince(ctx context.Context, accountID int64, since time.Time) (ShareUsage, error) {
	var u ShareUsage
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(active_ms),0), COALESCE(SUM(requests),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0), COALESCE(SUM(cost_usd),0) FROM ai_share_usage WHERE account_id = ? AND hour >= ?`,
		accountID, usageHour(since)).Scan(&u.ActiveMS, &u.Requests, &u.InputTokens, &u.OutputTokens, &u.CacheRead, &u.CacheWrite, &u.CostUSD)
	return u, err
}

// LoginCost is what the turns on one login cost (as the CLI reported it).
type LoginCost struct {
	Provider  string
	AccountID int64
	HostID    int64
	HostHome  string
	CostUSD   float64
}

// TurnCostsSince sums finished turns' cost per login since t.
func (s *Store) TurnCostsSince(ctx context.Context, provider string, since time.Time) ([]LoginCost, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.provider, t.account_id, t.host_id, t.host_home, COALESCE(SUM(u.cost_usd),0)
		FROM turns u JOIN threads t ON t.id = u.thread_id
		WHERE u.created_at >= ? AND t.provider = ? GROUP BY t.provider, t.account_id, t.host_id, t.host_home`,
		since.UTC().Format("2006-01-02T15:04:05.000Z"), provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginCost
	for rows.Next() {
		var c LoginCost
		var home sql.NullString
		if err := rows.Scan(&c.Provider, &c.AccountID, &c.HostID, &home, &c.CostUSD); err != nil {
			return nil, err
		}
		c.HostHome = home.String
		out = append(out, c)
	}
	return out, rows.Err()
}
