package sdk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Init prepares shared platform tables before requests are served.
// The host owns the database; New calls Init and Start reports its errors.
func (c *Client) Init(ctx context.Context) error {
	c.initOnce.Do(func() {
		if c.opts.DB == nil {
			return
		}
		tx, err := c.opts.DB.BeginTx(ctx, nil)
		if err != nil {
			c.startErr = err
			return
		}
		defer tx.Rollback()
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS pocket_platform (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pocket_identities (issuer TEXT NOT NULL, subject TEXT NOT NULL, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, PRIMARY KEY(issuer,subject), UNIQUE(issuer,user_id))`,
			`CREATE TABLE IF NOT EXISTS pocket_sessions (session_hash TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE, issuer TEXT NOT NULL, subject TEXT NOT NULL, revision INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pocket_actions(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,kind TEXT NOT NULL,payload BLOB NOT NULL,created_at INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',response BLOB,done_at INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE IF NOT EXISTS pocket_operations (operation_key TEXT PRIMARY KEY, request_hash TEXT NOT NULL, response BLOB NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS pocket_outbox (id TEXT PRIMARY KEY, body BLOB NOT NULL, created_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '')`,
		} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				c.startErr = err
				return
			}
		}
		if c.Configured() {
			var v string
			err = tx.QueryRowContext(ctx, `SELECT value FROM pocket_platform WHERE key='session_origin_v1'`).Scan(&v)
			if errors.Is(err, sql.ErrNoRows) {
				// Old sessions did not retain their authentication origin. A one-time
				// sign-in is required so an old Core login cannot bypass revocation.
				if _, err = tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
					c.startErr = err
					return
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO pocket_platform VALUES ('session_origin_v1','1')`); err != nil {
					c.startErr = err
					return
				}
			} else if err != nil {
				c.startErr = err
				return
			}
		}
		c.startErr = tx.Commit()
	})
	return c.startErr
}

// IdentityKey uses the provider's stable identity. Numeric IDs support older
// Core servers during a coordinated rollout; email is never used as a key.
func (c *Client) IdentityKey(in *Introspection) (string, string) {
	issuer := in.Issuer
	if issuer == "" {
		issuer = c.OIDC().Issuer
	}
	if issuer == "" {
		issuer = c.base
	}
	subject := ""
	if in.User != nil {
		subject = in.User.Subject
		if subject == "" && in.User.ID > 0 {
			subject = fmt.Sprint(in.User.ID)
		}
	}
	return issuer, subject
}

// ResolveIdentity binds a verified identity once. lookup may locate/create the
// existing email account for migration. An account already bound to another
// subject of this issuer cannot be claimed by a recycled email address.
func (c *Client) ResolveIdentity(ctx context.Context, issuer, subject string, lookup func() (int64, error)) (int64, error) {
	if c.opts.DB == nil || issuer == "" || subject == "" {
		return 0, errors.New("stable identity unavailable")
	}
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	var id int64
	err := c.opts.DB.QueryRowContext(ctx, `SELECT user_id FROM pocket_identities WHERE issuer=? AND subject=?`, issuer, subject).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	id, err = lookup()
	if err != nil {
		return 0, err
	}
	_, err = c.opts.DB.ExecContext(ctx, `INSERT INTO pocket_identities(issuer,subject,user_id) VALUES(?,?,?)`, issuer, subject, id)
	if err != nil {
		return 0, fmt.Errorf("identity binding requires account-link review: %w", err)
	}
	return id, nil
}

func sessionHash(id string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(id))) }

// TrackSession retains Core's identity and revocation revision, never its
// access token. External OIDC providers keep their own session policy.
func (c *Client) TrackSession(ctx context.Context, id, issuer, subject string, revision int64) error {
	if !c.Configured() {
		return nil
	}
	coreIssuer := c.OIDC().Issuer
	if issuer != coreIssuer && strings.TrimRight(issuer, "/") != c.base {
		return nil
	}
	if revision <= 0 || subject == "" {
		return errors.New("Core upgrade required for revocable sessions")
	}
	_, err := c.opts.DB.ExecContext(ctx, `INSERT INTO pocket_sessions(session_hash,issuer,subject,revision) VALUES(?,?,?,?)`, sessionHash(id), issuer, subject, revision)
	return err
}

// ValidateSession fails closed for Core sessions on revocation, loss of
// audience, or Core outage. Password and external-provider sessions stay local.
func (c *Client) ValidateSession(ctx context.Context, id string) error {
	if c.opts.DB == nil {
		return nil
	}
	var issuer, subject string
	var revision int64
	err := c.opts.DB.QueryRowContext(ctx, `SELECT issuer,subject,revision FROM pocket_sessions WHERE session_hash=?`, sessionHash(id)).Scan(&issuer, &subject, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var out struct {
		Active bool   `json:"active"`
		Issuer string `json:"iss"`
	}
	_, err = c.do(ctx, "POST", "/api/v1/auth/subject", c.Token(), map[string]any{"sub": subject, "auth_revision": revision}, &out)
	if err != nil {
		return err
	}
	if !out.Active || out.Issuer != issuer {
		_, _ = c.opts.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, sessionHash(id))
		return errors.New("Core session revoked")
	}
	return nil
}

// Start runs the durable publisher until the application shutdown context ends.
func (c *Client) Start(ctx context.Context) error {
	if err := c.Init(ctx); err != nil {
		return err
	}
	c.startOnce.Do(func() {
		if c.opts.DB != nil && c.Configured() {
			go c.runOutbox(ctx)
		}
	})
	return nil
}
