package hold

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary is pocket-hold when asked: attach starts the holder with
// os.Executable().
func TestMain(m *testing.M) {
	if os.Getenv("POCKET_HOLD_TEST_MAIN") == "1" {
		os.Exit(Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeCLI answers a stdin line {"type":"user","n":N} with N slow lines and a
// result; it exits 3 when stdin closes.
const fakeCLI = `while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*) n=$(echo "$line" | sed 's/.*"n":\([0-9]*\).*/\1/'); i=1
      while [ $i -le $n ]; do echo "{\"line\":$i}"; sleep 0.15; i=$((i+1)); done
      echo '{"type":"result"}';;
  esac
done
exit 3`

type attached struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	read  int64 // stdout bytes after the header
	hd    Header
	lines chan string
}

func attach(t *testing.T, home string, args ...string) *attached {
	t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"attach", "-start-mark", `"type":"user"`, "-end-mark", `"type":"result"`}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "POCKET_HOLD_TEST_MAIN=1")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &attached{t: t, cmd: cmd, in: in, out: bufio.NewReader(out), lines: make(chan string, 100)}
	first, err := a.out.ReadString('\n')
	if err != nil {
		t.Fatalf("no header: %v", err)
	}
	if err := json.Unmarshal([]byte(first), &a.hd); err != nil || a.hd.Type != HeaderType {
		t.Fatalf("bad header %q", first)
	}
	go func() {
		for {
			l, err := a.out.ReadString('\n')
			if err != nil {
				close(a.lines)
				return
			}
			a.read += int64(len(l))
			a.lines <- strings.TrimSpace(l)
		}
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return a
}

func (a *attached) send(s string) {
	if _, err := io.WriteString(a.in, s+"\n"); err != nil {
		a.t.Fatal(err)
	}
}

func (a *attached) next() string {
	a.t.Helper()
	select {
	case l, ok := <-a.lines:
		if !ok {
			a.t.Fatal("output ended")
		}
		return l
	case <-time.After(10 * time.Second):
		a.t.Fatal("no output")
	}
	return ""
}

func (a *attached) expect(want ...string) {
	a.t.Helper()
	for _, w := range want {
		if got := a.next(); got != w {
			a.t.Fatalf("got %q, want %q", got, w)
		}
	}
}

func lineN(i int) string { return fmt.Sprintf(`{"line":%d}`, i) }

func TestHoldSurvivesDetach(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ph") // short: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	slot := "thread-1"

	if got := attach(t, home, "-only", slot); !got.hd.Gone {
		t.Fatalf("-only without a holder: %+v", got.hd)
	}

	a := attach(t, home, slot, "--", "sh", "-c", fakeCLI)
	if !a.hd.Started || a.hd.Busy || a.hd.PID == 0 {
		t.Fatalf("first attach: %+v", a.hd)
	}
	a.send(`{"type":"user","n":2}`)
	a.expect(lineN(1), lineN(2), `{"type":"result"}`)
	a.send(`{"type":"user","n":8}`)
	a.expect(lineN(1), lineN(2))
	seen := a.read
	// the link drops mid-turn
	_ = a.cmd.Process.Signal(syscall.SIGKILL)
	_ = a.cmd.Wait()
	time.Sleep(400 * time.Millisecond) // the CLI keeps going meanwhile

	st, ok, err := Status(slot)
	if err != nil || !ok || !st.Busy || st.Exited {
		t.Fatalf("status after the drop: %+v %v %v", st, ok, err)
	}

	// the same session reconnects where it stopped: nothing twice, nothing lost
	b := attach(t, home, "-only", "-from", fmt.Sprint(seen), slot)
	if b.hd.Started || b.hd.Gone || !b.hd.Busy || b.hd.Offset != seen || b.hd.Gap {
		t.Fatalf("reconnect header: %+v (seen %d)", b.hd, seen)
	}
	b.expect(lineN(3), lineN(4))
	_ = b.cmd.Process.Signal(syscall.SIGKILL)
	_ = b.cmd.Wait()

	// a new Pocket AI gets the running turn from its start; argv is ignored
	// while the holder lives
	c := attach(t, home, slot, "--", "false")
	if c.hd.Started || !c.hd.Busy {
		t.Fatalf("adopt header: %+v", c.hd)
	}
	c.expect(lineN(1), lineN(2), lineN(3), lineN(4))
	for i := 5; i <= 8; i++ {
		c.expect(lineN(i))
	}
	c.expect(`{"type":"result"}`)

	// the stop line ends the CLI; attach reports its exit and goes
	c.send(StopLine)
	c.expect(`{"type":"pocket_hold_exit","code":3}`)
	if err := c.cmd.Wait(); err == nil || c.cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("attach exit: %v", err)
	}
	for i := 0; ; i++ {
		if _, ok, _ := Status(slot); !ok {
			break
		}
		if i > 50 {
			t.Fatal("the holder did not clean up")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A CLI that ended while nobody was attached is reported to the next attach
// (with its last output); a start then begins a new one.
func TestHoldExitWhileDetached(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	slot := "thread-2"
	a := attach(t, home, slot, "--", "sh", "-c", `read x; echo '{"bye":1}'; exit 7`)
	_ = a.cmd.Process.Signal(syscall.SIGKILL)
	_ = a.cmd.Wait()
	if err := Stop(slot); err != nil { // as Pocket AI does when it closes a session it lost
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	b := attach(t, home, "-only", slot)
	if !b.hd.Exited {
		t.Fatalf("header: %+v", b.hd)
	}
	b.expect(`{"bye":1}`, `{"type":"pocket_hold_exit","code":7}`)

	c := attach(t, home, slot, "--", "sh", "-c", `echo '{"fresh":1}'; read x`)
	if !c.hd.Started {
		t.Fatalf("restart header: %+v", c.hd)
	}
	c.expect(`{"fresh":1}`)
}

// Sub-agents started in an earlier turn are told to a reader that adopts
// the CLI later, though their turn is no longer replayed.
func TestHoldKeepsTaskLines(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	slot := "thread-3"
	cli := `while IFS= read -r line; do
  echo '{"type":"system","subtype":"task_started","task_id":"a1"}'
  echo '{"type":"system","subtype":"task_progress","task_id":"a1"}'
  echo '{"type":"result"}'
done`
	a := attach(t, home, "-keep-mark", `"subtype":"task_started"`, slot, "--", "sh", "-c", cli)
	a.send(`{"type":"user"}`)
	a.expect(`{"type":"system","subtype":"task_started","task_id":"a1"}`, `{"type":"system","subtype":"task_progress","task_id":"a1"}`, `{"type":"result"}`)
	_ = a.cmd.Process.Signal(syscall.SIGKILL)
	_ = a.cmd.Wait()

	b := attach(t, home, "-only", slot)
	if len(b.hd.Kept) != 1 || b.hd.Kept[0] != `{"type":"system","subtype":"task_started","task_id":"a1"}` {
		t.Fatalf("kept: %q", b.hd.Kept)
	}
	// an exact reconnect has its state: nothing kept is sent
	c := attach(t, home, "-only", "-from", fmt.Sprint(b.hd.Offset), slot)
	if len(c.hd.Kept) != 0 {
		t.Fatalf("kept on reconnect: %q", c.hd.Kept)
	}
}

// A start right after a stop waits for the old CLI to end and starts its
// own, instead of attaching to the one on its way out.
func TestHoldStartAfterStop(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	slot := "thread-4"
	a := attach(t, home, slot, "--", "sh", "-c", `echo '{"old":1}'; while read x; do :; done; sleep 1; echo '{"old":"bye"}'`)
	a.expect(`{"old":1}`)
	a.send(StopLine)
	b := attach(t, home, slot, "--", "sh", "-c", `echo '{"new":1}'; read x`)
	if !b.hd.Started {
		t.Fatalf("attached to the stopping one: %+v", b.hd)
	}
	b.expect(`{"new":1}`)
	a.expect(`{"old":"bye"}`, `{"type":"pocket_hold_exit","code":0}`) // the one stopping still got its exit
}
