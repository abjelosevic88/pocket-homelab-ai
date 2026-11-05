package server

import (
	"encoding/json"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A holder remembers the login its CLI was started on, so a thread moved to
// another login since is not given that CLI back.
func TestHolderMetaKeepsLogin(t *testing.T) {
	o := agent.Options{MCPConfig: `{"mcpServers":{"pocket":{"url":"http://127.0.0.1:4711/mcp","headers":{"Authorization":"Bearer k"}}}}`}
	var m holdMeta
	if err := json.Unmarshal([]byte(holderMeta(o, "claude:1")), &m); err != nil {
		t.Fatal(err)
	}
	if m.Login != "claude:1" || m.Port != 4711 || m.Cap != "k" {
		t.Fatalf("meta = %+v", m)
	}
	if err := json.Unmarshal([]byte(holderMeta(agent.Options{}, "claude")), &m); err != nil || m.Login != "claude" {
		t.Fatalf("no tools: meta = %+v, %v", m, err)
	}

	th := &store.Thread{Provider: "claude", AccountID: 3, HostHome: "/home/x/.claude-max"}
	if got := threadLogin(th, nil); got != "claude:3" {
		t.Fatalf("local login = %q", got)
	}
	if got := threadLogin(th, &store.Host{ID: 2}); got != hostLimitsKey("claude", 2, th.HostHome) {
		t.Fatalf("host login = %q", got)
	}
}
