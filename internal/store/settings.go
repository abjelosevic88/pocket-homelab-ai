package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// DefaultTitle is the app title until someone changes it.
const DefaultTitle = "Pocket AI"

// AgentDefaults are a person's defaults for one provider.
type AgentDefaults struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Mode   string `json:"mode"`
	Speed  string `json:"speed"`
	// Account is the login new threads use (0 = the server's own).
	Account int64 `json:"account_id"`
}

// Settings are the box settings plus one person's preferences.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	AppModel        string        `json:"app_model"`        // empty follows Claude defaults
	DefaultProvider string        `json:"default_provider"` // claude | codex
	DefaultCwd      string        `json:"default_cwd"`
	Claude          AgentDefaults `json:"claude"`
	Codex           AgentDefaults `json:"codex"`
	SystemPrompt    string        `json:"system_prompt"`

	NotifyKind     string `json:"notify_kind"` // apprise | ntfy | webhook
	NotifyURL      string `json:"notify_url"`
	NotifyToken    string `json:"-"`
	NotifyFinished bool   `json:"notify_finished"`
	NotifyInput    bool   `json:"notify_input"`
	NotifyFailed   bool   `json:"notify_failed"`
	NotifyOnlyAway bool   `json:"notify_only_away"`

	EnterSends   bool `json:"enter_sends"`
	ShowThinking bool `json:"show_thinking"`

	// The plan-limit panel in the sidebar, and which limits it shows
	// ("provider:window id"; empty = all of them).
	UsageWidget bool     `json:"usage_widget"`
	UsagePinned []string `json:"usage_pinned"`

	// Open threads as tabs along the top (the tabs themselves live in the
	// browser).
	ThreadTabs bool `json:"thread_tabs"`
}

func defaults() Settings {
	return Settings{
		Title: DefaultTitle, DefaultProvider: "claude",
		Claude:     AgentDefaults{Model: "default", Effort: "high", Mode: "default"},
		Codex:      AgentDefaults{Model: "", Effort: "high", Mode: "on-request"},
		NotifyKind: "apprise", NotifyFinished: true, NotifyInput: true, NotifyFailed: true, NotifyOnlyAway: true,
		EnterSends:  true,
		UsageWidget: true,
		ThreadTabs:  true,
	}
}

// GetSettings loads the box settings plus the preferences of userID, with
// defaults applied. userID 0 gives the box settings with default preferences.
func (s *Store) GetSettings(ctx context.Context, userID int64) (Settings, error) {
	st := defaults()
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN ('title', 'setup_complete')
		UNION ALL SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		switch k {
		case "title":
			if v != "" {
				st.Title = v
			}
		case "setup_complete":
			st.SetupComplete = v == "1"
		case "app_model":
			st.AppModel = v
		case "default_provider":
			st.DefaultProvider = v
		case "default_cwd":
			st.DefaultCwd = v
		case "claude.model":
			st.Claude.Model = v
		case "claude.effort":
			st.Claude.Effort = v
		case "claude.mode":
			st.Claude.Mode = v
		case "claude.speed":
			st.Claude.Speed = v
		case "codex.speed":
			st.Codex.Speed = v
		case "claude.account":
			st.Claude.Account, _ = strconv.ParseInt(v, 10, 64)
		case "codex.account":
			st.Codex.Account, _ = strconv.ParseInt(v, 10, 64)
		case "codex.model":
			st.Codex.Model = v
		case "codex.effort":
			st.Codex.Effort = v
		case "codex.mode":
			st.Codex.Mode = v
		case "system_prompt":
			st.SystemPrompt = v
		case "notify_kind":
			st.NotifyKind = v
		case "notify_url":
			st.NotifyURL = v
		case "notify_token":
			st.NotifyToken = v
		case "notify_finished":
			st.NotifyFinished = v != "0"
		case "notify_input":
			st.NotifyInput = v != "0"
		case "notify_failed":
			st.NotifyFailed = v != "0"
		case "notify_only_away":
			st.NotifyOnlyAway = v != "0"
		case "enter_sends":
			st.EnterSends = v != "0"
		case "show_thinking":
			st.ShowThinking = v == "1"
		case "usage_widget":
			st.UsageWidget = v != "0"
		case "thread_tabs":
			st.ThreadTabs = v != "0"
		case "usage_pinned":
			st.UsagePinned = splitList(v)
		}
	}
	return st, rows.Err()
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// SaveSettings stores a person's preferences (not the notify token).
func (s *Store) SaveSettings(ctx context.Context, userID int64, st Settings) error {
	return s.putUserSettings(ctx, userID, map[string]string{
		"app_model": st.AppModel, "default_provider": st.DefaultProvider, "default_cwd": st.DefaultCwd,
		"claude.model": st.Claude.Model, "claude.effort": st.Claude.Effort, "claude.mode": st.Claude.Mode,
		"codex.model": st.Codex.Model, "codex.effort": st.Codex.Effort, "codex.mode": st.Codex.Mode,
		"claude.speed": st.Claude.Speed, "codex.speed": st.Codex.Speed,
		"claude.account": strconv.FormatInt(st.Claude.Account, 10), "codex.account": strconv.FormatInt(st.Codex.Account, 10),
		"system_prompt": st.SystemPrompt,
		"notify_kind":   st.NotifyKind, "notify_url": st.NotifyURL, "notify_finished": b2s(st.NotifyFinished),
		"notify_input": b2s(st.NotifyInput), "notify_failed": b2s(st.NotifyFailed), "notify_only_away": b2s(st.NotifyOnlyAway),
		"enter_sends": b2s(st.EnterSends), "show_thinking": b2s(st.ShowThinking),
		"usage_widget": b2s(st.UsageWidget), "thread_tabs": b2s(st.ThreadTabs), "usage_pinned": strings.Join(st.UsagePinned, ","),
	})
}

func splitList(v string) []string {
	out := []string{}
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// UserSetting reads one raw key of a person's settings ("" when missing).
func (s *Store) UserSetting(ctx context.Context, userID int64, key string) string {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	return v
}

// SetUserSetting stores one key for a person.
func (s *Store) SetUserSetting(ctx context.Context, userID int64, key, value string) error {
	return s.putUserSettings(ctx, userID, map[string]string{key: value})
}

// SaveBoxSettings stores the settings of the box itself: title and setup state.
func (s *Store) SaveBoxSettings(ctx context.Context, st Settings) error {
	kv := map[string]string{"title": st.Title, "setup_complete": b2s(st.SetupComplete)}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) putUserSettings(ctx context.Context, userID int64, kv map[string]string) error {
	if userID <= 0 {
		return ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
			ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OwnerID is the account the box was set up with (the first user): keys
// given to the whole box through the environment are theirs.
func (s *Store) OwnerID(ctx context.Context) int64 {
	var id sql.NullInt64
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(id) FROM users`).Scan(&id)
	return id.Int64
}

// UserIDs lists every user id.
func (s *Store) UserIDs(ctx context.Context) []int64 {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// BoxSetting reads one box-wide key ("" when missing).
func (s *Store) BoxSetting(ctx context.Context, key string) string {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

// SetBoxSetting stores one box-wide key.
func (s *Store) SetBoxSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
