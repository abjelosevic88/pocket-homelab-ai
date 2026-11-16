package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSSH runs the remote command here, in $HOME: a stand-in machine.
func fakeSSH(t *testing.T) (home string) {
	t.Helper()
	bin, home := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != -- ]; do shift; done; shift; exec /bin/sh -c \"$*\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	return home
}

func checkPrivate(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("%s: %q %v", path, b, err)
	}
	for _, p := range []string{path, filepath.Dir(path)} {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is %v: others can read it", p, fi.Mode().Perm())
		}
	}
}

// A remote conversation's MCP config carries the Pocket tools key: it goes
// to a file there, and the CLI's command line only has its path.
func TestPrivateMCPConfigRemote(t *testing.T) {
	home := fakeSSH(t)
	key := fmt.Sprintf("k%x", time.Now().UnixNano())
	config := `{"mcpServers":{"pocket":{"type":"http","url":"http://127.0.0.1:41234/mcp","headers":{"Authorization":"Bearer ` + key + `"}}}}`
	o := Options{MCPConfig: config, Remote: &Remote{Target: "box", Slot: "thread-9"}}
	if err := privateMCPConfig(context.Background(), &o); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".pocket-ai/private/thread-9.mcp.json"); o.MCPConfig != want {
		t.Fatalf("path %q, want %q", o.MCPConfig, want)
	}
	checkPrivate(t, o.MCPConfig, config)
	cmd, err := Command(o.Remote, "/work", nil, "claude", "--mcp-config", o.MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(remoteScriptOf(t, cmd.Args), key) {
		t.Fatal("the key is in the remote command")
	}

	// one without a key (and a local one) stays as it is
	for _, o := range []Options{{MCPConfig: `{"mcpServers":{}}`, Remote: &Remote{Target: "box"}}, {MCPConfig: config}} {
		before := o.MCPConfig
		if err := privateMCPConfig(context.Background(), &o); err != nil || o.MCPConfig != before {
			t.Fatalf("changed: %q %v", o.MCPConfig, err)
		}
	}
}

// A holder's meta with the key goes to a private file where the holder runs.
func TestPrivateMeta(t *testing.T) {
	home := fakeSSH(t)
	meta := `{"port":41234,"cap":"k1"}`
	f, err := privateMeta(context.Background(), Options{Remote: &Remote{Target: "box"}}, "thread-9", meta)
	if err != nil || f != filepath.Join(home, ".pocket-ai/private/thread-9.hold.json") {
		t.Fatalf("remote: %q %v", f, err)
	}
	checkPrivate(t, f, meta)

	run := t.TempDir()
	f, err = privateMeta(context.Background(), Options{MCPConfig: filepath.Join(run, "ask-mcp-1.json")}, "thread-9", meta)
	if err != nil || f != filepath.Join(run, "holders", "thread-9.hold.json") {
		t.Fatalf("here: %q %v", f, err)
	}
	checkPrivate(t, f, meta)

	if f, err := privateMeta(context.Background(), Options{}, "thread-9", `{"login":"x"}`); f != "" || err != nil {
		t.Fatalf("no key: %q %v", f, err)
	}
}

// remoteScriptOf decodes the sh script an ssh command carries (remoteCommand).
func remoteScriptOf(t *testing.T, args []string) string {
	t.Helper()
	last := args[len(args)-1]
	i := strings.Index(last, "echo ")
	j := strings.Index(last, " | base64")
	if i < 0 || j < i {
		t.Fatalf("not a remote command: %q", last)
	}
	out, err := decodeB64(last[i+5 : j])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeB64(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}
