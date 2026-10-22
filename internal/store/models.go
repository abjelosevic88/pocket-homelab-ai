package store

import "encoding/json"

// User is an account. Data is strictly per person; nobody, admins included,
// sees another person's threads.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`    // admin | member
	CanRun       bool   `json:"can_run"` // members only: may start agents
	CreatedAt    string `json:"created_at"`
}

// MayRun reports whether the person may start agents on this server.
func (u *User) MayRun() bool { return u != nil && (u.Role == "admin" || u.CanRun) }

// MarshalJSON reports can_run as what the person may do (admins always may).
func (u User) MarshalJSON() ([]byte, error) {
	type plain User
	p := plain(u)
	p.CanRun = u.MayRun()
	return json.Marshal(p)
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Workspace is a folder agents start in.
type Workspace struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"-"`
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	Color     string  `json:"color"`
	SortOrder float64 `json:"sort_order"`
	CreatedAt string  `json:"created_at"`
	Threads   int     `json:"threads"`
}

// Usage adds up what a thread cost.
type Usage struct {
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	CachedTokens  int64   `json:"cached_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	ContextTokens int64   `json:"context_tokens"` // how full the context window is now
	ContextWindow int64   `json:"context_window"`
	Turns         int     `json:"turns"`
}

// Thread is one conversation with one agent.
type Thread struct {
	NoMemory          bool   `json:"no_memory"`
	ID                string `json:"id"`
	UserID            int64  `json:"owner_id"`
	Title             string `json:"title"`
	Provider          string `json:"provider"`
	AccountID         int64  `json:"account_id"` // 0 = the server's own login
	Model             string `json:"model"`
	Effort            string `json:"effort"`
	Mode              string `json:"mode"`
	Speed             string `json:"speed"`
	Cwd               string `json:"cwd"`
	WorkspaceID       *int64 `json:"workspace_id"`
	ProviderSessionID string `json:"provider_session_id"`
	LogOffset         int64  `json:"-"`
	Status            string `json:"status"` // idle | running | waiting | error
	Error             string `json:"error"`
	Pinned            bool   `json:"pinned"`
	Archived          bool   `json:"archived"`
	Unread            bool   `json:"unread"`
	Source            string `json:"source"` // web | recipe | schedule | import | api
	SourceRef         string `json:"source_ref"`
	Preview           string `json:"preview"`
	LastPrompt        string `json:"last_prompt"`
	PromptAt          string `json:"prompt_at"`
	Usage             Usage  `json:"usage"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	ActivityAt        string `json:"activity_at"`

	// Live state, filled by the runtime.
	Live    bool `json:"live"`    // a CLI process is attached
	Pending int  `json:"pending"` // requests waiting for an answer
	Queued  int  `json:"queued"`  // messages waiting for the turn to end
	// External: the session is being driven somewhere else right now (a
	// terminal, another app) and Pocket AI is following its log.
	External bool `json:"external"`
	// Subagents at work right now (background sub-agents of a live session).
	Subagents any `json:"subagents,omitempty"`
	// Sharing: who else may open it, and whose it is.
	Shares    []ThreadShare `json:"shares,omitempty"`
	OwnerName string        `json:"owner_name,omitempty"`
}

// Item is one stored timeline entry; Data is the JSON the runtime wrote.
type Item struct {
	ID        int64           `json:"id"`
	ThreadID  string          `json:"thread_id"`
	Key       string          `json:"key"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	Body      string          `json:"-"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// Upload is a file attached to a message.
type Upload struct {
	ExtractedText string  `json:"-"`
	ID            string  `json:"id"`
	UserID        int64   `json:"-"`
	ThreadID      *string `json:"thread_id"`
	Name          string  `json:"name"`
	Mime          string  `json:"mime"`
	Size          int64   `json:"size"`
	Path          string  `json:"-"`
	CreatedAt     string  `json:"created_at"`
}

// Recipe is a saved prompt.
type Recipe struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"-"`
	Name      string  `json:"name"`
	Prompt    string  `json:"prompt"`
	Icon      string  `json:"icon"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Effort    string  `json:"effort"`
	Mode      string  `json:"mode"`
	Speed     string  `json:"speed"`
	Cwd       string  `json:"cwd"`
	Pinned    bool    `json:"pinned"`
	SortOrder float64 `json:"sort_order"`
	Runs      int     `json:"runs"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// Schedule runs a prompt on its own.
type Schedule struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"-"`
	Name         string `json:"name"`
	Prompt       string `json:"prompt"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	Mode         string `json:"mode"`
	Speed        string `json:"speed"`
	Cwd          string `json:"cwd"`
	Spec         string `json:"spec"`
	Notify       string `json:"notify"`
	Enabled      bool   `json:"enabled"`
	NextRunAt    string `json:"next_run_at"`
	LastRunAt    string `json:"last_run_at"`
	LastThreadID string `json:"last_thread_id"`
	LastStatus   string `json:"last_status"`
	LastSummary  string `json:"last_summary"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// Turn is one finished agent turn (usage page).
type Turn struct {
	UserID       int64
	ThreadID     string
	Provider     string
	Model        string
	OK           bool
	DurationMS   int64
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	CostUSD      float64
}
