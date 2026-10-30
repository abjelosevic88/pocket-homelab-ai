package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func TestHoldsKeepKeys(t *testing.T) {
	hs := &wtHolds{m: map[string][]heldMsg{}}
	var sent []string
	add := func(key string) bool { return hs.add("t", key, func() { sent = append(sent, key) }) }
	if add("a") {
		t.Fatal("not on hold: nothing to keep")
	}
	hs.hold("t")
	add("a")
	add("b")
	add("c")
	add("d")
	if !hs.drop("t", "b") || hs.drop("t", "b") || hs.drop("t", "zz") {
		t.Fatal("drop finds a waiting message once")
	}
	if !hs.moveFirst("t", "c") || hs.moveFirst("t", "zz") || hs.count("t") != 3 {
		t.Fatal("moveFirst")
	}
	hs.release("t")
	if strings.Join(sent, ",") != "c,a,d" || hs.held("t") {
		t.Fatalf("sent %v", sent)
	}
	hs.hold("t")
	add("e")
	add("f")
	if keys := hs.clear("t"); strings.Join(keys, ",") != "e,f" || !hs.held("t") || hs.count("t") != 0 {
		t.Fatalf("clear: %v", keys)
	}
}

// itemKey is the key of the user item with this text.
func itemKey(t *testing.T, items []agent.Item, text string) string {
	t.Helper()
	for _, it := range items {
		if it.Kind == "user" && it.Text == text {
			return it.Key
		}
	}
	t.Fatalf("no message %q: %+v", text, items)
	return ""
}

// ✕ and Edit on messages that wait for a reconnected process: they are taken
// out of the hold, so they never go out when the process is done.
func TestHeldMessageDropAndEdit(t *testing.T) {
	old := reattachPoll
	reattachPoll = 50 * time.Millisecond
	defer func() { reattachPoll = old }()
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "held@example.test", "admin", true)
	argsFile := fakeClaude(t, s, root)
	th := cutThread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	home := s.defaultHome("claude")
	logPath := filepath.Join(home, "projects", "-work", th.ProviderSessionID+".jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	if err := os.WriteFile(logPath, []byte(cutSessionLog()), 0o644); err != nil {
		t.Fatal(err)
	}
	// this test process plays the leftover agent (never ended: not Pocket AI's own)
	reg := filepath.Join(home, "sessions", strconv.Itoa(os.Getpid())+".json")
	_ = os.MkdirAll(filepath.Dir(reg), 0o755)
	defer os.Remove(reg)
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
	for _, text := range []string{"first", "second", "third"} {
		owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": text}, 200, nil)
	}
	items := threadItems(t, s, th.ID)
	second, third := itemKey(t, items, "second"), itemKey(t, items, "third")
	owner.want("DELETE", "/api/v1/threads/"+th.ID+"/queue/"+second, nil, 204, nil)
	var draft struct {
		Text string `json:"text"`
	}
	owner.want("POST", "/api/v1/threads/"+th.ID+"/queue/"+third+"/edit", nil, 200, &draft)
	if draft.Text != "third" {
		t.Fatalf("edit draft %q", draft.Text)
	}
	items = threadItems(t, s, th.ID)
	if n := s.holds().count(th.ID); n != 1 || countText(items, "user", "second") != 0 || countText(items, "user", "third") != 0 {
		t.Fatalf("%d waiting: %+v", n, items)
	}
	// taken out already: a second Edit says so
	owner.want("POST", "/api/v1/threads/"+th.ID+"/queue/"+third+"/edit", nil, 404, nil)
	// the leftover finishes: only "first" goes out
	setState("idle")
	waitFor(t, func() bool { _, err := os.Stat(argsFile); return err == nil })
	waitFor(t, func() bool { return !s.isReattached(th.ID) })
	r := s.rt.get(th.ID)
	if r == nil {
		t.Fatal("no process for the waiting message")
	}
	r.mu.Lock()
	queue, running := len(r.queue), r.running
	r.mu.Unlock()
	if queue != 0 || !running {
		t.Fatalf("queue %d running %v: dropped messages went out", queue, running)
	}
}

// Send now on a message that waits for a reconnected process: the process
// is ended, then this message goes first and the others after it.
func TestHeldMessageSendNowEndsTheLeftover(t *testing.T) {
	old := reattachPoll
	reattachPoll = 50 * time.Millisecond
	defer func() { reattachPoll = old }()
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "heldnow@example.test", "admin", true)
	argsFile := fakeClaude(t, s, root)
	th := cutThread(t, s, u.ID, root)
	defer s.rt.stop(th.ID)
	home := s.defaultHome("claude")
	logPath := filepath.Join(home, "projects", "-work", th.ProviderSessionID+".jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	full := cutSessionLog()
	cut := strings.Index(full, `"uuid":"x2"`)
	cut = strings.LastIndex(full[:cut], "\n") + 1
	if err := os.WriteFile(logPath, []byte(full[:cut]), 0o644); err != nil {
		t.Fatal(err)
	}
	// the leftover: looks like a Pocket AI agent, still busy
	leftover := exec.Command("sh", "-c", "sleep 300 & wait", "sh", "--input-format", "stream-json", "--permission-prompt-tool", "stdio")
	leftover.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leftover.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-leftover.Process.Pid, syscall.SIGKILL) }()
	exited := make(chan struct{})
	go func() { _ = leftover.Wait(); close(exited) }()
	reg := filepath.Join(home, "sessions", strconv.Itoa(leftover.Process.Pid)+".json")
	_ = os.MkdirAll(filepath.Dir(reg), 0o755)
	b, _ := json.Marshal(map[string]any{"pid": leftover.Process.Pid, "sessionId": th.ProviderSessionID, "status": "busy"})
	if err := os.WriteFile(reg, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s.reconnect(th.ID)
	if !s.isReattached(th.ID) {
		t.Fatal("not reconnected")
	}
	owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "first"}, 200, nil)
	owner.want("POST", "/api/v1/threads/"+th.ID+"/messages", map[string]any{"text": "urgent"}, 200, nil)
	urgent := itemKey(t, threadItems(t, s, th.ID), "urgent")
	owner.want("POST", "/api/v1/threads/"+th.ID+"/queue/"+urgent+"/now", nil, 204, nil)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the leftover still runs")
	}
	waitFor(t, func() bool { _, err := os.Stat(argsFile); return err == nil })
	waitFor(t, func() bool { return !s.isReattached(th.ID) })
	r := s.rt.get(th.ID)
	if r == nil {
		t.Fatal("no new process")
	}
	waitFor(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.queue) == 1
	})
	r.mu.Lock()
	next := r.queue[0].key
	r.mu.Unlock()
	items := threadItems(t, s, th.ID)
	if next != itemKey(t, items, "first") {
		t.Fatalf("queued next: %s", next)
	}
	stopped := 0
	for _, it := range items {
		if it.Kind == "user" && it.Text == "urgent" && it.Queued {
			t.Fatalf("urgent still waits: %+v", it)
		}
		if it.Kind == "notice" && strings.HasPrefix(it.Text, "Stopped.") {
			stopped++
		}
	}
	if stopped != 1 || countText(items, "user", "first") != 1 || countText(items, "user", "urgent") != 1 {
		t.Fatalf("stopped notices %d: %+v", stopped, items)
	}
}
