package relay

import (
	"context"
	"sync"
)

// Priority orders hold streams waiting to catch up after a reconnect.
type Priority int

const (
	Idle   Priority = iota // nobody looks, no turn runs
	Busy                   // a turn runs
	Viewed                 // someone is looking at the conversation
)

// DefaultCatchUp is how many hold streams catch up at once. A stream has at
// most one yamux window (256 KiB) in flight, so this caps catch-up bytes in
// flight; tuned by the soak test (TestSoak) to keep pings and small commands
// under 2 s on a 10 Mbit/s link.
const DefaultCatchUp = 4

// Gate lets n callers through at once; waiters go in by priority (viewed,
// then busy, then idle), first come first served within one. Callers acquire
// before OpenHold and release once the stream has caught up.
type Gate struct {
	mu      sync.Mutex
	n, used int
	waiting [Viewed + 1][]chan struct{}
}

func NewGate(n int) *Gate {
	if n < 1 {
		n = 1
	}
	return &Gate{n: n}
}

// Acquire waits for a place. release gives it back (more calls do nothing).
func (g *Gate) Acquire(ctx context.Context, p Priority) (release func(), err error) {
	p = min(max(p, Idle), Viewed)
	g.mu.Lock()
	if g.used < g.n && g.nobodyWaiting() {
		g.used++
		g.mu.Unlock()
		return g.releaser(), nil
	}
	ch := make(chan struct{})
	g.waiting[p] = append(g.waiting[p], ch)
	g.mu.Unlock()
	select {
	case <-ch:
		return g.releaser(), nil
	case <-ctx.Done():
		g.mu.Lock()
		defer g.mu.Unlock()
		select {
		case <-ch: // handed over just now: pass it on
			g.used--
			g.wake()
		default:
			q := g.waiting[p]
			for i, c := range q {
				if c == ch {
					g.waiting[p] = append(q[:i:i], q[i+1:]...)
					break
				}
			}
		}
		return nil, ctx.Err()
	}
}

func (g *Gate) nobodyWaiting() bool {
	for _, q := range g.waiting {
		if len(q) > 0 {
			return false
		}
	}
	return true
}

func (g *Gate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.used--
			g.wake()
			g.mu.Unlock()
		})
	}
}

// wake hands free places to the first waiters of the highest priority; with g.mu held.
func (g *Gate) wake() {
	for p := Viewed; p >= Idle && g.used < g.n; {
		if len(g.waiting[p]) == 0 {
			p--
			continue
		}
		ch := g.waiting[p][0]
		g.waiting[p] = g.waiting[p][1:]
		g.used++
		close(ch)
	}
}

// InUse is how many places are taken (for status).
func (g *Gate) InUse() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}
