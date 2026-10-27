package agent

import (
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

// A protocol fixture, not a model run. Unexpected turn/start calls fail.
func rewindCodexFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/usr/bin/env python3
import sys,json,os
mode=os.environ.get('REWIND_SCENARIO','legacy')
turns=[{'id':'t1','status':'completed','items':[{'id':'u1','type':'userMessage','content':[{'type':'text','text':'one'}]}]}, {'id':'t2','status':'interrupted','items':[{'id':'u2','type':'userMessage','content':[{'type':'text','text':'two'}]}]}]
if mode=='active': turns[0]['status']='inProgress'
if mode=='missing-old-users':
 for t in turns: t['items']=[]
if mode=='steered': turns[0]['items'].extend(turns.pop()['items'])
for line in sys.stdin:
 m=json.loads(line)
 if 'id' not in m: continue
 method=m['method']; p=m.get('params',{})
 if method=='initialize':
  assert p['capabilities']['experimentalApi'] is True
  result={}
 elif method=='thread/read':
  sid=p['threadId']; assert sid in ('original','copy')
  data=turns if sid=='original' or mode=='ignore' else turns[:1]
  if sid=='copy' and mode=='altered': data=[dict(data[0],id='wrong-turn')]
  result={'thread':{'id':sid,'historyMode':'paginated' if mode in ('paginated','cursor-loop') else 'legacy','turns':data if p.get('includeTurns') else []}}
 elif method=='thread/turns/list':
  assert p['sortDirection']=='asc' and p['itemsView']=='full'
  data=turns if p['threadId']=='original' else turns[:1]
  index=int(p.get('cursor','0'))
  result={'data':data[index:index+1], 'nextCursor':str(index+1) if index+1<len(data) else None}
  if mode=='cursor-loop': result['nextCursor']='1'
 elif method=='thread/fork':
  assert p['threadId']=='original'
  if mode!='full-fork': assert p['lastTurnId']=='t1'
  else: assert 'lastTurnId' not in p
  assert os.path.samefile(p['cwd'],os.getcwd())
  assert p['model']=='test-model' and p['serviceTier']=='fast'
  assert p['approvalPolicy']=='never' and p['sandbox']=='danger-full-access'
  assert p['config']['model_reasoning_effort']=='high'
  assert p['deferGoalContinuation'] is True
  result={'thread':{'id':'original' if mode=='same-id' else 'copy','turns':[]}}
 else: raise Exception('Unexpected call, must never send a turn: '+method)
 print(json.dumps({'id':m['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestRewindCodexUsesNativeBoundary(t *testing.T) {
	bin, dir := rewindCodexFixture(t)
	visible := []Item{{Key: "pocket-one", Text: "one"}, {Key: "pocket-two", Text: "two"}}
	options := Options{Bin: bin, Cwd: dir, ResumeID: "original", Model: "test-model", Effort: "high", Mode: "full-access", Speed: "fast"}
	for _, scenario := range []string{"legacy", "paginated"} {
		t.Run(scenario, func(t *testing.T) {
			o := options
			o.Env = []string{"REWIND_SCENARIO=" + scenario}
			id, err := RewindCodex(t.Context(), o, visible)
			if err != nil || id != "copy" {
				t.Fatal(id, err)
			}
			id, err = RewindCodex(t.Context(), o, visible[:1])
			if err != nil || id != "" {
				t.Fatal("first message must start fresh", id, err)
			}
		})
	}
	for _, scenario := range []string{"ignore", "same-id", "altered", "active", "steered", "cursor-loop"} {
		t.Run(scenario, func(t *testing.T) {
			o := options
			o.Env = []string{"REWIND_SCENARIO=" + scenario}
			if id, err := RewindCodex(t.Context(), o, visible); err == nil {
				t.Fatal("unsafe checkpoint accepted", id)
			}
		})
	}
	t.Run("history mismatch", func(t *testing.T) {
		if _, err := RewindCodex(t.Context(), options, []Item{{Text: "wrong"}, {Text: "two"}}); err == nil {
			t.Fatal("mismatched history accepted")
		}
	})
}

func TestRewindCodexUsesSavedReplyAfterMissingUserHistory(t *testing.T) {
	bin, dir := rewindCodexFixture(t)
	o := Options{Bin: bin, Cwd: dir, ResumeID: "original", Model: "test-model", Effort: "high", Mode: "full-access", Speed: "fast", Env: []string{"REWIND_SCENARIO=missing-old-users"}}
	visible := []Item{{Text: "older prompt"}, {Text: "interrupted prompt"}, {Text: "Status?"}}
	// Reproduce the reported error before supplying the stable reply anchor.
	if _, err := RewindCodex(t.Context(), o, visible); err == nil || !strings.Contains(err.Error(), "not in the agent history") {
		t.Fatal("fixture does not reproduce the old failure", err)
	}
	id, err := RewindCodex(t.Context(), o, visible, "t1")
	if err != nil || id != "copy" {
		t.Fatal("valid reply boundary was blocked by missing user records", id, err)
	}
	if _, err := RewindCodex(t.Context(), o, visible, "missing-turn"); err == nil {
		t.Fatal("unknown checkpoint was guessed")
	}
}

func TestRewindCodexOverSSH(t *testing.T) {
	bin, dir := rewindCodexFixture(t)
	marker := filepath.Join(dir, "ssh-called")
	ssh := `#!/usr/bin/env python3
import sys,os,base64,re
args=sys.argv[1:]
assert args[args.index('--')-1]=='test-remote'
assert args[args.index('-p')+1]=='2222'
open(os.environ['SSH_TEST_MARKER'],'w').write('remote transport used')
encoded=re.search(r'echo ([A-Za-z0-9+/=]+)',args[-1]).group(1)
os.execv('/bin/sh',['/bin/sh','-c',base64.b64decode(encoded).decode()])
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_TEST_MARKER", marker)
	o := Options{Bin: bin, Cwd: dir, ResumeID: "original", Model: "test-model", Effort: "high", Mode: "full-access", Speed: "fast", Env: []string{"REWIND_SCENARIO=paginated"}, Remote: &Remote{Target: "test-remote", Port: 2222}}
	id, err := RewindCodex(t.Context(), o, []Item{{Text: "one"}, {Text: "two"}})
	if err != nil || id != "copy" {
		t.Fatal(id, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("SSH transport was bypassed", err)
	}
}

// Opt in with an installed CLI. Uses an isolated CODEX_HOME and synthetic
// history only; it never signs in, contacts a model, or touches real sessions.
func TestRewindCodexRealCLIWithMissingOldUserRecords(t *testing.T) {
	bin := os.Getenv("POCKET_AI_TEST_CODEX_BIN")
	if bin == "" {
		t.Skip("set POCKET_AI_TEST_CODEX_BIN for the offline CLI integration check")
	}
	dir := t.TempDir()
	sid := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(dir, "sessions", "2026", "10", "26", "rollout-2026-10-26T21-00-00-"+sid+".jsonl")
	var log strings.Builder
	add := func(kind string, payload map[string]any) {
		data, err := json.Marshal(map[string]any{"timestamp": "2026-10-26T21:00:00.000Z", "type": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		log.Write(data)
		log.WriteByte('\n')
	}
	add("session_meta", map[string]any{"id": sid, "timestamp": "2026-10-26T21:00:00.000Z", "cwd": dir, "originator": "codex_cli_rs", "cli_version": "0.163.0", "source": "cli", "model_provider": "openai"})
	for _, turn := range []string{"turn-1", "turn-2"} {
		add("event_msg", map[string]any{"type": "task_started", "turn_id": turn, "model_context_window": 200000})
		add("turn_context", map[string]any{"turn_id": turn, "cwd": dir, "approval_policy": "never", "sandbox_policy": map[string]any{"type": "danger-full-access"}, "model": "gpt-6.1-sol", "summary": "auto"})
		// Both user records are absent; the real saved turn boundaries remain.
		add("response_item", map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "Reply for " + turn}}})
		add("event_msg", map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "Reply for " + turn})
	}
	writeFile(t, path, log.String())
	o := Options{Bin: bin, Cwd: dir, ResumeID: sid, Mode: "full-access", Env: []string{"CODEX_HOME=" + dir}}
	id, err := RewindCodex(t.Context(), o, []Item{{Text: "Old message"}, {Text: "Interrupted message"}, {Text: "Status?"}}, "turn-1")
	if err != nil || id == "" || id == sid {
		t.Fatal("could not use native saved checkpoint", id, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != log.String() {
		t.Fatal("original history was changed", err)
	}
}

func TestForkClaudeFreezesIndependentSession(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "projects", "test", "original.jsonl")
	original := `{"type":"user","uuid":"u1","sessionId":"original","message":{"content":"hello"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"original","message":{"content":[{"type":"text","text":"snapshot"}]}}
`
	writeFile(t, source, original)
	id, cloned, err := ForkClaude(home, "original")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || id == "original" {
		t.Fatal("not private")
	}
	before, _ := os.ReadFile(cloned)
	f, _ := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"type":"user","uuid":"later","message":{"content":"FUTURE"}}` + "\n")
	f.Close()
	after, _ := os.ReadFile(cloned)
	if string(before) != string(after) || strings.Contains(string(after), "FUTURE") || strings.Contains(string(after), `"sessionId":"original"`) {
		t.Fatal("fork changed with parent")
	}
	os.Remove(source)
	if _, err := os.Stat(cloned); err != nil {
		t.Fatal("fork depended on parent", err)
	}
}

func TestForkCodexCreatesIndependentSessionWithoutInference(t *testing.T) {
	bin, dir := rewindCodexFixture(t)
	o := Options{Bin: bin, Cwd: dir, ResumeID: "original", Model: "test-model", Effort: "high", Mode: "full-access", Speed: "fast", Env: []string{"REWIND_SCENARIO=full-fork"}}
	id, err := ForkCodex(t.Context(), o)
	if err != nil || id != "copy" {
		t.Fatal(id, err)
	}
	o.Env = []string{"REWIND_SCENARIO=same-id"}
	// Use a separate fixture that accepts a whole-history fork but returns the parent ID.
	data, _ := os.ReadFile(bin)
	data = []byte(strings.ReplaceAll(string(data), "if mode!='full-fork':", "if mode not in ('full-fork','same-id'):"))
	if err := os.WriteFile(bin, data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ForkCodex(t.Context(), o); err == nil {
		t.Fatal("accepted shared provider session")
	}
}
