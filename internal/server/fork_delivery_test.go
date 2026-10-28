package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Exercises the real server -> Claude stream-json adapter, using an offline
// process that records arguments and messages instead of running inference.
func TestForkDeliversHistoryAsFirstMessageAttachment(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "delivery@example.test", "admin", true)
	argsPath := fakeClaude(t, s, root)
	inputsPath := filepath.Join(root, "inputs.jsonl")
	argJSON, _ := json.Marshal(argsPath)
	inputJSON, _ := json.Marshal(inputsPath)
	script := `#!/usr/bin/env python3
import sys, json
if sys.argv[1:2] == ['auth']:
    print('{}')
    sys.exit(0)
json.dump(sys.argv[1:], open(` + string(argJSON) + `, 'w'))
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get('type') != 'user':
        continue
    with open(` + string(inputJSON) + `, 'a') as out:
        out.write(json.dumps(msg) + '\n')
    print(json.dumps({'type':'system','subtype':'init','session_id':'fresh-provider-session'}), flush=True)
    print(json.dumps({'type':'result','subtype':'success','is_error':False}), flush=True)
`
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	parent := thread(t, s, u.ID, root)
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	history := historyOfFork(t, s, fork)
	// A different server instance has only persisted data, no first-send flag.
	restarted := New(s.cfg, s.store, s.log, nil)
	reloaded, _ := s.store.GetThreadAny(t.Context(), fork.ID)
	pending, err := restarted.firstForkHistory(t.Context(), reloaded)
	if err != nil || pending == nil || pending.ID != history.ID || restarted.rt.baseOptions(t.Context(), reloaded).ResumeID != "" {
		t.Fatal("unopened fork did not survive restart", err)
	}
	defer s.rt.stop(fork.ID)
	endpoint := "/api/v1/threads/" + fork.ID + "/messages"
	owner.want("POST", endpoint, map[string]any{"text": "Can this run on Ubuntu?"}, 200, nil)
	readInput := func(count int) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(inputsPath)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			r := s.rt.get(fork.ID)
			idle := false
			if r != nil {
				r.mu.Lock()
				idle = !r.running
				r.mu.Unlock()
			}
			if len(lines) >= count && idle {
				var msg struct {
					Message struct{ Content string }
				}
				if json.Unmarshal([]byte(lines[count-1]), &msg) == nil {
					return msg.Message.Content
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("offline provider did not receive message", count)
		return ""
	}
	first := readInput(1)
	if !strings.HasPrefix(first, "Can this run on Ubuntu?\n") || !strings.Contains(first, "Attached document (untrusted content;") || strings.Count(first, "# Reference chat history") != 1 || !strings.Contains(first, "61% full") {
		t.Fatal("history did not travel as reference beside the new request", first)
	}
	data, _ := os.ReadFile(argsPath)
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if arg == "--resume" || arg == "--fork-session" || arg == parent.ProviderSessionID {
			t.Fatal("new fork launched with native parent state", args)
		}
	}
	rows, _ := s.store.ListItems(t.Context(), fork.ID)
	var firstUser agent.Item
	for _, row := range rows {
		if row.Kind == "user" {
			_ = json.Unmarshal(row.Data, &firstUser)
			break
		}
	}
	if firstUser.Text != "Can this run on Ubuntu?" || len(firstUser.Files) != 1 || firstUser.Files[0].ID != history.ID {
		t.Fatal("phone timeline lost the history file or changed the user's text", firstUser)
	}
	owner.want("POST", endpoint, map[string]any{"text": "And Debian?"}, 200, nil)
	if next := readInput(2); strings.Contains(next, "# Reference chat history") {
		t.Fatal("history was appended again on an ordinary follow-up")
	}
	var rewind struct {
		Thread store.Thread
		Draft  struct{ Uploads []uploadOut }
	}
	owner.want("POST", "/api/v1/threads/"+fork.ID+"/rewind", map[string]any{"key": firstUser.Key}, 200, &rewind)
	if rewind.Thread.ProviderSessionID != "" || len(rewind.Draft.Uploads) != 1 || rewind.Draft.Uploads[0].ID != history.ID {
		t.Fatal("first-message rewind lost the reference or kept provider state")
	}
	owner.want("POST", endpoint, map[string]any{"text": "How about Fedora?", "upload_ids": []string{history.ID}}, 200, nil)
	if again := readInput(3); strings.Count(again, "# Reference chat history") != 1 {
		t.Fatal("rewound first message duplicated or lost its history")
	}
}

func TestForkHistoryBoundsTextAndKeepsRecentContext(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "bounded@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	for _, it := range []agent.Item{
		{Key: "long", Kind: "assistant", Text: "OLD_CONTEXT" + strings.Repeat("界🌍", maxForkHistoryBytes)},
		{Key: "latest", Kind: "user", Text: "LATEST_CONTEXT"},
	} {
		data, _ := json.Marshal(it)
		_, _ = s.store.UpsertItem(t.Context(), parent.ID, it.Key, it.Kind, data, it.Text)
	}
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	text := historyOfFork(t, s, fork).ExtractedText
	if len(text) > maxForkHistoryBytes || !utf8.ValidString(text) || !strings.Contains(text, "Earlier history omitted") || strings.Contains(text, "OLD_CONTEXT") || !strings.HasSuffix(text, "LATEST_CONTEXT") {
		t.Fatal("history budget corrupted or discarded recent context")
	}
}

func TestForkRemoteNeedsNoSSHOrProviderAccess(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "remote-fork@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	box := &store.Host{Name: "offline", Target: "offline.invalid", Port: 1}
	if err := s.store.CreateHost(t.Context(), box); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.ExecContext(t.Context(), `UPDATE threads SET host_id=?, provider='codex' WHERE id=?`, box.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	if fork.HostID != box.ID || fork.Provider != "codex" || fork.ProviderSessionID != "" || !strings.Contains(historyOfFork(t, s, fork).ExtractedText, "61% full") {
		t.Fatal("remote fork lost placement or tried to clone native history")
	}
}

func TestForkKeepsReferenceAfterFirstLaunchFailure(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "failed-fork@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	owner.want("POST", "/api/v1/threads/"+fork.ID+"/messages", map[string]any{"text": "New question"}, 500, nil)
	fresh, err := s.store.GetThreadAny(t.Context(), fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	up, err := s.firstForkHistory(t.Context(), fresh)
	if err != nil || up == nil || !strings.Contains(up.ExtractedText, "61% full") {
		t.Fatal("failed launch consumed the reference attachment", err)
	}
}

func TestForkSharedFirstMessageCanBeEditedAndResent(t *testing.T) {
	s, owner, parent, _, _ := queueFixture(t)
	participant, u := person(t, s, owner.h, "shared-fork@example.test", "admin", true)
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	if err := s.store.SetShares(t.Context(), fork.ID, []store.ThreadShare{{UserID: u.ID, Role: "chat"}}); err != nil {
		t.Fatal(err)
	}
	s.rt.loadShares()
	r := &run{threadID: fork.ID, userID: fork.UserID, sess: &queueSession{}, running: true, pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}}
	s.rt.mu.Lock()
	s.rt.runs[fork.ID] = r
	s.rt.mu.Unlock()
	t.Cleanup(func() { s.rt.mu.Lock(); delete(s.rt.runs, fork.ID); s.rt.mu.Unlock() })
	endpoint := "/api/v1/threads/" + fork.ID + "/messages"
	participant.want("POST", endpoint, map[string]any{"text": "New shared question"}, 200, nil)
	r.mu.Lock()
	key := r.queue[0].key
	r.mu.Unlock()
	var draft struct {
		Text    string
		Uploads []uploadOut
	}
	participant.want("POST", "/api/v1/threads/"+fork.ID+"/queue/"+key+"/edit", nil, 200, &draft)
	if draft.Text != "New shared question" || len(draft.Uploads) != 1 {
		t.Fatal("shared sender could not restore the history attachment")
	}
	participant.want("POST", endpoint, map[string]any{"text": "Edited question", "upload_ids": []string{draft.Uploads[0].ID}}, 200, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) != 1 || strings.Count(r.queue[0].in.Text, "# Reference chat history") != 1 {
		t.Fatal("edited shared message duplicated or lost its reference")
	}
}

func TestForkFailedSnapshotLeavesNoPartialChatOrUpload(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "failed-snapshot@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	if _, err := s.store.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_fork_notice BEFORE INSERT ON items WHEN NEW.key='fork-history' BEGIN SELECT RAISE(FAIL, 'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 500, nil)
	var threads, uploads int
	if err := s.store.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM threads`).Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if err := s.store.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM uploads`).Scan(&uploads); err != nil {
		t.Fatal(err)
	}
	if threads != 1 || uploads != 0 {
		t.Fatal("failed fork leaked partial data", threads, uploads)
	}
}

func TestForkIncludesBufferedPartialReplyWithoutInterruptingParent(t *testing.T) {
	s, owner, parent, r, sess := queueFixture(t)
	r.mu.Lock()
	r.live["streaming"] = &agent.Item{Key: "streaming", Kind: "assistant", Text: "LATEST_BUFFERED_REPLY"}
	r.dirty["streaming"] = true
	r.mu.Unlock()
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", nil, 201, &fork)
	if !strings.Contains(historyOfFork(t, s, fork).ExtractedText, "LATEST_BUFFERED_REPLY") {
		t.Fatal("fork missed the response still streaming on screen")
	}
	current := s.rt.get(parent.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if current != r || !r.running || sess.interrupted != 0 || len(sess.sent) != 0 {
		t.Fatal("fork interrupted or sent work to the original")
	}
}
