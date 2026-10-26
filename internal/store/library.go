package store

import "context"

// ---------- workspaces ----------

// ListWorkspaces lists a person's workspaces with their thread counts.
func (s *Store) ListWorkspaces(ctx context.Context, userID int64) ([]*Workspace, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT w.id, w.user_id, w.name, w.path, w.color, w.sort_order, w.created_at,
		(SELECT COUNT(1) FROM threads t WHERE t.workspace_id = w.id AND t.archived = 0)
		FROM workspaces w WHERE w.user_id = ? ORDER BY w.sort_order, w.name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Path, &w.Color, &w.SortOrder, &w.CreatedAt, &w.Threads); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// GetWorkspace loads one of a person's workspaces.
func (s *Store) GetWorkspace(ctx context.Context, userID, id int64) (*Workspace, error) {
	var w Workspace
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, name, path, color, sort_order, created_at FROM workspaces WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&w.ID, &w.UserID, &w.Name, &w.Path, &w.Color, &w.SortOrder, &w.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &w, nil
}

// SaveWorkspace inserts (ID 0) or updates a workspace.
func (s *Store) SaveWorkspace(ctx context.Context, w *Workspace) error {
	if w.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO workspaces (user_id, name, path, color, sort_order) VALUES (?, ?, ?, ?, ?)`,
			w.UserID, w.Name, w.Path, w.Color, w.SortOrder)
		if err != nil {
			return err
		}
		w.ID, _ = res.LastInsertId()
		w.CreatedAt = Now()
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE workspaces SET name = ?, path = ?, color = ?, sort_order = ? WHERE id = ? AND user_id = ?`,
		w.Name, w.Path, w.Color, w.SortOrder, w.ID, w.UserID)
	return err
}

// DeleteWorkspace removes a workspace (threads keep their folder).
func (s *Store) DeleteWorkspace(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkspaceForPath finds a person's workspace whose folder is path.
func (s *Store) WorkspaceForPath(ctx context.Context, userID int64, path string) *int64 {
	var id int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE user_id = ? AND path = ? LIMIT 1`, userID, path).Scan(&id); err != nil {
		return nil
	}
	return &id
}

// ---------- recipes ----------

const recipeCols = `id, user_id, name, prompt, icon, provider, model, effort, mode, speed, cwd, pinned, sort_order, runs, created_at, updated_at`

func scanRecipe(row interface{ Scan(...any) error }) (*Recipe, error) {
	var r Recipe
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &r.Prompt, &r.Icon, &r.Provider, &r.Model, &r.Effort, &r.Mode, &r.Speed, &r.Cwd,
		&r.Pinned, &r.SortOrder, &r.Runs, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// ListRecipes lists a person's recipes.
func (s *Store) ListRecipes(ctx context.Context, userID int64) ([]*Recipe, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+recipeCols+` FROM recipes WHERE user_id = ? ORDER BY pinned DESC, sort_order, name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Recipe{}
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRecipe loads one of a person's recipes.
func (s *Store) GetRecipe(ctx context.Context, userID, id int64) (*Recipe, error) {
	return scanRecipe(s.DB.QueryRowContext(ctx, `SELECT `+recipeCols+` FROM recipes WHERE id = ? AND user_id = ?`, id, userID))
}

// SaveRecipe inserts (ID 0) or updates a recipe.
func (s *Store) SaveRecipe(ctx context.Context, r *Recipe) error {
	now := Now()
	r.UpdatedAt = now
	if r.ID == 0 {
		r.CreatedAt = now
		res, err := s.DB.ExecContext(ctx, `INSERT INTO recipes (user_id, name, prompt, icon, provider, model, effort, mode, speed, cwd, pinned, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.UserID, r.Name, r.Prompt, r.Icon, r.Provider, r.Model, r.Effort, r.Mode, r.Speed, r.Cwd,
			boolInt(r.Pinned), r.SortOrder, now, now)
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE recipes SET name = ?, prompt = ?, icon = ?, provider = ?, model = ?, effort = ?, mode = ?, speed = ?, cwd = ?,
		pinned = ?, sort_order = ?, updated_at = ? WHERE id = ? AND user_id = ?`, r.Name, r.Prompt, r.Icon, r.Provider, r.Model, r.Effort, r.Mode, r.Speed, r.Cwd,
		boolInt(r.Pinned), r.SortOrder, now, r.ID, r.UserID)
	return err
}

// DeleteRecipe removes a recipe.
func (s *Store) DeleteRecipe(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM recipes WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountRecipeRun bumps a recipe's run counter.
func (s *Store) CountRecipeRun(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE recipes SET runs = runs + 1 WHERE id = ?`, id)
}

// ---------- schedules ----------

const scheduleCols = `id, user_id, name, prompt, provider, model, effort, mode, speed, host_id, cwd, spec, notify, enabled, next_run_at, last_run_at,
	last_thread_id, last_status, last_summary, created_at, updated_at`

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var sc Schedule
	if err := row.Scan(&sc.ID, &sc.UserID, &sc.Name, &sc.Prompt, &sc.Provider, &sc.Model, &sc.Effort, &sc.Mode, &sc.Speed, &sc.HostID, &sc.Cwd, &sc.Spec,
		&sc.Notify, &sc.Enabled, &sc.NextRunAt, &sc.LastRunAt, &sc.LastThreadID, &sc.LastStatus, &sc.LastSummary,
		&sc.CreatedAt, &sc.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &sc, nil
}

func (s *Store) listSchedules(ctx context.Context, q string, args ...any) ([]*Schedule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+scheduleCols+` FROM schedules `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListSchedules lists a person's schedules.
func (s *Store) ListSchedules(ctx context.Context, userID int64) ([]*Schedule, error) {
	return s.listSchedules(ctx, `WHERE user_id = ? ORDER BY name COLLATE NOCASE`, userID)
}

// DueSchedules lists enabled schedules whose next run is at or before now.
func (s *Store) DueSchedules(ctx context.Context, now string) ([]*Schedule, error) {
	return s.listSchedules(ctx, `WHERE enabled = 1 AND next_run_at <> '' AND next_run_at <= ? ORDER BY next_run_at`, now)
}

// GetSchedule loads one of a person's schedules.
func (s *Store) GetSchedule(ctx context.Context, userID, id int64) (*Schedule, error) {
	return scanSchedule(s.DB.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE id = ? AND user_id = ?`, id, userID))
}

// ScheduleByThread finds the schedule whose last run is threadID.
func (s *Store) ScheduleByThread(ctx context.Context, threadID string) (*Schedule, error) {
	return scanSchedule(s.DB.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE last_thread_id = ?`, threadID))
}

// SaveSchedule inserts (ID 0) or updates a schedule.
func (s *Store) SaveSchedule(ctx context.Context, sc *Schedule) error {
	now := Now()
	sc.UpdatedAt = now
	if sc.ID == 0 {
		sc.CreatedAt = now
		res, err := s.DB.ExecContext(ctx, `INSERT INTO schedules (user_id, name, prompt, provider, model, effort, mode, speed, host_id, cwd, spec, notify, enabled, next_run_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sc.UserID, sc.Name, sc.Prompt, sc.Provider, sc.Model, sc.Effort, sc.Mode, sc.Speed, sc.HostID, sc.Cwd,
			sc.Spec, sc.Notify, boolInt(sc.Enabled), sc.NextRunAt, now, now)
		if err != nil {
			return err
		}
		sc.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE schedules SET name = ?, prompt = ?, provider = ?, model = ?, effort = ?, mode = ?, speed = ?, host_id = ?, cwd = ?, spec = ?,
		notify = ?, enabled = ?, next_run_at = ?, last_run_at = ?, last_thread_id = ?, last_status = ?, last_summary = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`, sc.Name, sc.Prompt, sc.Provider, sc.Model, sc.Effort, sc.Mode, sc.Speed, sc.HostID, sc.Cwd, sc.Spec, sc.Notify,
		boolInt(sc.Enabled), sc.NextRunAt, sc.LastRunAt, sc.LastThreadID, sc.LastStatus, sc.LastSummary, now, sc.ID, sc.UserID)
	return err
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM schedules WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
