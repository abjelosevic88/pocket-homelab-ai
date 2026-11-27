package relay

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay/wantest"
)

// The test binary also plays an unrelated program listening on a port
// (RELAY_TEST_LISTEN=port): nothing may end it.
func TestMain(m *testing.M) {
	if os.Getenv("RELAY_TEST_STUBBORN") != "" {
		// a relay that does not end on SIGTERM
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if p := os.Getenv("RELAY_TEST_LISTEN"); p != "" {
		l, err := net.Listen("tcp", "127.0.0.1:"+p)
		if err != nil {
			os.Exit(1)
		}
		fmt.Println("listening")
		for {
			c, err := l.Accept()
			if err == nil {
				c.Close()
			}
		}
	}
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

var origHome = os.Getenv("HOME")

var (
	binOnce sync.Once
	binDir  string
	binErr  error
)

// holdBin is pocket-hold built from this tree (relay and hold together).
func holdBin(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "mlrelay-bin")
		if binErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "pocket-hold"), "../../cmd/pocket-hold")
		// the module cache stays where it was, whatever HOME a test set
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "HOME="+origHome)
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return filepath.Join(binDir, "pocket-hold")
}

// pair is an in-process link: Serve on one end, a Client on the other.
type pair struct {
	c       *Client
	served  chan struct{} // closed when Serve returned
	err     error         // what it returned
	machine net.Conn
	server  net.Conn
	link    *wantest.Link
}

type pairOpts struct {
	machine Options
	dial    DialOptions
	wan     *wantest.Options // nil: net.Pipe
}

func startPair(t testing.TB, po pairOpts) *pair {
	t.Helper()
	p := &pair{served: make(chan struct{})}
	if po.wan != nil {
		p.server, p.machine, p.link = wantest.New(*po.wan)
	} else {
		p.server, p.machine = net.Pipe()
	}
	if po.machine.Home == "" {
		po.machine.Home = t.TempDir()
	}
	if !po.machine.Test && po.machine.Installation == "" {
		po.machine.Test, po.dial.Test = true, true
	}
	go func() { p.err = Serve(p.machine, po.machine); close(p.served) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := NewClient(ctx, p.server, po.dial)
	if err != nil {
		t.Fatalf("link start: %v", err)
	}
	p.c = c
	t.Cleanup(func() {
		c.Close()
		p.machine.Close()
		select {
		case <-p.served:
		case <-time.After(10 * time.Second):
			t.Error("relay did not end")
		}
	})
	return p
}

// groupGone waits until no process of group pgid is left.
func groupGone(t testing.TB, pgid int, within time.Duration) {
	t.Helper()
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, _ := exec.Command("ps", "-o", "pid,pgid,stat,args", "-g", strconv.Itoa(pgid)).CombinedOutput()
	t.Fatalf("process group %d still there after %s:\n%s", pgid, within, out)
}

// lineWriter hands each line written through it to a channel.
type lineWriter struct {
	mu    sync.Mutex
	part  []byte
	lines chan string
}

func newLineWriter() *lineWriter { return &lineWriter{lines: make(chan string, 1000)} }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.part = append(w.part, p...)
	for {
		i := -1
		for j, c := range w.part {
			if c == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			return len(p), nil
		}
		select {
		case w.lines <- string(w.part[:i]):
		default:
		}
		w.part = w.part[i+1:]
	}
}

func (w *lineWriter) next(t testing.TB, within time.Duration) string {
	t.Helper()
	select {
	case l := <-w.lines:
		return l
	case <-time.After(within):
		t.Fatal("no line came")
	}
	return ""
}

// Minimal holder protocol (internal/hold): type byte, 4-byte length, data.

func holdWrite(w io.Writer, typ byte, p []byte) error {
	b := make([]byte, 5+len(p))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:], uint32(len(p)))
	copy(b[5:], p)
	_, err := w.Write(b)
	return err
}

func holdRead(r *bufio.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	p := make([]byte, binary.BigEndian.Uint32(h[1:]))
	_, err := io.ReadFull(r, p)
	return h[0], p, err
}

// holdHello attaches from offset from and returns the holder's header.
func holdHello(c net.Conn, from int64) (map[string]any, *bufio.Reader, error) {
	b, _ := json.Marshal(map[string]any{"from": from})
	if err := holdWrite(c, 'h', b); err != nil {
		return nil, nil, err
	}
	r := bufio.NewReader(c)
	typ, p, err := holdRead(r)
	if err != nil {
		return nil, nil, err
	}
	if typ != 'H' {
		return nil, nil, fmt.Errorf("frame %q, not a header", typ)
	}
	var hd map[string]any
	return hd, r, json.Unmarshal(p, &hd)
}

// startHolder runs `pocket-hold serve slot -- argv` under home, in a session
// of its own as attach would, and waits until it listens.
func startHolder(t testing.TB, home, slot string, argv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(holdBin(t), append([]string{"serve", slot, "--"}, argv...)...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waited
	})
	sock := filepath.Join(home, ".pocket-ai", "hold", slot, "s")
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return cmd
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("holder %s did not listen", slot)
	return nil
}

// freePort is a port nothing listens on just now.
func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
