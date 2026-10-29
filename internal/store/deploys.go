package store

import "context"

// Deploy is one run of a project's deploy command for a branch
// (server/link_deploy.go runs it).
type Deploy struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"-"`
	Project    string `json:"project"`
	HostID     int64  `json:"host_id"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
	Source     string `json:"source"`
	Ref        string `json:"ref"`
	Status     string `json:"status"` // running | ok | failed
	Note       string `json:"note"`   // Pocket AI's own lines (what it pushed)
	Log        string `json:"log,omitempty"`
	Error      string `json:"error"`
	URL        string `json:"url"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

const deployCols = `id, user_id, project, host_id, branch, sha, source, ref, status, note, log, error, url, started_at, finished_at`

func scanDeploy(row interface{ Scan(...any) error }) (*Deploy, error) {
	var d Deploy
	if err := row.Scan(&d.ID, &d.UserID, &d.Project, &d.HostID, &d.Branch, &d.SHA, &d.Source, &d.Ref, &d.Status, &d.Note, &d.Log, &d.Error, &d.URL,
		&d.StartedAt, &d.FinishedAt); err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

// CreateDeploy inserts a deploy that is starting.
func (s *Store) CreateDeploy(ctx context.Context, d *Deploy) error {
	d.StartedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO deploys (user_id, project, host_id, branch, sha, source, ref, status, note, log, error, url, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.UserID, d.Project, d.HostID, d.Branch, d.SHA, d.Source, d.Ref, d.Status, d.Note, d.Log, d.Error, d.URL, d.StartedAt, d.FinishedAt)
	if err != nil {
		return err
	}
	d.ID, err = res.LastInsertId()
	return err
}

// UpdateDeploy saves a deploy's progress.
func (s *Store) UpdateDeploy(ctx context.Context, d *Deploy) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE deploys SET sha = ?, status = ?, log = ?, error = ?, url = ?, finished_at = ? WHERE id = ?`,
		d.SHA, d.Status, d.Log, d.Error, d.URL, d.FinishedAt, d.ID)
	return err
}

// GetDeploy loads a deploy by id (any person's: a slot is shared).
func (s *Store) GetDeploy(ctx context.Context, id int64) (*Deploy, error) {
	return scanDeploy(s.DB.QueryRowContext(ctx, `SELECT `+deployCols+` FROM deploys WHERE id = ?`, id))
}

// LatestDeploy is the newest deploy of a project (nil = none yet).
func (s *Store) LatestDeploy(ctx context.Context, project string) (*Deploy, error) {
	d, err := scanDeploy(s.DB.QueryRowContext(ctx, `SELECT `+deployCols+` FROM deploys WHERE project = ? ORDER BY id DESC LIMIT 1`, project))
	if err == ErrNotFound {
		return nil, nil
	}
	return d, err
}

// LastGoodDeploy is the newest deploy of a project that went through (nil = none).
func (s *Store) LastGoodDeploy(ctx context.Context, project string) (*Deploy, error) {
	d, err := scanDeploy(s.DB.QueryRowContext(ctx, `SELECT `+deployCols+` FROM deploys WHERE project = ? AND status = 'ok' ORDER BY id DESC LIMIT 1`, project))
	if err == ErrNotFound {
		return nil, nil
	}
	return d, err
}

// RunningDeploys are the deploys not finished yet (to watch again after a restart).
func (s *Store) RunningDeploys(ctx context.Context) ([]*Deploy, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+deployCols+` FROM deploys WHERE status = 'running' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		d, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecentDeploys are a project's newest deploys, newest first.
func (s *Store) RecentDeploys(ctx context.Context, project string, limit int) ([]*Deploy, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+deployCols+` FROM deploys WHERE project = ? ORDER BY id DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		d, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
