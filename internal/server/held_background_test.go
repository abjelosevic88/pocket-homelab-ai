package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// backgroundClaude ends its turn at once, then (as when a background
// sub-agent finishes) carries on by itself a few seconds later: a turn its
// holder never sees start, as no message went in.
const backgroundClaude = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-bg"}'
      printf '%s\n' '{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"started the sub-agents"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}'
      sleep 4
      printf '%s\n' '{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"carried on"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.02,"usage":{"input_tokens":2,"output_tokens":3}}';;
  esac
done`

// Pocket AI restarts while its agent's turn is over but Claude Code still
// works (waiting on background sub-agents, then carrying on by itself). The
// holder says idle, Claude Code's registry says busy: the new Pocket AI
// takes it back instead of leaving it to look like another app's session
// (no Stop, nothing can be sent), and its next turn shows up as usual.
func TestRestartTakesBackAgentBusyOnlyInClaude(t *testing.T) {
	if _, _, ok := hold.Binary(runtime.GOOS, runtime.GOARCH); !ok {
		t.Skip("pocket-hold not built in (go generate ./internal/hold)")
	}
	home, err := os.MkdirTemp("/tmp", "pb")
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
	if err := os.WriteFile(bin, []byte(backgroundClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_BIN", bin)
	t.Setenv("CODEX_BIN", filepath.Join(root, "no-codex"))
	for _, ag := range agent.ACPAgents {
		t.Setenv(strings.ToUpper(ag.ID)+"_BIN", filepath.Join(root, "no-"+ag.ID))
	}
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
	u := &store.User{Email: "bg@example.test", Name: "Bg", Role: "admin", CanRun: true}
	if err := st.CreateUser(ctx1, u); err != nil {
		t.Fatal(err)
	}
	th := &store.Thread{ID: "0b0c1f9e-bgbg-4f00-9a00-000000000002", UserID: u.ID, Provider: "claude", Mode: "default", Cwd: root, Title: "Sub-agents"}
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
			switch a.Kind {
			case "assistant":
				out[a.Text]++
			case "turn":
				out["turn"]++
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
	waitFor("the first turn to end", func() bool {
		got, _ := st.GetThreadAny(context.Background(), th.ID)
		return texts()["turn"] == 1 && got.Status == "idle" && got.ProviderSessionID == "sess-bg"
	})

	stop1()
	var hd hold.Header
	waitFor("letting go", func() bool {
		var ok bool
		hd, ok, _ = hold.Status(th.ID)
		return ok && !hd.Busy && hd.PID > 0 && s1.rt.get(th.ID) == nil
	})
	// what Claude Code says of itself: still working on that session
	dir, _ := s1.accountHome(context.Background(), "claude", th.AccountID)
	reg := filepath.Join(dir, "sessions", strconv.Itoa(hd.PID)+".json")
	_ = os.MkdirAll(filepath.Dir(reg), 0o755)
	b, _ := json.Marshal(map[string]any{"pid": hd.PID, "sessionId": "sess-bg", "status": "busy"})
	if err := os.WriteFile(reg, b, 0o644); err != nil {
		t.Fatal(err)
	}

	s2 := New(cfg, st, log, nil)
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	if err := s2.Bootstrap(ctx2); err != nil {
		t.Fatal(err)
	}
	waitFor("taking it back", func() bool { return s2.rt.get(th.ID) != nil })
	waitFor("the turn it carried on with", func() bool {
		x := texts()
		return x["carried on"] == 1 && x["turn"] == 2
	})
	if s2.rt.isExternal(th.ID) {
		t.Fatal("its own agent shown as another app's")
	}
	s2.rt.stop(th.ID)
	waitFor("the agent to end", func() bool { _, ok, _ := hold.Status(th.ID); return !ok })
}
