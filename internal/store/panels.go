package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Panels: one question put to several models at once, in steps the server
// runs (internal/server/panels.go).

// PanelStage is one step of a panel.
type PanelStage struct {
	Name string `json:"name"`
	// Who answers: "all" (every participant) or "chair" (the one that writes the summary).
	Who string `json:"who"`
	// Sees: what of the earlier steps is handed over: "" (nothing), "others"
	// (the other participants' latest answers), "all" (everyone's latest
	// answers) or "everything" (every step so far).
	Sees   string `json:"sees"`
	Prompt string `json:"prompt"`
	// Depth: the step runs from this depth up (0 quick, 1 standard, 2 deep).
	Depth int `json:"depth,omitempty"`
}

// PanelSeatSpec is who sits where: an agent, its model and the login it runs on.
type PanelSeatSpec struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	AccountID int64  `json:"account_id"`
	HostID    int64  `json:"host_id"`
	HostHome  string `json:"host_home"`
	Role      string `json:"role"`
}

// PanelSpec is how a panel runs, fixed when it starts.
type PanelSpec struct {
	Depth    int          `json:"depth"`     // 0 quick, 1 standard, 2 deep
	Stages   []PanelStage `json:"stages"`    // the steps that run (already cut to the depth)
	KindName string       `json:"kind_name"` // "Council", or a recipe's name
	RecipeID int64        `json:"recipe_id,omitempty"`
	Cwd      string       `json:"cwd"`            // the folder the participants look at
	HostID   int64        `json:"host_id"`        // the machine that folder is on
	LimitMin int          `json:"limit_min"`      // minutes one participant gets per step
	Quorum   int          `json:"quorum"`         // answers the first step needs
	Mode     string       `json:"mode,omitempty"` // build: the participants' permission mode ("" = their default)
	App      string       `json:"app,omitempty"`  // the Pocket app that started it over a Core link
	// Access: what the participants may do without asking: "" only look, "auto"
	// each agent's own automatic mode, "full" never asks.
	Access string `json:"access,omitempty"`
	// EditorKnows: the version the editor (the summary writer) last wrote or was
	// handed; when the current one is another, it is told before it goes on.
	EditorKnows int  `json:"editor_knows,omitempty"`
	ChairSwap   bool `json:"chair_swap,omitempty"` // the summary writer was replaced once already
}

// Panel is one run.
type Panel struct {
	ID           string    `json:"id"`
	UserID       int64     `json:"-"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Question     string    `json:"question"`
	Material     string    `json:"material"`
	Spec         PanelSpec `json:"spec"`
	Status       string    `json:"status"`
	Stage        int       `json:"stage"`
	StageAt      string    `json:"stage_at"`
	Result       string    `json:"result"`
	Error        string    `json:"error"`
	FromThreadID string    `json:"from_thread_id"`
	Report       bool      `json:"report"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
	FinishedAt   string    `json:"finished_at"`
}

// PanelSeat is one participant of a panel (or its summary writer).
type PanelSeat struct {
	ID      int64  `json:"id"`
	PanelID string `json:"panel_id"`
	Idx     int    `json:"idx"`
	Chair   bool   `json:"chair"`
	Label   string `json:"label"`
	PanelSeatSpec
	ThreadID string `json:"thread_id"`
	Asked    int    `json:"asked"`
	State    string `json:"state"`
	Error    string `json:"error"`
}

// PanelAnswer is what one participant said in one step.
type PanelAnswer struct {
	ID        int64  `json:"id"`
	SeatID    int64  `json:"seat_id"`
	Stage     int    `json:"stage"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

const panelCols = `id, user_id, kind, title, question, material, spec, status, stage, stage_at, result, error, from_thread_id, report, created_at, updated_at, finished_at`

func scanPanel(row interface{ Scan(...any) error }) (*Panel, error) {
	var p Panel
	var spec string
	var report int
	if err := row.Scan(&p.ID, &p.UserID, &p.Kind, &p.Title, &p.Question, &p.Material, &spec, &p.Status, &p.Stage, &p.StageAt, &p.Result, &p.Error,
		&p.FromThreadID, &report, &p.CreatedAt, &p.UpdatedAt, &p.FinishedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal([]byte(spec), &p.Spec)
	p.Report = report != 0
	return &p, nil
}

func (s *Store) CreatePanel(ctx context.Context, p *Panel, seats []*PanelSeat) error {
	spec, _ := json.Marshal(p.Spec)
	now := Now()
	p.CreatedAt, p.UpdatedAt, p.StageAt = now, now, now
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO panels (`+panelCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.UserID, p.Kind, p.Title, p.Question, p.Material, string(spec), p.Status, p.Stage, p.StageAt, p.Result, p.Error,
		p.FromThreadID, boolInt(p.Report), p.CreatedAt, p.UpdatedAt, p.FinishedAt); err != nil {
		return err
	}
	for _, st := range seats {
		st.PanelID = p.ID
		res, err := tx.ExecContext(ctx, `INSERT INTO panel_seats (panel_id, idx, chair, label, provider, model, effort, account_id, host_id, host_home, role, thread_id, asked, state, error)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, st.PanelID, st.Idx, boolInt(st.Chair), st.Label, st.Provider, st.Model, st.Effort, st.AccountID, st.HostID, st.HostHome,
			st.Role, st.ThreadID, st.Asked, st.State, st.Error)
		if err != nil {
			return err
		}
		st.ID, _ = res.LastInsertId()
	}
	return tx.Commit()
}

// AddPanelSeat adds a participant to a running panel (a replacement summary writer).
func (s *Store) AddPanelSeat(ctx context.Context, st *PanelSeat) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO panel_seats (panel_id, idx, chair, label, provider, model, effort, account_id, host_id, host_home, role, thread_id, asked, state, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, st.PanelID, st.Idx, boolInt(st.Chair), st.Label, st.Provider, st.Model, st.Effort, st.AccountID, st.HostID, st.HostHome,
		st.Role, st.ThreadID, st.Asked, st.State, st.Error)
	if err != nil {
		return err
	}
	st.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) GetPanel(ctx context.Context, userID int64, id string) (*Panel, error) {
	return scanPanel(s.DB.QueryRowContext(ctx, `SELECT `+panelCols+` FROM panels WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) GetPanelAny(ctx context.Context, id string) (*Panel, error) {
	return scanPanel(s.DB.QueryRowContext(ctx, `SELECT `+panelCols+` FROM panels WHERE id = ?`, id))
}

func (s *Store) ListPanels(ctx context.Context, userID int64, limit int) ([]*Panel, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.panels(ctx, `SELECT `+panelCols+` FROM panels WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`, userID, limit)
}

// RunningPanels are the panels that were at work when the server stopped.
func (s *Store) RunningPanels(ctx context.Context) ([]*Panel, error) {
	return s.panels(ctx, `SELECT `+panelCols+` FROM panels WHERE status = 'running' ORDER BY created_at`)
}

// PanelsFrom lists the panels started from a conversation, newest first.
func (s *Store) PanelsFrom(ctx context.Context, threadID string) ([]*Panel, error) {
	return s.panels(ctx, `SELECT `+panelCols+` FROM panels WHERE from_thread_id = ? ORDER BY created_at DESC LIMIT 50`, threadID)
}

func (s *Store) panels(ctx context.Context, q string, args ...any) ([]*Panel, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Panel{}
	for rows.Next() {
		p, err := scanPanel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePanel writes the fields that change while a panel runs.
func (s *Store) SavePanel(ctx context.Context, p *Panel) error {
	spec, _ := json.Marshal(p.Spec)
	p.UpdatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `UPDATE panels SET spec = ?, status = ?, stage = ?, stage_at = ?, result = ?, error = ?, report = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
		string(spec), p.Status, p.Stage, p.StageAt, p.Result, p.Error, boolInt(p.Report), p.UpdatedAt, p.FinishedAt, p.ID)
	return err
}

// RenamePanel gives a panel another title. was: only when it is still called
// that ("" = whatever it is called), so a name the person typed is not
// replaced by one found for them.
func (s *Store) RenamePanel(ctx context.Context, userID int64, id, title, was string) (bool, error) {
	q, args := `UPDATE panels SET title = ?, updated_at = ? WHERE id = ? AND user_id = ?`, []any{title, Now(), id, userID}
	if was != "" {
		q += ` AND title = ?`
		args = append(args, was)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) DeletePanel(ctx context.Context, userID int64, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM panels WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM panel_seats WHERE panel_id = ?`, id)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM panel_answers WHERE panel_id = ?`, id)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM panel_versions WHERE panel_id = ?`, id)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM panel_chat WHERE panel_id = ?`, id)
	return nil
}

func (s *Store) PanelSeats(ctx context.Context, panelID string) ([]*PanelSeat, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, panel_id, idx, chair, label, provider, model, effort, account_id, host_id, host_home, role, thread_id, asked, state, error
		FROM panel_seats WHERE panel_id = ? ORDER BY chair, idx, id`, panelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelSeat{}
	for rows.Next() {
		var st PanelSeat
		var chair int
		if err := rows.Scan(&st.ID, &st.PanelID, &st.Idx, &chair, &st.Label, &st.Provider, &st.Model, &st.Effort, &st.AccountID, &st.HostID, &st.HostHome,
			&st.Role, &st.ThreadID, &st.Asked, &st.State, &st.Error); err != nil {
			return nil, err
		}
		st.Chair = chair != 0
		out = append(out, &st)
	}
	return out, rows.Err()
}

// PanelSeatMeta is a seat with its thread usage and host name joined in one query
// for list views (avoiding per-panel/per-seat N+1 lookups).
type PanelSeatMeta struct {
	Seat        *PanelSeat
	Usage       Usage
	MachineName string
	HasHost     bool
}

// PanelSeatsFor loads seats, thread usage and host names for many panels in one query.
func (s *Store) PanelSeatsFor(ctx context.Context, panelIDs []string) (map[string][]PanelSeatMeta, error) {
	out := make(map[string][]PanelSeatMeta, len(panelIDs))
	if len(panelIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(panelIDs))
	args := make([]any, len(panelIDs))
	for i, id := range panelIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT ps.id, ps.panel_id, ps.idx, ps.chair, ps.label, ps.provider, ps.model, ps.effort, ps.account_id, ps.host_id, ps.host_home, ps.role, ps.thread_id, ps.asked, ps.state, ps.error,
		COALESCE(t.usage, '{}'), h.name
		FROM panel_seats ps
		LEFT JOIN threads t ON t.id = ps.thread_id AND ps.thread_id != ''
		LEFT JOIN hosts h ON h.id = ps.host_id AND ps.host_id != 0
		WHERE ps.panel_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY ps.panel_id, ps.chair, ps.idx, ps.id`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st PanelSeat
		var chair int
		var usage string
		var hostName sql.NullString
		if err := rows.Scan(&st.ID, &st.PanelID, &st.Idx, &chair, &st.Label, &st.Provider, &st.Model, &st.Effort, &st.AccountID, &st.HostID, &st.HostHome,
			&st.Role, &st.ThreadID, &st.Asked, &st.State, &st.Error, &usage, &hostName); err != nil {
			return nil, err
		}
		st.Chair = chair != 0
		m := PanelSeatMeta{Seat: &st, MachineName: hostName.String, HasHost: hostName.Valid}
		_ = json.Unmarshal([]byte(usage), &m.Usage)
		out[st.PanelID] = append(out[st.PanelID], m)
	}
	return out, rows.Err()
}

func (s *Store) SavePanelSeat(ctx context.Context, st *PanelSeat) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE panel_seats SET model = ?, thread_id = ?, asked = ?, state = ?, error = ? WHERE id = ?`,
		st.Model, st.ThreadID, st.Asked, st.State, st.Error, st.ID)
	return err
}

// PanelOfThread names the panel a thread takes part in ("" = none).
func (s *Store) PanelOfThread(ctx context.Context, threadID string) string {
	var id string
	_ = s.DB.QueryRowContext(ctx, `SELECT panel_id FROM panel_seats WHERE thread_id = ? LIMIT 1`, threadID).Scan(&id)
	return id
}

func (s *Store) AddPanelAnswer(ctx context.Context, panelID string, seatID int64, stage int, text string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO panel_answers (panel_id, seat_id, stage, text, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(panel_id, seat_id, stage) DO UPDATE SET text = excluded.text, created_at = excluded.created_at`, panelID, seatID, stage, text, Now())
	return err
}

func (s *Store) PanelAnswers(ctx context.Context, panelID string) ([]*PanelAnswer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, seat_id, stage, text, created_at FROM panel_answers WHERE panel_id = ? ORDER BY stage, seat_id`, panelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelAnswer{}
	for rows.Next() {
		var a PanelAnswer
		if err := rows.Scan(&a.ID, &a.SeatID, &a.Stage, &a.Text, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ---------- saved line-ups ----------

// PanelLineup is a saved set of participants.
type PanelLineup struct {
	ID        int64           `json:"id"`
	UserID    int64           `json:"-"`
	Name      string          `json:"name"`
	Seats     []PanelSeatSpec `json:"seats"`
	Chair     *PanelSeatSpec  `json:"chair"`
	Default   bool            `json:"default"`
	Access    string          `json:"access"` // "" | auto | full: what its participants may do without asking
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func (s *Store) ListPanelLineups(ctx context.Context, userID int64) ([]*PanelLineup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, seats, chair, is_default, access, created_at, updated_at FROM panel_lineups WHERE user_id = ? ORDER BY is_default DESC, name COLLATE NOCASE, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelLineup{}
	for rows.Next() {
		var l PanelLineup
		var seats, chair string
		var def int
		if err := rows.Scan(&l.ID, &l.UserID, &l.Name, &seats, &chair, &def, &l.Access, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(seats), &l.Seats)
		var c PanelSeatSpec
		if json.Unmarshal([]byte(chair), &c) == nil && c.Provider != "" {
			l.Chair = &c
		}
		if l.Seats == nil {
			l.Seats = []PanelSeatSpec{}
		}
		l.Default = def != 0
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (s *Store) SavePanelLineup(ctx context.Context, l *PanelLineup) error {
	seats, _ := json.Marshal(l.Seats)
	chair := []byte("{}")
	if l.Chair != nil {
		chair, _ = json.Marshal(l.Chair)
	}
	now := Now()
	if l.Default {
		if _, err := s.DB.ExecContext(ctx, `UPDATE panel_lineups SET is_default = 0 WHERE user_id = ?`, l.UserID); err != nil {
			return err
		}
	}
	if l.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO panel_lineups (user_id, name, seats, chair, is_default, access, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
			l.UserID, l.Name, string(seats), string(chair), boolInt(l.Default), l.Access, now, now)
		if err != nil {
			return err
		}
		l.ID, _ = res.LastInsertId()
		l.CreatedAt, l.UpdatedAt = now, now
		return nil
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE panel_lineups SET name = ?, seats = ?, chair = ?, is_default = ?, access = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		l.Name, string(seats), string(chair), boolInt(l.Default), l.Access, now, l.ID, l.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	l.UpdatedAt = now
	return nil
}

func (s *Store) DeletePanelLineup(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM panel_lineups WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- a person's own kinds of panel ----------

// PanelRecipe is a person's own kind of panel.
type PanelRecipe struct {
	ID          int64        `json:"id"`
	UserID      int64        `json:"-"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Stages      []PanelStage `json:"stages"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

func scanPanelRecipe(row interface{ Scan(...any) error }) (*PanelRecipe, error) {
	var r PanelRecipe
	var stages string
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &r.Description, &stages, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal([]byte(stages), &r.Stages)
	if r.Stages == nil {
		r.Stages = []PanelStage{}
	}
	return &r, nil
}

func (s *Store) ListPanelRecipes(ctx context.Context, userID int64) ([]*PanelRecipe, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, description, stages, created_at, updated_at FROM panel_recipes WHERE user_id = ? ORDER BY name COLLATE NOCASE, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelRecipe{}
	for rows.Next() {
		r, err := scanPanelRecipe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetPanelRecipe(ctx context.Context, userID, id int64) (*PanelRecipe, error) {
	return scanPanelRecipe(s.DB.QueryRowContext(ctx, `SELECT id, user_id, name, description, stages, created_at, updated_at FROM panel_recipes WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) SavePanelRecipe(ctx context.Context, r *PanelRecipe) error {
	stages, _ := json.Marshal(r.Stages)
	now := Now()
	if r.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO panel_recipes (user_id, name, description, stages, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			r.UserID, r.Name, r.Description, string(stages), now, now)
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		r.CreatedAt, r.UpdatedAt = now, now
		return nil
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE panel_recipes SET name = ?, description = ?, stages = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		r.Name, r.Description, string(stages), now, r.ID, r.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	r.UpdatedAt = now
	return nil
}

func (s *Store) DeletePanelRecipe(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM panel_recipes WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- versions of what a panel works on ----------

// PanelVersion is one version of the plan or text a panel works on.
type PanelVersion struct {
	ID        int64  `json:"id"`
	PanelID   string `json:"-"`
	N         int    `json:"n"`
	Text      string `json:"text"`
	Source    string `json:"source"` // original | rewrite | chat | edit | restore
	Note      string `json:"note"`
	Stage     int    `json:"stage"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) PanelVersions(ctx context.Context, panelID string) ([]*PanelVersion, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, panel_id, n, text, source, note, stage, created_at FROM panel_versions WHERE panel_id = ? ORDER BY n`, panelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelVersion{}
	for rows.Next() {
		var v PanelVersion
		if err := rows.Scan(&v.ID, &v.PanelID, &v.N, &v.Text, &v.Source, &v.Note, &v.Stage, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// AddPanelVersion appends a version (its number is the next one). A step
// that already wrote a version does not write a second one.
func (s *Store) AddPanelVersion(ctx context.Context, v *PanelVersion) error {
	if v.Stage >= 0 {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT n FROM panel_versions WHERE panel_id = ? AND stage = ? LIMIT 1`, v.PanelID, v.Stage).Scan(&n); err == nil {
			v.N = n
			return nil
		}
	}
	v.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO panel_versions (panel_id, n, text, source, note, stage, created_at)
		VALUES (?, (SELECT COALESCE(MAX(n), 0) + 1 FROM panel_versions WHERE panel_id = ?), ?, ?, ?, ?, ?)`,
		v.PanelID, v.PanelID, v.Text, v.Source, v.Note, v.Stage, v.CreatedAt)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return s.DB.QueryRowContext(ctx, `SELECT n FROM panel_versions WHERE id = ?`, v.ID).Scan(&v.N)
}

// PanelChatMessage is one message of the person's chat with a panel's editor.
type PanelChatMessage struct {
	ID        int64  `json:"id"`
	PanelID   string `json:"-"`
	Role      string `json:"role"` // person | editor
	Text      string `json:"text"`
	Proposal  string `json:"proposal,omitempty"`
	VersionN  int    `json:"version_n,omitempty"`
	State     string `json:"state,omitempty"` // editor: working | done | failed
	CreatedAt string `json:"created_at"`
}

func (s *Store) PanelChat(ctx context.Context, panelID string) ([]*PanelChatMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, panel_id, role, text, proposal, version_n, state, created_at FROM panel_chat WHERE panel_id = ? ORDER BY id`, panelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PanelChatMessage{}
	for rows.Next() {
		var m PanelChatMessage
		if err := rows.Scan(&m.ID, &m.PanelID, &m.Role, &m.Text, &m.Proposal, &m.VersionN, &m.State, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *Store) AddPanelChat(ctx context.Context, m *PanelChatMessage) error {
	m.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO panel_chat (panel_id, role, text, proposal, version_n, state, created_at) VALUES (?,?,?,?,?,?,?)`,
		m.PanelID, m.Role, m.Text, m.Proposal, m.VersionN, m.State, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) SavePanelChat(ctx context.Context, m *PanelChatMessage) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE panel_chat SET text = ?, proposal = ?, version_n = ?, state = ? WHERE id = ?`, m.Text, m.Proposal, m.VersionN, m.State, m.ID)
	return err
}

// PanelsByTitle finds a person's panels whose title contains q, newest first.
func (s *Store) PanelsByTitle(ctx context.Context, userID int64, q string, limit int) ([]*Panel, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.panels(ctx, `SELECT `+panelCols+` FROM panels WHERE user_id = ? AND title LIKE ? ESCAPE '\' ORDER BY updated_at DESC LIMIT ?`, userID, "%"+likeEscape(q)+"%", limit)
}
