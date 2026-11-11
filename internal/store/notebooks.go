package store

import "context"

// Notebook is a person's set of sources (files, links, notes, pasted text)
// that its chats answer from. The sources live as files in the notebook's
// folder; the agent of a chat in it works in that folder.
type Notebook struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"-"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	// Link: what it was started from ("projects-issue:athena/ATH-340",
	// "projects-project:athena/12"), its label and page; "" = nothing.
	Link      string `json:"link"`
	LinkLabel string `json:"link_label"`
	LinkURL   string `json:"link_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// filled by the list
	Sources int `json:"sources"`
	Chats   int `json:"chats"`
}

// NotebookSource is one source of a notebook: File is its text (relative to
// the notebook folder), Original the file as it came (uploads), Origin where
// it came from (a URL, "notes:12", a file name).
type NotebookSource struct {
	ID         int64  `json:"id"`
	NotebookID int64  `json:"notebook_id"`
	Kind       string `json:"kind"` // file | link | note | document | pocket-file | text
	Title      string `json:"title"`
	Origin     string `json:"origin"`
	File       string `json:"file"`
	Original   string `json:"original"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Chars      int64  `json:"chars"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const notebookCols = `id, user_id, name, instructions, link, link_label, link_url, created_at, updated_at`

func scanNotebook(row interface{ Scan(...any) error }) (*Notebook, error) {
	var n Notebook
	if err := row.Scan(&n.ID, &n.UserID, &n.Name, &n.Instructions, &n.Link, &n.LinkLabel, &n.LinkURL, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &n, nil
}

func (s *Store) ListNotebooks(ctx context.Context, userID int64) ([]*Notebook, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+notebookCols+`, (SELECT COUNT(1) FROM notebook_sources WHERE notebook_id=notebooks.id)
		FROM notebooks WHERE user_id=? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Notebook{}
	for rows.Next() {
		var n Notebook
		if err := rows.Scan(&n.ID, &n.UserID, &n.Name, &n.Instructions, &n.Link, &n.LinkLabel, &n.LinkURL, &n.CreatedAt, &n.UpdatedAt, &n.Sources); err != nil {
			return nil, err
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (s *Store) GetNotebook(ctx context.Context, userID, id int64) (*Notebook, error) {
	return scanNotebook(s.DB.QueryRowContext(ctx, `SELECT `+notebookCols+` FROM notebooks WHERE user_id=? AND id=?`, userID, id))
}

func (s *Store) SaveNotebook(ctx context.Context, n *Notebook) error {
	now := Now()
	if n.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO notebooks (user_id, name, instructions, link, link_label, link_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, n.UserID, n.Name, n.Instructions, n.Link, n.LinkLabel, n.LinkURL, now, now)
		if err != nil {
			return err
		}
		n.ID, err = res.LastInsertId()
		n.CreatedAt, n.UpdatedAt = now, now
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE notebooks SET name=?, instructions=?, link=?, link_label=?, link_url=?, updated_at=? WHERE id=? AND user_id=?`, n.Name, n.Instructions, n.Link, n.LinkLabel, n.LinkURL, now, n.ID, n.UserID)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	n.UpdatedAt = now
	return nil
}

// TouchNotebook moves a notebook to the top of the list.
func (s *Store) TouchNotebook(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE notebooks SET updated_at=? WHERE id=?`, Now(), id)
}

func (s *Store) DeleteNotebook(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM notebooks WHERE user_id=? AND id=?`, userID, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

const sourceCols = `id, notebook_id, kind, title, origin, file, original, mime, size, chars, created_at, updated_at`

func scanSource(row interface{ Scan(...any) error }) (*NotebookSource, error) {
	var x NotebookSource
	if err := row.Scan(&x.ID, &x.NotebookID, &x.Kind, &x.Title, &x.Origin, &x.File, &x.Original, &x.Mime, &x.Size, &x.Chars, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &x, nil
}

func (s *Store) ListNotebookSources(ctx context.Context, notebookID int64) ([]*NotebookSource, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+sourceCols+` FROM notebook_sources WHERE notebook_id=? ORDER BY id`, notebookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotebookSource{}
	for rows.Next() {
		x, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetNotebookSource(ctx context.Context, notebookID, id int64) (*NotebookSource, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM notebook_sources WHERE notebook_id=? AND id=?`, notebookID, id))
}

// NextSourceID is the id the next source will most likely get (it names its file).
func (s *Store) NextSourceID(ctx context.Context) int64 {
	var n int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM notebook_sources`).Scan(&n)
	return n
}

func (s *Store) SaveNotebookSource(ctx context.Context, x *NotebookSource) error {
	now := Now()
	if x.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO notebook_sources (notebook_id, kind, title, origin, file, original, mime, size, chars, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, x.NotebookID, x.Kind, x.Title, x.Origin, x.File, x.Original, x.Mime, x.Size, x.Chars, now, now)
		if err != nil {
			return err
		}
		x.ID, err = res.LastInsertId()
		x.CreatedAt, x.UpdatedAt = now, now
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE notebook_sources SET title=?, origin=?, file=?, original=?, mime=?, size=?, chars=?, updated_at=? WHERE id=? AND notebook_id=?`,
		x.Title, x.Origin, x.File, x.Original, x.Mime, x.Size, x.Chars, now, x.ID, x.NotebookID)
	x.UpdatedAt = now
	return err
}

func (s *Store) DeleteNotebookSource(ctx context.Context, notebookID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM notebook_sources WHERE notebook_id=? AND id=?`, notebookID, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// ThreadsInFolder lists a person's conversations working in one folder of
// this server (a notebook's chats), newest first.
func (s *Store) ThreadsInFolder(ctx context.Context, userID int64, cwd string) ([]*Thread, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+threadCols+` FROM threads WHERE user_id=? AND host_id=0 AND cwd=? ORDER BY activity_at DESC LIMIT 200`, userID, cwd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
