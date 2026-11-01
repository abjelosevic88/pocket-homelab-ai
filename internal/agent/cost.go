package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
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

// Restore is a Claude process of a session starting from a saved total.
type Restore struct {
	At  time.Time // its first line
	USD float64   // the running cost it restored
}

// ClaudeRestores lists when each process of a session (after the first)
// began and the total it started from: every cost-state line, timed by the
// first line written after it. Forks and rewinds copy their source's lines,
// so their file holds the totals they started from too. end is the total the
// last process saved as it ended (nil: it has not ended, or never saved).
func ClaudeRestores(path string) (out []Restore, end *float64, err error) {
	f, err := OpenConfined(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var pending *float64
	r := bufio.NewReaderSize(f, 1<<20)
	tsMark := []byte(`"timestamp":"`)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.HasPrefix(line, costStateMark) {
				if v, ok := lastCostState(line); ok {
					pending = &v
				}
			} else if i := bytes.Index(line, tsMark); i >= 0 && pending != nil {
				rest := line[i+len(tsMark):]
				if j := bytes.IndexByte(rest, '"'); j > 0 {
					if at, e := time.Parse(time.RFC3339Nano, string(rest[:j])); e == nil {
						out = append(out, Restore{At: at, USD: *pending})
						pending = nil
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out, pending, nil
}

// RemoteClaudeRestoresSh prints, for pairs of arguments "login folder"
// "session id" ("" = the default folder), "@@ <id>" and then that session
// file's cost-state lines (up to the total) each followed by the first
// timestamp after it: what ClaudeRestores reads, without copying the file.
const RemoteClaudeRestoresSh = `while [ $# -ge 2 ]; do d="${1:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}"; sid=$2; shift 2
echo "@@ $sid"
f=$(ls -1t "$d"/projects/*/"$sid".jsonl 2>/dev/null | head -n 1)
[ -n "$f" ] || { echo "@@missing"; continue; }
LC_ALL=C grep -o -E '^\{"type":"cost-state"[^}]*"totalCostUSD":[-0-9.eE+]+|"timestamp":"[^"]*"' "$f" | awk '/cost-state/{print; p=1; next} p&&/timestamp/{print; p=0}'
done`

// RemoteRestores is ClaudeRestores of one session on another machine.
type RemoteRestores struct {
	Restores []Restore
	End      *float64
}

// ParseRemoteRestores reads what RemoteClaudeRestoresSh printed; sessions
// whose file is missing there are left out.
func ParseRemoteRestores(out string) map[string]*RemoteRestores {
	res := map[string]*RemoteRestores{}
	var cur *RemoteRestores
	var id string
	var pending *float64
	done := func() {
		if cur != nil {
			cur.End = pending
		}
		pending = nil
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "@@missing"):
			delete(res, id)
			cur = nil
		case strings.HasPrefix(line, "@@ "):
			done()
			cur, id = &RemoteRestores{}, strings.TrimSpace(line[3:])
			res[id] = cur
		case cur == nil:
		case strings.HasPrefix(line, `{"type":"cost-state"`):
			if i := strings.Index(line, `"totalCostUSD":`); i >= 0 {
				if v, err := strconv.ParseFloat(line[i+len(`"totalCostUSD":`):], 64); err == nil {
					pending = &v
				}
			}
		case strings.HasPrefix(line, `"timestamp":"`) && pending != nil:
			if at, err := time.Parse(time.RFC3339Nano, strings.TrimSuffix(strings.TrimPrefix(line, `"timestamp":"`), `"`)); err == nil {
				cur.Restores = append(cur.Restores, Restore{At: at, USD: *pending})
				pending = nil
			}
		}
	}
	done()
	return res
}
