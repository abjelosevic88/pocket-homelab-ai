package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClaudeProc is a running Claude Code process as it registers itself in
// ~/.claude/sessions/<pid>.json: which session it is on and whether it is
// working ("busy") or waiting for the next message ("idle").
type ClaudeProc struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	ProcStart string `json:"procStart"`
}

// Busy reports whether the process is in the middle of a turn.
func (p ClaudeProc) Busy() bool { return p.Status != "" && p.Status != "idle" }

// ClaudeProcs maps session ids to the live Claude Code processes on them.
// ok is false when this Claude Code does not keep the registry (older
// versions): callers then fall back to reading the session file.
func ClaudeProcs(claudeHome string) (procs map[string]ClaudeProc, ok bool) {
	files, err := filepath.Glob(filepath.Join(claudeHome, "sessions", "*.json"))
	if err != nil {
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(claudeHome, "sessions")); err != nil {
		return nil, false
	}
	procs = map[string]ClaudeProc{}
	for _, f := range files {
		b, err := ReadConfined(f)
		if err != nil {
			continue
		}
		var p ClaudeProc
		if json.Unmarshal(b, &p) != nil || p.PID <= 0 || p.SessionID == "" || !procAlive(p.PID, p.ProcStart) {
			continue
		}
		if old, dup := procs[p.SessionID]; dup && old.Busy() {
			continue // two processes on one session: the busy one wins
		}
		procs[p.SessionID] = p
	}
	return procs, true
}

// procAlive: the pid exists and (when known) is the same process that wrote
// the file, not a later one that got the pid again.
func procAlive(pid int, start string) bool {
	b, err := ReadConfined("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	if start == "" {
		return true
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the command name may contain spaces
	if i < 0 {
		return true
	}
	f := strings.Fields(s[i+1:])
	// fields after ")" start at stat field 3; starttime is field 22
	return len(f) <= 19 || f[19] == start
}
