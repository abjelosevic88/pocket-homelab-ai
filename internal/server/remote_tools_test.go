package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func TestRemotePocketTools(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{"pocket":{"type":"http","url":"http://127.0.0.1:38211/mcp","headers":{"Authorization":"Bearer cap"}}}}`), 0o600)
	o := agent.Options{MCPConfig: f, Remote: &agent.Remote{Target: "ubuntu@box"}}
	if err := remotePocketTools(&o, nil); err != nil {
		t.Fatal(err)
	}
	if len(o.Remote.Forwards) != 1 || !regexp.MustCompile(`^127\.0\.0\.1:\d{5}:127\.0\.0\.1:38211$`).MatchString(o.Remote.Forwards[0]) {
		t.Fatalf("forwards: %v", o.Remote.Forwards)
	}
	port := strings.Split(o.Remote.Forwards[0], ":")[1]
	var c struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(o.MCPConfig), &c); err != nil {
		t.Fatalf("config is not inline JSON: %q", o.MCPConfig)
	}
	p := c.Servers["pocket"]
	if p.URL != "http://127.0.0.1:"+port+"/mcp" || p.Type != "http" || p.Headers["Authorization"] != "Bearer cap" {
		t.Fatalf("pocket: %+v", p)
	}
	cmd, _ := agent.Command(o.Remote, "/work", nil, "claude", "-p")
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-o ExitOnForwardFailure=yes -R "+o.Remote.Forwards[0]) || strings.Index(args, "-R") > strings.Index(args, "ubuntu@box") {
		t.Fatalf("ssh args: %s", args)
	}
}
