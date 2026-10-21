package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ReadClaude turns a Claude Code session file into timeline items.
func ReadClaude(path string) (items []Item, cwd string, err error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	index := map[string]int{} // tool_use id → position in items
	for sc.Scan() {
		var m struct {
			Type        string `json:"type"`
			UUID        string `json:"uuid"`
			Cwd         string `json:"cwd"`
			IsMeta      bool   `json:"isMeta"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.IsSidechain {
			continue
		}
		if cwd == "" && m.Cwd != "" {
			cwd = m.Cwd
		}
		switch m.Type {
		case "user":
			if m.IsMeta {
				continue
			}
			var blocks []struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			if json.Unmarshal(m.Message.Content, &blocks) == nil {
				handled := false
				for _, b := range blocks {
					if b.Type != "tool_result" {
						continue
					}
					handled = true
					if i, ok := index[b.ToolUseID]; ok && items[i].Tool != nil {
						t := *items[i].Tool
						t.Output = clip(toolResultText(b.Content), 20000)
						t.Status = "done"
						if b.IsError {
							t.Status = "error"
						}
						if d := structuredDiff(m.ToolUseResult); d != "" && (t.Kind == "edit" || t.Kind == "write") {
							t.Diff = d
						}
						items[i].Tool = &t
					}
				}
				if handled {
					continue
				}
			}
			if t := userText(m.Message.Content); t != "" {
				items = append(items, Item{Key: "u-" + m.UUID, Kind: "user", Text: t})
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
				continue
			}
			for i, b := range blocks {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						items = append(items, Item{Key: "a-" + m.UUID + "-" + itoa(i), Kind: "assistant", Text: b.Text})
					}
				case "tool_use":
					if b.Name == "TodoWrite" {
						items = append(items, Item{Key: b.ID, Kind: "todos", Todos: claudeTodos(b.Input)})
						continue
					}
					t := claudeTool(b.Name, b.Input)
					t.Status = "done"
					index[b.ID] = len(items)
					items = append(items, Item{Key: b.ID, Kind: "tool", Tool: t})
				}
			}
		}
	}
	return items, cwd, sc.Err()
}

// ListCodex lists Codex threads through the app-server, newest first.
func ListCodex(ctx context.Context, bin, q string, limit int) ([]Past, error) {
	c, err := startRPC(bin, os.TempDir(), nil)
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
func ReadCodex(ctx context.Context, bin, threadID string) (items []Item, cwd, title string, err error) {
	c, err := startRPC(bin, os.TempDir(), nil)
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
			Cwd     string `json:"cwd"`
			Preview string `json:"preview"`
			Name    string `json:"name"`
			Turns   []struct {
				Items []json.RawMessage `json:"items"`
			} `json:"turns"`
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
	}
	return items, r.Thread.Cwd, oneLine(firstNonEmpty(r.Thread.Name, r.Thread.Preview), 140), nil
}
