package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// fakeOpenCode answers ACP by method; a prompt gets one line of text back,
// with the OPENROUTER key it was given (so the test sees the keys arrive).
const fakeOpenCode = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"resume":{}}}}}' ;;
    *'"method":"session/new"'*|*'"method":"session/resume"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"fake-1","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"free/a","options":[{"value":"free/a","name":"Free A"},{"value":"google/gemini-4-neon","name":"Gemini 4 Neon"}]},{"id":"mode","category":"mode","type":"select","currentValue":"build","options":[{"value":"build","name":"build"},{"value":"plan","name":"plan"}]}]}}' ;;
    *'"method":"session/set_config_option"'*)
      printf '%s\n' "$line" >> "$FAKE_LOG"
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
    *'"method":"session/prompt"'*)
      printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello from fake, key '"$OPENROUTER_API_KEY"'"}}}}'
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":4}}}' ;;
    *'"id":'*'"method"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
  esac
done
`

func TestACPThread(t *testing.T) {
	s, h, root := testServer(t)
	bin := filepath.Join(root, "fake-opencode")
	if err := os.WriteFile(bin, []byte(fakeOpenCode), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_BIN", bin)
	t.Setenv("FAKE_LOG", filepath.Join(root, "fake.log"))
	owner, _ := person(t, s, h, "owner@example.test", "admin", true)

	// keys: unsafe names refused, secrets masked when read back
	owner.want("PUT", "/api/v1/agents/acp/opencode/env", map[string]any{"env": map[string]string{"PATH": "/evil"}}, 400, nil)
	var list []acpView
	owner.want("PUT", "/api/v1/agents/acp/opencode/env", map[string]any{"env": map[string]string{"OPENROUTER_API_KEY": "sk-or-123456789"}}, 200, &list)
	var oc acpView
	for _, v := range list {
		if v.ID == "opencode" {
			oc = v
		}
	}
	if !oc.Installed || !oc.Available || oc.Set["OPENROUTER_API_KEY"] != "••••6789" || oc.Models != 2 {
		t.Fatalf("opencode view %+v", oc)
	}
	other, _ := person(t, s, h, "other@example.test", "member", true)
	other.want("GET", "/api/v1/agents/acp", nil, 403, nil)

	// defaults for the agent are kept with the person's settings
	owner.want("PUT", "/api/v1/settings", map[string]any{"agents": map[string]any{"opencode": map[string]string{"model": "google/gemini-4-neon"}}}, 200, nil)

	var th struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Mode  string `json:"mode"`
	}
	owner.want("POST", "/api/v1/threads", map[string]any{"provider": "opencode", "text": "hi", "cwd": root}, 201, &th)
	if th.Model != "google/gemini-4-neon" || th.Mode != "ask" {
		t.Fatalf("thread model %q mode %q", th.Model, th.Mode)
	}
	var reply string
	for i := 0; i < 200 && reply == ""; i++ {
		items, _ := s.store.ListItems(context.Background(), th.ID)
		for _, it := range items {
			var a agent.Item
			_ = json.Unmarshal(it.Data, &a)
			if a.Kind == "assistant" {
				reply = a.Text
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reply != "Hello from fake, key sk-or-123456789" {
		t.Fatalf("reply %q", reply)
	}
	log, _ := os.ReadFile(filepath.Join(root, "fake.log"))
	if !strings.Contains(string(log), `"value":"google/gemini-4-neon"`) {
		t.Fatalf("model not switched: %s", log)
	}
	if strings.Contains(string(log), `"configId":"mode"`) {
		t.Fatalf("ask runs in build mode, which it already is: %s", log)
	}
	// ACP agents run here only
	owner.want("POST", "/api/v1/threads", map[string]any{"provider": "opencode", "text": "hi", "host_id": 5}, 400, nil)
}
