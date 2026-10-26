package store

import (
	"context"
	"database/sql"
)

// WorktreeGroup is one of a person's own groups on the Worktrees page.
type WorktreeGroup struct {
	ID        int64   `json:"id"`
	ParentID  *int64  `json:"parent_id"` // nil: at the top
	Name      string  `json:"name"`
	Position  int     `json:"position"`
	Collapsed bool    `json:"collapsed"`
	Items     []int64 `json:"items"` // worktree ids, in order
}

// WorktreeGroups lists a person's groups with their worktrees, in order.
func (s *Store) WorktreeGroups(ctx context.Context, userID int64) ([]*WorktreeGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, parent_id, name, position, collapsed FROM worktree_groups WHERE user_id = ? ORDER BY position, id`, userID)
	if err != nil {
		return nil, err
	}
	out := []*WorktreeGroup{}
	byID := map[int64]*WorktreeGroup{}
	for rows.Next() {
		g := &WorktreeGroup{Items: []int64{}}
		if err := rows.Scan(&g.ID, &g.ParentID, &g.Name, &g.Position, &g.Collapsed); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
		byID[g.ID] = g
	}
	rows.Close()
	items, err := s.DB.QueryContext(ctx, `SELECT i.group_id, i.worktree_id FROM worktree_group_items i JOIN worktree_groups g ON g.id = i.group_id
		WHERE g.user_id = ? ORDER BY i.position, i.worktree_id`, userID)
	if err != nil {
		return nil, err
	}
	defer items.Close()
	for items.Next() {
		var gid, wid int64
		if err := items.Scan(&gid, &wid); err != nil {
			return nil, err
		}
		if g := byID[gid]; g != nil {
			g.Items = append(g.Items, wid)
		}
	}
	return out, items.Err()
}

// CreateWorktreeGroup adds a group at the end of its parent (0: at the top).
func (s *Store) CreateWorktreeGroup(ctx context.Context, userID int64, name string, parentID int64) (*WorktreeGroup, error) {
	g := &WorktreeGroup{Name: name, Items: []int64{}}
	if parentID != 0 {
		if !s.ownsGroup(ctx, s.DB, userID, parentID) {
			return nil, ErrNotFound
		}
		g.ParentID = &parentID
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM worktree_groups WHERE user_id = ? AND parent_id IS ?`, userID, g.ParentID).Scan(&g.Position)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO worktree_groups (user_id, parent_id, name, position, created_at) VALUES (?, ?, ?, ?, ?)`, userID, g.ParentID, name, g.Position, Now())
	if err != nil {
		return nil, err
	}
	g.ID, _ = res.LastInsertId()
	return g, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) ownsGroup(ctx context.Context, q queryer, userID, id int64) bool {
	var u int64
	return q.QueryRowContext(ctx, `SELECT user_id FROM worktree_groups WHERE id = ?`, id).Scan(&u) == nil && u == userID
}

// MoveWorktreeGroup puts a group inside another (0: at the top) and sets the
// order there (order: the parent's groups after the move). A group never
// goes inside itself or one of its own groups.
func (s *Store) MoveWorktreeGroup(ctx context.Context, userID, id, parentID int64, order []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !s.ownsGroup(ctx, tx, userID, id) {
		return ErrNotFound
	}
	var parent *int64
	if parentID != 0 {
		if !s.ownsGroup(ctx, tx, userID, parentID) {
			return ErrNotFound
		}
		for p, depth := parentID, 0; p != 0 && depth < 100; depth++ {
			if p == id {
				return ErrConflict
			}
			var up sql.NullInt64
			if tx.QueryRowContext(ctx, `SELECT parent_id FROM worktree_groups WHERE id = ?`, p).Scan(&up) != nil {
				break
			}
			p = up.Int64
		}
		parent = &parentID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_groups SET parent_id = ? WHERE id = ?`, parent, id); err != nil {
		return err
	}
	has := false
	for _, g := range order {
		has = has || g == id
	}
	if !has {
		order = append(order, id)
	}
	pos := 0
	for _, g := range order {
		res, err := tx.ExecContext(ctx, `UPDATE worktree_groups SET position = ? WHERE id = ? AND user_id = ? AND parent_id IS ?`, pos, g, userID, parent)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			pos++
		}
	}
	return tx.Commit()
}

// UpdateWorktreeGroup renames, folds or unfolds one of the person's groups.
func (s *Store) UpdateWorktreeGroup(ctx context.Context, userID, id int64, name *string, collapsed *bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE worktree_groups SET name = COALESCE(?, name), collapsed = COALESCE(?, collapsed) WHERE id = ? AND user_id = ?`, name, collapsed, id, userID)
	return oneRow(res, err)
}

// DeleteWorktreeGroup removes a group; its worktrees and groups move up to
// its parent (at the top: the worktrees go back to their repository).
func (s *Store) DeleteWorktreeGroup(ctx context.Context, userID, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !s.ownsGroup(ctx, tx, userID, id) {
		return ErrNotFound
	}
	var parent sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT parent_id FROM worktree_groups WHERE id = ?`, id).Scan(&parent)
	var up *int64
	if parent.Valid {
		up = &parent.Int64
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_groups SET parent_id = ?, position = position + 10000 WHERE parent_id = ? AND user_id = ?`, up, id, userID); err != nil {
		return err
	}
	if up != nil {
		_, err = tx.ExecContext(ctx, `UPDATE worktree_group_items SET group_id = ?, position = position + 10000 WHERE group_id = ?`, *up, id)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM worktree_group_items WHERE group_id = ?`, id)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_groups WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// OrderWorktreeGroups puts the person's groups in the given order.
func (s *Store) OrderWorktreeGroups(ctx context.Context, userID int64, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_groups SET position = ? WHERE id = ? AND user_id = ?`, i, id, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlaceWorktree moves one of the person's worktrees into a group (0: out of
// any group) and sets that group's order (ids: the group's worktrees after
// the move; ones that are not the person's are skipped).
func (s *Store) PlaceWorktree(ctx context.Context, userID, worktreeID, groupID int64, order []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM worktrees WHERE id = ?`, worktreeID).Scan(&owner); err != nil || owner != userID {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_group_items WHERE worktree_id = ?`, worktreeID); err != nil {
		return err
	}
	if groupID != 0 {
		var g int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM worktree_groups WHERE id = ? AND user_id = ?`, groupID, userID).Scan(&g); err != nil {
			return ErrNotFound
		}
		has := false
		for _, wid := range order {
			has = has || wid == worktreeID
		}
		if !has {
			order = append(order, worktreeID)
		}
		pos := 0
		for _, wid := range order {
			var o int64
			if tx.QueryRowContext(ctx, `SELECT user_id FROM worktrees WHERE id = ?`, wid).Scan(&o) != nil || o != userID {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_group_items (worktree_id, group_id, position) VALUES (?, ?, ?)
				ON CONFLICT(worktree_id) DO UPDATE SET group_id = excluded.group_id, position = excluded.position`, wid, groupID, pos); err != nil {
				return err
			}
			pos++
		}
	}
	return tx.Commit()
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
