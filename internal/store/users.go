package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u.Username == "" {
		u.Username = s.freeUsername(ctx, u.Email)
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users (email, name, username, password_hash, role, can_run) VALUES (?, ?, ?, ?, ?, ?)`, u.Email, u.Name, u.Username, u.PasswordHash, u.Role, boolInt(u.CanRun))
	if err != nil {
		return conflict(err)
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// UpdateUser updates name/email/password/role/can_run.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET email = ?, name = ?, username = ?, password_hash = ?, role = ?, can_run = ?, updated_at = ? WHERE id = ?`, u.Email, u.Name, u.Username, u.PasswordHash, u.Role, boolInt(u.CanRun), Now(), u.ID)
	return conflict(err)
}

const userCols = `id, email, name, username, password_hash, role, can_run, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Username, &u.PasswordHash, &u.Role, &u.CanRun, &u.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// GetUserByEmail finds a user by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// ListUsers returns all users.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// DeleteUser removes a user with everything they had.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountAdmins returns how many admin accounts exist.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// RandomToken returns n random bytes hex encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashToken returns the sha256 hex digest of a token.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateSession creates a session for a user and returns its id.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	id := RandomToken(32)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`, HashToken(id), userID, time.Now().Add(ttl).UTC().Format(time.RFC3339))
	return id, err
}

// GetSessionUser resolves a session id to a user.
func (s *Store) GetSessionUser(ctx context.Context, sessionID string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.username, u.password_hash, u.role, u.can_run, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ? AND s.expires_at > ?`, HashToken(sessionID), time.Now().UTC().Format(time.RFC3339)))
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, HashToken(sessionID))
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}

// CreateAPIToken creates a token and returns the plaintext (shown once).
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string) (string, *APIToken, error) {
	plain := "pai_" + RandomToken(24)
	prefix := plain[:12]
	res, err := s.DB.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix) VALUES (?, ?, ?, ?)`, userID, name, HashToken(plain), prefix)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	return plain, &APIToken{ID: id, UserID: userID, Name: name, Prefix: prefix, CreatedAt: Now()}, nil
}

// GetUserByAPIToken resolves a bearer token.
func (s *Store) GetUserByAPIToken(ctx context.Context, token string) (*User, error) {
	h := HashToken(token)
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.username, u.password_hash, u.role, u.can_run, u.created_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, h))
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, Now(), h)
	return u, nil
}

// ListAPITokens lists tokens for a user.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, prefix, last_used_at, created_at FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken removes a token.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// GetUserByUsername finds a user by the CalDAV principal name (or email).
func (s *Store) GetUserByUsername(ctx context.Context, name string) (*User, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, name)); err == nil {
		return u, nil
	}
	return s.GetUserByEmail(ctx, name)
}

// freeUsername derives a principal name from an email that no one has yet.
func (s *Store) freeUsername(ctx context.Context, email string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(email, "@")[0]))
	base = Slug(base)
	if base == "" {
		base = "user"
	}
	name := base
	for i := 2; i < 1000; i++ {
		var n int
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE username = ?`, name).Scan(&n)
		if n == 0 {
			return name
		}
		name = base + strconv.Itoa(i)
	}
	return base + RandomToken(3)
}

// Slug keeps letters, digits, dot, dash and underscore.
func Slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".-_")
}
