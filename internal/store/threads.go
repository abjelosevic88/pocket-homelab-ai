package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"strings"
)

const threadCols = `id, user_id, title, provider, model, effort, mode, speed, cwd, workspace_id, provider_session_id, log_offset, status, error,
	pinned, archived, unread, completed_unread, source, source_ref, preview, last_prompt, prompt_at, usage, created_at, updated_at, activity_at, no_memory, account_id, host_id, worktree_id, host_home`

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	var t Thread
	var ws sql.NullInt64
	var usage string
	if err := row.Scan(&t.ID, &t.UserID, &t.Title, &t.Provider, &t.Model, &t.Effort, &t.Mode, &t.Speed, &t.Cwd, &ws, &t.ProviderSessionID, &t.LogOffset,
		&t.Status, &t.Error, &t.Pinned, &t.Archived, &t.Unread, &t.CompletedUnread, &t.Source, &t.SourceRef, &t.Preview, &t.LastPrompt, &t.PromptAt, &usage,
		&t.CreatedAt, &t.UpdatedAt, &t.ActivityAt, &t.NoMemory, &t.AccountID, &t.HostID, &t.WorktreeID, &t.HostHome); err != nil {
		return nil, notFound(err)
	}
	if ws.Valid {
		t.WorkspaceID = &ws.Int64
	}
	_ = jsonUnmarshal(usage, &t.Usage)
	return &t, nil
}

// CreateThread inserts a thread (ID must be set).
func (s *Store) CreateThread(ctx context.Context, t *Thread) error {
	now := Now()
	t.CreatedAt, t.UpdatedAt, t.ActivityAt = now, now, now
	if t.Status == "" {
		t.Status = "idle"
	}
	if t.Source == "" {
		t.Source = "web"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO threads (id, user_id, title, provider, model, effort, mode, speed, cwd, workspace_id,
		provider_session_id, log_offset, status, source, source_ref, usage, created_at, updated_at, activity_at, no_memory, account_id, host_id, worktree_id, host_home)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Title, t.Provider, t.Model, t.Effort, t.Mode, t.Speed, t.Cwd, nullInt(t.WorkspaceID), t.ProviderSessionID, t.LogOffset,
		t.Status, t.Source, t.SourceRef, jsonMarshal(t.Usage), now, now, now, t.NoMemory, t.AccountID, t.HostID, t.WorktreeID, t.HostHome)
	if err == nil {
		s.claimSession(ctx, t)
	}
	return err
}

// GetThread loads a thread of userID (ErrNotFound for anyone else's).
func (s *Store) GetThread(ctx context.Context, userID int64, id string) (*Thread, error) {
	return scanThread(s.DB.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ? AND user_id = ?`, id, userID))
}

// GetThreadAny loads a thread whoever owns it (runtime use only).
func (s *Store) GetThreadAny(ctx context.Context, id string) (*Thread, error) {
	return scanThread(s.DB.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
}

// ThreadFilter narrows ListThreads.
type ThreadFilter struct {
	Archived    bool
	WorkspaceID int64
	Q           string
	Limit       int
}

// ListThreads lists a person's threads, pinned first, newest activity first.
func (s *Store) ListThreads(ctx context.Context, userID int64, f ThreadFilter) ([]*Thread, error) {
	q := `SELECT ` + threadCols + ` FROM threads WHERE user_id = ? AND archived = ?`
	args := []any{userID, boolInt(f.Archived)}
	if f.WorkspaceID > 0 {
		q += ` AND workspace_id = ?`
		args = append(args, f.WorkspaceID)
	}
	if t := strings.TrimSpace(f.Q); t != "" {
		q += ` AND (title LIKE ? ESCAPE '\' OR cwd LIKE ? ESCAPE '\')`
		like := "%" + likeEscape(t) + "%"
		args = append(args, like, like)
	}
	q += ` ORDER BY pinned DESC, activity_at DESC`
	if f.Limit <= 0 {
		f.Limit = 500
	}
	q += ` LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
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

// UpdateThread writes the editable and runtime fields of a thread.
func (s *Store) UpdateThread(ctx context.Context, t *Thread) error {
	t.UpdatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `UPDATE threads SET title = ?, model = ?, effort = ?, mode = ?, speed = ?, cwd = ?, workspace_id = ?,
		provider_session_id = ?, log_offset = ?, status = ?, error = ?, pinned = ?, archived = ?, unread = ?, completed_unread = ?, preview = ?, last_prompt = ?, prompt_at = ?, usage = ?,
		updated_at = ?, activity_at = ?, account_id = ?, host_home = ? WHERE id = ?`,
		t.Title, t.Model, t.Effort, t.Mode, t.Speed, t.Cwd, nullInt(t.WorkspaceID), t.ProviderSessionID, t.LogOffset, t.Status, t.Error,
		boolInt(t.Pinned), boolInt(t.Archived), boolInt(t.Unread), boolInt(t.CompletedUnread), t.Preview, t.LastPrompt, t.PromptAt, jsonMarshal(t.Usage), t.UpdatedAt, t.ActivityAt, t.AccountID, t.HostHome, t.ID)
	if err == nil {
		s.claimSession(ctx, t)
	}
	return err
}

// claimSession records that the thread's CLI session is its person's (the
// first claim wins).
func (s *Store) claimSession(ctx context.Context, t *Thread) {
	if t.ProviderSessionID == "" || t.UserID == 0 {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO session_owners (provider, session_id, user_id) VALUES (?, ?, ?)`, t.Provider, t.ProviderSessionID, t.UserID)
}

// SessionOwners maps "provider:session id" to the person whose thread used it.
func (s *Store) SessionOwners(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, session_id, user_id FROM session_owners`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, sid string
		var uid int64
		if rows.Scan(&p, &sid, &uid) == nil {
			out[p+":"+sid] = uid
		}
	}
	return out
}

// DeleteThread removes a thread and its timeline.
func (s *Store) DeleteThread(ctx context.Context, userID int64, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM threads WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ThreadBySession finds a person's thread that resumes a provider session.
func (s *Store) ThreadBySession(ctx context.Context, userID int64, provider, sessionID string) (*Thread, error) {
	return scanThread(s.DB.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE user_id = ? AND provider = ? AND provider_session_id = ? ORDER BY created_at LIMIT 1`, userID, provider, sessionID))
}

// SessionThreads maps provider session ids to a person's thread ids.
func (s *Store) SessionThreads(ctx context.Context, userID int64, provider string) map[string]string {
	out := map[string]string{}
	rows, err := s.DB.QueryContext(ctx, `SELECT provider_session_id, id FROM threads WHERE user_id = ? AND provider = ? AND provider_session_id <> ''`, userID, provider)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sid, id string
		if rows.Scan(&sid, &id) == nil {
			if _, ok := out[sid]; !ok {
				out[sid] = id
			}
		}
	}
	return out
}

// SessionThread is the thread that shows a CLI session, and how it started.
type SessionThread struct{ ID, Source string }

// SessionThreadsAll maps "provider:session id" to a person's thread for it.
func (s *Store) SessionThreadsAll(ctx context.Context, userID int64) map[string]SessionThread {
	out := map[string]SessionThread{}
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, provider_session_id, id, source FROM threads WHERE user_id = ? AND provider_session_id <> '' ORDER BY created_at`, userID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, sid, id, src string
		if rows.Scan(&p, &sid, &id, &src) == nil {
			if _, ok := out[p+":"+sid]; !ok {
				out[p+":"+sid] = SessionThread{id, src}
			}
		}
	}
	return out
}

// ResetRunning marks threads left running by a previous process as idle and
// returns the ones that were cut off mid-turn.
func (s *Store) ResetRunning(ctx context.Context) []string {
	var cut []string
	if rows, err := s.DB.QueryContext(ctx, `SELECT id FROM threads WHERE status IN ('running', 'waiting')`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				cut = append(cut, id)
			}
		}
		rows.Close()
	}
	// messages still waiting in a queue were lost with the process
	_, _ = s.DB.ExecContext(ctx, `UPDATE items SET data = json_set(json_remove(data, '$.queued'), '$.unsent', json('true')) WHERE kind = 'user' AND json_extract(data, '$.queued') = 1`)
	_, _ = s.DB.ExecContext(ctx, `UPDATE threads SET status = 'idle' WHERE status IN ('running', 'waiting')`)
	_, _ = s.DB.ExecContext(ctx, `UPDATE items SET data = json_set(data, '$.request.status', 'expired') WHERE kind = 'request' AND json_extract(data, '$.request.status') = 'pending'`)
	_, _ = s.DB.ExecContext(ctx, `UPDATE items SET data = json_set(data, '$.tool.status', 'error') WHERE kind = 'tool' AND json_extract(data, '$.tool.status') = 'running'`)
	return cut
}

// ---------- items ----------

// UpsertItem inserts or replaces the item with (thread, key) and returns it.
func (s *Store) UpsertItem(ctx context.Context, threadID, key, kind string, data []byte, body string) (*Item, error) {
	now := Now()
	row := s.DB.QueryRowContext(ctx, `INSERT INTO items (thread_id, key, kind, data, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id, key) DO UPDATE SET kind = excluded.kind, data = excluded.data, body = excluded.body, updated_at = excluded.updated_at
		RETURNING id, created_at, updated_at`, threadID, key, kind, string(data), body, now, now)
	it := &Item{ThreadID: threadID, Key: key, Kind: kind, Data: data, Body: body}
	if err := row.Scan(&it.ID, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	return it, nil
}

// GetItem loads one item by key.
func (s *Store) GetItem(ctx context.Context, threadID, key string) (*Item, error) {
	var it Item
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT id, thread_id, key, kind, data, body, created_at, updated_at FROM items WHERE thread_id = ? AND key = ?`, threadID, key).
		Scan(&it.ID, &it.ThreadID, &it.Key, &it.Kind, &data, &it.Body, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	it.Data = json.RawMessage(data)
	return &it, nil
}

// HasUserItems reports whether a user message has been saved in this chat.
func (s *Store) HasUserItems(ctx context.Context, threadID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE thread_id=? AND kind='user')`, threadID).Scan(&exists)
	return exists, err
}

// ListItems returns a thread's timeline in order.
func (s *Store) ListItems(ctx context.Context, threadID string) ([]*Item, error) {
	// "acct-" notices ("From here on this conversation runs on …") were left
	// in conversations by account switches before 0.13.1: no longer shown.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, thread_id, key, kind, data, body, created_at, updated_at FROM items WHERE thread_id = ? AND key NOT LIKE 'acct-%' ORDER BY id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Item{}
	for rows.Next() {
		var it Item
		var data string
		if err := rows.Scan(&it.ID, &it.ThreadID, &it.Key, &it.Kind, &data, &it.Body, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Data = json.RawMessage(data)
		out = append(out, &it)
	}
	return out, rows.Err()
}

// DeleteItem removes one item.
func (s *Store) DeleteItem(ctx context.Context, threadID, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM items WHERE thread_id = ? AND key = ?`, threadID, key)
	return err
}

// SearchHit is one full-text match.
type SearchHit struct {
	ThreadID   string `json:"thread_id"`
	Title      string `json:"title"`
	Provider   string `json:"provider"`
	Snippet    string `json:"snippet"`
	ItemKey    string `json:"item_key"`
	ActivityAt string `json:"activity_at"`
}

// Search finds a person's messages matching q (FTS5), best thread first.
func (s *Store) Search(ctx context.Context, userID int64, q string, limit int) ([]SearchHit, error) {
	fts := ftsQuery(q)
	if fts == "" {
		return []SearchHit{}, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id, t.title, t.provider, snippet(items_fts, 0, char(1), char(2), '…', 14), i.key, t.activity_at
		FROM items_fts JOIN items i ON i.id = items_fts.rowid JOIN threads t ON t.id = i.thread_id
		WHERE items_fts MATCH ? AND t.user_id = ? ORDER BY rank LIMIT ?`, fts, userID, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	seen := map[string]bool{}
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ThreadID, &h.Title, &h.Provider, &h.Snippet, &h.ItemKey, &h.ActivityAt); err != nil {
			return nil, err
		}
		if seen[h.ThreadID] {
			continue
		}
		seen[h.ThreadID] = true
		h.Snippet = strings.NewReplacer("\x01", "<mark>", "\x02", "</mark>").Replace(html.EscapeString(h.Snippet))
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// ftsQuery turns what someone typed into prefix terms ANDed together.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		w = strings.Map(func(r rune) rune {
			if r == '"' || r == '*' || r == '(' || r == ')' || r == ':' || r == '^' {
				return -1
			}
			return r
		}, w)
		if w != "" {
			terms = append(terms, `"`+w+`"*`)
		}
	}
	return strings.Join(terms, " ")
}

// ---------- uploads ----------

// CreateUpload records an uploaded file.
func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	u.CreatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO uploads (id, user_id, name, mime, size, path, created_at, extracted_text) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.UserID, u.Name, u.Mime, u.Size, u.Path, u.CreatedAt, u.ExtractedText)
	return err
}

// GetUpload loads one of a person's uploads.
func (s *Store) GetUpload(ctx context.Context, userID int64, id string) (*Upload, error) {
	var u Upload
	var tid sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, thread_id, name, mime, size, path, created_at, extracted_text FROM uploads WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&u.ID, &u.UserID, &tid, &u.Name, &u.Mime, &u.Size, &u.Path, &u.CreatedAt, &u.ExtractedText)
	if err != nil {
		return nil, notFound(err)
	}
	if tid.Valid {
		u.ThreadID = &tid.String
	}
	return &u, nil
}

// AttachUpload ties an upload to the thread it was sent in.
func (s *Store) AttachUpload(ctx context.Context, id, threadID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE uploads SET thread_id = ? WHERE id = ?`, threadID, id)
	return err
}

// DeleteUpload removes the row (the caller removes the file).
func (s *Store) DeleteUpload(ctx context.Context, userID int64, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM uploads WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// OrphanUploads lists uploads never sent, older than the cutoff, or whose thread is gone.
func (s *Store) OrphanUploads(ctx context.Context, before string) ([]Upload, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, path FROM uploads WHERE thread_id IS NULL AND created_at < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		if rows.Scan(&u.ID, &u.UserID, &u.Path) == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// UploadsOfThread lists the files attached in a thread (to delete with it).
func (s *Store) UploadsOfThread(ctx context.Context, threadID string) []Upload {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, path FROM uploads WHERE thread_id = ?`, threadID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		if rows.Scan(&u.ID, &u.UserID, &u.Path) == nil {
			out = append(out, u)
		}
	}
	return out
}

// ---------- turns / usage ----------

// AddTurn records a finished turn.
func (s *Store) AddTurn(ctx context.Context, t Turn) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO turns (user_id, thread_id, provider, model, ok, duration_ms, input_tokens, output_tokens, cached_tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.UserID, t.ThreadID, t.Provider, t.Model, boolInt(t.OK), t.DurationMS,
		t.InputTokens, t.OutputTokens, t.CachedTokens, t.CostUSD)
	return err
}

// UsageDay is one day of the usage chart.
type UsageDay struct {
	Date         string  `json:"date"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int     `json:"turns"`
	Claude       float64 `json:"claude_cost_usd"`
	Codex        int     `json:"codex_turns"`
}

// UsageByDay sums a person's turns per local day since `since` (UTC ISO).
func (s *Store) UsageByDay(ctx context.Context, userID int64, since, tzOffset string) ([]UsageDay, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT date(created_at, ?) d, SUM(cost_usd), SUM(input_tokens), SUM(output_tokens), COUNT(1),
		SUM(CASE WHEN provider = 'claude' THEN cost_usd ELSE 0 END), SUM(CASE WHEN provider = 'codex' THEN 1 ELSE 0 END)
		FROM turns WHERE user_id = ? AND created_at >= ? GROUP BY d ORDER BY d`, tzOffset, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDay{}
	for rows.Next() {
		var d UsageDay
		if err := rows.Scan(&d.Date, &d.CostUSD, &d.InputTokens, &d.OutputTokens, &d.Turns, &d.Claude, &d.Codex); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ProviderTotal is one provider's total over a period.
type ProviderTotal struct {
	CostUSD      float64 `json:"cost_usd"`
	Turns        int     `json:"turns"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// UsageByProvider sums a person's turns per provider since `since`.
func (s *Store) UsageByProvider(ctx context.Context, userID int64, since string) (map[string]ProviderTotal, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, SUM(cost_usd), COUNT(1), SUM(input_tokens), SUM(output_tokens) FROM turns
		WHERE user_id = ? AND created_at >= ? GROUP BY provider`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProviderTotal{"claude": {}, "codex": {}}
	for rows.Next() {
		var p string
		var t ProviderTotal
		if err := rows.Scan(&p, &t.CostUSD, &t.Turns, &t.InputTokens, &t.OutputTokens); err != nil {
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// CompletionBadge counts conversations, never turns, and excludes other owners.
func (s *Store) CompletionBadge(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads WHERE user_id=? AND completed_unread=1 AND unread=1 AND archived=0 AND status='idle'`, userID).Scan(&count)
	return count, err
}
