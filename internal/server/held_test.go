package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// slowClaude answers a message with a reply, a pause long enough to restart
// Pocket AI in, a second reply and the result.
const slowClaude = `#!/bin/sh
n=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      n=$((n+1))
      printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-held"}'
      printf '%s\n' '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m'$n'a"}}}'
      printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}'
      printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first '$n'"}}}'
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'a","content":[{"type":"text","text":"first '$n'"}]}}'
      sleep 3
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'b","content":[{"type":"text","text":"second '$n'"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}';;
  esac
done`

// A redeploy in the middle of a turn: the old Pocket AI lets go of the
// agent, the new one takes it back at boot, and the turn finishes as if
// nothing happened.
func TestRestartTakesBackRunningAgent(t *testing.T) { restartTakeBack(t, false) }

// The turn ends while Pocket AI is down: the next one still shows its last
// reply and its end (it replays the whole last turn).
func TestRestartTakesBackFinishedTurn(t *testing.T) { restartTakeBack(t, true) }

func restartTakeBack(t *testing.T, finishWhileDown bool) {
	if _, _, ok := hold.Binary(runtime.GOOS, runtime.GOARCH); !ok {
		t.Skip("pocket-hold not built in (go generate ./internal/hold)")
	}
	home, err := os.MkdirTemp("/tmp", "pr") // short: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("POCKET_HOLD", "")

	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AUTH_MODE", "local")
	t.Setenv("CORE_URL", "")
	t.Setenv("BASE_URL", "https://ai.example")
	t.Setenv("AGENT_HOME", root)
	t.Setenv("ROOTS", root)
	bin := filepath.Join(root, "claude")
	if err := os.WriteFile(bin, []byte(slowClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_BIN", bin)
	t.Setenv("CODEX_BIN", filepath.Join(root, "no-codex"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s1 := New(cfg, st, log, nil)
	ctx1, stop1 := context.WithCancel(context.Background())
	defer stop1()
	if err := s1.Bootstrap(ctx1); err != nil {
		t.Fatal(err)
	}
	u := &store.User{Email: "held@example.test", Name: "Held", Role: "admin", CanRun: true}
	if err := st.CreateUser(ctx1, u); err != nil {
		t.Fatal(err)
	}
	th := &store.Thread{ID: "0b0c1f9e-held-4f00-9a00-000000000001", UserID: u.ID, Provider: "claude", Mode: "default", Cwd: root, Title: "Long job"}
	if err := st.CreateThread(ctx1, th); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.rt.send(th, "u1", agent.Input{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	texts := func() map[string]int {
		items, _ := st.ListItems(context.Background(), th.ID)
		out := map[string]int{}
		for _, it := range items {
			var a agent.Item
			_ = json.Unmarshal(it.Data, &a)
			switch {
			case a.Kind == "assistant":
				out[a.Text]++
			case a.Kind == "turn":
				out["turn"]++
			case strings.HasPrefix(a.Key, "restart-"):
				out["restart notice"]++
			}
		}
		return out
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; !cond(); i++ {
			if i > 300 {
				t.Fatalf("timed out waiting for %s; items %v", what, texts())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("the first reply", func() bool { return texts()["first 1"] == 1 })

	// redeploy: this Pocket AI stops mid-turn
	stop1()
	waitFor("letting go", func() bool { st, ok, _ := hold.Status(th.ID); return ok && st.Busy && s1.rt.get(th.ID) == nil })
	if got, _ := st.GetThreadAny(context.Background(), th.ID); got.Status != "running" {
		t.Fatalf("status after letting go: %s", got.Status)
	}
	if finishWhileDown {
		waitFor("the turn to end unseen", func() bool { st, ok, _ := hold.Status(th.ID); return ok && !st.Busy })
	}

	// the new one takes the turn back at boot; what an app watching the
	// thread sees must not flicker: never idle before the turn ends, never a
	// reply blanked out to be streamed again
	s2 := New(cfg, st, log, nil)
	seen := s2.rt.subscribeThread(th.ID, u.ID)
	var flicker []string
	var sawReconnecting bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		ended := false
		for ev := range seen {
			switch ev.Name {
			case "thread":
				tt := ev.Data.(*store.Thread)
				sawReconnecting = sawReconnecting || tt.Reconnecting
				if tt.Status == "idle" && !ended && !finishWhileDown {
					flicker = append(flicker, "idle mid-turn")
				}
			case "item":
				it := ev.Data.(map[string]any)
				if it["kind"] == "turn" {
					ended = true
				}
				if it["kind"] == "assistant" && it["text"] == nil {
					flicker = append(flicker, "reply blanked: "+fmt.Sprint(it["key"]))
				}
				if it["kind"] == "notice" && strings.HasPrefix(fmt.Sprint(it["key"]), "restart-") {
					flicker = append(flicker, "restart notice")
				}
			}
		}
	}()
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	if err := s2.Bootstrap(ctx2); err != nil {
		t.Fatal(err)
	}
	waitFor("the turn to finish", func() bool {
		got, _ := st.GetThreadAny(context.Background(), th.ID)
		return texts()["turn"] == 1 && got.Status == "idle"
	})
	if x := texts(); x["first 1"] != 1 || x["second 1"] != 1 || x["restart notice"] != 0 {
		t.Fatalf("timeline after taking back: %v", x)
	}
	s2.rt.unsubscribeThread(th.ID, seen)
	close(seen)
	<-watched
	if len(flicker) > 0 || (!sawReconnecting && !finishWhileDown) {
		t.Fatalf("flicker while taking back: %v (reconnecting shown: %v)", flicker, sawReconnecting)
	}
	// and it goes on as usual
	if _, err := s2.rt.send(th, "u2", agent.Input{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	waitFor("the next turn", func() bool { return texts()["turn"] == 2 && texts()["second 2"] == 1 })
	s2.rt.stop(th.ID)
	waitFor("the agent to end", func() bool { _, ok, _ := hold.Status(th.ID); return !ok })
}

// While a held agent's connection is down, messages wait in the queue (the
// thread says why) and go out when it is back; after a day away they are
// left unsent instead.
func TestLinkDownQueuesMessages(t *testing.T) {
	s, c, th, r, sess := queueFixture(t)
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	k := &runSink{rt: s.rt, r: r}
	endpoint := "/api/v1/threads/" + th.ID + "/messages"
	item := func(match func(*agent.Item) bool) *agent.Item {
		rows, _ := s.store.ListItems(context.Background(), th.ID)
		for _, row := range rows {
			var it agent.Item
			_ = json.Unmarshal(row.Data, &it)
			if match(&it) {
				return &it
			}
		}
		return nil
	}
	notice := func(prefix string) *agent.Item {
		return item(func(it *agent.Item) bool { return it.Kind == "notice" && strings.HasPrefix(it.Text, prefix) })
	}
	waitSent := func(n int) {
		t.Helper()
		for i := 0; ; i++ {
			r.mu.Lock()
			got := len(sess.sent)
			r.mu.Unlock()
			if got == n {
				return
			}
			if i > 100 {
				t.Fatalf("sent %d, want %d", got, n)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	k.Link(false)
	if notice("Lost the connection") != nil {
		t.Fatal("an idle thread got a notice for nothing")
	}
	c.want("POST", endpoint, map[string]any{"text": "while away"}, 200, nil)
	if len(sess.sent) != 0 || strings.Join(queuedTexts(r), ",") != "while away" {
		t.Fatalf("not queued: sent %v queue %v", sess.sent, queuedTexts(r))
	}
	for i := 0; notice("Lost the connection") == nil; i++ {
		if i > 100 {
			t.Fatal("no notice for the queued message")
		}
		time.Sleep(20 * time.Millisecond)
	}
	k.Link(true)
	waitSent(1)
	if !strings.HasPrefix(sess.sent[0].Text, "while away") || notice("Reconnected to this server after") == nil {
		t.Fatalf("after reconnecting: %v", sess.sent)
	}

	// a day away: what waited is not sent that late
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	k.Link(false)
	c.want("POST", endpoint, map[string]any{"text": "stale"}, 200, nil)
	r.mu.Lock()
	r.linkDownAt = time.Now().Add(-25 * time.Hour)
	r.mu.Unlock()
	k.Link(true)
	time.Sleep(100 * time.Millisecond)
	if len(sess.sent) != 1 {
		t.Fatalf("a day-old message went out: %v", sess.sent)
	}
	if it := item(func(it *agent.Item) bool { return it.Kind == "user" && it.Text == "stale" }); it == nil || !it.Unsent || it.Queued || it.UnsentWhy == "" {
		t.Fatalf("stale message: %+v", it)
	}
}
