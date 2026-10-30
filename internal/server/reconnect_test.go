package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestParseProbe(t *testing.T) {
	p, err := parseProbe("state\tbusy\t4242\t1\t1234\ndata\taGVsbG8K\n")
	if err != nil || p.state != "busy" || p.pid != 4242 || !p.own || p.size != 1234 || string(p.data) != "hello\n" {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := parseProbe("state\tweird\t\t0\t-1\n"); err != nil || p.state != "gone" || p.size != -1 || p.own {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parseProbe("Permission denied\n"); err == nil {
		t.Fatal("no state line must be an error")
	}
}

// claudeLine is one session log line.
func claudeLine(typ, uuid string, content any, stop string) string {
	msg := map[string]any{"content": content}
	if stop != "" {
		msg["stop_reason"] = stop
	}
	b, _ := json.Marshal(map[string]any{"type": typ, "uuid": uuid, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "message": msg})
	return string(b) + "\n"
}

// A session log as the leftover process left it: an earlier turn, then the
// cut-off one, of which Pocket AI showed the prompt, the first text and the
// first tool call (cut, no output) live before the restart.
func cutSessionLog() string {
	return claudeLine("user", "p0", "an earlier question", "") +
		claudeLine("assistant", "x0", []any{map[string]any{"type": "text", "text": "an earlier answer"}}, "end_turn") +
		claudeLine("user", "p1", "fix the build", "") +
		claudeLine("assistant", "x1", []any{map[string]any{"type": "text", "text": "Looking at the build."},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "make"}}}, "") +
		claudeLine("user", "r1", []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok: built"}}, "") +
		claudeLine("assistant", "x2", []any{map[string]any{"type": "tool_use", "id": "toolu_2", "name": "Bash", "input": map[string]any{"command": "make test"}}}, "") +
		claudeLine("user", "r2", []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": "PASS"}}, "") +
		claudeLine("assistant", "x3", []any{map[string]any{"type": "text", "text": "The build works again."}}, "end_turn")
}

func cutThread(t *testing.T, s *Server, uid int64, cwd string) *store.Thread {
	t.Helper()
	th := &store.Thread{ID: "cut-" + strconv.FormatInt(time.Now().UnixNano(), 36), UserID: uid, Provider: "claude", Mode: "default", Cwd: cwd,
		Title: "Fix the build", ProviderSessionID: "11111111-2222-3333-4444-" + strconv.FormatInt(time.Now().UnixNano()%1e12, 10)}
	if err := s.store.CreateThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	for _, it := range []agent.Item{
		{Key: "u-old", Kind: "user", Text: "an earlier question"}, {Key: "c-old", Kind: "assistant", Text: "an earlier answer"},
		{Key: "u-live", Kind: "user", Text: "fix the build"}, {Key: "c-msg_1-0", Kind: "assistant", Text: "Looking at the build."},
		{Key: "toolu_1", Kind: "tool", Tool: &agent.Tool{Kind: "shell", Title: "make", Status: "error"}},
	} {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(context.Background(), th.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	return th
}

func threadItems(t *testing.T, s *Server, id string) []agent.Item {
	t.Helper()
	list, err := s.store.ListItems(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []agent.Item{}
	for _, st := range list {
		var it agent.Item
		_ = json.Unmarshal(st.Data, &it)
		out = append(out, it)
	}
	return out
}

func countText(items []agent.Item, kind, text string) int {
	n := 0
	for _, it := range items {
		if it.Kind == kind && it.Text == text {
			n++
		}
	}
	return n
}

// The catch-up shows what the leftover process did after the restart, once:
// no second prompt or first text, the cut tool gets its output, nothing of
// the earlier turn comes back.
func TestLeftoverCatchUpShowsTheRestOnce(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "catchup@example.test", "admin", true)
	th := cutThread(t, s, u.ID, root)
	ap := s.newLeftoverApplier(context.Background(), th)
	log := cutSessionLog()
	// read from the middle of the first line, like a look at the log's end
	if next := ap.catchUp([]byte(log[5:]), 5); next != int64(len(log)) {
		t.Fatalf("next %d, want %d", next, len(log))
	}
	if !ap.ended {
		t.Fatal("the turn ended in the log")
	}
	items := threadItems(t, s, th.ID)
	for _, c := range []struct {
		kind, text string
		n          int
	}{{"user", "fix the build", 1}, {"assistant", "Looking at the build.", 1}, {"assistant", "The build works again.", 1},
		{"user", "an earlier question", 1}, {"assistant", "an earlier answer", 1}} {
		if got := countText(items, c.kind, c.text); got != c.n {
			t.Fatalf("%s %q: %d times, want %d: %+v", c.kind, c.text, got, c.n, items)
		}
	}
	tools := map[string]*agent.Tool{}
	turns := 0
	for _, it := range items {
		if it.Kind == "tool" {
			tools[it.Key] = it.Tool
		}
		if it.Kind == "turn" {
			turns++
		}
	}
	if tools["toolu_1"] == nil || tools["toolu_1"].Status != "done" || tools["toolu_1"].Output != "ok: built" {
		t.Fatalf("cut tool: %+v", tools["toolu_1"])
	}
	if tools["toolu_2"] == nil || tools["toolu_2"].Output != "PASS" {
		t.Fatalf("new tool: %+v", tools["toolu_2"])
	}
	if turns != 1 {
		t.Fatalf("turn footers: %d", turns)
	}
	// later looks add only what is new
	more := claudeLine("user", "p2", "and the docs?", "")
	if next := ap.feed([]byte(more), int64(len(log))); next != int64(len(log)+len(more)) || ap.ended {
		t.Fatalf("feed: %d ended=%v", next, ap.ended)
	}
	if got := countText(threadItems(t, s, th.ID), "user", "and the docs?"); got != 1 {
		t.Fatalf("new prompt: %d", got)
	}
}

// A turn whose process is gone continues by itself — once: a second cut
// right after asks the person instead of looping.
func TestCutTurnContinuesOnItsOwnOnce(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "cont@example.test", "admin", true)
	argsFile := fakeClaude(t, s, root)
	th := cutThread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	s.reconnect(th.ID)
	waitFor(t, func() bool { _, err := os.Stat(argsFile); return err == nil })
	b, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(b), th.ProviderSessionID) {
		t.Fatalf("not resumed: %s", b)
	}
	items := threadItems(t, s, th.ID)
	last := items[len(items)-1]
	if last.Kind != "user" || last.Text != autoContinueText || last.By != "Pocket AI" {
		t.Fatalf("last item: %+v", last)
	}
	s.rt.stop(th.ID)
	waitFor(t, func() bool { return s.rt.get(th.ID) == nil })
	_ = os.Remove(argsFile)
	s.reconnect(th.ID)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("continued twice in a row")
	}
	items = threadItems(t, s, th.ID)
	asked := false
	for _, it := range items {
		asked = asked || (it.Kind == "notice" && strings.Contains(it.Text, "Send a message"))
	}
	if !asked || countText(items, "user", autoContinueText) != 1 {
		t.Fatalf("want one auto continue, then the ask-the-person notice: %+v", items)
	}
}

// A cut turn whose process still works: Pocket AI reconnects, shows its
// progress, holds a message sent meanwhile (no second process), and sends it
// once the process is done.
func TestCutTurnReconnectsToAWorkingProcess(t *testing.T) {
	old := reattachPoll
	reattachPoll = 50 * time.Millisecond
	defer func() { reattachPoll = old }()
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "reattach@example.test", "admin", true)
	argsFile := fakeClaude(t, s, root)
	th := cutThread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	home := s.defaultHome("claude")
	logPath := filepath.Join(home, "projects", "-work", th.ProviderSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	full := cutSessionLog()
	cut := strings.Index(full, `"uuid":"x2"`)
	cut = strings.LastIndex(full[:cut], "\n") + 1
	if err := os.WriteFile(logPath, []byte(full[:cut]), 0o644); err != nil {
		t.Fatal(err)
	}
	// this test process plays the leftover agent (never ended: not Pocket AI's own)
	reg := filepath.Join(home, "sessions", strconv.Itoa(os.Getpid())+".json")
	_ = os.MkdirAll(filepath.Dir(reg), 0o755)
	setState := func(st string) {
		b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": th.ProviderSessionID, "status": st})
		if err := os.WriteFile(reg, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setState("busy")
	s.reconnect(th.ID)
	if !s.isReattached(th.ID) {
		t.Fatal("not reconnected")
	}
	got, _ := s.store.GetThreadAny(context.Background(), th.ID)
	if got = s.rt.decorate(got); got.Status != "running" || !got.Reattached {
		t.Fatalf("thread: status %s reattached %v", got.Status, got.Reattached)
	}
	var resp struct {
		Queued bool `json:"queued"`
	}
	owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "also run the tests"}, 200, &resp)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("a second process started next to the working one")
	}
	// the leftover goes on and finishes
	if err := os.WriteFile(logPath, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return countText(threadItems(t, s, th.ID), "assistant", "The build works again.") == 1
	})
	setState("idle")
	waitFor(t, func() bool { _, err := os.Stat(argsFile); return err == nil })
	if s.isReattached(th.ID) {
		t.Fatal("still reconnected")
	}
	items := threadItems(t, s, th.ID)
	if countText(items, "assistant", "Looking at the build.") != 1 || countText(items, "user", "fix the build") != 1 || countText(items, "user", autoContinueText) != 0 {
		t.Fatalf("items: %+v", items)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
