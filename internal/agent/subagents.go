package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExternalSubagents lists the sub-agents still at work in a Claude Code
// session that another program drives (Paseo, a terminal). Claude Code keeps
// each one in <session>/subagents/agent-<id>.jsonl with a .meta.json beside
// it; one is done once its own transcript ends its turn.
func ExternalSubagents(sessionPath string) []Subagent {
	dir := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
	metas, _ := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	var out []Subagent
	for _, mp := range metas {
		jp := strings.TrimSuffix(mp, ".meta.json") + ".jsonl"
		info, err := os.Stat(jp)
		if err != nil || time.Since(info.ModTime()) > 10*time.Minute {
			continue // finished long ago, or never wrote anything
		}
		var meta struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
		}
		if b, err := os.ReadFile(mp); err != nil || json.Unmarshal(b, &meta) != nil {
			continue
		}
		done, activity, tools := subagentTail(jp)
		if done {
			continue
		}
		out = append(out, Subagent{ID: strings.TrimPrefix(filepath.Base(strings.TrimSuffix(jp, ".jsonl")), "agent-"),
			Description: firstNonEmpty(meta.Description, meta.AgentType, "Sub-agent"), Type: meta.AgentType,
			Activity: activity, StartedAt: firstTimestamp(jp), ToolUses: tools})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// subagentTail reads the end of a sub-agent transcript: whether it ended its
// turn, what it is doing now (its latest tool call) and how many tools it used.
func subagentTail(path string) (done bool, activity string, tools int) {
	sc := bufio.NewScanner(strings.NewReader(string(readTail(path, 512<<10))))
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				Content    json.RawMessage `json:"content"`
				StopReason string          `json:"stop_reason"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "assistant":
			var blocks []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(m.Message.Content, &blocks)
			handback := false
			for _, b := range blocks {
				if b.Type == "tool_use" {
					tools++
					activity = claudeTool(b.Name, b.Input).Title
					handback = b.Name == "SubagentHandback" // its result went back to the main agent
				}
			}
			done = handback || m.Message.StopReason == "end_turn" || m.Message.StopReason == "stop_sequence"
		case "user":
			if !done || !strings.Contains(string(m.Message.Content), "tool_result") {
				done = false // a tool result came back: it goes on
			}
		}
	}
	return done, oneLine(activity, 120), tools
}

func firstTimestamp(path string) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for i := 0; i < 5 && sc.Scan(); i++ {
		var m struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Timestamp != "" {
			return m.Timestamp
		}
	}
	return ""
}

// SubagentInfo is one sub-agent of a Claude Code session, working or finished.
type SubagentInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Type        string `json:"type,omitempty"`
	Model       string `json:"model,omitempty"`
	// Status: running | done | stopped (it went quiet without finishing).
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	ToolUses  int    `json:"tool_uses,omitempty"`
	// Tokens is how full its context was at its last reply.
	Tokens   int64  `json:"tokens,omitempty"`
	Activity string `json:"activity,omitempty"` // its latest tool call
	Result   string `json:"result,omitempty"`   // the start of its final answer
}

var subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// SubagentPath is the transcript of sub-agent id of a session.
func SubagentPath(sessionPath, id string) (string, error) {
	if !subagentID.MatchString(id) {
		return "", errors.New("bad sub-agent id")
	}
	p := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents", "agent-"+id+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// ListSubagents lists every sub-agent a session started, oldest first.
func ListSubagents(sessionPath string) []SubagentInfo {
	dir := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	out := []SubagentInfo{}
	for _, mp := range metas {
		if info, ok := subagentInfo(mp); ok {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

type subagentCached struct {
	size int64
	mod  time.Time
	info SubagentInfo
}

// Transcripts only grow, so one read per change of size is enough.
var subagentCache = struct {
	sync.Mutex
	m map[string]subagentCached
}{m: map[string]subagentCached{}}

func subagentInfo(metaPath string) (SubagentInfo, bool) {
	jp := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
	st, err := os.Stat(jp)
	if err != nil {
		return SubagentInfo{}, false
	}
	var meta struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
		Model       string `json:"model"`
		Finished    bool   `json:"finished"`
	}
	if b, err := os.ReadFile(metaPath); err != nil || json.Unmarshal(b, &meta) != nil {
		return SubagentInfo{}, false
	}
	subagentCache.Lock()
	c, ok := subagentCache.m[jp]
	subagentCache.Unlock()
	if !ok || c.size != st.Size() || !c.mod.Equal(st.ModTime()) {
		c = subagentCached{size: st.Size(), mod: st.ModTime(), info: scanSubagent(jp)}
		subagentCache.Lock()
		subagentCache.m[jp] = c
		subagentCache.Unlock()
	}
	info := c.info
	info.ID = strings.TrimPrefix(filepath.Base(strings.TrimSuffix(jp, ".jsonl")), "agent-")
	info.Description = firstNonEmpty(meta.Description, meta.AgentType, "Sub-agent")
	info.Type = meta.AgentType
	if info.Model == "" && meta.Model != "inherit" {
		info.Model = meta.Model
	}
	switch {
	case meta.Finished || info.Status == "done":
		info.Status = "done"
	case time.Since(st.ModTime()) > 10*time.Minute:
		info.Status = "stopped"
	default:
		info.Status = "running"
	}
	if info.Status != "done" {
		info.Result = ""
	}
	return info, true
}

// scanSubagent reads a whole sub-agent transcript: model, tool calls, how
// full its context got, whether it finished and what it answered.
func scanSubagent(path string) (info SubagentInfo) {
	fh, err := os.Open(path)
	if err != nil {
		return info
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	done := false
	for sc.Scan() {
		var m struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model      string          `json:"model"`
				Content    json.RawMessage `json:"content"`
				StopReason string          `json:"stop_reason"`
				Usage      *struct {
					Input       int64 `json:"input_tokens"`
					CacheRead   int64 `json:"cache_read_input_tokens"`
					CacheCreate int64 `json:"cache_creation_input_tokens"`
					Output      int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Timestamp != "" {
			if info.StartedAt == "" {
				info.StartedAt = m.Timestamp
			}
			info.EndedAt = m.Timestamp
		}
		switch m.Type {
		case "assistant":
			if m.Message.Model != "" && !strings.HasPrefix(m.Message.Model, "<") {
				info.Model = m.Message.Model
			}
			if u := m.Message.Usage; u != nil {
				info.Tokens = u.Input + u.CacheRead + u.CacheCreate + u.Output
			}
			var blocks []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Text  string          `json:"text"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(m.Message.Content, &blocks)
			handback := false
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					info.ToolUses++
					info.Activity = oneLine(claudeTool(b.Name, b.Input).Title, 120)
					handback = handback || b.Name == "SubagentHandback"
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						info.Result = oneLine(b.Text, 240)
					}
				}
			}
			done = handback || m.Message.StopReason == "end_turn" || m.Message.StopReason == "stop_sequence"
		case "user":
			if strings.Contains(string(m.Message.Content), "tool_result") {
				done = false // a tool result came back: it goes on
			}
		}
	}
	if done {
		info.Status = "done"
	}
	return info
}
