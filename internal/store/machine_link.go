package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The machine link's state on the server (docs/machine-link-plan.md):
// the controller lease, the installation id, each machine's link mode,
// generation and fallback, and each agent's route.

// Link modes a machine can ask for.
const (
	LinkOff    = "off"    // ssh per agent and per command, as before
	LinkStatus = "status" // phase 1: read-only status scripts over the link
	LinkAgents = "agents" // phase 2 on: agents and every short command
)

// ValidLinkMode reports whether m is a mode a machine can be set to.
func ValidLinkMode(m string) bool { return m == LinkOff || m == LinkStatus || m == LinkAgents }

// Agent routes (agent_routes.route).
const (
	RouteSSH    = "ssh"     // its own ssh connection with an -R forward
	RouteToLink = "to-link" // moving onto the link: written before the ssh one is closed
	RouteLink   = "link"    // on the link, its tools port bound by the relay
	RouteToSSH  = "to-ssh"  // moving back: written before the port is unbound
)

// LinkFallback says when and why a machine fell back to ssh per agent.
type LinkFallback struct {
	At  string `json:"at"`
	Why string `json:"why"`
}

// ErrNotLeaseHolder: a generation was asked for by a process that does not
// hold the controller lease (any more).
var ErrNotLeaseHolder = errors.New("this Pocket AI process does not hold the controller lease")

// InstallationID is this installation's id, made the first time it is
// asked for and the same ever after (across restarts and deploys).
func (s *Store) InstallationID(ctx context.Context) (string, error) {
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('installation_id', ?) ON CONFLICT(key) DO NOTHING`, uuid.NewString()); err != nil {
		return "", err
	}
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'installation_id'`).Scan(&id)
	if err == nil && id == "" {
		// an empty value written by hand: make it once now
		id = uuid.NewString()
		_, err = s.DB.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = 'installation_id' AND value = ''`, id)
		if err == nil {
			err = s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'installation_id'`).Scan(&id)
		}
	}
	return id, err
}

// TakeLease takes the controller lease for holder, or renews it when holder
// has it already, until now+ttl. It fails (false) while another holder's
// lease has not run out.
func (s *Store) TakeLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO controller_lease (id, holder, expires_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE controller_lease.holder = excluded.holder OR controller_lease.expires_at <= ?`,
		holder, now.Add(ttl).UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReleaseLease gives the lease up, if holder has it: the next process takes
// it at once instead of after it runs out.
func (s *Store) ReleaseLease(ctx context.Context, holder string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM controller_lease WHERE id = 1 AND holder = ?`, holder)
	return err
}

// LeaseHolder is who holds the lease and until when ("" when nobody).
func (s *Store) LeaseHolder(ctx context.Context) (string, time.Time, error) {
	var holder string
	var until int64
	err := s.DB.QueryRowContext(ctx, `SELECT holder, expires_at FROM controller_lease WHERE id = 1`).Scan(&holder, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return holder, time.UnixMilli(until), err
}

// NextLinkGeneration allocates the next generation for a machine's link. It
// does so only for the lease holder, checked in the same transaction: a
// stale process cannot get a fresh, higher generation.
func (s *Store) NextLinkGeneration(ctx context.Context, hostID int64, holder string, now time.Time) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var cur string
	var until int64
	err = tx.QueryRowContext(ctx, `SELECT holder, expires_at FROM controller_lease WHERE id = 1`).Scan(&cur, &until)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (cur != holder || until <= now.UnixMilli())) {
		return 0, ErrNotLeaseHolder
	}
	if err != nil {
		return 0, err
	}
	// Generations rise across the whole installation, not per machine: a
	// machine entry made later (another entry for an account whose fence a
	// deleted entry released, or one that got a deleted entry's id) starts
	// above everything handed out before it.
	var gen, seq int64
	if err := tx.QueryRowContext(ctx, `SELECT link_generation FROM hosts WHERE id = ?`, hostID).Scan(&gen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	var v sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'link_generation_seq'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if v.Valid {
		_, _ = fmt.Sscan(v.String, &seq)
	} else if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(link_generation), 0) FROM hosts`).Scan(&seq); err != nil {
		return 0, err
	}
	gen = max(gen, seq) + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('link_generation_seq', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(gen)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET link_generation = ? WHERE id = ?`, gen, hostID); err != nil {
		return 0, err
	}
	return gen, tx.Commit()
}

// SetHostLinkMode stores the link mode a machine asked for.
func (s *Store) SetHostLinkMode(ctx context.Context, hostID int64, mode string) error {
	if !ValidLinkMode(mode) {
		return errors.New("unknown link mode " + mode)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE hosts SET link_mode = ? WHERE id = ?`, mode, hostID)
	return err
}

// SetHostLinkFallback records that a machine fell back to ssh per agent
// (nil: it is back on the link, or was switched back).
func (s *Store) SetHostLinkFallback(ctx context.Context, hostID int64, fb *LinkFallback) error {
	v := ""
	if fb != nil {
		v = jsonMarshal(fb)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE hosts SET link_fallback = ? WHERE id = ?`, v, hostID)
	return err
}

// AgentRoute is how one agent's stream and tools port are carried.
type AgentRoute struct {
	ThreadID  string `json:"thread_id"`
	HostID    int64  `json:"host_id"`
	Port      int    `json:"port"`
	Route     string `json:"route"`
	UpdatedAt string `json:"updated_at"`
}

// ValidRoute reports whether r is a route.
func ValidRoute(r string) bool {
	return r == RouteSSH || r == RouteToLink || r == RouteLink || r == RouteToSSH
}

// SetAgentRoute records an agent's route (written before the old route is
// closed when it is a move state).
func (s *Store) SetAgentRoute(ctx context.Context, r *AgentRoute) error {
	if !ValidRoute(r.Route) {
		return errors.New("unknown route " + r.Route)
	}
	r.UpdatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_routes (thread_id, host_id, port, route, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET host_id = excluded.host_id, port = excluded.port, route = excluded.route, updated_at = excluded.updated_at`,
		r.ThreadID, r.HostID, r.Port, r.Route, r.UpdatedAt)
	return err
}

// GetAgentRoute loads one agent's route (ErrNotFound: none recorded, i.e. ssh).
func (s *Store) GetAgentRoute(ctx context.Context, threadID string) (*AgentRoute, error) {
	var r AgentRoute
	err := s.DB.QueryRowContext(ctx, `SELECT thread_id, host_id, port, route, updated_at FROM agent_routes WHERE thread_id = ?`, threadID).
		Scan(&r.ThreadID, &r.HostID, &r.Port, &r.Route, &r.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// ListAgentRoutes lists the routes recorded for a machine's agents.
func (s *Store) ListAgentRoutes(ctx context.Context, hostID int64) ([]*AgentRoute, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT thread_id, host_id, port, route, updated_at FROM agent_routes WHERE host_id = ? ORDER BY thread_id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentRoute{}
	for rows.Next() {
		var r AgentRoute
		if err := rows.Scan(&r.ThreadID, &r.HostID, &r.Port, &r.Route, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// DeleteAgentRoute forgets an agent's route (it ended).
func (s *Store) DeleteAgentRoute(ctx context.Context, threadID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM agent_routes WHERE thread_id = ?`, threadID)
	return err
}
