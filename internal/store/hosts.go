package store

import (
	"context"
	"database/sql"
)

// Host is another machine agents run on over ssh (host 0, this server, has
// no row).
type Host struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Target    string   `json:"target"` // ssh destination: a Host from ~/.ssh/config or user@host
	Port      int      `json:"port"`
	ClaudeBin string   `json:"claude_bin"` // empty = "claude" on the remote PATH
	CodexBin  string   `json:"codex_bin"`
	Info      HostInfo `json:"info"`
	CreatedAt string   `json:"created_at"`
	// UserID is who added it (0 = the box owner): it is reached with their
	// ssh key, and only they see and use it.
	UserID int64 `json:"user_id,omitempty"`
	// Home is set by the server when the machine is reached from a
	// person's own folder (their key, in their jail).
	Home string `json:"-"`
}

// HostInfo is what the last check found on the machine.
type HostInfo struct {
	OK        bool              `json:"ok"`
	Error     string            `json:"error,omitempty"`
	Home      string            `json:"home,omitempty"`
	OS        string            `json:"os,omitempty"`
	Hostname  string            `json:"hostname,omitempty"`
	Versions  map[string]string `json:"versions,omitempty"` // claude | codex → version
	Missing   map[string]string `json:"missing,omitempty"`  // claude | codex → why it can't run
	CheckedAt string            `json:"checked_at,omitempty"`
}

func scanHost(row interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var info string
	var uid sql.NullInt64
	if err := row.Scan(&h.ID, &h.Name, &h.Target, &h.Port, &h.ClaudeBin, &h.CodexBin, &info, &h.CreatedAt, &uid); err != nil {
		return nil, notFound(err)
	}
	h.UserID = uid.Int64
	_ = jsonUnmarshal(info, &h.Info)
	return &h, nil
}

const hostCols = `id, name, target, port, claude_bin, codex_bin, info, created_at, user_id`

// ListHosts returns every host, oldest first.
func (s *Store) ListHosts(ctx context.Context) ([]*Host, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHost loads one host.
func (s *Store) GetHost(ctx context.Context, id int64) (*Host, error) {
	return scanHost(s.DB.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE id = ?`, id))
}

// CreateHost inserts a host.
func (s *Store) CreateHost(ctx context.Context, h *Host) error {
	h.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO hosts (name, target, port, claude_bin, codex_bin, info, created_at, user_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Name, h.Target, h.Port, h.ClaudeBin, h.CodexBin, jsonMarshal(h.Info), h.CreatedAt, sql.NullInt64{Int64: h.UserID, Valid: h.UserID > 0})
	if err != nil {
		return conflict(err)
	}
	h.ID, _ = res.LastInsertId()
	return nil
}

// UpdateHost saves a host, its last check included.
func (s *Store) UpdateHost(ctx context.Context, h *Host) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hosts SET name = ?, target = ?, port = ?, claude_bin = ?, codex_bin = ?, info = ? WHERE id = ?`,
		h.Name, h.Target, h.Port, h.ClaudeBin, h.CodexBin, jsonMarshal(h.Info), h.ID)
	return err
}

// DeleteHost removes a host. Its threads stay (read-only until it is added again).
func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	return err
}
