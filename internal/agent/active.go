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

// ActiveSession is a Claude Code or Codex session whose turn is in progress
// right now — started by Pocket AI, Paseo, a terminal or any other app.
// It is read from the CLIs' own session files, so it works whoever drives it.
type ActiveSession struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`    // the message the agent is working on
	PromptAt  string `json:"prompt_at"` // when it was sent
	UpdatedAt string `json:"updated_at"`
	Origin    string `json:"origin"` // pocket-ai | paseo | terminal | app
}

// Running sessions older than this without a write are treated as dead
// (a crashed CLI never writes "turn ended").
const activeWindow = 15 * time.Minute

// ActiveSessions lists the sessions with a turn in progress.
func ActiveSessions(claudeHome, codexHome, paseoHome string) []ActiveSession {
	paseo := paseoSessions(paseoHome)
	var out []ActiveSession
	out = append(out, activeClaude(claudeHome, paseo)...)
	out = append(out, activeCodex(codexHome, paseo)...)
	sort.Slice(out, func(i, j int) bool { return out[i].PromptAt > out[j].PromptAt })
	return out
}

func readTail(path string, n int64) []byte {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil
	}
	off := fi.Size() - n
	if off < 0 {
		off = 0
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(fh)
	if off > 0 { // drop the partial first line
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b
}

func activeClaude(home string, paseo map[string]paseoAgent) []ActiveSession {
	dirs, err := os.ReadDir(filepath.Join(home, "projects"))
	if err != nil {
		return nil
	}
	var out []ActiveSession
	now := time.Now()
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(home, "projects", d.Name())
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) > activeWindow {
				continue
			}
			path := filepath.Join(dir, e.Name())
			s, running := claudeState(path, 1<<20)
			if !running {
				continue
			}
			if s.Prompt == "" {
				// a long turn pushed the prompt out of the tail: remember it,
				// or look further back once
				promptMu.Lock()
				c, ok := promptCache[path]
				promptMu.Unlock()
				if !ok {
					deep, _ := claudeState(path, 48<<20)
					c = [2]string{deep.Prompt, deep.PromptAt}
				}
				s.Prompt, s.PromptAt = c[0], c[1]
			}
			promptMu.Lock()
			promptCache[path] = [2]string{s.Prompt, s.PromptAt}
			promptMu.Unlock()
			past := claudePast(path, info)
			s.Provider, s.SessionID, s.UpdatedAt = "claude", strings.TrimSuffix(e.Name(), ".jsonl"), info.ModTime().UTC().Format(time.RFC3339)
			s.Cwd = firstNonEmpty(s.Cwd, past.Cwd)
			s.Title = past.Title
			if p, ok := paseo[s.SessionID]; ok {
				s.Origin = "paseo"
				if p.Title != "" {
					s.Title = p.Title
				}
			}
			if s.Title == "" {
				s.Title = oneLine(s.Prompt, 140)
			}
			out = append(out, s)
		}
	}
	return out
}

// claudeState reads the end of a session file: the last prompt and whether
// the turn after it is still going.
var (
	promptMu    sync.Mutex
	promptCache = map[string][2]string{} // session file → last prompt, when
)

func claudeState(path string, tailBytes int64) (ActiveSession, bool) {
	var s ActiveSession
	running := false
	entry := ""
	sc := bufio.NewScanner(bytes.NewReader(readTail(path, tailBytes)))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			IsMeta      bool   `json:"isMeta"`
			IsSidechain bool   `json:"isSidechain"`
			Entrypoint  string `json:"entrypoint"`
			Timestamp   string `json:"timestamp"`
			Message     struct {
				Content    json.RawMessage `json:"content"`
				StopReason string          `json:"stop_reason"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.IsSidechain {
			continue
		}
		if m.Cwd != "" {
			s.Cwd = m.Cwd
		}
		if m.Entrypoint != "" {
			entry = m.Entrypoint
		}
		switch m.Type {
		case "user":
			if m.IsMeta {
				continue
			}
			var str string
			if json.Unmarshal(m.Message.Content, &str) == nil && strings.HasPrefix(strings.TrimSpace(str), "[Request interrupted") {
				running = false
				continue
			}
			if bytes.Contains(m.Message.Content, []byte(`"tool_result"`)) {
				if bytes.Contains(m.Message.Content, []byte("[Request interrupted")) {
					running = false
				} else {
					running = true
				}
				continue
			}
			if t := userText(m.Message.Content); t != "" {
				s.Prompt, s.PromptAt, running = oneLine(t, 400), m.Timestamp, true
			}
		case "assistant":
			switch m.Message.StopReason {
			case "end_turn", "stop_sequence", "max_tokens", "refusal":
				running = false
			case "tool_use":
				running = true
			}
		}
	}
	switch entry {
	case "cli":
		s.Origin = "terminal"
	default:
		s.Origin = "app"
	}
	return s, running
}

func activeCodex(home string, paseo map[string]paseoAgent) []ActiveSession {
	var out []ActiveSession
	now := time.Now()
	for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) > activeWindow {
				continue
			}
			path := filepath.Join(dir, e.Name())
			s, running := codexState(path)
			if !running || s.SessionID == "" {
				continue
			}
			s.Provider, s.UpdatedAt = "codex", info.ModTime().UTC().Format(time.RFC3339)
			if p, ok := paseo[s.SessionID]; ok {
				s.Origin = "paseo"
				if p.Title != "" {
					s.Title = p.Title
				}
			}
			if s.Title == "" {
				s.Title = oneLine(s.Prompt, 140)
			}
			out = append(out, s)
		}
	}
	return out
}

func codexState(path string) (ActiveSession, bool) {
	var s ActiveSession
	// session_meta is the first line
	if fh, err := os.Open(path); err == nil {
		r := bufio.NewReaderSize(fh, 1<<16)
		line, _ := r.ReadBytes('\n')
		fh.Close()
		var m struct {
			Payload struct {
				ID         string `json:"id"`
				Cwd        string `json:"cwd"`
				Originator string `json:"originator"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &m) == nil {
			s.SessionID, s.Cwd = m.Payload.ID, m.Payload.Cwd
			switch o := strings.ToLower(m.Payload.Originator); {
			case o == "pocket-ai":
				s.Origin = "pocket-ai"
			case strings.Contains(o, "paseo"):
				s.Origin = "paseo"
			case strings.Contains(o, "cli") || strings.Contains(o, "tui") || strings.Contains(o, "exec"):
				s.Origin = "terminal"
			default:
				s.Origin = "app"
			}
		}
	}
	running := false
	sc := bufio.NewScanner(bytes.NewReader(readTail(path, 1<<20)))
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "event_msg" {
			continue
		}
		switch m.Payload.Type {
		case "task_started":
			running = true
		case "task_complete", "turn_aborted":
			running = false
		case "user_message":
			if t := strings.TrimSpace(m.Payload.Message); t != "" {
				s.Prompt, s.PromptAt = oneLine(t, 400), m.Timestamp
			}
		}
	}
	return s, running
}

// ---------- Paseo ----------

type paseoAgent struct {
	Title string
	mod   time.Time
}

var (
	paseoMu    sync.Mutex
	paseoCache = map[string]paseoAgent{} // agent file → its session
	paseoByID  = map[string]string{}     // agent file → session id
)

// paseoSessions maps session ids to the Paseo agents that drive them (from
// PASEO_HOME/agents, recently changed files only).
func paseoSessions(home string) map[string]paseoAgent {
	out := map[string]paseoAgent{}
	if home == "" {
		return out
	}
	dirs, err := os.ReadDir(filepath.Join(home, "agents"))
	if err != nil {
		return out
	}
	paseoMu.Lock()
	defer paseoMu.Unlock()
	now := time.Now()
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(home, "agents", d.Name())
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) > 48*time.Hour {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if c, ok := paseoCache[path]; ok && c.mod.Equal(info.ModTime()) {
				if id := paseoByID[path]; id != "" {
					out[id] = c
				}
				continue
			}
			var a struct {
				Title       string `json:"title"`
				Persistence struct {
					SessionID string `json:"sessionId"`
				} `json:"persistence"`
			}
			b, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(b, &a) != nil {
				continue
			}
			c := paseoAgent{Title: strings.TrimSpace(a.Title), mod: info.ModTime()}
			paseoCache[path], paseoByID[path] = c, a.Persistence.SessionID
			if a.Persistence.SessionID != "" {
				out[a.Persistence.SessionID] = c
			}
		}
	}
	return out
}
