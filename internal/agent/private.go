package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Pocket tools key (the bearer in a conversation's MCP config, and the
// "cap" kept with its holder) stays off command lines: every account on a
// machine can read those (ps), and with the key and the forwarded port it
// could call the person's Pocket tools. On the machine the agent runs on it
// goes in a file only that account can read instead.

const privateDir = ".pocket-ai/private" // under the remote home, 700

// privateMCPConfig moves an inline MCP config that carries a key (a remote
// conversation's: remotePocketTools) into such a file there.
func privateMCPConfig(ctx context.Context, o *Options) error {
	if o.Remote == nil || !carriesKey(o.MCPConfig) {
		return nil
	}
	path, err := o.Remote.PutPrivate(ctx, 30*time.Second, privateDir+"/"+privateName(o.Remote.Slot)+".mcp.json", []byte(o.MCPConfig))
	if err != nil {
		return fmt.Errorf("Pocket tools config to %s: %w", o.Remote.Target, err)
	}
	o.MCPConfig = path
	return nil
}

// privateMeta writes a holder's meta into such a file where the holder runs
// and returns its path for pocket-hold attach -meta-file. "" (and the meta
// stays inline) when it carries no key or there is no private place for it.
func privateMeta(ctx context.Context, o Options, slot, meta string) (string, error) {
	if !strings.Contains(meta, `"cap"`) {
		return "", nil
	}
	name := privateName(slot) + ".hold.json"
	if o.Remote != nil {
		return o.Remote.PutPrivate(ctx, 30*time.Second, privateDir+"/"+name, []byte(meta))
	}
	// here: next to the conversation's own MCP config file (the server's
	// data folder, or the person's run folder inside their jail)
	if !filepath.IsAbs(o.MCPConfig) {
		return "", nil
	}
	dir := filepath.Join(filepath.Dir(o.MCPConfig), "holders")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	tmp := fmt.Sprintf("%s.part.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(meta), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func carriesKey(config string) bool {
	c := strings.TrimSpace(config)
	return strings.HasPrefix(c, "{") && strings.Contains(c, "Authorization")
}

// privateName is a file name for slot (a Pocket thread), or a random one.
func privateName(slot string) string {
	if slot != "" && !strings.ContainsAny(slot, `/\`) && !strings.HasPrefix(slot, ".") {
		return slot
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
