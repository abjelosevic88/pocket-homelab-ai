package server

// The controller lease (docs/machine-link-plan.md, "Link lifecycle" 2): one
// server process of an installation drives the machine links. It holds a
// row in the database with an expiry, renews it every 10 s, and it runs out
// after 30 s. Only the holder allocates link generations, in the same
// transaction that checks the lease (store.NextLinkGeneration). A process
// that loses its lease, or cannot renew it, stops every link it drives at
// once. During a deploy the new process takes the lease when the old one
// gives it up on shutdown, or when it runs out.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

const (
	leaseEvery = 10 * time.Second
	leaseTTL   = 30 * time.Second
)

type controllerLease struct {
	st  *store.Store
	log *slog.Logger
	id  string // this process: random, never reused
	now func() time.Time

	// onLost: stop every link now (called without mu). onGained: links may
	// be driven again.
	onLost, onGained func()

	mu    sync.Mutex
	held  bool
	until time.Time
}

func newControllerLease(st *store.Store, log *slog.Logger, now func() time.Time) *controllerLease {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	if now == nil {
		now = time.Now
	}
	return &controllerLease{st: st, log: log, id: "p-" + hex.EncodeToString(b), now: now}
}

// Held: this process holds the lease, as far as it knows (it renewed it and
// the renewal has not run out).
func (l *controllerLease) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held && l.now().Before(l.until)
}

// renew takes or renews the lease. A failed renewal while it was held (taken
// by another process, or the database could not be written) counts as lost:
// every link stops at once.
func (l *controllerLease) renew(ctx context.Context) bool {
	now := l.now()
	ok, err := l.st.TakeLease(ctx, l.id, now, leaseTTL)
	l.mu.Lock()
	// held until now, even if it ran out meanwhile (a renewal that hung):
	// a failed renewal stops the links all the same
	was := l.held
	if ok && err == nil {
		l.held, l.until = true, now.Add(leaseTTL)
	} else {
		l.held = false
	}
	held := l.held
	l.mu.Unlock()
	switch {
	case was && !held:
		if l.log != nil {
			l.log.Warn("machine links: lost the controller lease, stopping every link", "err", err)
		}
		if l.onLost != nil {
			l.onLost()
		}
	case !was && held:
		if l.log != nil {
			l.log.Info("machine links: this process drives them now")
		}
		if l.onGained != nil {
			l.onGained()
		}
	}
	return held
}

// expire: the lease ran out without a renewal (the database hung, the
// process was paused): every link stops at once, before another process
// can take over.
func (l *controllerLease) expire() {
	l.mu.Lock()
	lost := l.held && !l.now().Before(l.until)
	if lost {
		l.held = false
	}
	l.mu.Unlock()
	if lost {
		if l.log != nil {
			l.log.Warn("machine links: the controller lease ran out, stopping every link")
		}
		if l.onLost != nil {
			l.onLost()
		}
	}
}

// allocate is the next generation for a machine's link, only while this
// process holds the lease (checked by the database in the same transaction).
func (l *controllerLease) allocate(ctx context.Context, hostID int64) (int64, error) {
	if !l.Held() {
		return 0, store.ErrNotLeaseHolder
	}
	gen, err := l.st.NextLinkGeneration(ctx, hostID, l.id, l.now())
	if err == store.ErrNotLeaseHolder {
		l.mu.Lock()
		was := l.held
		l.held = false
		l.mu.Unlock()
		if was && l.onLost != nil {
			l.onLost()
		}
	}
	return gen, err
}

// release gives the lease up (shutdown): the next process takes it at once.
func (l *controllerLease) release(ctx context.Context) {
	l.mu.Lock()
	was := l.held
	l.held = false
	l.mu.Unlock()
	if was && l.onLost != nil {
		l.onLost()
	}
	_ = l.st.ReleaseLease(ctx, l.id)
}

// run renews the lease every 10 s until ctx ends, then gives it up.
func (l *controllerLease) run(ctx context.Context) {
	t := time.NewTicker(leaseEvery)
	defer t.Stop()
	go func() { // a renewal that hangs must not keep the links past the lease
		w := time.NewTicker(2 * time.Second)
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.C:
				l.expire()
			}
		}
	}()
	for {
		l.renew(ctx)
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			l.release(rctx)
			cancel()
			return
		case <-t.C:
		}
	}
}
