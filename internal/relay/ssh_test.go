package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sshHost runs `ssh -T -o BatchMode=yes …` the way the server's link does
// (no ControlMaster, keep-alives, stderr kept apart).
type sshHost struct {
	t    *testing.T
	host string
	dir  string // throwaway folder on the machine: the binary and a HOME
	bin  string
}

func (h *sshHost) args(extra ...string) []string {
	a := append([]string{"-T", "-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=15"}, extra...)
	return append(a, h.host, "--")
}

// remote wraps script as the agent package does (remoteCommand): base64 in
// one single-quoted word, whatever the login shell; the profile and PATH
// preamble of remoteScript; HOME is the throwaway one.
func (h *sshHost) remote(script string) string {
	full := `HOME=` + h.dir + `/home; export HOME; ` +
		`[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1 </dev/null; [ -r "$HOME/.profile" ] && . "$HOME/.profile" >/dev/null 2>&1 </dev/null; ` +
		`PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"; export PATH; ` + script
	b := base64.StdEncoding.EncodeToString([]byte(full))
	return `exec /bin/sh -c 'eval "$(echo ` + b + ` | base64 -d 2>/dev/null || echo ` + b + ` | base64 -D)"'`
}

func (h *sshHost) cmd(script string, extra ...string) *exec.Cmd {
	return exec.Command("ssh", append(h.args(extra...), h.remote(script))...)
}

// run runs script over plain ssh and returns its stdout and exit code.
func (h *sshHost) run(script string) (string, int) {
	h.t.Helper()
	var errb bytes.Buffer
	c := h.cmd(script)
	c.Stderr = &errb
	out, err := c.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("ssh: %v %s", err, errb.String())
	}
	return strings.TrimSpace(string(out)), code
}

// TestSSH runs the relay over real ssh: RELAY_SSH_TEST=localhost (or any
// host this account reaches with BatchMode). pocket-hold is built for the
// machine, copied to a throwaway folder there, and removed at the end.
func TestSSH(t *testing.T) {
	host := os.Getenv("RELAY_SSH_TEST")
	if host == "" {
		t.Skip("RELAY_SSH_TEST not set")
	}
	h := &sshHost{t: t, host: host}
	uname, code := h.run("uname -sm")
	if code != 0 {
		t.Fatalf("uname: %d", code)
	}
	goos, goarch := "", ""
	switch f := strings.Fields(uname); {
	case len(f) != 2:
	case f[0] == "Linux":
		goos = "linux"
	case f[0] == "Darwin":
		goos = "darwin"
	}
	switch strings.Fields(uname)[1] {
	case "x86_64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	}
	if goos == "" || goarch == "" {
		t.Fatalf("machine is %q", uname)
	}
	local := filepath.Join(t.TempDir(), "pocket-hold")
	build := exec.Command("go", "build", "-trimpath", "-o", local, "../../cmd/pocket-hold")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "HOME="+origHome)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir, code := h.run(`d=$(mktemp -d /tmp/mlrelay-test.XXXXXX) && mkdir "$d/home" && echo "$d"`)
	if code != 0 || !strings.HasPrefix(dir, "/tmp/mlrelay-test.") {
		t.Fatalf("remote dir: %q %d", dir, code)
	}
	h.dir, h.bin = dir, dir+"/pocket-hold"
	t.Cleanup(func() {
		// end what the test left (holders) by what they are, then the folder
		h.run(`for s in "$HOME"/.pocket-ai/hold/*/; do [ -d "$s" ] && "` + h.bin + `" stop "$(basename "$s")"; done; sleep 1; rm -rf "` + dir + `"`)
		if out, _ := h.run(`[ -e "` + dir + `" ] && echo left`); out == "left" {
			t.Errorf("%s left on %s", dir, host)
		}
	})
	data, _ := os.ReadFile(local)
	up := h.cmd(`set -- "` + h.bin + `"; cat > "$1" && chmod 700 "$1"`)
	up.Stdin = bytes.NewReader(data)
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("upload: %v %s", err, out)
	}
	t.Logf("%s: %s, pocket-hold in %s", host, uname, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pocket tools here")
	}))
	defer srv.Close()
	tools := func(int) (net.Conn, error) { return net.Dial("tcp", srv.Listener.Addr().String()) }
	inst := "e2e-" + strconv.Itoa(rand.Intn(1e6))
	start := func(gen int64, test bool) (*Client, *exec.Cmd, error) {
		token := fmt.Sprintf("tok-e2e-%d-%d", gen, rand.Int63())
		script := `exec "` + h.bin + `" relay -test -ping 500ms -dead 3s`
		if !test {
			script = fmt.Sprintf(`exec "%s" relay -installation %s -entry 1 -generation %d -token %s -ping 500ms -dead 3s`, h.bin, inst, gen, token)
		}
		cmd := h.cmd(script)
		t0 := time.Now()
		c, err := Start(context.Background(), cmd, DialOptions{Installation: inst, Entry: 1, Generation: gen, Token: token, Test: test,
			Tools: tools, PingEvery: 500 * time.Millisecond, DeadAfter: 3 * time.Second})
		if err == nil {
			t.Logf("link generation %d up in %s (relay pid %d, %s/%s, version %s)", gen, time.Since(t0).Round(time.Millisecond),
				c.Hello().PID, c.Hello().OS, c.Hello().Arch, c.Hello().Version)
			t.Cleanup(func() { c.Close() })
		}
		return c, cmd, err
	}
	ctx := context.Background()

	// stray output from a login script fails cleanly
	stray := exec.Command("ssh", append(h.args(), h.remote(`echo "Last login: yesterday"; exec "`+h.bin+`" relay -test`))...)
	if _, err := Start(ctx, stray, DialOptions{Test: true}); err == nil || !strings.Contains(err.Error(), "Last login") {
		t.Fatalf("stray output: %v", err)
	}

	// a test-mode relay leaves no fence
	tc, _, err := start(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := goos + "/" + goarch; tc.Hello().OS+"/"+tc.Hello().Arch != want {
		t.Fatalf("hello %+v, want %s", tc.Hello(), want)
	}
	tc.Close()

	a, _, err := start(10, false)
	if err != nil {
		t.Fatal(err)
	}
	// exec: environment like a plain ssh command, stdin to EOF, exit code
	var out bytes.Buffer
	res, err := a.Exec(ctx, ExecRequest{Script: `echo "$HOME"; wc -c | tr -d ' '; exit 7`, Stdin: strings.NewReader(strings.Repeat("x", 300000)), Stdout: &out})
	if err != nil || res.Code != 7 || out.String() != dir+"/home\n300000\n" {
		t.Fatalf("exec: %+v %v %q", res, err, out.String())
	}
	plain, _ := h.run(`env | sort | grep -vE '^(SSH_|_=|SHLVL=|PWD=|OLDPWD=)'`)
	out.Reset()
	a.Exec(ctx, ExecRequest{Script: `env | sort | grep -vE '^(SSH_|_=|SHLVL=|PWD=|OLDPWD=)'`, Stdout: &out})
	if diff := lineDiff(plain, strings.TrimSpace(out.String())); diff != "" {
		t.Logf("environment: a plain ssh command and the link differ in:\n%s", diff)
	} else {
		t.Log("environment: same as a plain ssh command")
	}
	var rtts []time.Duration
	for range 10 {
		t0 := time.Now()
		if _, err := a.Exec(ctx, ExecRequest{Script: "true"}); err != nil {
			t.Fatal(err)
		}
		rtts = append(rtts, time.Since(t0))
	}
	t0 := time.Now()
	h.run("true")
	t.Logf("exec round trips over the link %v; one plain ssh command %s", rtts, time.Since(t0).Round(time.Millisecond))

	// cancel: the command's group is gone on the machine
	cctx, cancel := context.WithCancel(ctx)
	lw := newLineWriter()
	done := make(chan error, 1)
	go func() {
		_, err := a.Exec(cctx, ExecRequest{Script: `echo $$; sleep 1000 & sleep 1000`, Stdout: lw})
		done <- err
	}()
	pg := lw.next(t, 10*time.Second)
	cancel()
	<-done
	time.Sleep(500 * time.Millisecond)
	if left, _ := h.run(`pgrep -g ` + pg + ` 2>/dev/null | head -1`); left != "" {
		t.Fatalf("process group %s still on the machine: %s", pg, left)
	}

	// a holder started over exec (in its own session), spoken to over a hold stream
	_, err = a.Exec(ctx, ExecRequest{Script: `"$1" attach e2e -- cat </dev/null >/dev/null 2>&1 & a=$!; i=0; ` +
		`while [ ! -S "$HOME/.pocket-ai/hold/e2e/s" ] && [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done; kill $a`, Args: []string{h.bin}})
	if err != nil {
		t.Fatal(err)
	}
	hc, err := a.OpenHold(ctx, "e2e")
	if err != nil {
		t.Fatal(err)
	}
	_, hr, err := holdHello(hc, 0)
	if err != nil {
		t.Fatal(err)
	}
	holdWrite(hc, 'i', []byte("over ssh\n"))
	if typ, b, err := holdRead(hr); err != nil || typ != 'o' || string(b) != "over ssh\n" {
		t.Fatalf("holder: %q %q %v", typ, b, err)
	}
	hc.Close()

	// tools: a port on the machine reaches the server's endpoint
	port := 40000 + rand.Intn(20000)
	if err := a.Bind(ctx, port); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := a.Exec(ctx, ExecRequest{Script: `curl -s "http://127.0.0.1:$1/"`, Args: []string{strconv.Itoa(port)}, Stdout: &out}); err != nil || out.String() != "pocket tools here" {
		t.Fatalf("tools: %q %v", out.String(), err)
	}

	// takeover: generation 11 ends 10 before it answers; its port is free for it
	b, _, err := start(11, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old link did not end")
	}
	if err := b.Bind(ctx, port); err != nil {
		t.Fatalf("new relay binds the old one's port: %v", err)
	}
	if _, _, err := start(10, false); err == nil {
		t.Fatal("an older generation started")
	} else {
		wantRefused(t, err, "newer")
	}
	if !alive(b) {
		t.Fatal("refused start disturbed the running relay")
	}

	// a dead link (the ssh client frozen: nothing reaches either side, the
	// TCP connection stays): both ends notice, the relay leaves the machine
	bpid := b.Hello().PID
	stall := time.Now()
	_ = b.cmd.Process.Signal(syscall.SIGSTOP)
	select {
	case <-b.Done():
		t.Logf("server noticed the frozen link after %s: %v", time.Since(stall).Round(time.Millisecond), b.Err())
	case <-time.After(10 * time.Second):
		t.Fatal("server did not notice")
	}
	var gone time.Duration
	for i := 0; i < 40; i++ {
		if _, code := h.run(`ps -o args= -p ` + strconv.Itoa(bpid) + ` | grep -q " relay "`); code != 0 {
			gone = time.Since(stall)
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if gone == 0 {
		t.Fatal("the relay is still on the machine 10 s after its link froze")
	}
	t.Logf("relay gone from the machine %s after the freeze (includes plain ssh checks)", gone.Round(time.Millisecond))
	fenceJSON, _ := h.run(`"` + h.bin + `" fence -installation ` + inst + ` show`)
	var fe Fence
	json.Unmarshal([]byte(fenceJSON), &fe)
	if fe.PID != 0 || fe.Generation != 11 || len(fe.Ports) != 0 {
		t.Fatalf("fence after the relay left: %s", fenceJSON)
	}

	// free-ports: an old forward of an agent's ssh connection, found by its
	// sshd record, is ended; nothing else is
	fport := 40000 + rand.Intn(20000)
	fwd := h.cmd(`mkdir -p "$HOME/.pocket-ai/run"; echo "$PPID $(ps -o lstart= -p $PPID)" > "$HOME/.pocket-ai/run/agent1.sshd"; echo ready; exec sleep 1000`,
		"-o", "ExitOnForwardFailure=yes", "-R", fmt.Sprintf("127.0.0.1:%d:%s", fport, srv.Listener.Addr().String()))
	fout, _ := fwd.StdoutPipe()
	if err := fwd.Start(); err != nil {
		t.Fatal(err)
	}
	fwdDone := make(chan error, 1)
	go func() { fwdDone <- fwd.Wait() }()
	defer fwd.Process.Kill()
	line := make([]byte, 6)
	if _, err := io.ReadFull(fout, line); err != nil || string(line) != "ready\n" {
		t.Fatalf("forward: %q %v", line, err)
	}
	if out, code := h.run(`curl -s http://127.0.0.1:` + strconv.Itoa(fport) + `/`); code != 0 || out != "pocket tools here" {
		t.Fatalf("forward not up: %q %d", out, code)
	}
	if out, code := h.run(`"` + h.bin + `" fence -installation ` + inst + ` -entry 1 -generation 10 free-ports -slot agent1 ` + strconv.Itoa(fport)); code != 3 {
		t.Fatalf("stale free-ports: %d %s", code, out)
	}
	if out, code := h.run(`"` + h.bin + `" fence -installation ` + inst + ` -entry 1 -generation 11 free-ports -slot agent1 ` + strconv.Itoa(fport)); code != 0 {
		t.Fatalf("free-ports: %d %s", code, out)
	}
	select {
	case <-fwdDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the forward's ssh connection was not ended")
	}

	// off: the fence advances, later starts of older generations are refused
	if out, code := h.run(`"` + h.bin + `" fence -installation ` + inst + ` -entry 1 -generation 12 off`); code != 0 {
		t.Fatalf("off: %d %s", code, out)
	}
	if _, _, err := start(11, false); err == nil {
		t.Fatal("started after off")
	} else {
		wantRefused(t, err, "off")
	}
}

func lineDiff(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var d []string
	for l := range ma {
		if !mb[l] {
			d = append(d, "  ssh only:  "+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			d = append(d, "  link only: "+l)
		}
	}
	return strings.Join(d, "\n")
}
