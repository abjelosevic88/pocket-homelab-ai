package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The remote command must reach the CLI byte for byte whatever login shell
// the remote account has (sshd runs it with `$SHELL -c`).
func TestRemoteCommandQuoting(t *testing.T) {
	args := []string{"it's", `a "quoted" \ back\slash`, "line\nbreak", "$HOME `id` $(id)", "", "--flag=x y", "ünï ✓"}
	dir := t.TempDir()
	script := remoteScript("", dir, []string{"POCKET_AI=1", "X=a b'c"}, nil, "sh", append([]string{"-c", `printf '%s|' "$PWD" "$POCKET_AI" "$X" "$@"`, "sh"}, args...))
	want := dir + "|1|a b'c|" + strings.Join(args, "|") + "|"
	for _, shell := range []string{"sh", "bash", "zsh", "fish"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		out, err := exec.Command(shell, "-c", remoteCommand(script)).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", shell, err, out)
		}
		if string(out) != want {
			t.Errorf("%s:\n got %q\nwant %q", shell, out, want)
		}
	}
}

// A process left in a thread's slot by a connection that dropped unnoticed
// is ended when the thread's next connection starts.
func TestRemoteSlotTakeover(t *testing.T) {
	home := t.TempDir()
	start := func(script string) *exec.Cmd {
		cmd := exec.Command("sh", "-c", remoteCommand(script))
		cmd.Env = append(cmd.Environ(), "HOME="+home)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // sshd makes it a session leader
		return cmd
	}
	old := start(remoteScript("thread-1", "", nil, nil, "sh", []string{"-c", "sleep 60; : stream-json"}))
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	defer old.Process.Kill()
	exited := make(chan error, 1)
	go func() { exited <- old.Wait() }()
	pidFile := filepath.Join(home, ".pocket-ai", "run", "thread-1")
	for i := 0; ; i++ {
		if b, _ := os.ReadFile(pidFile); strings.TrimSpace(string(b)) == strconv.Itoa(old.Process.Pid) {
			break
		}
		if i > 50 {
			t.Fatal("the first connection never recorded its pid")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// another thread's slot leaves it alone
	if out, err := start(remoteScript("thread-2", "", nil, nil, "echo", []string{"ok"})).CombinedOutput(); err != nil || string(out) != "ok\n" {
		t.Fatalf("other slot: %v %q", err, out)
	}
	select {
	case <-exited:
		t.Fatal("a start in another slot ended this one")
	case <-time.After(300 * time.Millisecond):
	}
	if out, err := start(remoteScript("thread-1", "", nil, nil, "echo", []string{"ok"})).CombinedOutput(); err != nil || string(out) != "ok\n" {
		t.Fatalf("takeover: %v %q", err, out)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the leftover process is still running")
	}
}

func TestLostLink(t *testing.T) {
	exit := func(code int) error {
		return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	}
	mac := &Remote{Target: "mac"}
	for _, c := range []struct {
		r      *Remote
		err    error
		stderr string
		want   string
	}{
		{mac, exit(255), "", "Lost the connection to mac. Send a message to continue."},
		{mac, exit(255), "Read from remote host mac.ts.net: Connection reset by peer\r\nclient_loop: send disconnect: Broken pipe", "Lost the connection to mac (connection reset). Send a message to continue."},
		{mac, exit(255), "Timeout, server mac not responding.", "Lost the connection to mac (timed out). Send a message to continue."},
		{mac, exit(1), "", ""},
		{nil, exit(255), "", ""},
		{mac, nil, "", ""},
	} {
		if got := lostLink(c.r, c.err, c.stderr); got != c.want {
			t.Errorf("lostLink(%v, %q) = %q, want %q", c.err, c.stderr, got, c.want)
		}
	}
}

// A dropped connection's sshd keeps the agent's forward; lsof cannot see
// sshd's sockets on Linux, so freePorts finds it through the slot's
// pocket-hold attach under it, ends it, and leaves other slots alone.
func TestFreePortsEndsTheSlotsOldConnection(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps")
	}
	dir := t.TempDir()
	hold := filepath.Join(dir, "pocket-hold-test")
	sshd := filepath.Join(dir, "sshd-session")
	if err := os.WriteFile(hold, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// "sshd" stays the parent (no exec), as sshd-session does
	if err := os.WriteFile(sshd, []byte("#!/bin/sh\n'"+hold+"' attach -from 1 -only \"$1\" -- claude\necho done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(slot string) *exec.Cmd {
		c := exec.Command(sshd, slot)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() })
		return c
	}
	slot, other := "8561216b-3c99-48cc-86be-09f05aad1428", "9c4b59a4-523b-4d12-bfd0-5b9dd8591f52"
	stale, keep := start(slot), start(other)
	time.Sleep(300 * time.Millisecond)
	out, err := exec.Command("sh", "-c", freePorts, "sh", slot).CombinedOutput()
	if err != nil {
		t.Fatalf("freePorts: %v: %s", err, out)
	}
	ended := make(chan error, 1)
	go func() { ended <- stale.Wait() }()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the slot's old connection was not ended")
	}
	if err := syscall.Kill(keep.Process.Pid, 0); err != nil {
		t.Fatalf("another slot's connection was ended: %v", err)
	}
}

// The sshd a slot's connection recorded is ended only while it is still that
// process (pid and start time), an sshd, and this account's; other slots'
// records are left alone.
func TestFreePortsEndsTheRecordedConnection(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps")
	}
	home, dir := t.TempDir(), t.TempDir()
	sshd := filepath.Join(dir, "sshd-session")
	if err := os.WriteFile(sshd, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(bin string, args ...string) *exec.Cmd {
		c := exec.Command(bin, args...)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() })
		return c
	}
	started := func(pid int) string {
		out, err := exec.Command("sh", "-c", `echo $(LC_ALL=C ps -o lstart= -p "$1")`, "sh", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	run := filepath.Join(home, ".pocket-ai", "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	record := func(slot string, pid int, when string) {
		if err := os.WriteFile(filepath.Join(run, slot+".sshd"), []byte(strconv.Itoa(pid)+" "+when+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale, reused, other, notSSHD := start(sshd), start(sshd), start(sshd), start("sleep", "30")
	time.Sleep(300 * time.Millisecond)
	record("slot-a", stale.Process.Pid, started(stale.Process.Pid))
	record("slot-b", reused.Process.Pid, "Thu Jan  1 00:00:00 1970")
	record("slot-c", notSSHD.Process.Pid, started(notSSHD.Process.Pid))
	for _, slot := range []string{"slot-b", "slot-c", "slot-d", "slot-a"} {
		cmd := exec.Command("sh", "-c", freePorts, "sh", slot)
		cmd.Env = append(os.Environ(), "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("freePorts %s: %v: %s", slot, err, out)
		}
	}
	ended := make(chan error, 1)
	go func() { ended <- stale.Wait() }()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorded connection was not ended")
	}
	for name, c := range map[string]*exec.Cmd{"a pid recorded with another start time": reused, "an sshd nobody recorded": other, "a process that is not sshd": notSSHD} {
		if err := syscall.Kill(c.Process.Pid, 0); err != nil {
			t.Errorf("%s was ended: %v", name, err)
		}
	}
}
