package hold

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// inproc attaches to slot's holder in-process (Open + AttachStream), the
// way Pocket AI does with Hold.Dial; the result reads like a pocket-hold
// attach process.
func inproc(t *testing.T, slot string, from int64) (a *attached, stdout io.Closer, wait func() int) {
	t.Helper()
	nc, err := DialSlot(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	c, hd, err := Open(nc, from)
	if err != nil {
		t.Fatal(err)
	}
	out, in, wait := AttachStream(c, hd, os.Stderr)
	a = &attached{t: t, in: in, out: bufio.NewReader(out), lines: make(chan string, 100)}
	t.Cleanup(func() { _ = out.Close() })
	a.start()
	return a, out, wait
}

// runHold runs the test binary as pocket-hold and returns the header it
// printed, its exit code and its stderr.
func runHold(t *testing.T, home string, args ...string) (Header, int, string) {
	t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "POCKET_HOLD_TEST_MAIN=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var hd Header
	line, _, _ := bytes.Cut(out.Bytes(), []byte("\n"))
	if len(line) > 0 {
		if err := json.Unmarshal(line, &hd); err != nil || hd.Type != HeaderType {
			t.Fatalf("bad header %q (stderr %s)", line, errb.String())
		}
	}
	return hd, code, errb.String()
}

func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "ph") // short: socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	return home
}

func waitNoHolder(t *testing.T, slot string) {
	t.Helper()
	for i := 0; ; i++ {
		if _, ok, _ := Status(slot); !ok {
			return
		}
		if i > 100 {
			t.Fatal("the holder did not go")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

var startMarks = []string{"-start-mark", `"type":"user"`, "-end-mark", `"type":"result"`}

// attach -start-only starts the holder and leaves; the in-process client
// then talks to it like attach would: output, input, offsets, the exit.
func TestStartOnlyThenInProcess(t *testing.T) {
	home := shortHome(t)
	const slot = "start-only"
	args := append(append([]string{"attach", "-start-only"}, startMarks...), slot, "--", "sh", "-c", fakeCLI)
	hd, code, stderr := runHold(t, home, args...)
	if code != 0 || !hd.Started || hd.Proto != Proto || hd.PID == 0 || hd.SlotBusy {
		t.Fatalf("first start-only: %+v exit %d %s", hd, code, stderr)
	}
	if again, code, _ := runHold(t, home, args...); code != 0 || again.Started || again.PID != hd.PID {
		t.Fatalf("second start-only: %+v exit %d", again, code)
	}
	if _, code, _ := runHold(t, home, "attach", "-start-only", "-only", slot, "--", "true"); code != 2 {
		t.Fatalf("-start-only with -only: exit %d", code)
	}

	a, out, _ := inproc(t, slot, FromReplay)
	if a.hd.Proto != Proto || a.hd.Busy || a.hd.Offset != 0 {
		t.Fatalf("header: %+v", a.hd)
	}
	user := `{"type":"user","n":4}`
	a.send(user)
	a.expect(lineN(1))
	seen := a.hd.Offset + a.read
	_ = out.Close() // let go mid-turn
	time.Sleep(200 * time.Millisecond)

	b, _, _ := inproc(t, slot, seen)
	if b.hd.Offset != seen || !b.hd.Busy || b.hd.In != int64(len(user)+1) || b.hd.Gap {
		t.Fatalf("reconnect: %+v (seen %d)", b.hd, seen)
	}
	b.expect(lineN(2), lineN(3), lineN(4), `{"type":"result"}`)
	// a line cut off by stdin closing is not sent (its writer sends it
	// again), and closing stdin only detaches it
	if _, err := io.WriteString(b.in, `{"type":"user","n":1}`); err != nil {
		t.Fatal(err)
	}
	_ = b.in.Close()
	time.Sleep(300 * time.Millisecond)
	if st, ok, _ := Status(slot); !ok || st.In != int64(len(user)+1) || st.Busy || st.Exited {
		t.Fatalf("after a cut-off line: %+v %v", st, ok)
	}
	select {
	case l, ok := <-b.lines:
		t.Fatalf("output after stdin closed: %q %v", l, ok)
	default:
	}

	c, _, wait := inproc(t, slot, b.hd.Offset+b.read)
	c.send(StopLine)
	c.expect(`{"type":"pocket_hold_exit","code":3}`)
	if code := wait(); code != 3 {
		t.Fatalf("wait: %d", code)
	}
	waitNoHolder(t, slot) // the exit was acknowledged
}

// An exit that was not read before the reader went is not acknowledged:
// the holder keeps it for the next one.
func TestAttachStreamExitNotDelivered(t *testing.T) {
	home := shortHome(t)
	const slot = "undelivered"
	args := append(append([]string{"attach", "-start-only"}, startMarks...), slot, "--", "sh", "-c", `read x; echo '{"bye":1}'; exit 5`)
	if _, code, stderr := runHold(t, home, args...); code != 0 {
		t.Fatalf("start-only: exit %d %s", code, stderr)
	}
	nc, err := DialSlot(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	c, hd, err := Open(nc, FromReplay)
	if err != nil {
		t.Fatal(err)
	}
	out, in, wait := AttachStream(c, hd, nil)
	r := bufio.NewReader(out)
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(in, "go\n")
	if l, _ := r.ReadString('\n'); l != "{\"bye\":1}\n" {
		t.Fatalf("got %q", l)
	}
	for i := 0; ; i++ { // the CLI has ended; its exit waits to be read
		if st, _, _ := Status(slot); st.Exited {
			break
		}
		if i > 100 {
			t.Fatal("the CLI did not end")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = out.Close()
	if code := wait(); code == 5 || code == 0 {
		t.Fatalf("wait after closing: %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	b, _, wait2 := inproc(t, slot, FromReplay)
	if !b.hd.Exited {
		t.Fatalf("header: %+v", b.hd)
	}
	b.expect(`{"bye":1}`, `{"type":"pocket_hold_exit","code":5}`) // the output since the last turn, then the exit
	if code := wait2(); code != 5 {
		t.Fatalf("wait: %d", code)
	}
	waitNoHolder(t, slot)
}

// A second start while another holds the slot's lock without listening
// yet starts nothing and says "slot busy" (exit 75), with and without
// -start-only.
func TestSlotBusy(t *testing.T) {
	home := shortHome(t)
	const slot = "busy"
	dir, _ := Dir(slot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "ran")
	cli := []string{"sh", "-c", "touch " + marker + "; read x"}
	type res struct {
		hd     Header
		code   int
		stderr string
	}
	got := make(chan res, 2)
	for _, extra := range [][]string{{"-start-only"}, nil} {
		args := append(append(append([]string{"attach"}, extra...), startMarks...), append([]string{slot, "--"}, cli...)...)
		go func() {
			hd, code, stderr := runHold(t, home, args...)
			got <- res{hd, code, stderr}
		}()
	}
	for range 2 {
		r := <-got
		if r.code != ExitSlotBusy || !r.hd.SlotBusy || r.hd.Started || !strings.Contains(r.stderr, "slot busy") {
			t.Fatalf("busy start: %+v exit %d %q", r.hd, r.code, r.stderr)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a CLI was started")
	}
	// the slot free again: a start works
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	hd, code, stderr := runHold(t, home, append(append([]string{"attach", "-start-only"}, startMarks...), append([]string{slot, "--"}, cli...)...)...)
	if code != 0 || !hd.Started {
		t.Fatalf("start after: %+v exit %d %s", hd, code, stderr)
	}
	_ = Stop(slot)
	_, _, wait := inproc(t, slot, 1<<62) // collects the exit, so the holder goes
	wait()
	waitNoHolder(t, slot)
}

// fakeHolder answers every hello on slot's socket with hd.
func fakeHolder(t *testing.T, slot string, hd Header) {
	t.Helper()
	dir, _ := Dir(slot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sockPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if _, _, err := readFrame(bufio.NewReader(c)); err != nil {
					return
				}
				b, _ := json.Marshal(hd)
				_ = writeFrame(c, fHeader, b)
				time.Sleep(time.Second)
			}()
		}
	}()
}

// A holder newer than this build is refused with a clear message, by the
// in-process client and by attach.
func TestProtoRefused(t *testing.T) {
	home := shortHome(t)
	const slot = "newer"
	fakeHolder(t, slot, Header{Proto: Proto + 1, PID: 1})
	want := fmt.Sprintf("holder speaks protocol %d, this Pocket AI knows %d: update Pocket AI", Proto+1, Proto)
	nc, err := DialSlot(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	var pe *ProtoError
	if _, _, err := Open(nc, FromReplay); !errors.As(err, &pe) || err.Error() != want {
		t.Fatalf("Open: %v", err)
	}
	nc, _ = DialSlot(context.Background(), slot)
	if _, err := Peek(nc); !errors.As(err, &pe) {
		t.Fatalf("Peek: %v", err)
	}
	if _, _, err := Status(slot); !errors.As(err, &pe) {
		t.Fatalf("Status: %v", err)
	}
	for _, args := range [][]string{{"attach", "-only", slot}, {"attach", "-start-only", slot, "--", "true"}, {"attach", slot, "--", "true"}} {
		hd, code, stderr := runHold(t, home, args...)
		if code != 125 || hd.Type != "" || !strings.Contains(stderr, want) {
			t.Fatalf("%v: header %+v exit %d %q", args, hd, code, stderr)
		}
	}
	// the same protocol, or none (an older holder), is fine
	const old = "older"
	fakeHolder(t, old, Header{PID: 1})
	nc, _ = DialSlot(context.Background(), old)
	if hd, err := Peek(nc); err != nil || hd.PID != 1 {
		t.Fatalf("older holder: %+v %v", hd, err)
	}
	if _, err := DialSlot(context.Background(), "nobody"); !errors.Is(err, ErrNoHolder) {
		t.Fatalf("no holder: %v", err)
	}
}

// The in-process client against a holder from the previous release:
// output, input, offsets and the exit. POCKET_HOLD_PREV is that release's
// pocket-hold (git archive v0.121.0 | tar -x; go build ./cmd/pocket-hold).
func TestClientPrevRelease(t *testing.T) {
	prev := os.Getenv("POCKET_HOLD_PREV")
	if prev == "" {
		t.Skip("POCKET_HOLD_PREV not set")
	}
	home := shortHome(t)
	const slot = "prev"
	// started as that release starts it: its attach, its serve
	a := attachWith(t, prev, home, slot, "--", "sh", "-c", fakeCLI)
	if !a.hd.Started || a.hd.Proto != 0 {
		t.Fatalf("previous release's attach: %+v", a.hd)
	}
	_ = a.cmd.Process.Signal(syscall.SIGKILL)
	_ = a.cmd.Wait()

	b, out, _ := inproc(t, slot, FromReplay)
	if b.hd.Proto != 0 || b.hd.Busy || b.hd.Offset != 0 || b.hd.In != 0 || b.hd.PID != a.hd.PID {
		t.Fatalf("header: %+v", b.hd)
	}
	user := `{"type":"user","n":3}`
	b.send(user)
	b.expect(lineN(1))
	seen := b.hd.Offset + b.read
	_ = out.Close()
	time.Sleep(200 * time.Millisecond)

	c, _, wait := inproc(t, slot, seen)
	if c.hd.Offset != seen || !c.hd.Busy || c.hd.In != int64(len(user)+1) || c.hd.Gap {
		t.Fatalf("reconnect: %+v (seen %d)", c.hd, seen)
	}
	c.expect(lineN(2), lineN(3), `{"type":"result"}`)
	nc, err := DialSlot(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := Peek(nc); err != nil || st.Busy || st.Offset != seen+c.read || st.Exited {
		t.Fatalf("peek: %+v %v (read to %d)", st, err, seen+c.read)
	}
	c.send(StopLine)
	c.expect(`{"type":"pocket_hold_exit","code":3}`)
	if code := wait(); code != 3 {
		t.Fatalf("wait: %d", code)
	}
	waitNoHolder(t, slot)

	// stopped through StopVia, and peeked after it
	d := attachWith(t, prev, home, slot, "--", "sh", "-c", fakeCLI)
	if !d.hd.Started {
		t.Fatalf("second start: %+v", d.hd)
	}
	_ = d.cmd.Process.Signal(syscall.SIGKILL)
	_ = d.cmd.Wait()
	nc, err = DialSlot(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StopVia(nc); err != nil {
		t.Fatal(err)
	}
	e, _, wait := inproc(t, slot, FromReplay)
	e.expect(`{"type":"pocket_hold_exit","code":3}`)
	if code := wait(); code != 3 {
		t.Fatalf("wait after StopVia: %d", code)
	}
	waitNoHolder(t, slot)
}
