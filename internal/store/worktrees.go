package store

import "context"

// Worktree is an isolated git checkout on its own branch that Pocket AI made
// for a task (worktrees.go in server has the git side).
type Worktree struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"owner_id"`
	HostID     int64  `json:"host_id"`
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	Base       string `json:"base"`
	BaseSHA    string `json:"base_sha"`
	Title      string `json:"title"`
	Status     string `json:"status"` // active | archived
	Setup      string `json:"setup"`  // "" | running | ok | failed
	ArchiveRef string `json:"archive_ref"`
	MergedAt   string `json:"merged_at"`
	PRURL      string `json:"pr_url"`
	CreatedAt  string `json:"created_at"`
	ArchivedAt string `json:"archived_at"`
}

const worktreeCols = `id, user_id, host_id, repo, path, branch, base, base_sha, title, status, setup, archive_ref, merged_at, pr_url, created_at, archived_at`

func scanWorktree(row interface{ Scan(...any) error }) (*Worktree, error) {
	var w Worktree
	if err := row.Scan(&w.ID, &w.UserID, &w.HostID, &w.Repo, &w.Path, &w.Branch, &w.Base, &w.BaseSHA, &w.Title, &w.Status, &w.Setup,
		&w.ArchiveRef, &w.MergedAt, &w.PRURL, &w.CreatedAt, &w.ArchivedAt); err != nil {
		return nil, notFound(err)
	}
	return &w, nil
}

// CreateWorktree inserts a worktree.
func (s *Store) CreateWorktree(ctx context.Context, w *Worktree) error {
	w.CreatedAt = Now()
	if w.Status == "" {
		w.Status = "active"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees (user_id, host_id, repo, path, branch, base, base_sha, title, status, setup, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, w.UserID, w.HostID, w.Repo, w.Path, w.Branch, w.Base, w.BaseSHA, w.Title, w.Status, w.Setup, w.CreatedAt)
	if err != nil {
		return err
	}
	w.ID, _ = res.LastInsertId()
	return nil
}

// UpdateWorktree saves everything but the owner and where it is.
func (s *Store) UpdateWorktree(ctx context.Context, w *Worktree) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET branch = ?, base = ?, base_sha = ?, title = ?, status = ?, setup = ?, archive_ref = ?,
		merged_at = ?, pr_url = ?, archived_at = ? WHERE id = ?`,
		w.Branch, w.Base, w.BaseSHA, w.Title, w.Status, w.Setup, w.ArchiveRef, w.MergedAt, w.PRURL, w.ArchivedAt, w.ID)
	return err
}

// GetWorktree loads one worktree of a person (ErrNotFound for anyone else's).
func (s *Store) GetWorktree(ctx context.Context, userID, id int64) (*Worktree, error) {
	return scanWorktree(s.DB.QueryRowContext(ctx, `SELECT `+worktreeCols+` FROM worktrees WHERE id = ? AND user_id = ?`, id, userID))
}

// GetWorktreeAny loads a worktree whoever owns it (runtime use only).
func (s *Store) GetWorktreeAny(ctx context.Context, id int64) (*Worktree, error) {
	return scanWorktree(s.DB.QueryRowContext(ctx, `SELECT `+worktreeCols+` FROM worktrees WHERE id = ?`, id))
}

// ListWorktrees lists a person's worktrees, newest first; status "" = all.
func (s *Store) ListWorktrees(ctx context.Context, userID int64, status string) ([]*Worktree, error) {
	q := `SELECT ` + worktreeCols + ` FROM worktrees WHERE user_id = ?`
	args := []any{userID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Worktree{}
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorktreeThreads lists the ids of the threads working in a worktree.
func (s *Store) WorktreeThreads(ctx context.Context, id int64) []string {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM threads WHERE worktree_id = ? ORDER BY created_at`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tid string
		if rows.Scan(&tid) == nil {
			out = append(out, tid)
		}
	}
	return out
}

// RepoSettings loads the stored settings JSON of a repository ("" = none).
func (s *Store) RepoSettings(ctx context.Context, hostID int64, repo string) string {
	var data string
	_ = s.DB.QueryRowContext(ctx, `SELECT data FROM repo_settings WHERE host_id = ? AND repo = ?`, hostID, repo).Scan(&data)
	return data
}

// SaveRepoSettings stores the settings JSON of a repository.
func (s *Store) SaveRepoSettings(ctx context.Context, hostID int64, repo, data string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO repo_settings (host_id, repo, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(host_id, repo) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, hostID, repo, data, Now())
	return err
}

// FailRunningSetups marks setups cut off by a restart as failed.
func (s *Store) FailRunningSetups(ctx context.Context) []int64 {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM worktrees WHERE setup = 'running'`)
	if err != nil {
		return nil
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	_, _ = s.DB.ExecContext(ctx, `UPDATE worktrees SET setup = 'failed' WHERE setup = 'running'`)
	return ids
}

// DeleteWorktree forgets a worktree; its threads stay (in a folder that is gone).
func (s *Store) DeleteWorktree(ctx context.Context, id int64) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE threads SET worktree_id = 0 WHERE worktree_id = ?`, id); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM worktrees WHERE id = ?`, id)
	return err
}
