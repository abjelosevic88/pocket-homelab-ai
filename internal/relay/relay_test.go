package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecOutputAndExit(t *testing.T) {
	p := startPair(t, pairOpts{})
	var out, errb bytes.Buffer
	res, err := p.c.Exec(context.Background(), ExecRequest{
		Script: `printf '%s|%s\n' "$1" "$2"; echo oops >&2; exit 3`,
		Args:   []string{"a b", "c'd"},
		Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || res.Code != 3 || out.String() != "a b|c'd\n" || errb.String() != "oops\n" {
		t.Fatalf("got %+v stdout %q stderr %q", res, out.String(), errb.String())
	}
	// a signal is reported as such
	res, err = p.c.Exec(context.Background(), ExecRequest{Script: `kill -TERM $$`})
	if err != nil || res.Signal != "SIGTERM" || res.Code != 128+15 {
		t.Fatalf("signal: %+v %v", res, err)
	}
}

func TestExecStdinToEOF(t *testing.T) {
	p := startPair(t, pairOpts{})
	in := strings.Repeat("0123456789abcdef", 64<<10) // 1 MiB: many i frames
	var out bytes.Buffer
	res, err := p.c.Exec(context.Background(), ExecRequest{Script: `wc -c | tr -d ' '`, Stdin: strings.NewReader(in), Stdout: &out})
	if err != nil || res.Code != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.TrimSpace(out.String()) != strconv.Itoa(len(in)) {
		t.Fatalf("command read %q bytes, sent %d", out.String(), len(in))
	}
	// without stdin the command reads /dev/null
	out.Reset()
	if _, err := p.c.Exec(context.Background(), ExecRequest{Script: `cat; echo end`, Stdout: &out}); err != nil || out.String() != "end\n" {
		t.Fatalf("no stdin: %q %v", out.String(), err)
	}
}

// pgidOf runs script (which must print $$ first) and returns the group id
// once it printed it, and a channel with Exec's result.
func execInBackground(t *testing.T, c *Client, ctx context.Context, script string) (int, chan error) {
	t.Helper()
	lw := newLineWriter()
	done := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, ExecRequest{Script: script, Stdout: lw})
		done <- err
	}()
	pgid, err := strconv.Atoi(lw.next(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return pgid, done
}

func TestExecCancelEndsGroup(t *testing.T) {
	p := startPair(t, pairOpts{})
	for _, tc := range []struct{ name, script string }{
		{"term", `echo $$; sleep 1000 & sleep 1000`},
		{"ignores TERM", `trap '' TERM; echo $$; sleep 1000 & sleep 1000`}, // needs the SIGKILL
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			pgid, done := execInBackground(t, p.c, ctx, tc.script)
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Exec after cancel: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Exec did not return after cancel")
			}
			groupGone(t, pgid, 5*time.Second)
			t.Logf("group gone %s after cancel", time.Since(start).Round(time.Millisecond))
		})
	}
}

func TestExecStreamResetEndsGroup(t *testing.T) {
	p := startPair(t, pairOpts{})
	s, err := p.c.open(context.Background(), openHeader{Kind: kindExec, Script: `echo $$; sleep 1000 & sleep 1000`})
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(s)
	var pgid int
	for pgid == 0 {
		typ, b, err := readFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		if typ == xOut {
			pgid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	s.Close() // gone before the exit: a reset
	groupGone(t, pgid, 5*time.Second)
}

func TestExecLinkDeathEndsGroup(t *testing.T) {
	p := startPair(t, pairOpts{})
	pgid, done := execInBackground(t, p.c, context.Background(), `echo $$; sleep 1000 & sleep 1000`)
	p.server.Close() // the link goes
	if err := <-done; !errors.Is(err, ErrUnknown) {
		t.Fatalf("Exec on a dead link: %v (want ErrUnknown)", err)
	}
	groupGone(t, pgid, 5*time.Second)
	select {
	case <-p.served:
	case <-time.After(5 * time.Second):
		t.Fatal("relay still serving a closed link")
	}
	// nothing goes out any more, provably
	if _, err := p.c.Exec(context.Background(), ExecRequest{Script: "true"}); !errors.Is(err, ErrNotSent) {
		t.Fatalf("Exec after the link died: %v (want ErrNotSent)", err)
	}
}

func TestHoldStream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	startHolder(t, home, "s1", "cat")
	p := startPair(t, pairOpts{})
	c, err := p.c.OpenHold(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hd, r, err := holdHello(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hd["pid"] == nil {
		t.Fatalf("header %v", hd)
	}
	if err := holdWrite(c, 'i', []byte("hello through the link\n")); err != nil {
		t.Fatal(err)
	}
	typ, b, err := holdRead(r)
	if err != nil || typ != 'o' || string(b) != "hello through the link\n" {
		t.Fatalf("got %q %q %v", typ, b, err)
	}
	// end the CLI: its exit comes, the ack lets the holder go
	_ = holdWrite(c, 'q', nil)
	for {
		typ, b, err = holdRead(r)
		if err != nil {
			t.Fatalf("no exit: %v", err)
		}
		if typ == 'x' {
			break
		}
	}
	var x struct{ Code int }
	_ = json.Unmarshal(b, &x)
	_ = holdWrite(c, 'a', nil)
	// the holder closes its end: the stream ends too
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, _, err := holdRead(r); err == nil {
		t.Fatal("stream still open after the holder ended")
	}
	if _, err := p.c.OpenHold(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "no holder") {
		t.Fatalf("OpenHold without a holder: %v", err)
	}
	if _, err := p.c.OpenHold(context.Background(), "../x"); err == nil {
		t.Fatal("bad slot accepted")
	}
}

func TestToolsRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tools for "+r.URL.Path)
	}))
	defer srv.Close()
	asked := make(chan int, 10)
	p := startPair(t, pairOpts{dial: DialOptions{Tools: func(port int) (net.Conn, error) {
		asked <- port
		return net.Dial("tcp", srv.Listener.Addr().String())
	}}})
	port := freePort(t)
	if err := p.c.Bind(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	// "the machine": a CLI calling its tools port
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "tools for /mcp" {
		t.Fatalf("got %q", b)
	}
	if got := <-asked; got != port {
		t.Fatalf("server dialled for port %d, not %d", got, port)
	}
}

func TestBindUnbindSync(t *testing.T) {
	hold := make(chan net.Conn, 10)
	p := startPair(t, pairOpts{dial: DialOptions{Tools: func(int) (net.Conn, error) {
		a, b := net.Pipe()
		hold <- b
		return a, nil
	}}})
	ctx := context.Background()
	a, b := freePort(t), freePort(t)
	if err := p.c.Bind(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := p.c.Bind(ctx, a); err != nil {
		t.Fatalf("second bind of the same port: %v", err)
	}
	// a port someone else holds
	other, _ := net.Listen("tcp", "127.0.0.1:0")
	defer other.Close()
	op := other.Addr().(*net.TCPAddr).Port
	if err := p.c.Bind(ctx, op); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("bind of a held port: %v", err)
	}
	if err := p.c.Bind(ctx, 80); err == nil {
		t.Fatal("privileged port accepted")
	}
	// a connection through a, then unbind: listener and connection go
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(a))
	if err != nil {
		t.Fatal(err)
	}
	<-hold // the server side of it exists
	if err := p.c.Unbind(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(a)); err == nil {
		t.Fatal("port still listening after unbind")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
		t.Fatalf("accepted connection not closed by unbind: %v", err)
	}
	// sync: exactly these
	if err := p.c.Bind(ctx, b); err != nil {
		t.Fatal(err)
	}
	bound, failed, err := p.c.Sync(ctx, []int{a, op})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bound, []int{a}) || failed[op] != "in use" {
		t.Fatalf("sync: bound %v failed %v", bound, failed)
	}
	st, err := p.c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Ports, []int{a}) {
		t.Fatalf("ports after sync %v", st.Ports)
	}
	if !portFree(b) {
		t.Fatal("sync left a port it did not list")
	}
	if _, _, err := p.c.Sync(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !portFree(a) {
		t.Fatal("empty sync left a port")
	}
}

func TestStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	startHolder(t, home, "st", "cat")
	p := startPair(t, pairOpts{})
	c, err := p.c.OpenHold(context.Background(), "st")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := p.c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Streams["hold"] != 1 || st.Streams["control"] != 1 || st.PID == 0 {
		t.Fatalf("status %+v", st)
	}
	if h := p.c.Hello(); !h.Test || h.OS == "" || h.PID == 0 {
		t.Fatalf("hello %+v", h)
	}
}

func TestPanicEndsOnlyItsStream(t *testing.T) {
	hook := func(h openHeader) {
		if h.Slot == "boom" {
			panic("a bad stream")
		}
	}
	testHook.Store(&hook)
	defer testHook.Store(nil)
	p := startPair(t, pairOpts{})
	if _, err := p.c.OpenHold(context.Background(), "boom"); err == nil {
		t.Fatal("the panicking stream answered")
	}
	var out bytes.Buffer
	if _, err := p.c.Exec(context.Background(), ExecRequest{Script: "echo still here", Stdout: &out}); err != nil || out.String() != "still here\n" {
		t.Fatalf("link after a panic: %q %v", out.String(), err)
	}
	if _, err := p.c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStrayOutputBeforeHandshake(t *testing.T) {
	bin := holdBin(t)
	cmd := exec.Command("/bin/sh", "-c", `echo "Welcome to the box, have a nice day"; exec "$0" relay -test`, bin)
	_, err := Start(context.Background(), cmd, DialOptions{Test: true})
	if err == nil || !strings.Contains(err.Error(), "unexpected output before the link started") || !strings.Contains(err.Error(), "Welcome to the box") {
		t.Fatalf("got %v", err)
	}
	// an old pocket-hold without the relay: ends at once, its words come along
	cmd = exec.Command("/bin/sh", "-c", `echo "pocket-hold: unknown command relay" >&2; exit 2`)
	_, err = Start(context.Background(), cmd, DialOptions{Test: true})
	if err == nil || !strings.Contains(err.Error(), "unknown command relay") {
		t.Fatalf("got %v", err)
	}
	// the relay is strict too: a wrong first line is refused
	a, b := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(b, Options{Test: true, Home: t.TempDir()}) }()
	go io.WriteString(a, "hello there\n")
	line, _ := readLine(bufio.NewReader(a), 1000)
	if !strings.HasPrefix(line, errorPrefix) {
		t.Fatalf("relay said %q", line)
	}
	if err := <-served; !errors.Is(err, ErrBadStart) {
		t.Fatalf("Serve: %v", err)
	}
	// and a real relay binary over a pipe works
	c, err := Start(context.Background(), exec.Command(bin, "relay", "-test"), DialOptions{Test: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := c.Exec(context.Background(), ExecRequest{Script: "echo ok", Stdout: &out}); err != nil || out.String() != "ok\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	c.Close()
}

func TestGate(t *testing.T) {
	g := NewGate(1)
	ctx := context.Background()
	first, _ := g.Acquire(ctx, Idle)
	type grant struct {
		name    string
		release func()
	}
	granted := make(chan grant, 10)
	wait := func(name string, p Priority) {
		go func() {
			rel, err := g.Acquire(ctx, p)
			if err != nil {
				granted <- grant{"err " + name, func() {}}
				return
			}
			granted <- grant{name, rel}
		}()
		time.Sleep(20 * time.Millisecond) // queued in this order
	}
	wait("idle1", Idle)
	wait("busy", Busy)
	wait("idle2", Idle)
	wait("viewed", Viewed)
	// a cancelled waiter gives up its turn
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { _, err := g.Acquire(cctx, Viewed); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled Acquire got through")
	}
	first()
	first() // twice: no effect
	var got []string
	for range 4 {
		gr := <-granted
		got = append(got, gr.name)
		select {
		case extra := <-granted:
			t.Fatalf("%s got in while %s held the only place", extra.name, gr.name)
		case <-time.After(20 * time.Millisecond):
		}
		gr.release()
	}
	want := []string{"viewed", "busy", "idle1", "idle2"}
	if !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if n := g.InUse(); n != 0 {
		t.Fatalf("%d still in use", n)
	}
}
