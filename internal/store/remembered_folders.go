package store

import (
	"context"
	"database/sql"
	"time"
)

type RememberedFolder struct {
	Path string `json:"path"`
}

type FolderHistory struct {
	Folders []RememberedFolder `json:"folders"`
	// nil means no previous choice; empty means explicitly use the machine default.
	SelectedPath *string `json:"selected_path"`
}

func (s *Store) FolderHistory(ctx context.Context, user, host int64) (FolderHistory, error) {
	out := FolderHistory{Folders: []RememberedFolder{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT path FROM remembered_folders WHERE user_id=? AND host_id=? ORDER BY used_at DESC, path LIMIT 12`, user, host)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var f RememberedFolder
		if err := rows.Scan(&f.Path); err != nil {
			rows.Close()
			return out, err
		}
		out.Folders = append(out.Folders, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	var p string
	err = s.DB.QueryRowContext(ctx, `SELECT path FROM folder_choices WHERE user_id=? AND host_id=?`, user, host).Scan(&p)
	if err == nil {
		out.SelectedPath = &p
	} else if err != sql.ErrNoRows {
		return out, err
	}
	return out, nil
}

func (s *Store) RememberFolder(ctx context.Context, user, host int64, path string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if path != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO remembered_folders(user_id,host_id,path,used_at) VALUES(?,?,?,?) ON CONFLICT(user_id,host_id,path) DO UPDATE SET used_at=excluded.used_at`, user, host, path, time.Now().UnixNano()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM remembered_folders WHERE user_id=? AND host_id=? AND path NOT IN (SELECT path FROM remembered_folders WHERE user_id=? AND host_id=? ORDER BY used_at DESC, path LIMIT 12)`, user, host, user, host); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO folder_choices(user_id,host_id,path) VALUES(?,?,?) ON CONFLICT(user_id,host_id) DO UPDATE SET path=excluded.path`, user, host, path); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ForgetFolder(ctx context.Context, user, host int64, path string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM remembered_folders WHERE user_id=? AND host_id=? AND path=?`, user, host, path); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM folder_choices WHERE user_id=? AND host_id=? AND path=?`, user, host, path); err != nil {
		return err
	}
	return tx.Commit()
}
