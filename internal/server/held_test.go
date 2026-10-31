package server

import (
	"context"
	"encoding/json"
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
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'a","content":[{"type":"text","text":"first '$n'"}]}}'
      sleep 3
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'b","content":[{"type":"text","text":"second '$n'"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}';;
  esac
done`

// A redeploy in the middle of a turn: the old Pocket AI lets go of the
// agent, the new one takes it back at boot, and the turn finishes as if
// nothing happened.
func TestRestartTakesBackRunningAgent(t *testing.T) {
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

	// the new one takes the turn back at boot
	s2 := New(cfg, st, log, nil)
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
	// and it goes on as usual
	if _, err := s2.rt.send(th, "u2", agent.Input{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	waitFor("the next turn", func() bool { return texts()["turn"] == 2 && texts()["second 2"] == 1 })
	s2.rt.stop(th.ID)
	waitFor("the agent to end", func() bool { _, ok, _ := hold.Status(th.ID); return !ok })
}
