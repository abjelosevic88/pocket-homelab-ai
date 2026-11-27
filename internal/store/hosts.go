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
	// The machine link (machine_link.go): the mode asked for (off | status |
	// agents), the last generation allocated, and the fallback to ssh per
	// agent (nil when it did not fall back). Saved apart from the rest
	// (SetHostLinkMode, SetHostLinkFallback, NextLinkGeneration), so an edit
	// of the machine never overwrites them.
	LinkMode       string        `json:"link_mode"`
	LinkGeneration int64         `json:"-"`
	LinkFallback   *LinkFallback `json:"link_fallback,omitempty"`
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
	var fallback string
	if err := row.Scan(&h.ID, &h.Name, &h.Target, &h.Port, &h.ClaudeBin, &h.CodexBin, &info, &h.CreatedAt, &uid,
		&h.LinkMode, &h.LinkGeneration, &fallback); err != nil {
		return nil, notFound(err)
	}
	h.UserID = uid.Int64
	_ = jsonUnmarshal(info, &h.Info)
	if fallback != "" {
		var fb LinkFallback
		if jsonUnmarshal(fallback, &fb) == nil {
			h.LinkFallback = &fb
		}
	}
	return &h, nil
}

const hostCols = `id, name, target, port, claude_bin, codex_bin, info, created_at, user_id, link_mode, link_generation, link_fallback`

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
	if h.LinkMode == "" {
		h.LinkMode = LinkOff
	}
	return nil
}

// UpdateHost saves a host, its last check included.
func (s *Store) UpdateHost(ctx context.Context, h *Host) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hosts SET name = ?, target = ?, port = ?, claude_bin = ?, codex_bin = ?, info = ? WHERE id = ?`,
		h.Name, h.Target, h.Port, h.ClaudeBin, h.CodexBin, jsonMarshal(h.Info), h.ID)
	return err
}

// HostHasThreads: some conversation runs (or ran) on host id. A machine
// with conversations is not new, whatever its last check found.
func (s *Store) HostHasThreads(ctx context.Context, id int64) bool {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM threads WHERE host_id = ?)`, id).Scan(&n); err != nil {
		return true // unknown: treat it as in use
	}
	return n != 0
}

// DeleteHost removes a host. Its threads stay (read-only until it is added again).
func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	if err == nil {
		_, err = s.DB.ExecContext(ctx, `DELETE FROM agent_routes WHERE host_id = ?`, id)
	}
	return err
}
