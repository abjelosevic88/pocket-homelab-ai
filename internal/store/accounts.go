package store

import (
	"context"
	"database/sql"
)

// Account is one more CLI login on the server (account 0, the server's own
// login, has no row).
type Account struct {
	ID        int64  `json:"id"`
	Provider  string `json:"provider"` // claude | codex
	Label     string `json:"label"`
	Home      string `json:"home"`    // the CLI's config folder
	UserID    *int64 `json:"user_id"` // nil = everyone who may run agents
	CreatedAt string `json:"created_at"`
}

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var uid sql.NullInt64
	if err := row.Scan(&a.ID, &a.Provider, &a.Label, &a.Home, &uid, &a.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	if uid.Valid {
		a.UserID = &uid.Int64
	}
	return &a, nil
}

const accountCols = `id, provider, label, home, user_id, created_at`

// ListAccounts returns every account, oldest first.
func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAccount loads one account.
func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
}

// CreateAccount inserts an account.
func (s *Store) CreateAccount(ctx context.Context, a *Account) error {
	a.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO accounts (provider, label, home, user_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.Provider, a.Label, a.Home, nullInt(a.UserID), a.CreatedAt)
	if err != nil {
		return conflict(err)
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// UpdateAccount saves the label and who may use it.
func (s *Store) UpdateAccount(ctx context.Context, a *Account) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE accounts SET label = ?, user_id = ? WHERE id = ?`, a.Label, nullInt(a.UserID), a.ID)
	return err
}

// DeleteAccount removes an account; its threads move to the server's own login.
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE threads SET account_id = 0 WHERE account_id = ?`, id); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	return err
}
