package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Agents run under pocket-hold (internal/hold) on machines that have it: a
// dropped ssh link or a Pocket AI restart leaves them working, and the next
// start of their thread attaches to them instead of starting another.

// holdMeta is kept with a holder: how its CLI reaches the Pocket tools, so
// the next Pocket AI serves them at the same place with the same key.
type holdMeta struct {
	Port int    `json:"port,omitempty"`
	Cap  string `json:"cap,omitempty"`
}

type holderCache struct {
	mu sync.Mutex
	at map[int64]holderPath // host id (0 = this server)
}

type holderPath struct {
	path string
	at   time.Time
}

var holders = holderCache{at: map[int64]holderPath{}}

// holder is pocket-hold on host h (nil: this server), installed there first
// when needed; "" when it cannot be had (agents then run as before).
//
// POCKET_HOLD=off turns holding off: agents then run as children of their
// connection again.
func (rt *engine) holder(ctx context.Context, h *store.Host) string {
	if os.Getenv("POCKET_HOLD") == "off" {
		return ""
	}
	var key int64
	if h != nil {
		key = h.ID
	}
	holders.mu.Lock()
	c, ok := holders.at[key]
	holders.mu.Unlock()
	if ok && c.path != "" && h == nil {
		if _, err := os.Stat(c.path); err != nil {
			ok = false // removed under us: put it back
		}
	}
	if ok && (c.path != "" || time.Since(c.at) < 10*time.Minute) {
		return c.path
	}
	path, err := installHolder(ctx, h)
	if err != nil {
		where := "this server"
		if h != nil {
			where = h.Name
		}
		rt.s.log.Warn("agents there are not held", "machine", where, "err", err)
	}
	holders.mu.Lock()
	holders.at[key] = holderPath{path, time.Now()}
	holders.mu.Unlock()
	return path
}

// forgetHolder drops what is known of host h's holder (it was edited).
func forgetHolder(id int64) {
	holders.mu.Lock()
	delete(holders.at, id)
	holders.mu.Unlock()
}

func installHolder(ctx context.Context, h *store.Host) (string, error) {
	if h == nil {
		data, name, ok := hold.Binary(runtime.GOOS, runtime.GOARCH)
		if !ok {
			return "", errors.New("pocket-hold is not in this build (go generate ./internal/hold)")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(home, ".pocket-ai", "bin")
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && fi.Size() == int64(len(data)) {
			return path, nil
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		tmp := fmt.Sprintf("%s.%d", path, os.Getpid())
		if err := os.WriteFile(tmp, data, 0o700); err != nil {
			return "", err
		}
		return path, os.Rename(tmp, path)
	}
	r := remoteOf(h)
	out, errOut, err := r.Run(ctx, 30*time.Second, `uname -sm; printf '%s\n' "$HOME"`)
	if err != nil {
		return "", errors.New(sshError(errOut, err))
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected answer %q", out)
	}
	goos, goarch := platform(lines[len(lines)-2])
	data, name, ok := hold.Binary(goos, goarch)
	if !ok {
		return "", fmt.Errorf("no pocket-hold for %q in this build", lines[len(lines)-2])
	}
	path := strings.TrimSpace(lines[len(lines)-1]) + "/.pocket-ai/bin/" + name
	out, errOut, err = r.Run(ctx, 20*time.Second, `mkdir -p "${1%/*}" && [ -x "$1" ] && echo there`, path)
	if err == nil && strings.TrimSpace(out) == "there" {
		return path, nil
	}
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ee.ExitCode() == 255 {
		return "", errors.New(sshError(errOut, err)) // not reached; not merely missing
	}
	if err := r.Put(ctx, 3*time.Minute, path, data); err != nil {
		return "", err
	}
	if _, errOut, err := r.Run(ctx, 20*time.Second, `chmod 700 "$1"`, path); err != nil {
		return "", errors.New(sshError(errOut, err))
	}
	return path, nil
}

// platform maps `uname -sm` to Go's names.
func platform(unameSM string) (goos, goarch string) {
	f := strings.Fields(strings.ToLower(unameSM))
	if len(f) < 2 {
		return "", ""
	}
	goos = f[0]
	switch f[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		goarch = f[1]
	}
	return goos, goarch
}

// holderStatus asks pocket-hold at bin whether thread slot's CLI still runs
// there.
func holderStatus(ctx context.Context, h *store.Host, bin, slot string) (hold.Header, bool) {
	var hd hold.Header
	if h == nil {
		hd, ok, err := hold.Status(slot)
		return hd, err == nil && ok && !hd.Exited && !hd.Stopping
	}
	out, _, err := remoteOf(h).Run(ctx, 30*time.Second, `"$1" status "$2"`, bin, slot)
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(out)), &hd) != nil {
		return hd, false
	}
	return hd, !hd.Gone && !hd.Exited && !hd.Stopping
}

// holdFor runs thread t's agent under pocket-hold when its machine has it,
// set to adopt the CLI still running there. pin is how that CLI reaches the
// Pocket tools (nil when a new one starts).
func (rt *engine) holdFor(ctx context.Context, t *store.Thread, h *store.Host, o *agent.Options) (pin *holdMeta) {
	if h == nil && agent.Jailed(o.Cwd, o.Env) {
		return nil // a person's sandbox: the holder could not live outside it
	}
	bin := rt.holder(ctx, h)
	if bin == "" {
		return nil
	}
	o.Hold = &agent.Hold{Bin: bin, Slot: t.ID, Leave: rt.ctx.Done()}
	if h == nil {
		o.Hold.Scope = serviceScope()
	}
	hd, ok := holderStatus(ctx, h, bin, t.ID)
	if !ok {
		return nil
	}
	o.Hold.Adopt = true
	var m holdMeta
	_ = json.Unmarshal([]byte(hd.Meta), &m)
	return &m
}

// serviceScope starts holders here outside Pocket AI's systemd unit, whose
// stop takes down everything in it.
func serviceScope() []string {
	if os.Getenv("INVOCATION_ID") == "" {
		return nil
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return nil
	}
	return []string{"systemd-run", "--user", "--scope", "--quiet", "--collect"}
}

// toolsMeta reads where o's CLI reaches the Pocket tools, to keep with it.
func toolsMeta(o agent.Options) string {
	raw := o.MCPConfig
	if raw != "" && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		b, err := os.ReadFile(raw)
		if err != nil {
			return ""
		}
		raw = string(b)
	}
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal([]byte(raw), &config) != nil {
		return ""
	}
	p, ok := config.Servers["pocket"]
	if !ok {
		return ""
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return ""
	}
	port, _ := strconv.Atoi(u.Port())
	b, _ := json.Marshal(holdMeta{Port: port, Cap: strings.TrimPrefix(p.Headers["Authorization"], "Bearer ")})
	return string(b)
}

// startSession starts the provider's CLI, held when o.Hold is set: it
// adopts the running one when told to, or starts anew if that one has just
// ended.
func startSession(ctx context.Context, provider string, o agent.Options, sink agent.Sink) (agent.Session, error) {
	start := func(o agent.Options) (agent.Session, error) {
		switch provider {
		case "claude":
			return agent.StartClaude(ctx, o, sink)
		case "codex":
			return agent.StartCodex(ctx, o, sink)
		}
		return nil, badRequest("unknown provider " + provider)
	}
	if o.Hold != nil {
		o.Hold.Meta = toolsMeta(o)
	}
	s, err := start(o)
	if errors.Is(err, agent.ErrNotHeld) {
		h := *o.Hold
		h.Adopt = false
		o.Hold = &h
		s, err = start(o)
	}
	return s, err
}

// adopt takes back thread id's agent if it still runs under a holder (Pocket
// AI restarted while it worked); false when there is none.
func (rt *engine) adopt(id string) bool {
	ctx, cancel := context.WithTimeout(rt.ctx, 2*time.Minute)
	defer cancel()
	t, err := rt.s.store.GetThreadAny(ctx, id)
	if err != nil {
		return false
	}
	h, err := rt.s.host(ctx, t.HostID)
	if err != nil {
		return false
	}
	bin := rt.holder(ctx, h)
	if bin == "" {
		return false
	}
	if _, ok := holderStatus(ctx, h, bin, id); !ok {
		return false
	}
	if _, err := rt.ensure(t); err != nil {
		rt.s.log.Warn("could not take back a running agent", "thread", id, "err", err)
		return false
	}
	rt.s.log.Info("took back a running agent", "thread", id)
	return true
}
