package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

// fakeHeldClaude answers each user message with a sub-agent starting, a
// first reply, a pause (to cut the connection in), a second reply and the
// result. It ends when stdin closes.
const fakeHeldClaude = `#!/bin/sh
n=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      n=$((n+1))
      printf '%s\n' '{"type":"system","subtype":"task_started","task_id":"sub'$n'","description":"helper '$n'","task_type":"local_agent"}'
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'a","content":[{"type":"text","text":"first '$n'"}]}}'
      sleep 1
      printf '%s\n' '{"type":"assistant","message":{"id":"m'$n'b","content":[{"type":"text","text":"second '$n'"}]}}'
      printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1}}';;
  esac
done`

type heldRec struct {
	*rec
	started int
	subs    []Subagent
	links   []bool
}

func (r *heldRec) Link(up bool) { r.mu.Lock(); r.links = append(r.links, up); r.mu.Unlock() }

func (r *heldRec) TurnStarted()           { r.mu.Lock(); r.started++; r.mu.Unlock() }
func (r *heldRec) Subagents(l []Subagent) { r.mu.Lock(); r.subs = l; r.mu.Unlock() }

func (r *heldRec) has(text string) bool {
	for _, it := range r.items {
		if it.Text == text {
			return true
		}
	}
	return false
}

func heldSetup(t *testing.T) (bin, holdBin string) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "pa") // short: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	holdBin = filepath.Join(home, "pocket-hold")
	build := exec.Command("go", "build", "-o", holdBin, "./cmd/pocket-hold")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pocket-hold: %v\n%s", err, out)
	}
	bin = filepath.Join(home, "claude")
	if err := os.WriteFile(bin, []byte(fakeHeldClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, holdBin
}

// A held Claude Code survives its connection dropping mid-turn and Pocket AI
// letting go of it; the next start adopts the running turn and its sub-agents.
func TestHeldClaude(t *testing.T) {
	bin, holdBin := heldSetup(t)
	dir := t.TempDir()
	leave := make(chan struct{})
	o := Options{Bin: bin, Cwd: dir, Mode: "default", Hold: &Hold{Bin: holdBin, Slot: "thread-x", Leave: leave}}

	r := &heldRec{rec: newRec()}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := StartClaude(ctx, o, r)
	if err != nil {
		t.Fatal(err)
	}
	cs := s.(*claudeSession)
	if !cs.l.Header.Started {
		t.Fatalf("first start did not start: %+v", cs.l.Header)
	}
	if err := s.Send(Input{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return r.has("first 1") })
	// the connection drops mid-turn
	cs.l.wmu.Lock()
	pid := cs.l.cmd.Process.Pid
	cs.l.wmu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	r.wait(t, func() bool { return r.has("second 1") && len(r.turns) == 1 })
	if len(r.links) != 0 {
		t.Fatalf("a blip was reported: %v", r.links)
	}
	// and it takes messages again
	if err := s.Send(Input{Text: "two"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 2 })

	// a longer outage: the machine cannot be reached for a while (pocket-hold
	// there cannot be run); what is sent meanwhile waits and goes out after
	retryMax = time.Second
	t.Cleanup(func() { retryMax = time.Minute })
	if err := os.Rename(holdBin, holdBin+".away"); err != nil {
		t.Fatal(err)
	}
	cs.l.wmu.Lock()
	pid = cs.l.cmd.Process.Pid
	cs.l.wmu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	r.wait(t, func() bool { return len(r.links) == 1 })
	if err := s.Send(Input{Text: "sent while away"}); err != nil {
		t.Fatalf("send while away: %v", err)
	}
	if err := os.Rename(holdBin+".away", holdBin); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.links) == 2 && r.links[1] && len(r.turns) == 3 && r.has("second 3") })

	// a third turn is running when Pocket AI stops: it lets go
	if err := s.Send(Input{Text: "four"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return r.has("first 4") })
	close(leave)
	cancel()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("did not let go")
	}
	select {
	case err := <-r.exited:
		t.Fatalf("reported an exit on letting go: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if st, ok, _ := hold.Status("thread-x"); !ok || !st.Busy {
		t.Fatalf("holder after letting go: %+v %v", st, ok)
	}

	// the next Pocket AI adopts it: the running turn, and every sub-agent
	r2 := &heldRec{rec: newRec()}
	o.Hold = &Hold{Bin: holdBin, Slot: "thread-x", Adopt: true}
	s2, err := StartClaude(context.Background(), o, r2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.started != 1 {
		t.Fatalf("adopted turn not reported running")
	}
	r2.wait(t, func() bool { return r2.has("first 4") && r2.has("second 4") && len(r2.turns) == 1 && len(r2.subs) == 4 })
	if len(r2.turns) != 1 || len(r2.items) != 2 {
		t.Fatalf("adopted: %d turns, items %v", len(r2.turns), r2.order)
	}
	// closing it ends the CLI for good
	_ = s2.Close()
	select {
	case err := <-r2.exited:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("did not end")
	}
	for i := 0; ; i++ {
		if _, ok, _ := hold.Status("thread-x"); !ok {
			break
		}
		if i > 100 {
			t.Fatal("holder still there")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := StartClaude(context.Background(), o, newRec()); err != ErrNotHeld {
		t.Fatalf("adopting nothing: %v", err)
	}
}
