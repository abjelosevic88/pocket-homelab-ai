package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Run is one prompt and the agent's work on it, read from a CLI's own
// session file (so it covers Pocket AI, Paseo, the terminal and any app).
type Run struct {
	Provider     string   `json:"provider"`
	SessionID    string   `json:"session_id"`
	Key          string   `json:"key,omitempty"` // timeline item of the prompt (Claude: "u-<uuid>")
	Title        string   `json:"title"`         // the session's title
	Prompt       string   `json:"prompt"`
	Images       int      `json:"images,omitempty"`
	Cwd          string   `json:"cwd"`
	Origin       string   `json:"origin"` // pocket-ai | schedule | paseo | terminal | app
	Model        string   `json:"model,omitempty"`
	StartedAt    string   `json:"started_at"`
	EndedAt      string   `json:"ended_at,omitempty"`
	DurationMS   int64    `json:"duration_ms"`
	Status       string   `json:"status"` // running | done | interrupted | unfinished
	Tools        int      `json:"tools"`
	FollowUps    int      `json:"follow_ups,omitempty"` // messages sent while it worked
	OutputTokens int64    `json:"output_tokens,omitempty"`
	ThreadID     string   `json:"thread_id,omitempty"`
	Account      int64    `json:"account_id"`
	SharedWith   []string `json:"shared_with,omitempty"` // its Pocket AI thread is shared with them (server fills it)
}

// ServerSession is what "On this server" shows about one CLI session.
type ServerSession struct {
	Provider   string   `json:"provider"`
	SessionID  string   `json:"session_id"`
	Title      string   `json:"title"`
	LastPrompt string   `json:"last_prompt"`
	Cwd        string   `json:"cwd"`
	Origin     string   `json:"origin"`
	Model      string   `json:"model,omitempty"`
	StartedAt  string   `json:"started_at"`
	UpdatedAt  string   `json:"updated_at"`
	Runs       int      `json:"runs"`
	WorkMS     int64    `json:"work_ms"` // time the agent spent on its runs
	Running    bool     `json:"running"`
	RunningAt  string   `json:"running_at,omitempty"` // when the running prompt was sent
	ThreadID   string   `json:"thread_id"`
	Account    int64    `json:"account_id"`
	SharedWith []string `json:"shared_with,omitempty"` // its Pocket AI thread is shared with them (server fills it)
	Path       string   `json:"-"`
}

// sessionScan is the parsed state of one session file. Files only grow, so
// a later look reads just the lines added since (offset).
type sessionScan struct {
	provider  string
	sessionID string
	cwd       string
	title     string
	entry     string // Claude entrypoint ("cli" = terminal)
	origin    string // Codex originator
	model     string
	runs      []*Run
	tools     map[string]bool  // Claude tool_use ids seen (a message repeats per block)
	outTokens map[string]int64 // Claude message id → output tokens
	offset    int64
	mod       time.Time
}

var (
	scanMu    sync.Mutex
	scanCache = map[string]*sessionScan{}
)

func ts(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (sc *sessionScan) open() *Run {
	if n := len(sc.runs); n > 0 && sc.runs[n-1].EndedAt == "" && sc.runs[n-1].Status == "" {
		return sc.runs[n-1]
	}
	return nil
}

func (sc *sessionScan) end(r *Run, at, status string) {
	if r == nil {
		return
	}
	r.EndedAt, r.Status = at, status
	if a, b := ts(r.StartedAt), ts(at); !a.IsZero() && b.After(a) {
		r.DurationMS = b.Sub(a).Milliseconds()
	}
}

func (sc *sessionScan) feedClaude(line []byte) {
	var m struct {
		Type        string `json:"type"`
		UUID        string `json:"uuid"`
		Timestamp   string `json:"timestamp"`
		Cwd         string `json:"cwd"`
		IsMeta      bool   `json:"isMeta"`
		IsSidechain bool   `json:"isSidechain"`
		Entrypoint  string `json:"entrypoint"`
		Message     struct {
			ID         string          `json:"id"`
			Model      string          `json:"model"`
			Content    json.RawMessage `json:"content"`
			StopReason string          `json:"stop_reason"`
			Usage      struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Attachment struct {
			Type string `json:"type"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &m) != nil || m.IsSidechain {
		return
	}
	if sc.cwd == "" && m.Cwd != "" {
		sc.cwd = m.Cwd
	}
	if m.Entrypoint != "" {
		sc.entry = m.Entrypoint
	}
	switch m.Type {
	case "user":
		if m.IsMeta {
			return
		}
		var str string
		if json.Unmarshal(m.Message.Content, &str) == nil && strings.HasPrefix(strings.TrimSpace(str), "[Request interrupted") {
			sc.end(sc.open(), m.Timestamp, "interrupted")
			return
		}
		if bytes.Contains(m.Message.Content, []byte(`"tool_result"`)) {
			if interruptedResult(m.Message.Content) {
				sc.end(sc.open(), m.Timestamp, "interrupted")
			}
			return
		}
		t, n := userText(m.Message.Content), countImages(m.Message.Content)
		if t == "" && n == 0 {
			return
		}
		if r := sc.open(); r != nil { // the previous turn never ended
			sc.end(r, firstNonEmpty(r.EndedAt, m.Timestamp), "interrupted")
		}
		if sc.title == "" {
			sc.title = oneLine(t, 140)
		}
		sc.runs = append(sc.runs, &Run{Key: "u-" + m.UUID, Prompt: oneLine(t, 400), Images: n, StartedAt: m.Timestamp, Model: sc.model})
	case "attachment":
		if m.Attachment.Type == "queued_command" {
			if r := sc.lastRun(); r != nil {
				r.FollowUps++
			}
		}
	case "assistant":
		r := sc.lastRun()
		if m.Message.Model != "" && m.Message.Model != "<synthetic>" {
			sc.model = m.Message.Model
			if r != nil && r.Model == "" {
				r.Model = m.Message.Model
			}
		}
		if r == nil {
			return
		}
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal(m.Message.Content, &blocks)
		for _, b := range blocks {
			if b.Type == "tool_use" && !sc.tools[b.ID] {
				sc.tools[b.ID] = true
				r.Tools++
			}
		}
		if m.Message.ID != "" && m.Message.Usage.OutputTokens > sc.outTokens[m.Message.ID] {
			r.OutputTokens += m.Message.Usage.OutputTokens - sc.outTokens[m.Message.ID]
			sc.outTokens[m.Message.ID] = m.Message.Usage.OutputTokens
		}
		switch m.Message.StopReason {
		case "end_turn", "stop_sequence", "max_tokens", "refusal":
			// a turn can end more than once (background sub-agents report
			// back later): the last end counts
			r.Status = ""
			sc.end(r, m.Timestamp, "done")
		}
	}
}

// interruptedResult reports whether a tool-result message is Claude Code's
// "[Request interrupted by user…]" (not just output that mentions it).
func interruptedResult(content json.RawMessage) bool {
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" && strings.HasPrefix(strings.TrimSpace(toolResultText(b.Content)), "[Request interrupted") {
			return true
		}
	}
	return false
}

func (sc *sessionScan) lastRun() *Run {
	if n := len(sc.runs); n > 0 {
		return sc.runs[n-1]
	}
	return nil
}

func (sc *sessionScan) feedCodex(line []byte) {
	var m struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type       string          `json:"type"`
			ID         string          `json:"id"`
			Cwd        string          `json:"cwd"`
			Originator string          `json:"originator"`
			Model      string          `json:"model"`
			Role       string          `json:"role"`
			Message    string          `json:"message"`
			Content    json.RawMessage `json:"content"`
			DurationMS int64           `json:"duration_ms"`
			Info       struct {
				Last struct {
					OutputTokens int64 `json:"output_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	p := m.Payload
	r := sc.lastRun()
	prompt := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || strings.HasPrefix(t, "<") || r == nil {
			return
		}
		if r.Prompt == "" {
			r.Prompt = oneLine(t, 400)
			if sc.title == "" {
				sc.title = oneLine(t, 140)
			}
		} else if r.Status == "" && r.EndedAt == "" {
			r.FollowUps++
		}
	}
	switch m.Type {
	case "session_meta":
		sc.sessionID = firstNonEmpty(sc.sessionID, p.ID)
		sc.cwd = firstNonEmpty(sc.cwd, p.Cwd)
		sc.origin = p.Originator
	case "turn_context":
		if p.Model != "" {
			sc.model = p.Model
		}
	case "response_item":
		switch p.Type {
		case "message":
			if p.Role == "user" {
				var parts []struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(p.Content, &parts)
				var b []string
				for _, x := range parts {
					b = append(b, x.Text)
				}
				prompt(strings.Join(b, "\n"))
			}
		case "function_call", "custom_tool_call", "local_shell_call", "web_search_call":
			if r != nil {
				r.Tools++
			}
		}
	case "event_msg":
		switch p.Type {
		case "task_started":
			if o := sc.open(); o != nil {
				sc.end(o, m.Timestamp, "interrupted")
			}
			sc.runs = append(sc.runs, &Run{StartedAt: m.Timestamp, Model: sc.model})
		case "user_message":
			prompt(p.Message)
		case "token_count":
			if r != nil {
				r.OutputTokens += p.Info.Last.OutputTokens
			}
		case "task_complete":
			if o := sc.open(); o != nil {
				sc.end(o, m.Timestamp, "done")
				if p.DurationMS > 0 {
					o.DurationMS = p.DurationMS
				}
			}
		case "turn_aborted":
			sc.end(sc.open(), m.Timestamp, "interrupted")
		}
	}
}

// scanSession brings the cached scan of a session file up to date.
func scanSession(provider, path string, info os.FileInfo) *sessionScan {
	scanMu.Lock()
	sc := scanCache[path]
	scanMu.Unlock()
	if sc != nil && sc.offset == info.Size() && sc.mod.Equal(info.ModTime()) {
		return sc
	}
	if sc == nil || info.Size() < sc.offset {
		sc = &sessionScan{provider: provider, tools: map[string]bool{}, outTokens: map[string]int64{}}
		if provider == "claude" {
			sc.sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		}
	} else {
		sc = sc.clone()
	}
	fh, err := OpenConfined(path)
	if err != nil {
		return sc
	}
	defer fh.Close()
	if _, err := fh.Seek(sc.offset, io.SeekStart); err != nil {
		return sc
	}
	r := bufio.NewReaderSize(fh, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			break // a partial last line waits for the next look
		}
		sc.offset += int64(len(line))
		if provider == "claude" {
			sc.feedClaude(line)
		} else {
			sc.feedCodex(line)
		}
		if err != nil {
			break
		}
	}
	sc.mod = info.ModTime()
	scanMu.Lock()
	scanCache[path] = sc
	scanMu.Unlock()
	return sc
}

// clone copies the scan so readers of the cached one never see it change.
func (sc *sessionScan) clone() *sessionScan {
	c := *sc
	c.runs = make([]*Run, len(sc.runs))
	for i, r := range sc.runs {
		x := *r
		c.runs[i] = &x
	}
	c.tools = make(map[string]bool, len(sc.tools))
	for k, v := range sc.tools {
		c.tools[k] = v
	}
	c.outTokens = make(map[string]int64, len(sc.outTokens))
	for k, v := range sc.outTokens {
		c.outTokens[k] = v
	}
	return &c
}

type sessionFile struct {
	provider string
	path     string
	info     os.FileInfo
}

// sessionFiles lists the top-level session files of both CLIs changed since
// `since` (zero = all).
func sessionFiles(claudeHome, codexHome string, since time.Time) []sessionFile {
	var out []sessionFile
	if dirs, err := os.ReadDir(filepath.Join(claudeHome, "projects")); claudeHome != "" && err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(claudeHome, "projects", d.Name())
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				if info, err := e.Info(); err == nil && info.Size() > 0 && !info.ModTime().Before(since) {
					out = append(out, sessionFile{"claude", filepath.Join(dir, e.Name()), info})
				}
			}
		}
	}
	if codexHome == "" {
		return out
	}
	_ = filepath.WalkDir(filepath.Join(codexHome, "sessions"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > 0 && !info.ModTime().Before(since) {
			out = append(out, sessionFile{"codex", path, info})
		}
		return nil
	})
	return out
}

func (sc *sessionScan) originName(paseo map[string]paseoAgent) string {
	if _, ok := paseo[sc.sessionID]; ok {
		return "paseo"
	}
	if sc.provider == "claude" {
		if sc.entry == "cli" {
			return "terminal"
		}
		return "app"
	}
	switch o := strings.ToLower(sc.origin); {
	case o == "pocket-ai":
		return "pocket-ai"
	case strings.Contains(o, "paseo"):
		return "paseo"
	case strings.Contains(o, "cli") || strings.Contains(o, "tui") || strings.Contains(o, "exec"):
		return "terminal"
	}
	return "app"
}

// finish settles a run that has not ended: still running while a process
// works on it (Claude Code's registry, when there is one) or, failing that,
// while the file is fresh; otherwise it stopped without saying so.
func finish(r Run, mod time.Time, live liveFn) Run {
	if r.Status == "" {
		if busy, known := live(r.Provider, r.SessionID); known {
			if busy {
				r.Status = "running"
			} else {
				r.Status = "unfinished"
			}
		} else if time.Since(mod) < activeWindow {
			r.Status = "running"
		} else {
			r.Status = "unfinished"
		}
	}
	return r
}

// liveFn says whether a session's process is busy, and whether that is known.
type liveFn func(provider, sessionID string) (busy, known bool)

func liveCheck(claudeHome string) liveFn {
	if claudeHome == "" {
		return func(string, string) (bool, bool) { return false, false }
	}
	procs, ok := ClaudeProcs(claudeHome)
	return func(provider, sessionID string) (bool, bool) {
		if provider != "claude" || !ok {
			return false, false
		}
		p, found := procs[sessionID]
		return found && p.Busy(), true
	}
}

// Runs lists every run started in [from, to), newest first.
func Runs(h Homes, paseoHome string, from, to time.Time) []Run {
	paseo := paseoSessionsWithin(paseoHome, time.Since(from)+48*time.Hour)
	out := []Run{}
	for _, f := range accountFiles(h, from) {
		sc := f.scan
		origin := sc.originName(paseo)
		title := sc.title
		if p, ok := paseo[sc.sessionID]; ok && p.Title != "" {
			title = p.Title
		}
		for _, r := range sc.runs {
			st := ts(r.StartedAt)
			if st.Before(from) || !st.Before(to) {
				continue
			}
			x := *r
			x.Provider, x.SessionID, x.Cwd, x.Origin, x.Title, x.Account = f.provider, sc.sessionID, sc.cwd, origin, title, f.account
			x = finish(x, f.info.ModTime(), f.live)
			if x.Prompt == "" {
				x.Prompt = "(no prompt text)"
			}
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// Sessions lists the sessions of both CLIs that have at least one run,
// newest activity first.
func Sessions(h Homes, paseoHome string) []ServerSession {
	paseo := paseoSessionsWithin(paseoHome, 0)
	out := []ServerSession{}
	for _, f := range accountFiles(h, time.Time{}) {
		sc := f.scan
		if len(sc.runs) == 0 {
			continue
		}
		s := ServerSession{Provider: f.provider, SessionID: sc.sessionID, Title: sc.title, Cwd: sc.cwd, Origin: sc.originName(paseo),
			Model: sc.model, StartedAt: sc.runs[0].StartedAt, UpdatedAt: f.info.ModTime().UTC().Format(time.RFC3339), Runs: len(sc.runs), Path: f.path,
			Account: f.account}
		if p, ok := paseo[sc.sessionID]; ok && p.Title != "" {
			s.Title = p.Title
		}
		for _, r := range sc.runs {
			s.WorkMS += r.DurationMS
		}
		last := *sc.runs[len(sc.runs)-1]
		last.Provider, last.SessionID = f.provider, sc.sessionID
		last = finish(last, f.info.ModTime(), f.live)
		s.LastPrompt = last.Prompt
		if last.Status == "running" {
			s.Running, s.RunningAt = true, last.StartedAt
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

type accountFile struct {
	sessionFile
	account int64
	scan    *sessionScan
	live    liveFn
}

// accountFiles scans the session files of every account changed since
// `since`. A session moved to another account exists in both folders; the
// copy written last is the one that counts (and names the account).
func accountFiles(h Homes, since time.Time) []accountFile {
	var all []accountFile
	h.each(func(provider string, a AccountHome) {
		claude, codex, live := a.Dir, "", liveCheck(a.Dir)
		if provider == "codex" {
			claude, codex, live = "", a.Dir, liveCheck("")
		}
		for _, f := range sessionFiles(claude, codex, since) {
			sc := scanSession(f.provider, f.path, f.info)
			if sc.sessionID == "" {
				continue
			}
			all = append(all, accountFile{sessionFile: f, account: a.ID, scan: sc, live: live})
		}
	})
	best := map[string]int{}
	out := all[:0]
	for _, f := range all {
		k := f.provider + "/" + f.scan.sessionID
		if i, ok := best[k]; ok {
			if f.info.ModTime().After(out[i].info.ModTime()) {
				out[i] = f
			}
			continue
		}
		best[k] = len(out)
		out = append(out, f)
	}
	return out
}

// WarmSessions reads every session file once (in the background at start),
// so the first look at "On this server" or Today is quick.
func WarmSessions(h Homes) {
	accountFiles(h, time.Time{})
}
