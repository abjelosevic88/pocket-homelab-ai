package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexPocketConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"pocket":{"url":"http://127.0.0.1:1234/mcp","headers":{"Authorization":"Bearer local-capability"}}}}`), 0600)
	config, err := codexPocketConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := config["mcp_servers.pocket"].(map[string]any)
	if server["url"] != "http://127.0.0.1:1234/mcp" || server["tool_timeout_sec"] != 660 {
		t.Fatal("bad Pocket config")
	}
	if len(config) != 1 {
		t.Fatal("unrelated config overridden")
	}
}

// Optional protocol check against the installed app-server; no model turn or
// real Pocket data is used. Resumption is checked separately with a saved turn.
func TestCodexPocketConnectionLive(t *testing.T) {
	if os.Getenv("POCKET_CODEX_INTEGRATION") != "1" {
		t.Skip("set POCKET_CODEX_INTEGRATION=1")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer local-test" {
			w.WriteHeader(403)
			return
		}
		var in struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any = map[string]any{}
		switch in.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "pocket-test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "pocket_test_read", "description": "Read a test value", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}, "annotations": map[string]bool{"readOnlyHint": true}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result})
	}))
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"pocket": map[string]any{"url": srv.URL, "headers": map[string]string{"Authorization": "Bearer local-test"}}}})
	os.WriteFile(path, raw, 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opts := Options{Cwd: dir, Mode: "full-access", MCPConfig: path}
	sess, err := StartCodex(ctx, opts, newRec())
	if err != nil {
		t.Fatal(err)
	}
	c := sess.(*codexSession)
	result, err := c.c.call(ctx, "mcpServerStatus/list", map[string]any{"threadId": c.threadID, "serverName": "pocket"})
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "pocket_test_read") {
		t.Fatalf("Pocket tool missing: %s", result)
	}
}
