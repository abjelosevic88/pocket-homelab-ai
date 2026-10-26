package store

import "context"

// Snippet is reusable text owned by one person. Inserting it never starts an agent.
type Snippet struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const snippetCols = `id, user_id, title, body, created_at, updated_at`

func scanSnippet(row interface{ Scan(...any) error }) (*Snippet, error) {
	var s Snippet
	if err := row.Scan(&s.ID, &s.UserID, &s.Title, &s.Body, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &s, nil
}

func (s *Store) ListSnippets(ctx context.Context, userID int64) ([]*Snippet, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+snippetCols+` FROM snippets WHERE user_id=? ORDER BY title COLLATE NOCASE, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Snippet{}
	for rows.Next() {
		snippet, err := scanSnippet(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, snippet)
	}
	return result, rows.Err()
}

func (s *Store) GetSnippet(ctx context.Context, userID, id int64) (*Snippet, error) {
	return scanSnippet(s.DB.QueryRowContext(ctx, `SELECT `+snippetCols+` FROM snippets WHERE user_id=? AND id=?`, userID, id))
}

func (s *Store) SaveSnippet(ctx context.Context, snippet *Snippet) error {
	now := Now()
	if snippet.ID == 0 {
		result, err := s.DB.ExecContext(ctx, `INSERT INTO snippets (user_id, title, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, snippet.UserID, snippet.Title, snippet.Body, now, now)
		if err != nil {
			return err
		}
		snippet.ID, err = result.LastInsertId()
		snippet.CreatedAt, snippet.UpdatedAt = now, now
		return err
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE snippets SET title=?, body=?, updated_at=? WHERE id=? AND user_id=?`, snippet.Title, snippet.Body, now, snippet.ID, snippet.UserID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	snippet.UpdatedAt = now
	return nil
}

func (s *Store) DeleteSnippet(ctx context.Context, userID, id int64) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM snippets WHERE user_id=? AND id=?`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
