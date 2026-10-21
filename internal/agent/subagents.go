package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
