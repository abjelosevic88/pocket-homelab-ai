package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// costStateMark starts the line Claude Code appends to a session file when a
// process ends: the session's running cost, which the next --resume (or
// --fork-session) of it restores and keeps adding to.
var costStateMark = []byte(`{"type":"cost-state"`)

// ClaudeSessionCost is the running cost a resumed Claude session starts
// from: the last cost-state line of its session file (ok false: none).
// Read from the end, where it is, so long sessions stay cheap.
func ClaudeSessionCost(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, false
	}
	size := fi.Size()
	for chunk := int64(1 << 20); ; chunk *= 4 {
		from := max(size-chunk, 0)
		buf := make([]byte, size-from)
		if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
			return 0, false
		}
		if v, ok := lastCostState(buf); ok {
			return v, true
		}
		if from == 0 {
			return 0, false
		}
	}
}

func lastCostState(buf []byte) (float64, bool) {
	for end := len(buf); end > 0; {
		i := bytes.LastIndex(buf[:end], costStateMark)
		if i < 0 {
			return 0, false
		}
		end = i
		if i > 0 && buf[i-1] != '\n' {
			continue // inside another line
		}
		line := buf[i:]
		if j := bytes.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		var c struct {
			TotalCostUSD *float64 `json:"totalCostUSD"`
		}
		if json.Unmarshal(line, &c) == nil && c.TotalCostUSD != nil {
			return *c.TotalCostUSD, true
		}
	}
	return 0, false
}

// RemoteClaudeCostSh prints the last cost-state line of session $2 under the
// login folder $1 (default ~/.claude) on a remote machine.
const RemoteClaudeCostSh = `d="${1:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}"; f=$(ls -1t "$d"/projects/*/"$2".jsonl 2>/dev/null | head -n 1)
[ -n "$f" ] || exit 0
tail -c 4194304 "$f" | grep '^{"type":"cost-state"' | tail -n 1`

// ParseCostState reads the line RemoteClaudeCostSh prints.
func ParseCostState(out string) (float64, bool) {
	return lastCostState([]byte(out))
}
