package agent

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

// A held Claude Code on a real machine over ssh: the link is cut mid-turn
// and the turn goes on. Opt in with POCKET_HOLD_REMOTE=<ssh target> (a
// linux/amd64 or darwin/arm64 machine); it cleans up after itself.
func TestHeldClaudeRemote(t *testing.T) {
	target := os.Getenv("POCKET_HOLD_REMOTE")
	if target == "" {
		t.Skip("POCKET_HOLD_REMOTE not set")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // ssh's shared-connection sockets need a short path (macOS)
	r := &Remote{Target: target}
	ctx := context.Background()
	out, _, err := r.Run(ctx, 30*time.Second, `uname -sm; echo "$HOME"`)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(out)
	goos, goarch := strings.ToLower(f[0]), map[string]string{"x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[f[1]]
	data, _, ok := hold.Binary(goos, goarch)
	if !ok {
		t.Skip("pocket-hold for", goos, goarch, "not built in")
	}
	dir := fmt.Sprintf("%s/.pocket-ai/holdtest-%d", f[2], rand.IntN(1e9))
	slot := fmt.Sprintf("holdtest-%d", rand.IntN(1e9))
	defer r.Run(ctx, 30*time.Second, `rm -rf "$1" "$HOME/.pocket-ai/hold/$2" "$HOME/.pocket-ai/run/$2"`, dir, slot)
	if _, _, err := r.Run(ctx, 30*time.Second, `mkdir -p "$1"`, dir); err != nil {
		t.Fatal(err)
	}
	holdBin, cli := dir+"/pocket-hold", dir+"/claude"
	if err := r.Put(ctx, 2*time.Minute, holdBin, data); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, time.Minute, cli, []byte(fakeHeldClaude)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(ctx, 30*time.Second, `chmod 700 "$1" "$2"`, holdBin, cli); err != nil {
		t.Fatal(err)
	}
	rr := &Remote{Target: target, Slot: slot}
	o := Options{Bin: cli, Cwd: dir, Mode: "default", Remote: rr, Hold: &Hold{Bin: holdBin, Slot: slot}}
	rec := &heldRec{rec: newRec()}
	s, err := StartClaude(ctx, o, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cs := s.(*claudeSession)
	if err := s.Send(Input{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, func() bool { return rec.has("first 1") })
	// the network drops: the ssh client dies
	cs.l.wmu.Lock()
	pid := cs.l.cmd.Process.Pid
	cs.l.wmu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec.mu.Lock()
		done := rec.has("second 1") && len(rec.turns) == 1
		rec.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn did not finish after the drop: %v", rec.order)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := s.Send(Input{Text: "two"}); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, func() bool { return len(rec.turns) == 2 })
	_ = s.Close()
	select {
	case err := <-rec.exited:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("did not end")
	}
	out, _, _ = r.Run(ctx, 30*time.Second, `"$1" status "$2"`, holdBin, slot)
	if !strings.Contains(out, `"gone":true`) {
		t.Fatalf("holder still there: %s", out)
	}
}
