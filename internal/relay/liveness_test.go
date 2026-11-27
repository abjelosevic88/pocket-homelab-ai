package relay

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay/wantest"
)

// countWriter counts what goes through it.
type countWriter struct{ n atomic.Int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.n.Add(int64(len(p))); return len(p), nil }

// liveness runs a link on a shaped connection, makes the machine flood
// output, stalls dirs and checks that both ends declare the link dead within
// dead (plus one check and a round trip) of the stall, while output from the
// machine kept arriving when only the way to it was stalled.
func liveness(t *testing.T, ping, dead time.Duration, dirs ...wantest.Direction) {
	wan := &wantest.Options{Latency: 10 * time.Millisecond, Bandwidth: 2 << 20}
	p := startPair(t, pairOpts{
		machine: Options{Ping: ping, Dead: dead},
		dial:    DialOptions{PingEvery: ping, DeadAfter: dead},
		wan:     wan,
	})
	// the machine floods the server: output that must not count as an answer
	var got countWriter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.c.Exec(ctx, ExecRequest{Script: "yes 'some agent output, again and again'", Stdout: &got})

	// healthy first: two dead periods without a false trip
	select {
	case <-p.c.Done():
		t.Fatalf("link died while healthy: %v", p.c.Err())
	case <-p.served:
		t.Fatalf("relay ended while healthy: %v", p.err)
	case <-time.After(2 * dead):
	}
	if got.n.Load() == 0 {
		t.Fatal("no output came while healthy")
	}

	stall := time.Now()
	for _, d := range dirs {
		p.link.Stall(d, true)
	}
	var serverAt, machineAt time.Duration
	var outAfter int64
	before := got.n.Load()
	deadline := time.After(dead + 10*time.Second)
	sDone, mDone := p.c.Done(), (<-chan struct{})(p.served)
	for sDone != nil || mDone != nil {
		select {
		case <-sDone:
			serverAt, outAfter, sDone = time.Since(stall), got.n.Load()-before, nil
		case <-mDone:
			machineAt, mDone = time.Since(stall), nil
		case <-deadline:
			t.Fatalf("not noticed: server %v machine %v", serverAt, machineAt)
		}
	}
	t.Logf("stalled %v: server noticed after %s (%v), machine after %s (%v); %d bytes of output arrived during the stall",
		dirs, serverAt.Round(time.Millisecond), p.c.Err(), machineAt.Round(time.Millisecond), p.err, outAfter)
	limit := dead + dead/30 + 2*wan.Latency + 100*time.Millisecond
	if serverAt > limit || machineAt > limit {
		t.Fatalf("noticed too late (limit %s)", limit)
	}
	if len(dirs) == 1 && dirs[0] == wantest.AtoB && outAfter == 0 {
		t.Fatal("one-way stall: output from the machine should have kept arriving")
	}
}

// AtoB is server → machine (startPair puts the server on end a).
func TestOneWayStallToMachine(t *testing.T) {
	liveness(t, 500*time.Millisecond, 3*time.Second, wantest.AtoB)
}

func TestOneWayStallFromMachine(t *testing.T) {
	liveness(t, 500*time.Millisecond, 3*time.Second, wantest.BtoA)
}

func TestHalfOpen(t *testing.T) {
	liveness(t, 500*time.Millisecond, 3*time.Second, wantest.AtoB, wantest.BtoA)
}

// The real numbers: 5 s pings, dead after 30 s.
func TestOneWayStallRealTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("takes over a minute")
	}
	liveness(t, DefaultPing, DefaultDead, wantest.AtoB)
}

func TestHalfOpenRealTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("takes over a minute")
	}
	liveness(t, DefaultPing, DefaultDead, wantest.AtoB, wantest.BtoA)
}

// Input closing is dead at once, on both ends.
func TestInputClosed(t *testing.T) {
	p := startPair(t, pairOpts{wan: &wantest.Options{Latency: 5 * time.Millisecond}})
	start := time.Now()
	p.machine.Close()
	select {
	case <-p.served:
	case <-time.After(2 * time.Second):
		t.Fatal("relay kept going with its input closed")
	}
	select {
	case <-p.c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server kept going after the relay closed")
	}
	t.Logf("both ends done %s after the close", time.Since(start).Round(time.Millisecond))
}

// stallReader passes reads through until stalled, then blocks for good: a
// link that drops everything without a reset.
type stallReader struct {
	r       io.Reader
	stalled chan struct{}
}

func (s stallReader) Read(p []byte) (int, error) {
	select {
	case <-s.stalled:
		select {} // nothing ever comes again
	default:
	}
	return s.r.Read(p)
}

// The relay process leaves on a dead link even when its stdout is full and
// its stdin stays open (sshd keeps a dead client's channel for minutes).
func TestRelayProcessExitsWhenStuck(t *testing.T) {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	cmd := exec.Command(holdBin(t), "relay", "-test", "-ping", "200ms", "-dead", "1500ms")
	cmd.Stdin, cmd.Stdout = inR, outW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	exited := make(chan time.Time, 1)
	go func() { _ = cmd.Wait(); exited <- time.Now() }()
	defer func() { _ = cmd.Process.Kill(); inW.Close(); outR.Close() }()
	stalled := make(chan struct{})
	conn := rwc{Reader: stallReader{outR, stalled}, Writer: inW, close: func() error { return nil }} // closes nothing: no EOF reaches the relay
	c, err := NewClient(context.Background(), conn, DialOptions{Test: true, PingEvery: 200 * time.Millisecond, DeadAfter: 1500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var got countWriter
	go c.Exec(context.Background(), ExecRequest{Script: "yes", Stdout: &got})
	time.Sleep(500 * time.Millisecond)
	if got.n.Load() == 0 {
		t.Fatal("no output before the stall")
	}
	close(stalled)
	at := time.Now()
	select {
	case end := <-exited:
		t.Logf("relay process exited %s after the stall", end.Sub(at).Round(time.Millisecond))
	case <-time.After(1500*time.Millisecond + 3*time.Second):
		t.Fatal("relay process still running with a dead link")
	}
}
