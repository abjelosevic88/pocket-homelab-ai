package server

import (
	"strings"
	"testing"
)

func TestEndedCommands(t *testing.T) {
	out := strings.Join([]string{
		` 205116  713177    01:16 /bin/bash -c source /home/arno/.claude/shell-snapshots/snapshot-bash-1-x.sh 2>/dev/null || true && shopt -u extglob 2>/dev/null || true && eval 'cd /data/wt && echo '\''hi'\'' && pnpm exec vitest run' < /dev/null && pwd -P >| /tmp/claude-1-cwd`,
		` 205117  205116    01:15 node vitest`,                         // started by the command: left out
		` 205200  713177  1:02:03 node /home/arno/mcp-server/index.js`, // MCP server: left out
		` 205300  713177    00:04 /bin/zsh -c source /home/a/.claude/shell-snapshots/snapshot-zsh-2-y.sh && eval 'sleep 600' < /dev/null && pwd -P >| /tmp/c`,
	}, "\n")
	got := endedCommands(out, 713177)
	want := []string{"`cd /data/wt && echo 'hi' && pnpm exec vitest run` (running 01:16)", "`sleep 600` (running 00:04)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if endedCommands("", 1) != nil {
		t.Fatal("nothing ran: no commands")
	}
}
