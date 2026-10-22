package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewindClaudeExcludesFutureContext(t *testing.T) {
	home := t.TempDir()
	sid := "original"
	path := filepath.Join(home, "projects", "-tmp", sid+".jsonl")
	records := []map[string]any{
		{"type": "user", "uuid": "one", "message": map[string]any{"content": "repeat"}},
		{"type": "assistant", "uuid": "reply-one", "parentUuid": "one", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": "FIRST REPLY"}}}},
		{"type": "user", "uuid": "two", "parentUuid": "reply-one", "message": map[string]any{"content": "repeat\nNo saved preferences are currently enabled. Earlier preferences may have been forgotten; do not rely on them.\n"}},
		{"type": "assistant", "uuid": "reply-two", "parentUuid": "two", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": "FUTURE SECRET"}}}},
		{"type": "summary", "summary": "FUTURE COMPACTION"},
	}
	var original strings.Builder
	for _, row := range records {
		row["sessionId"] = sid
		data, _ := json.Marshal(row)
		original.Write(data)
		original.WriteByte('\n')
	}
	writeFile(t, path, original.String())
	visible := []Item{{Key: "pocket-one", Kind: "user", Text: "repeat"}, {Key: "pocket-two", Kind: "user", Text: "repeat"}}
	id, copyPath, err := RewindClaude(home, sid, visible)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || id == sid {
		t.Fatal("not an independent session")
	}
	data, _ := os.ReadFile(copyPath)
	if strings.Contains(string(data), "FUTURE") || !strings.Contains(string(data), "FIRST REPLY") || strings.Contains(string(data), `"sessionId":"original"`) {
		t.Fatalf("wrong checkpoint: %s", data)
	}
	if !strings.Contains(string(data), `"parentUuid":"one"`) {
		t.Fatal("message ancestry lost")
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != original.String() {
		t.Fatal("original was modified")
	}
	fi, _ := os.Stat(copyPath)
	if fi.Mode().Perm() != 0600 {
		t.Fatal("session must be private")
	}
	id, copyPath, err = RewindClaude(home, sid, visible[:1])
	if err != nil || id != "" || copyPath != "" {
		t.Fatal("first message should start fresh", id, copyPath, err)
	}
	// A stopped send with no provider record can rewind from the end of the
	// verified prefix without inventing any future context.
	stopped := append(append([]Item{}, visible...), Item{Key: "never-recorded", Text: "stopped before dispatch"})
	_, stoppedPath, err := RewindClaude(home, sid, stopped)
	if err != nil {
		t.Fatal("stopped send", err)
	}
	stoppedData, _ := os.ReadFile(stoppedPath)
	if !strings.Contains(string(stoppedData), "FUTURE SECRET") {
		t.Fatal("previous completed reply lost")
	}
	visible[1].Text = "missing"
	if _, _, err := RewindClaude(home, sid, visible); err == nil {
		t.Fatal("mismatched history accepted")
	}
	// Imported messages use the provider UUID, even if earlier UI history is partial.
	id, _, err = RewindClaude(home, sid, []Item{{Key: "u-two", Kind: "user", Text: "repeat"}})
	if err != nil || id == "" {
		t.Fatal("imported message not found", err)
	}
}

func TestRewindCodexUsesNativeBoundary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/usr/bin/env python3
import sys,json,os
turns=[{'id':'t1','status':'completed','items':[{'id':'u1','type':'userMessage','content':[{'type':'text','text':'one'}]}]}, {'id':'t2','status':'interrupted','items':[{'id':'u2','type':'userMessage','content':[{'type':'text','text':'two'}]}]}]
for line in sys.stdin:
 m=json.loads(line)
 if 'id' not in m: continue
 method=m['method']
 if method=='initialize': result={}
 elif method=='thread/read':
  assert m['params']['threadId']=='original'
  result={'thread':{'id':'original','turns':turns}}
 elif method=='thread/fork':
  assert m['params']['threadId']=='original'
  assert m['params']['lastTurnId']=='t1'
  result={'thread':{'id':'copy','turns':turns if os.environ.get('IGNORE_BOUNDARY') else turns[:1]}}
 else: raise Exception('Unexpected call, must never send a turn: '+method)
 print(json.dumps({'id':m['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	visible := []Item{{Key: "pocket-one", Text: "one"}, {Key: "pocket-two", Text: "two"}}
	id, err := RewindCodex(context.Background(), bin, dir, nil, "original", visible)
	if err != nil || id != "copy" {
		t.Fatal(id, err)
	}
	id, err = RewindCodex(context.Background(), bin, dir, nil, "original", visible[:1])
	if err != nil || id != "" {
		t.Fatal("first message must start fresh", id, err)
	}
	if _, err := RewindCodex(context.Background(), bin, dir, []string{"IGNORE_BOUNDARY=1"}, "original", visible); err == nil {
		t.Fatal("old CLI silently kept future context")
	}
}
