package store

import (
	"context"
	"database/sql"
)

// ---------- conversations other Pocket apps prepare ----------

// LinkDraft is a conversation another app prepared for a person to send.
type LinkDraft struct {
	ID        string  `json:"id"`
	UserID    int64   `json:"-"`
	Provider  string  `json:"provider"`
	AccountID *int64  `json:"account_id"`
	Model     string  `json:"model"`
	Effort    string  `json:"effort"`
	Mode      string  `json:"mode"`
	Cwd       string  `json:"cwd"`
	Title     string  `json:"title"`
	Text      string  `json:"text"`
	System    string  `json:"system"`
	Source    string  `json:"source"`
	Ref       string  `json:"ref"`
	RefURL    string  `json:"ref_url"`
	RefLabel  string  `json:"ref_label"`
	ThreadID  *string `json:"thread_id"`
	CreatedAt string  `json:"created_at"`
	UsedAt    *string `json:"used_at"`
	// UploadIDs: files the app attached; they go with the first message.
	UploadIDs []string `json:"upload_ids"`
	// Worktree: the app asks for a git worktree of its own (nil = the folder itself).
	Worktree *DraftWorktree `json:"worktree"`
	// HostID: the machine it runs on (Settings → Servers), 0 = this server.
	HostID int64 `json:"host_id"`
}

// DraftWorktree is a new worktree an app asks for: Name is the branch (a
// ticket's branch), Base the branch to start from ("" = the repo's).
type DraftWorktree struct {
	Name string `json:"name"`
	Base string `json:"base,omitempty"`
}

const draftCols = `id, user_id, provider, account_id, model, effort, mode, cwd, title, text, system, source, ref, ref_url, ref_label, thread_id, created_at, used_at, upload_ids, worktree, host_id`

func scanDraft(row interface{ Scan(...any) error }) (*LinkDraft, error) {
	var d LinkDraft
	var acct sql.NullInt64
	var tid, used sql.NullString
	var uploads, wt string
	if err := row.Scan(&d.ID, &d.UserID, &d.Provider, &acct, &d.Model, &d.Effort, &d.Mode, &d.Cwd, &d.Title, &d.Text, &d.System,
		&d.Source, &d.Ref, &d.RefURL, &d.RefLabel, &tid, &d.CreatedAt, &used, &uploads, &wt, &d.HostID); err != nil {
		return nil, notFound(err)
	}
	d.UploadIDs = []string{}
	_ = jsonUnmarshal(uploads, &d.UploadIDs)
	if wt != "" {
		d.Worktree = &DraftWorktree{}
		if jsonUnmarshal(wt, d.Worktree) != nil || d.Worktree.Name == "" {
			d.Worktree = nil
		}
	}
	if acct.Valid {
		d.AccountID = &acct.Int64
	}
	if tid.Valid {
		d.ThreadID = &tid.String
	}
	if used.Valid {
		d.UsedAt = &used.String
	}
	return &d, nil
}

// CreateLinkDraft stores a prepared conversation.
func (s *Store) CreateLinkDraft(ctx context.Context, d *LinkDraft) error {
	d.CreatedAt = Now()
	wt := ""
	if d.Worktree != nil {
		wt = jsonMarshal(d.Worktree)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO link_drafts (id, user_id, provider, account_id, model, effort, mode, cwd, title, text, system, source, ref, ref_url, ref_label, created_at, upload_ids, worktree, host_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.ID, d.UserID, d.Provider, d.AccountID, d.Model, d.Effort, d.Mode, d.Cwd, d.Title, d.Text, d.System,
		d.Source, d.Ref, d.RefURL, d.RefLabel, d.CreatedAt, jsonMarshal(d.UploadIDs), wt, d.HostID)
	return err
}

// GetLinkDraft loads one of a person's drafts (ErrNotFound for anyone else's).
func (s *Store) GetLinkDraft(ctx context.Context, userID int64, id string) (*LinkDraft, error) {
	return scanDraft(s.DB.QueryRowContext(ctx, `SELECT `+draftCols+` FROM link_drafts WHERE id = ? AND user_id = ?`, id, userID))
}

// LinkDrafts lists a person's drafts for one item of another app, newest first.
func (s *Store) LinkDrafts(ctx context.Context, userID int64, source, ref string) ([]*LinkDraft, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+draftCols+` FROM link_drafts WHERE user_id = ? AND source = ? AND ref = ? ORDER BY created_at DESC LIMIT 50`, userID, source, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LinkDraft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StartedLinkDrafts are the person's latest sent drafts from one app (any ref).
func (s *Store) StartedLinkDrafts(ctx context.Context, userID int64, source string) ([]*LinkDraft, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+draftCols+` FROM link_drafts WHERE user_id = ? AND source = ? AND thread_id IS NOT NULL ORDER BY created_at DESC LIMIT 200`, userID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LinkDraft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClaimLinkDraft marks a draft as being sent; false when it already was.
func (s *Store) ClaimLinkDraft(ctx context.Context, userID int64, id string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE link_drafts SET used_at = ? WHERE id = ? AND user_id = ? AND used_at IS NULL`, Now(), id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FinishLinkDraft records the thread a draft became (empty: release the claim).
func (s *Store) FinishLinkDraft(ctx context.Context, id, threadID string) error {
	if threadID == "" {
		_, err := s.DB.ExecContext(ctx, `UPDATE link_drafts SET used_at = NULL WHERE id = ? AND thread_id IS NULL`, id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE link_drafts SET thread_id = ? WHERE id = ?`, threadID, id)
	return err
}

// LinkDraftOfThread is the draft a thread was started from (ErrNotFound for
// a thread no app prepared).
func (s *Store) LinkDraftOfThread(ctx context.Context, userID int64, threadID string) (*LinkDraft, error) {
	return scanDraft(s.DB.QueryRowContext(ctx, `SELECT `+draftCols+` FROM link_drafts WHERE user_id = ? AND thread_id = ? ORDER BY created_at DESC LIMIT 1`, userID, threadID))
}

// ThreadContext is what the app that prepared a thread added to it.
type ThreadContext struct {
	ThreadID string `json:"thread_id"`
	System   string `json:"-"`
	Source   string `json:"source"`
	Ref      string `json:"ref"`
	RefURL   string `json:"ref_url"`
	RefLabel string `json:"ref_label"`
}

// SaveThreadContext stores (or replaces) a thread's context.
func (s *Store) SaveThreadContext(ctx context.Context, c *ThreadContext) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO thread_context (thread_id, system, source, ref, ref_url, ref_label) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET system = excluded.system, source = excluded.source, ref = excluded.ref, ref_url = excluded.ref_url, ref_label = excluded.ref_label`,
		c.ThreadID, c.System, c.Source, c.Ref, c.RefURL, c.RefLabel)
	return err
}

// GetThreadContext loads a thread's context (ErrNotFound when it has none).
func (s *Store) GetThreadContext(ctx context.Context, threadID string) (*ThreadContext, error) {
	var c ThreadContext
	err := s.DB.QueryRowContext(ctx, `SELECT thread_id, system, source, ref, ref_url, ref_label FROM thread_context WHERE thread_id = ?`, threadID).
		Scan(&c.ThreadID, &c.System, &c.Source, &c.Ref, &c.RefURL, &c.RefLabel)
	if err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// DeleteLinkDraft forgets a prepared conversation (the thread, if one was
// started, stays unless the caller removes it).
func (s *Store) DeleteLinkDraft(ctx context.Context, userID int64, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM link_drafts WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
