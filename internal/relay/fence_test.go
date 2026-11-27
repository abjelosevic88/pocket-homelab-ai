package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const inst = "inst-1"

type relayProc struct {
	c     *Client
	pid   int
	token string
}

// runRelay starts `pocket-hold relay` in run mode under home, the way the
// server would over ssh (here without ssh).
func runRelay(t *testing.T, home string, entry, gen int64) (*relayProc, error) {
	t.Helper()
	token := fmt.Sprintf("tok-%d-%d-%d", entry, gen, time.Now().UnixNano())
	cmd := exec.Command(holdBin(t), "relay", "-installation", inst, "-entry", strconv.FormatInt(entry, 10),
		"-generation", strconv.FormatInt(gen, 10), "-token", token)
	cmd.Env = append(os.Environ(), "HOME="+home)
	c, err := Start(context.Background(), cmd, DialOptions{Installation: inst, Entry: entry, Generation: gen, Token: token})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { c.Close() })
	return &relayProc{c: c, pid: c.Hello().PID, token: token}, nil
}

func readFence(t *testing.T, home string) Fence {
	t.Helper()
	var fe Fence
	b, err := os.ReadFile(filepath.Join(home, ".pocket-ai", "relay", inst+".fence"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fe); err != nil {
		t.Fatal(err)
	}
	return fe
}

// fenceCmd runs `pocket-hold fence` and returns its exit code and stdout.
func fenceCmd(t *testing.T, home string, entry, gen int64, args ...string) (int, string) {
	t.Helper()
	a := append([]string{"fence", "-installation", inst, "-entry", strconv.FormatInt(entry, 10), "-generation", strconv.FormatInt(gen, 10)}, args...)
	cmd := exec.Command(holdBin(t), a...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if errb.Len() > 0 {
		t.Logf("fence %v: %s", args, strings.TrimSpace(errb.String()))
	}
	return code, strings.TrimSpace(string(out))
}

func wantRefused(t *testing.T, err error, reason string) {
	t.Helper()
	var rf *Refused
	if !errors.As(err, &rf) || rf.Reason != reason {
		t.Fatalf("got %v, want refused %q", err, reason)
	}
}

func alive(c *Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Status(ctx)
	return err == nil
}

func TestFenceTakeoverOrder(t *testing.T) {
	home := t.TempDir()
	a, err := runRelay(t, home, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	if err := a.c.Bind(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	if fe := readFence(t, home); fe.PID != a.pid || len(fe.Ports) != 1 || fe.Ports[0] != port {
		t.Fatalf("fence after bind: %+v", fe)
	}
	// a higher generation takes over: the first one is gone and its port
	// free before the second answers
	b, err := runRelay(t, home, 1, 6)
	if err != nil {
		t.Fatal(err)
	}
	if isRelay(a.pid, a.token) {
		t.Fatal("the old relay still runs after the new one answered")
	}
	if !portFree(port) {
		t.Fatal("the old relay's port is still held after the new one answered")
	}
	select {
	case <-a.c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old link did not end")
	}
	if err := b.c.Bind(context.Background(), port); err != nil {
		t.Fatalf("new relay binds the port: %v", err)
	}
	fe := readFence(t, home)
	if fe.PID != b.pid || fe.Token != b.token || fe.Generation != 6 || fe.Entry != 1 || len(fe.Ports) != 1 {
		t.Fatalf("fence after takeover: %+v", fe)
	}

	// lower and equal generations are refused; the newer one is untouched
	for _, g := range []int64{4, 5, 6} {
		_, err := runRelay(t, home, 1, g)
		wantRefused(t, err, "newer")
	}
	if !alive(b.c) {
		t.Fatal("the running relay was disturbed by refused starts")
	}
	// another machine entry is refused whatever its generation
	_, err = runRelay(t, home, 2, 100)
	wantRefused(t, err, "other-entry")
	if code, out := fenceCmd(t, home, 2, 100, "end-relay"); code != 3 || !strings.Contains(out, "other-entry") {
		t.Fatalf("other entry's end-relay: %d %s", code, out)
	}
	// a stale controller (generation 5) cannot end its replacement
	if code, out := fenceCmd(t, home, 1, 5, "end-relay"); code != 3 || !strings.Contains(out, `"reason":"newer"`) {
		t.Fatalf("stale end-relay: %d %s", code, out)
	}
	if code, _ := fenceCmd(t, home, 1, 5, "free-ports", strconv.Itoa(port)); code != 3 {
		t.Fatalf("stale free-ports: exit %d", code)
	}
	if code, _ := fenceCmd(t, home, 1, 6, "check"); code != 0 {
		t.Fatalf("current check: exit %d", code)
	}
	if code, _ := fenceCmd(t, home, 1, 5, "check"); code != 3 {
		t.Fatalf("stale check: exit %d", code)
	}
	if !alive(b.c) {
		t.Fatal("the running relay was disturbed")
	}

	// the relay going on its own clears its record; the generation stays
	b.c.Close()
	waitFor(t, 5*time.Second, func() bool { return readFence(t, home).PID == 0 })
	if fe := readFence(t, home); fe.Token != "" || fe.Generation != 6 || len(fe.Ports) != 0 {
		t.Fatalf("fence after a clean end: %+v", fe)
	}
}

func TestFenceOffRefusesDelayedActions(t *testing.T) {
	home := t.TempDir()
	r, err := runRelay(t, home, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	if err := r.c.Bind(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	// off needs a higher generation
	if code, _ := fenceCmd(t, home, 1, 3, "off"); code != 3 {
		t.Fatalf("off at the same generation: exit %d", code)
	}
	if code, _ := fenceCmd(t, home, 1, 4, "off"); code != 0 {
		t.Fatalf("off: exit %d", code)
	}
	if isRelay(r.pid, r.token) || !portFree(port) {
		t.Fatal("off left the relay or its port")
	}
	fe := readFence(t, home)
	if !fe.Off || fe.Generation != 4 || fe.PID != 0 {
		t.Fatalf("fence after off: %+v", fe)
	}
	// a start and a cleanup delayed from before the switch back
	_, err = runRelay(t, home, 1, 3)
	wantRefused(t, err, "off")
	if code, out := fenceCmd(t, home, 1, 3, "free-ports", strconv.Itoa(port)); code != 3 || !strings.Contains(out, `"reason":"off"`) {
		t.Fatalf("delayed free-ports: %d %s", code, out)
	}
	if code, _ := fenceCmd(t, home, 1, 3, "end-relay"); code != 3 {
		t.Fatalf("delayed end-relay: exit %d", code)
	}
	// show prints the fence
	code, out := fenceCmd(t, home, 0, 0, "show")
	if code != 0 || !strings.Contains(out, `"off":true`) {
		t.Fatalf("show: %d %s", code, out)
	}
	// switching on again takes a new, higher generation
	if _, err := runRelay(t, home, 1, 5); err != nil {
		t.Fatal(err)
	}
}

func TestFenceReusedPIDNotKilled(t *testing.T) {
	home := t.TempDir()
	sleep := exec.Command("sleep", "1000")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = sleep.Wait(); close(waited) }()
	defer func() { _ = sleep.Process.Kill(); <-waited }()
	// the fence names a relay whose pid is now this sleep
	dir := filepath.Join(home, ".pocket-ai", "relay")
	os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(Fence{Installation: inst, Entry: 1, Generation: 2, PID: sleep.Process.Pid, Token: "tok-old-relay-gone"})
	os.WriteFile(filepath.Join(dir, inst+".fence"), b, 0o600)

	if _, err := runRelay(t, home, 1, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
		t.Fatal("the unrelated process with the recorded pid was ended")
	case <-time.After(200 * time.Millisecond):
	}
	if code, _ := fenceCmd(t, home, 1, 3, "end-relay"); code != 0 {
		t.Fatal("end-relay failed")
	}
	if code, _ := fenceCmd(t, home, 1, 4, "off"); code != 0 {
		t.Fatal("off failed")
	}
	select {
	case <-waited:
		t.Fatal("the unrelated process was ended")
	default:
	}
}

func TestFreePortsOnlyOwnProcesses(t *testing.T) {
	home := t.TempDir()
	// an unrelated program on a port
	self, _ := os.Executable()
	port := freePort(t)
	other := exec.Command(self)
	other.Env = append(os.Environ(), "RELAY_TEST_LISTEN="+strconv.Itoa(port))
	out, _ := other.StdoutPipe()
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = other.Wait(); close(waited) }()
	defer func() { _ = other.Process.Kill(); <-waited }()
	if l, _ := bufio.NewReader(out).ReadString('\n'); l != "listening\n" {
		t.Fatalf("helper: %q", l)
	}
	// a stale pocket-hold (a test relay no fence knows) on another port
	stale, err := Start(context.Background(), exec.Command(holdBin(t), "relay", "-test"), DialOptions{Test: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	sport := freePort(t)
	if err := stale.Bind(context.Background(), sport); err != nil {
		t.Fatal(err)
	}
	code, _ := fenceCmd(t, home, 1, 1, "free-ports", strconv.Itoa(port), strconv.Itoa(sport))
	if code != 1 {
		t.Fatalf("free-ports with an unrelated listener: exit %d (want 1: still held)", code)
	}
	select {
	case <-waited:
		t.Fatal("the unrelated listener was ended")
	default:
	}
	if !portFree(sport) {
		t.Fatal("the stale pocket-hold's port is still held")
	}
	select {
	case <-stale.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the stale relay still runs")
	}
	// the recorded relay's own ports are refused: end-relay first
	r, err := runRelay(t, home, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	rport := freePort(t)
	if err := r.c.Bind(context.Background(), rport); err != nil {
		t.Fatal(err)
	}
	if code, out := fenceCmd(t, home, 1, 2, "free-ports", strconv.Itoa(rport)); code != 3 || !strings.Contains(out, "busy") {
		t.Fatalf("free-ports of the live relay's port: %d %s", code, out)
	}
	if !alive(r.c) {
		t.Fatal("live relay disturbed")
	}
}

// A relay that ignores SIGTERM is killed within the takeover's bounds.
func TestTakeoverKillsStubbornRelay(t *testing.T) {
	home := t.TempDir()
	// a stand-in relay: this test binary, named pocket-hold, with the token
	// on its command line, ignoring TERM
	self, _ := os.Executable()
	fake := filepath.Join(t.TempDir(), "pocket-hold")
	if err := os.Link(self, fake); err != nil {
		data, _ := os.ReadFile(self)
		os.WriteFile(fake, data, 0o755)
	}
	token := "tok-stubborn-123"
	cmd := exec.Command(fake, "relay", "-token", token)
	cmd.Env = append(os.Environ(), "RELAY_TEST_STUBBORN=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	defer func() { _ = cmd.Process.Kill(); <-waited }()
	waitFor(t, 5*time.Second, func() bool { return isRelay(cmd.Process.Pid, token) })
	fdir := filepath.Join(home, ".pocket-ai", "relay")
	os.MkdirAll(fdir, 0o700)
	b, _ := json.Marshal(Fence{Installation: inst, Entry: 1, Generation: 1, PID: cmd.Process.Pid, Token: token})
	os.WriteFile(filepath.Join(fdir, inst+".fence"), b, 0o600)
	start := time.Now()
	if _, err := runRelay(t, home, 1, 2); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("the stubborn relay survived")
	}
	if took < takeoverGrace || took > takeoverLimit {
		t.Fatalf("takeover took %s (TERM, then KILL after %s)", took, takeoverGrace)
	}
	t.Logf("stubborn relay ended by SIGKILL; takeover took %s", took.Round(time.Millisecond))
}

func waitFor(t *testing.T, within time.Duration, ok func() bool) {
	t.Helper()
	end := time.Now().Add(within)
	for !ok() {
		if time.Now().After(end) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A test-mode relay (the button's candidate) never reads or writes the fence.
func TestTestModeLeavesNoFence(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(holdBin(t), "relay", "-test")
	cmd.Env = append(os.Environ(), "HOME="+home)
	c, err := Start(context.Background(), cmd, DialOptions{Test: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Bind(context.Background(), freePort(t)); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := os.Stat(filepath.Join(home, ".pocket-ai", "relay")); !os.IsNotExist(err) {
		t.Fatalf("test relay touched the fence folder: %v", err)
	}
}
