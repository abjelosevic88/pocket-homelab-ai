// Package agent drives the coding-agent CLIs installed on the server —
// Claude Code (stream-json over stdio) and Codex (app-server JSON-RPC over
// stdio) — and turns what they say into one timeline shape.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Item is one timeline entry. Key is stable per thread: streaming updates
// and the final version of a message replace the entry with the same key.
type Item struct {
	Key    string  `json:"key"`
	Kind   string  `json:"kind"` // user | assistant | thinking | tool | todos | request | notice | turn
	Text   string  `json:"text,omitempty"`
	Images []Image `json:"images,omitempty"`
	Files  []Image `json:"files,omitempty"`
	// LogImages counts images that live only in the CLI's session file (a
	// screenshot pasted in Paseo); the web app fetches them by item key.
	LogImages int `json:"log_images,omitempty"`
	// Unsent: it was waiting in the queue when Pocket AI stopped, so it never
	// reached the agent.
	Unsent bool `json:"unsent,omitempty"`
	// UnsentWhy says why, when it is not a restart ("" = Pocket AI restarted).
	UnsentWhy string `json:"unsent_why,omitempty"`
	// By: who sent a message in a shared thread (person id and name).
	ByID    int64     `json:"by_id,omitempty"`
	By      string    `json:"by,omitempty"`
	Queued  bool      `json:"queued,omitempty"`
	Tool    *Tool     `json:"tool,omitempty"`
	Todos   []Todo    `json:"todos,omitempty"`
	Request *Request  `json:"request,omitempty"`
	Level   string    `json:"level,omitempty"` // notice: info | warn | error
	Fix     string    `json:"fix,omitempty"`   // notice: what the page offers to put it right ("folder": move the conversation)
	Turn    *TurnInfo `json:"turn,omitempty"`
}

// Image is an attachment shown on a user message.
type Image struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Tool is one tool call and what came back.
type Tool struct {
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	Kind     string          `json:"kind"` // bash | edit | write | read | search | web | task | mcp | other
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`
	Status   string          `json:"status"` // running | done | error | denied
	ExitCode *int            `json:"exit_code,omitempty"`
	Diff     string          `json:"diff,omitempty"`
	Images   int             `json:"images,omitempty"` // images in the result (Read of a picture), served from the session file
}

// Todo is one line of the agent's plan / to-do list.
type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status"` // pending | in_progress | completed
}

// Question is one question the agent asks (Claude AskUserQuestion, Codex request_user_input).
type Question struct {
	ID       string   `json:"id"`
	Header   string   `json:"header"`
	Question string   `json:"question"`
	Multi    bool     `json:"multi"`
	Options  []Option `json:"options"`
	Other    bool     `json:"other"`
}

// Option is one answer offered for a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Request is something the agent waits on: a permission, questions, or a plan to approve.
type Request struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"` // permission | question | plan
	ToolName        string          `json:"tool_name,omitempty"`
	Title           string          `json:"title"`
	Input           json.RawMessage `json:"input,omitempty"`
	Questions       []Question      `json:"questions,omitempty"`
	Plan            string          `json:"plan,omitempty"`
	CanAllowSession bool            `json:"can_allow_session"`
	Status          string          `json:"status"` // pending | allowed | denied | answered | expired
	Answer          any             `json:"answer,omitempty"`
	// ForID / For: a Pocket app change in a shared thread waits for this
	// person (their apps); only they may answer it.
	ForID int64  `json:"for_id,omitempty"`
	For   string `json:"for,omitempty"`
	// App / Kind: a Pocket app change (kind add or change), so the card can
	// offer "always allow"; Auto names the rule that allowed it without asking.
	App  string `json:"app,omitempty"`
	Kind string `json:"kind,omitempty"`
	Auto string `json:"auto,omitempty"`
}

// TurnInfo summarises a finished turn.
type TurnInfo struct {
	ID           string  `json:"id,omitempty"` // the CLI's own turn id, when it has one (Codex)
	OK           bool    `json:"ok"`
	DurationMS   int64   `json:"duration_ms"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Error        string  `json:"error,omitempty"`
	Interrupted  bool    `json:"interrupted,omitempty"`
}

// Subagent is a sub-agent at work.
type Subagent struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Activity    string `json:"activity,omitempty"` // what it is doing now
	Type        string `json:"type,omitempty"`
	StartedAt   string `json:"started_at"`
	ToolUses    int    `json:"tool_uses,omitempty"`
}

// Task is background work of a session that is not a sub-agent: a shell
// command put in the background, or a monitor waiting for something.
type Task struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // shell, monitor
	Description string `json:"description"`
	StartedAt   string `json:"started_at"`
}

// Context is how full the model's context window is.
type Context struct {
	Tokens int64
	Window int64
	// Cached: this was a model call, so the provider's prompt cache was used
	// (and kept warm) just now. CacheTTL is how long that cache lives in
	// seconds when the call said so (Claude: 300 or 3600), 0 = unknown.
	Cached   bool
	CacheTTL int64
}

// Window is one rate-limit window of a provider account.
type Window struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Used     float64 `json:"used"` // 0..1
	ResetsAt string  `json:"resets_at,omitempty"`
}

// Response is the person's answer to a Request.
type Response struct {
	Decision string              `json:"decision"` // allow | allow_session | deny
	Message  string              `json:"message,omitempty"`
	Answers  map[string][]string `json:"answers,omitempty"`
}

// Input is one message sent to the agent.
type Input struct {
	Text   string
	Images []ImageFile
}

// ImageFile is an image on disk to hand to the agent.
type ImageFile struct {
	Path string
	Mime string
}

// Sink receives what an agent session does. Calls come from one goroutine
// per session, in order.
type Sink interface {
	Item(it Item)
	Delta(key, field, text string)
	SessionID(id string)
	Context(c Context)
	Limits(provider string, w []Window)
	Request(r Request)
	TurnStarted()
	TurnDone(t TurnInfo)
	// Subagents is the full list of sub-agents working right now (Claude
	// runs them in the background; they can outlive the turn that started them).
	Subagents(list []Subagent)
	// Tasks is the full list of background work other than sub-agents (shell
	// commands, monitors) running right now. It dies with the process, so the
	// thread must not be stopped while the list is not empty.
	Tasks(list []Task)
	Exited(err error)
}

// Options start a session.
type Options struct {
	Ephemeral    bool // do not persist one-shot app generation as a CLI conversation
	Restricted   bool // Pocket Ask: only the supplied MCP server, no built-in tools
	MCPConfig    string
	Bin          string
	Cwd          string
	Model        string
	Effort       string
	Mode         string
	Speed        string // "" standard, "fast" (Claude fast mode) or a Codex service tier id
	ResumeID     string // provider session to continue
	Fork         bool   // continue ResumeID under a new id
	SystemPrompt string
	Env          []string
	// Remote: run the CLI on another machine over ssh (nil = this server).
	Remote *Remote
	// Hold: run it under pocket-hold, so it outlives the connection (link.go).
	Hold *Hold
	Log  *slog.Logger
}

// Session is a live CLI process attached to one thread.
type Session interface {
	Send(in Input) error
	Interrupt() error
	Respond(requestID string, r Response) error
	// SetMode changes the permission mode of the live session (Claude at
	// once, Codex from its next turn).
	SetMode(mode string) error
	Close() error
	Done() <-chan struct{}
}

// RemoteInfo is where a session with Claude Remote Control on can be opened.
type RemoteInfo struct {
	SessionURL      string // https://claude.ai/code/session_…
	BridgeSessionID string // reattach to the same claude.ai session after a restart
}

// RemoteControllable is a session that can be reached from claude.ai/code
// and the Claude app (Claude Code's Remote Control). reattach is a
// BridgeSessionID from before ("" = a new claude.ai session).
type RemoteControllable interface {
	RemoteControl(ctx context.Context, on bool, name, reattach string) (RemoteInfo, error)
}

// BridgeSink is the optional part of a Sink told how the Remote Control link
// is doing (ready, connected, reconnecting, failed, …).
type BridgeSink interface {
	Bridge(state string)
}

// CancelSink is the optional part of a Sink told that the CLI withdrew a
// request (answered in the Claude app, or no longer needed).
type CancelSink interface {
	RequestCancelled(id string)
}

// ErrClosed is returned once the CLI process is gone.
var ErrClosed = errors.New("the agent process has exited")

// Provider describes one CLI.
type Provider struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Available     bool     `json:"available"`
	Version       string   `json:"version"`
	Error         string   `json:"error"`
	Models        []Model  `json:"models"`
	Efforts       []string `json:"efforts"`
	Modes         []Mode   `json:"modes"`
	Speeds        []Speed  `json:"speeds"`
	DefaultModel  string   `json:"default_model"`
	DefaultEffort string   `json:"default_effort"`
	DefaultMode   string   `json:"default_mode"`
	Account       string   `json:"account"`
}

// Model is one model a provider offers.
type Model struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Efforts     []string `json:"efforts"`
	Fast        bool     `json:"fast"` // the model has a faster speed
}

// Speed is a service speed. Available is false when the account cannot use
// it right now; Note says why.
type Speed struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Note        string `json:"note,omitempty"`
}

// Mode is a permission mode.
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ---------- helpers shared by the drivers ----------

// tail keeps the last n bytes written (stderr of a CLI, for error messages).
type tail struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if r, ok := v.(json.RawMessage); ok {
		return r
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func unixISO(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// simpleDiff renders an old→new replacement as a unified-looking diff.
func simpleDiff(path, oldS, newS string) string {
	var b strings.Builder
	if path != "" {
		b.WriteString("--- " + path + "\n+++ " + path + "\n")
	}
	if oldS != "" {
		for _, l := range strings.Split(strings.TrimRight(oldS, "\n"), "\n") {
			b.WriteString("-" + l + "\n")
		}
	}
	if newS != "" {
		for _, l := range strings.Split(strings.TrimRight(newS, "\n"), "\n") {
			b.WriteString("+" + l + "\n")
		}
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (" + itoa(len(s)-n) + " more bytes)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
