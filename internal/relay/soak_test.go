package relay

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay/wantest"
)

type samples struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *samples) add(d time.Duration) { s.mu.Lock(); s.d = append(s.d, d); s.mu.Unlock() }

func (s *samples) stats() (n int, p50, p95, worst time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := slices.Clone(s.d)
	slices.Sort(d)
	if len(d) == 0 {
		return 0, 0, 0, 0
	}
	return len(d), d[len(d)/2], d[len(d)*95/100], d[len(d)-1]
}

// TestSoak: the phase 2 gate's workload on a simulated WAN (100 ms round
// trip, 10 Mbit/s, loss as random 200 ms stalls): 100 streams, one of them
// flooding, 50 holders catching up through the Gate, 49 idle commands, while
// small control, exec, tools and "viewed conversation" round trips are timed.
// Real ping timing (5 s / 30 s): no dead-link trip may happen.
func TestSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	const (
		holders  = 50
		backlog  = 400_000 // bytes each holder has to catch up
		idleCmds = 49
	)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for i := range holders {
		startHolder(t, home, "c"+strconv.Itoa(i), "sh", "-c",
			`yes "an agent's output line, the kind that piles up while the link is down" | head -c `+strconv.Itoa(backlog)+`; exec cat`)
	}
	startHolder(t, home, "viewed", "cat")

	echo := func(int) (net.Conn, error) { // the server's tools endpoint: echoes lines
		a, b := net.Pipe()
		go func() { defer b.Close(); _, _ = io.Copy(b, b) }()
		return a, nil
	}
	wan := &wantest.Options{Latency: 50 * time.Millisecond, Bandwidth: 10_000_000 / 8, StallEvery: 2 * time.Second, StallFor: 200 * time.Millisecond, Seed: 7}
	p := startPair(t, pairOpts{wan: wan, dial: DialOptions{Tools: echo}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// the viewed conversation's stream comes first, as after a reconnect
	vc, err := p.c.OpenHold(ctx, "viewed")
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Close()
	_, vr, err := holdHello(vc, 0)
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	if err := p.c.Bind(ctx, port); err != nil {
		t.Fatal(err)
	}

	// one stream floods for the whole run
	var flooded countWriter
	go p.c.Exec(ctx, ExecRequest{Script: `yes "flood flood flood flood flood flood flood flood flood flood"`, Stdout: &flooded})
	// idle commands waiting on input that never comes
	for range idleCmds {
		pr, pw := io.Pipe()
		defer pw.Close()
		go p.c.Exec(ctx, ExecRequest{Script: "cat", Stdin: pr, Stdout: io.Discard})
	}

	// the holders catch up through the gate, each from offset 0
	gateN := DefaultCatchUp
	if n, err := strconv.Atoi(os.Getenv("RELAY_SOAK_GATE")); err == nil {
		gateN = n // to measure other caps
	}
	gate := NewGate(gateN)
	var caughtUp sync.WaitGroup
	var catchErr atomic.Value
	start := time.Now()
	var lastDone atomic.Int64
	for i := range holders {
		caughtUp.Add(1)
		go func() {
			defer caughtUp.Done()
			release, err := gate.Acquire(ctx, Priority(i%3))
			if err != nil {
				return
			}
			defer release()
			s, err := p.c.OpenHold(ctx, "c"+strconv.Itoa(i))
			if err != nil {
				catchErr.Store(err)
				return
			}
			go func() { <-ctx.Done(); s.Close() }() // stays open (idle) until the end
			_, r, err := holdHello(s, 0)
			if err != nil {
				catchErr.Store(err)
				return
			}
			got := 0
			for got < backlog {
				typ, b, err := holdRead(r)
				if err != nil {
					catchErr.Store(fmt.Errorf("holder %d after %d bytes: %w", i, got, err))
					return
				}
				if typ == 'o' {
					got += len(b)
				}
			}
			lastDone.Store(int64(time.Since(start)))
		}()
	}

	// the probes
	var ctl, ex, tools, viewed, rtt samples
	probe := func(s *samples, every time.Duration, f func() error) {
		go func() {
			for ctx.Err() == nil {
				t0 := time.Now()
				if err := f(); err != nil {
					if ctx.Err() == nil {
						t.Errorf("probe: %v", err)
					}
					return
				}
				s.add(time.Since(t0))
				time.Sleep(every)
			}
		}()
	}
	probe(&ctl, 250*time.Millisecond, func() error { _, err := p.c.Status(ctx); return err })
	probe(&ex, 250*time.Millisecond, func() error {
		var out bytes.Buffer
		_, err := p.c.Exec(ctx, ExecRequest{Script: "echo hi", Stdout: &out})
		if err == nil && out.String() != "hi\n" {
			err = fmt.Errorf("exec said %q", out.String())
		}
		return err
	})
	tc, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)) // a CLI's tools connection
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	tr := bufio.NewReader(tc)
	n := 0
	probe(&tools, 250*time.Millisecond, func() error {
		n++
		want := "call " + strconv.Itoa(n) + "\n"
		if _, err := io.WriteString(tc, want); err != nil {
			return err
		}
		got, err := tr.ReadString('\n')
		if err == nil && got != want {
			err = fmt.Errorf("tools echo %q", got)
		}
		return err
	})
	v := 0
	probe(&viewed, 250*time.Millisecond, func() error {
		v++
		want := "viewed " + strconv.Itoa(v)
		if err := holdWrite(vc, 'i', []byte(want+"\n")); err != nil {
			return err
		}
		for {
			typ, b, err := holdRead(vr)
			if err != nil {
				return err
			}
			if typ == 'o' && strings.Contains(string(b), want) {
				return nil
			}
		}
	})
	go func() {
		for ctx.Err() == nil {
			time.Sleep(time.Second)
			if d := p.c.RTT(); d > 0 {
				rtt.add(d)
			}
		}
	}()

	all := make(chan struct{})
	go func() { caughtUp.Wait(); close(all) }()
	select {
	case <-all:
	case <-time.After(3 * time.Minute):
		t.Fatal("catch-up did not finish in 3 minutes")
	case <-p.c.Done():
		t.Fatalf("false dead-link trip (server): %v", p.c.Err())
	case <-p.served:
		t.Fatalf("false dead-link trip (relay): %v", p.err)
	}
	if err, _ := catchErr.Load().(error); err != nil {
		t.Fatal(err)
	}
	catchUp := time.Duration(lastDone.Load())
	// keep going a while with the flood and the idle streams only
	select {
	case <-time.After(15 * time.Second):
	case <-p.c.Done():
		t.Fatalf("false dead-link trip (server): %v", p.c.Err())
	case <-p.served:
		t.Fatalf("false dead-link trip (relay): %v", p.err)
	}
	st, err := p.c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	streams := 0
	for _, n := range st.Streams {
		streams += n
	}
	cancel()

	mb := float64(holders*backlog) / 1e6
	t.Logf("link: %s one way, %d Mbit/s, %s stalls every ~%s; window %d KiB, catch-up gate %d",
		wan.Latency, wan.Bandwidth*8/1_000_000, wan.StallFor, wan.StallEvery, muxConfig().MaxStreamWindowSize>>10, gateN)
	t.Logf("streams open at the end: %d %v", streams, st.Streams)
	t.Logf("catch-up: %d holders × %d bytes (%.0f MB) in %s; flood got %.1f MB meanwhile",
		holders, backlog, mb, catchUp.Round(time.Millisecond), float64(flooded.n.Load())/1e6)
	failed := false
	for _, k := range []struct {
		name string
		s    *samples
	}{{"control status", &ctl}, {"exec echo", &ex}, {"tools call", &tools}, {"viewed output", &viewed}, {"ping RTT", &rtt}} {
		n, p50, p95, worst := k.s.stats()
		t.Logf("%-15s n=%4d p50=%-8s p95=%-8s max=%s", k.name, n, p50.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond))
		if k.name != "ping RTT" && (n < 10 || p95 > 2*time.Second) {
			failed = true
		}
	}
	if failed {
		t.Fatal("a small round trip's p95 is over 2 s (or too few samples)")
	}
}
