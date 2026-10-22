package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Past is a session a CLI already has on disk (started in a terminal, by
// Paseo, or by Pocket AI itself).
type Past struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
	Messages  int    `json:"messages"`
	Path      string `json:"-"`
	ThreadID  string `json:"thread_id"`
}

type pastCacheEntry struct {
	mod  time.Time
	size int64
	past Past
}

var (
	pastMu    sync.Mutex
	pastCache = map[string]pastCacheEntry{}
)

// ListClaude lists Claude Code sessions under claudeHome/projects, newest first.
func ListClaude(claudeHome, q string, limit int) ([]Past, error) {
	dirs, err := os.ReadDir(filepath.Join(claudeHome, "projects"))
	if err != nil {
		return nil, err
	}
	type f struct {
		path string
		info os.FileInfo
	}
	var files []f
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
			if info, err := e.Info(); err == nil && info.Size() > 0 {
				files = append(files, f{filepath.Join(dir, e.Name()), info})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().After(files[j].info.ModTime()) })
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Past{}
	for _, fl := range files {
		p := claudePast(fl.path, fl.info)
		if p.Title == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Cwd), q) {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func claudePast(path string, info os.FileInfo) Past {
	pastMu.Lock()
	if e, ok := pastCache[path]; ok && e.mod.Equal(info.ModTime()) && e.size == info.Size() {
		pastMu.Unlock()
		return e.past
	}
	pastMu.Unlock()
	p := Past{Provider: "claude", SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path,
		UpdatedAt: info.ModTime().UTC().Format(time.RFC3339)}
	fh, err := os.Open(path)
	if err != nil {
		return p
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.Contains(line, []byte(`"type":"user"`)) || bytes.Contains(line, []byte(`"type":"assistant"`)) {
			p.Messages++
		}
		if p.Title != "" && p.Cwd != "" {
			if info.Size() > 16<<20 {
				break // big file: stop counting
			}
			continue
		}
		var m struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if p.Cwd == "" && m.Cwd != "" {
			p.Cwd = m.Cwd
		}
		if p.Title == "" && m.Type == "user" && !m.IsMeta {
			if t := userText(m.Message.Content); t != "" {
				p.Title = oneLine(t, 140)
			}
		}
	}
	pastMu.Lock()
	pastCache[path] = pastCacheEntry{mod: info.ModTime(), size: info.Size(), past: p}
	pastMu.Unlock()
	return p
}

// userText is the typed text of a Claude user message ("" for tool results,
// slash-command plumbing and the like).
func userText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return realPrompt(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b []string
	for _, p := range parts {
		if p.Type == "text" {
			if t := realPrompt(p.Text); t != "" {
				b = append(b, t)
			}
		}
	}
	return strings.Join(b, "\n")
}

func realPrompt(s string) string {
	t := strings.TrimSpace(s)
	// Paseo starts a continued conversation with a summary of the earlier one
	// in front of the new message: the prompt is what follows it.
	const sumOpen, sumClose = "<chat-history-summary>", "</chat-history-summary>"
	if strings.HasPrefix(t, sumOpen) {
		i := strings.Index(t, sumClose)
		if i < 0 {
			return ""
		}
		t = strings.TrimSpace(t[i+len(sumClose):])
	}
	for _, pre := range []string{"<command-", "<local-command", "Caveat:", "<system-reminder", "[Request interrupted", "<bash-", "<task-notification"} {
		if strings.HasPrefix(t, pre) {
			return ""
		}
	}
	return t
}

// FindClaude finds the file of a Claude Code session.
func FindClaude(claudeHome, sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\.") {
		return "", errors.New("bad session id")
	}
	m, _ := filepath.Glob(filepath.Join(claudeHome, "projects", "*", sessionID+".jsonl"))
	if len(m) == 0 {
		return "", os.ErrNotExist
	}
	return m[0], nil
}

// ClaudeLog turns lines of a Claude Code session file into timeline
// changes, one line at a time (import reads a whole file; following a live
// session feeds the lines as they are appended).
type ClaudeLog struct {
	Cwd string

	promptKey string    // the message that started the current turn
	promptAt  time.Time // and when
}

// LogOp is one change a session-file line makes to the timeline.
type LogOp struct {
	Item        *Item       // insert or replace by key
	Result      *ToolResult // fill in the output of an earlier tool call
	TurnStarted bool        // someone sent a message
	TurnEnded   bool        // the agent finished its turn
}

// ToolResult is what came back for a tool call.
type ToolResult struct {
	ToolUseID string
	Output    string
	IsError   bool
	Diff      string
	Images    int
}

// Feed parses one line.
func (l *ClaudeLog) Feed(line []byte) []LogOp {
	var m struct {
		Type       string    `json:"type"`
		UUID       string    `json:"uuid"`
		Timestamp  time.Time `json:"timestamp"`
		Cwd        string    `json:"cwd"`
		Attachment struct {
			Type        string          `json:"type"`
			Prompt      json.RawMessage `json:"prompt"`
			CommandMode string          `json:"commandMode"`
		} `json:"attachment"`
		IsMeta      bool `json:"isMeta"`
		IsSidechain bool `json:"isSidechain"`
		Message     struct {
			Content    json.RawMessage `json:"content"`
			StopReason string          `json:"stop_reason"`
		} `json:"message"`
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(line, &m) != nil || m.IsSidechain {
		return nil
	}
	if l.Cwd == "" && m.Cwd != "" {
		l.Cwd = m.Cwd
	}
	var ops []LogOp
	switch m.Type {
	case "user":
		if m.IsMeta {
			return nil
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if json.Unmarshal(m.Message.Content, &blocks) == nil {
			results := false
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				results = true
				ops = append(ops, LogOp{Result: &ToolResult{ToolUseID: b.ToolUseID, Output: clip(toolResultText(b.Content), 20000),
					IsError: b.IsError, Diff: structuredDiff(m.ToolUseResult), Images: countImages(b.Content)}})
			}
			if results {
				return ops
			}
		}
		t, n := userText(m.Message.Content), countImages(m.Message.Content)
		if t != "" || n > 0 {
			ops = append(ops, LogOp{Item: &Item{Key: "u-" + m.UUID, Kind: "user", Text: t, LogImages: n}, TurnStarted: true})
			l.promptKey, l.promptAt = m.UUID, m.Timestamp
		}
	case "attachment":
		// A message sent while the agent was working: Claude Code hands it to
		// the running turn as a queued command instead of a new prompt.
		a := m.Attachment
		if a.Type != "queued_command" || (a.CommandMode != "" && a.CommandMode != "prompt") {
			return nil
		}
		if t, n := userText(a.Prompt), countImages(a.Prompt); t != "" || n > 0 {
			ops = append(ops, LogOp{Item: &Item{Key: "u-" + m.UUID, Kind: "user", Text: t, LogImages: n}})
		}
	case "assistant":
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(m.Message.Content, &blocks) != nil {
			return nil
		}
		for i, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					ops = append(ops, LogOp{Item: &Item{Key: "a-" + m.UUID + "-" + itoa(i), Kind: "assistant", Text: b.Text}})
				}
			case "tool_use":
				if b.Name == "TodoWrite" {
					ops = append(ops, LogOp{Item: &Item{Key: b.ID, Kind: "todos", Todos: claudeTodos(b.Input)}})
					continue
				}
				ops = append(ops, LogOp{Item: &Item{Key: b.ID, Kind: "tool", Tool: claudeTool(b.Name, b.Input)}})
			}
		}
		if m.Message.StopReason == "end_turn" || m.Message.StopReason == "stop_sequence" {
			ops = append(ops, LogOp{TurnEnded: true})
			// The log has no turn summary, only timestamps: the footer shows how
			// long the turn took (a later end of the same turn replaces it).
			if l.promptKey != "" && !l.promptAt.IsZero() && m.Timestamp.After(l.promptAt) {
				ops = append(ops, LogOp{Item: &Item{Key: "turn-" + l.promptKey, Kind: "turn",
					Turn: &TurnInfo{OK: true, DurationMS: m.Timestamp.Sub(l.promptAt).Milliseconds()}}})
			}
		}
	}
	return ops
}

// Apply adds a tool result to the tool item it belongs to.
func (r *ToolResult) Apply(t *Tool) {
	t.Output = r.Output
	t.Images = r.Images
	t.Status = "done"
	if r.IsError {
		t.Status = "error"
	}
	if r.Diff != "" && (t.Kind == "edit" || t.Kind == "write") {
		t.Diff = r.Diff
	}
}

// ReadClaude turns a Claude Code session file into timeline items; size is
// how many bytes were read (where following the file continues).
func ReadClaude(path string) (items []Item, cwd string, size int64, err error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, "", 0, err
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<20)
	index := map[string]int{} // item key (tool_use id for tools) → position in items
	var log ClaudeLog
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			size += int64(len(line))
			for _, op := range log.Feed(line) {
				switch {
				case op.Item != nil:
					it := *op.Item
					if it.Tool != nil {
						t := *it.Tool
						t.Status = "done"
						it.Tool = &t
					}
					// one entry per key: a later version (a turn that ended
					// twice) replaces the earlier one in place
					if i, ok := index[it.Key]; ok {
						items[i] = it
					} else {
						index[it.Key] = len(items)
						items = append(items, it)
					}
				case op.Result != nil:
					if i, ok := index[op.Result.ToolUseID]; ok && items[i].Tool != nil {
						t := *items[i].Tool
						op.Result.Apply(&t)
						items[i].Tool = &t
					}
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return items, log.Cwd, size, rerr
		}
	}
	return items, log.Cwd, size, nil
}

// ClaudeTail reads complete lines appended to a session file after offset.
// It returns the lines and the new offset (a partial last line stays unread).
func ClaudeTail(path string, offset int64) ([][]byte, int64, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, offset, err
	}
	if fi.Size() < offset { // rewritten: start over at its end
		return nil, fi.Size(), nil
	}
	if fi.Size() == offset {
		return nil, offset, nil
	}
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(io.LimitReader(fh, 64<<20))
	if err != nil {
		return nil, offset, err
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil, offset, nil
	}
	var lines [][]byte
	for _, l := range bytes.Split(data[:end], []byte{'\n'}) {
		if len(l) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, offset + int64(end) + 1, nil
}

// FileSize is the size of a file (-1 when it is missing).
func FileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// codexTurn is one turn as `thread/read` returns it.
type codexTurn struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"` // completed | interrupted | failed | inProgress
	DurationMS int64             `json:"durationMs"`
	Items      []json.RawMessage `json:"items"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// footer is the timeline footer of a finished turn (same key as the one
// Pocket AI writes when it drives the turn itself).
func (t codexTurn) footer() (Item, bool) {
	if t.ID == "" || t.DurationMS <= 0 || (t.Status != "completed" && t.Status != "interrupted" && t.Status != "failed") {
		return Item{}, false
	}
	ti := TurnInfo{ID: t.ID, OK: t.Status == "completed", DurationMS: t.DurationMS, Interrupted: t.Status == "interrupted"}
	if !ti.OK {
		ti.Error = "Interrupted"
		if t.Error != nil && t.Error.Message != "" {
			ti.Error = t.Error.Message
		}
	}
	return Item{Key: "turn-" + t.ID, Kind: "turn", Turn: &ti}, true
}

// ListCodex lists Codex threads through the app-server, newest first.
func ListCodex(ctx context.Context, bin, q string, limit int, env []string) ([]Past, error) {
	c, err := startRPC(bin, os.TempDir(), env)
	if err != nil {
		return nil, err
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.initialize(ctx, Version); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	params := map[string]any{"limit": limit, "sortKey": "updated_at"}
	if strings.TrimSpace(q) != "" {
		params["searchTerm"] = q
	}
	res, err := c.call(ctx, "thread/list", params)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			ID        string `json:"id"`
			Preview   string `json:"preview"`
			Name      string `json:"name"`
			Cwd       string `json:"cwd"`
			UpdatedAt int64  `json:"updatedAt"`
			Ephemeral bool   `json:"ephemeral"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	out := []Past{}
	for _, t := range r.Data {
		if t.Ephemeral {
			continue
		}
		out = append(out, Past{Provider: "codex", SessionID: t.ID, Cwd: t.Cwd, Title: oneLine(firstNonEmpty(t.Name, t.Preview, "(untitled)"), 140),
			UpdatedAt: unixISO(t.UpdatedAt)})
	}
	return out, nil
}

// ReadCodex loads a Codex thread's turns as timeline items.
func ReadCodex(ctx context.Context, bin, threadID string, env []string) (items []Item, cwd, title string, err error) {
	c, err := startRPC(bin, os.TempDir(), env)
	if err != nil {
		return nil, "", "", err
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := c.initialize(ctx, Version); err != nil {
		return nil, "", "", err
	}
	res, err := c.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return nil, "", "", err
	}
	var r struct {
		Thread struct {
			Cwd     string      `json:"cwd"`
			Preview string      `json:"preview"`
			Name    string      `json:"name"`
			Turns   []codexTurn `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, "", "", err
	}
	for _, t := range r.Thread.Turns {
		for _, raw := range t.Items {
			if it, ok := CodexItem(raw); ok {
				if it.Tool != nil && it.Tool.Status == "running" {
					it.Tool.Status = "done"
				}
				items = append(items, it)
			}
		}
		if it, ok := t.footer(); ok {
			items = append(items, it)
		}
	}
	return items, r.Thread.Cwd, oneLine(firstNonEmpty(r.Thread.Name, r.Thread.Preview), 140), nil
}
