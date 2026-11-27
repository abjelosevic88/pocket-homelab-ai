package agent

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// An agent's connection to a real machine drops without the machine
// noticing: its sshd keeps the agent's forwarded port, while the process the
// connection ran there is gone (the holder let that attach go). FreeForwards
// must end that sshd on the first try, and never touch another program's
// listener, on another port or on that very port. Opt in with
// POCKET_PORTS_SSH_TEST=<ssh target> (localhost works); run it alone
// (-run TestFreeForwardsOverSSH): it ends the shared ssh connection it made.
func TestFreeForwardsOverSSH(t *testing.T) {
	target := os.Getenv("POCKET_PORTS_SSH_TEST")
	if target == "" {
		t.Skip("POCKET_PORTS_SSH_TEST not set")
	}
	muxBase, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(muxBase) })
	t.Setenv("XDG_RUNTIME_DIR", muxBase)
	if !strings.HasPrefix(muxDir(), muxBase) {
		t.Skip("shared ssh connections already set up by another test: run this one alone")
	}
	base := &Remote{Target: target}
	t.Cleanup(func() {
		_ = exec.Command("ssh", append(muxArgs(), "-O", "exit", target)...).Run()
	})
	ctx := context.Background()
	run := func(script string, args ...string) (string, error) {
		out, stderr, err := base.Run(ctx, 30*time.Second, script, args...)
		if err != nil && stderr != "" {
			err = fmt.Errorf("%w: %s", err, stderr)
		}
		return strings.TrimSpace(out), err
	}

	// a home of its own there: nothing goes into the machine's ~/.pocket-ai
	out, err := run(`mktemp -d "${TMPDIR:-/tmp}/pocket-ports.XXXXXX"`)
	if err != nil {
		t.Fatal(err)
	}
	home := lastLine(out)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("no temp home: %q", out)
	}
	testRemoteHome = home
	t.Cleanup(func() {
		testRemoteHome = ""
		_, _ = run(`rm -rf -- "$1"`, home)
	})

	// a port Pocket AI could forward to there right now
	canForward := func(port int) (bool, string) {
		r := &Remote{Target: target, Forwards: []string{fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9", port)}}
		cmd, err := r.command("", nil, nil, "true", nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return err == nil, strings.TrimSpace(string(out))
	}
	freePort := func() int {
		for {
			p := 20000 + rand.IntN(40000)
			if ok, _ := canForward(p); ok {
				return p
			}
		}
	}
	alive := func(pid string) bool {
		_, err := run(`kill -0 "$1"`, pid)
		return err == nil
	}

	// another program's listener there, started over ssh (its parent is an sshd)
	type listener struct {
		pid  string
		port int
		done chan struct{}
	}
	listen := func(port int) *listener {
		py := `import socket,sys,time; s=socket.socket(); s.bind(("127.0.0.1", int(sys.argv[1]))); s.listen(); print("listening", flush=True); time.sleep(900)`
		cmd := exec.Command("ssh", "-T", "-o", "BatchMode=yes", target, "--",
			remoteCommand(remotePath+"; echo $$; exec python3 -c "+shq(py)+" "+strconv.Itoa(port)))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		l := &listener{port: port, done: make(chan struct{})}
		go func() { _ = cmd.Wait(); close(l.done) }()
		t.Cleanup(func() {
			if l.pid != "" {
				_, _ = run(`kill "$1"`, l.pid)
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-l.done
		})
		lines := make(chan string, 2)
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		for _, want := range []string{"pid", "listening"} {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("listener on %d ended before %s", port, want)
				}
				if want == "pid" {
					l.pid = strings.TrimSpace(line)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("listener on %d: no %s", port, want)
			}
		}
		return l
	}
	untouched := func(l *listener) {
		t.Helper()
		select {
		case <-l.done:
			t.Errorf("the ssh session of the program on port %d was ended", l.port)
		default:
		}
		if !alive(l.pid) {
			t.Errorf("the program listening on port %d was ended", l.port)
		}
		if ok, _ := canForward(l.port); ok {
			t.Errorf("port %d is no longer held by its program", l.port)
		}
	}

	other := listen(freePort())

	// the agent's connection, through a relay here that never passes a close
	// on: when the client dies, the machine keeps the connection
	up, alias := sshHostPort(t, target)
	relay := newStuckRelay(t, up)
	slot := fmt.Sprintf("portstest-%d", rand.IntN(1e9))
	port := freePort()
	fwd := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9", port)
	conn := &Remote{Target: target, Port: relay.port(), Slot: slot, Forwards: []string{fwd}}
	cmd, err := conn.command("", nil, nil, "sleep", []string{"900"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = append([]string{cmd.Args[0], "-o", "HostName=127.0.0.1", "-o", "HostKeyAlias=" + alias}, cmd.Args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	var agentPid string
	for i := 0; agentPid == ""; i++ {
		if i > 100 {
			t.Fatal("the agent never started there")
		}
		time.Sleep(200 * time.Millisecond)
		agentPid, _ = run(`cat "$HOME/.pocket-ai/run/$1" 2>/dev/null`, slot)
	}
	t.Cleanup(func() { // the test failed before the agent was ended
		_, _ = run(`case "$(ps -o args= -p "$1" 2>/dev/null)" in "sleep 900") kill "$1";; esac`, agentPid)
	})
	if ok, msg := canForward(port); ok || !strings.Contains(msg, "remote port forwarding failed") {
		t.Fatalf("the agent's connection does not hold its port: %v %q", ok, msg)
	}
	rec, err := run(`read -r p t < "$HOME/.pocket-ai/run/$1.sshd" && echo "$p $(ps -o comm= -p "$p") ($t)"`, slot)
	if err != nil || !strings.Contains(rec, "sshd") {
		t.Fatalf("no record of the connection's sshd: %v %q", err, rec)
	}
	t.Logf("the connection's sshd: %s", rec)

	// the link drops: the client dies, the machine does not notice; the
	// process the connection ran there ends (the holder let it go)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	if _, err := run(`kill "$1"`, agentPid); err != nil {
		t.Fatal(err)
	}
	for i := 0; alive(agentPid); i++ {
		if i > 50 {
			t.Fatal("the agent did not end")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if ok, msg := canForward(port); ok || !strings.Contains(msg, "remote port forwarding failed") {
		t.Fatalf("the dropped connection no longer holds the port, nothing to test: %v %q", ok, msg)
	}

	// what link.connect does: free the port, then connect once more
	if err := (&Remote{Target: target, Slot: slot, Forwards: []string{fwd}}).FreeForwards(ctx); err != nil {
		t.Errorf("FreeForwards: %v", err)
	}
	if ok, msg := canForward(port); !ok {
		t.Fatalf("the port is still held after FreeForwards: %s", msg)
	}
	untouched(other)

	// another program on the very port an agent wants: left alone, and the
	// call says why it failed
	same := listen(freePort())
	err = (&Remote{Target: target, Slot: slot + "-2", Forwards: []string{fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9", same.port)}}).FreeForwards(ctx)
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(same.port)) {
		t.Errorf("FreeForwards on another program's port: %v, want an error naming the port", err)
	} else {
		t.Logf("another program's port: %v", err)
	}
	untouched(same)
	untouched(other)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// sshHostPort is where ssh connects for target, and the name its host key
// is known by.
func sshHostPort(t *testing.T, target string) (string, string) {
	out, err := exec.Command("ssh", "-G", target).Output()
	if err != nil {
		t.Fatal(err)
	}
	conf := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			conf[k] = v
		}
	}
	alias := conf["hostkeyalias"]
	if alias == "" || alias == "none" {
		alias = conf["hostname"]
		if conf["port"] != "22" {
			alias = "[" + alias + "]:" + conf["port"]
		}
	}
	return net.JoinHostPort(conf["hostname"], conf["port"]), alias
}

// stuckRelay passes bytes between ssh and the machine but never a close:
// a client that dies leaves the machine's side of the connection open, as a
// network that drops does.
type stuckRelay struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newStuckRelay(t *testing.T, upstream string) *stuckRelay {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &stuckRelay{ln: ln}
	t.Cleanup(r.close)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", upstream)
			if err != nil {
				c.Close()
				continue
			}
			r.mu.Lock()
			r.conns = append(r.conns, c, u)
			r.mu.Unlock()
			go pass(c, u)
			go pass(u, c)
		}
	}()
	return r
}

func pass(from, to net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := from.Read(buf)
		if n > 0 {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return // and nothing closed
		}
	}
}

func (r *stuckRelay) port() int { return r.ln.Addr().(*net.TCPAddr).Port }

func (r *stuckRelay) close() {
	r.ln.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		c.Close()
	}
}
