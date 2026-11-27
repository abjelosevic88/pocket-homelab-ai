// Package wantest is a simulated wide-area link for the relay's tests: a
// pair of connected net.Conns with latency, bandwidth, a send buffer, random
// short stalls (what loss and retransmits look like to a TCP reader) and
// switches that stall one direction or both for good (a one-way stall, a
// half-open connection: packets dropped, no reset).
package wantest

import (
	"errors"
	"io"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"
)

// Options shape both directions.
type Options struct {
	Latency   time.Duration // one way
	Bandwidth int           // bytes per second; 0: unlimited
	Buffer    int           // bytes a writer may queue before Write blocks (socket buffer + ssh window); 0: 4 MiB
	// StallEvery: on average this often (per direction, random), the link
	// delivers nothing for StallFor. 0: never.
	StallEvery, StallFor time.Duration
	Seed                 int64
}

// Direction names one way of the link.
type Direction int

const (
	AtoB Direction = iota // what A writes, B reads
	BtoA
)

// Link controls a pair made by New.
type Link struct {
	dirs [2]*pipe
}

// New returns the two ends of a link.
func New(o Options) (a, b net.Conn, l *Link) {
	if o.Buffer <= 0 {
		o.Buffer = 4 << 20
	}
	ab := newPipe(o, o.Seed)
	ba := newPipe(o, o.Seed+1)
	l = &Link{dirs: [2]*pipe{ab, ba}}
	a = &end{r: ba, w: ab, name: "a"}
	b = &end{r: ab, w: ba, name: "b"}
	return a, b, l
}

// Stall stops (on) or resumes (off) delivery in one direction. Bytes written
// meanwhile queue up and the writer blocks once the buffer is full, as on a
// connection whose packets are dropped.
func (l *Link) Stall(d Direction, on bool) { l.dirs[d].setStalled(on) }

// Delivered is how many bytes have reached the reader in direction d.
func (l *Link) Delivered(d Direction) int64 {
	p := l.dirs[d]
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered
}

type chunk struct {
	b   []byte
	at  time.Time // when it reaches the reader
	eof bool
}

// pipe is one direction: a send queue, a wire that serialises at the
// bandwidth, and what has arrived.
type pipe struct {
	o    Options
	rand *rand.Rand

	mu        sync.Mutex
	cond      *sync.Cond
	sendQ     []chunk // written, not yet on the wire
	queued    int
	wire      []chunk // on the wire or arrived, in order
	stalled   bool
	wclosed   bool // the writer closed (EOF queued)
	rclosed   bool // the reader closed: writes are dropped
	delivered int64
	readDL    time.Time
}

func newPipe(o Options, seed int64) *pipe {
	p := &pipe{o: o, rand: rand.New(rand.NewSource(seed))}
	p.cond = sync.NewCond(&p.mu)
	go p.run()
	go p.ticker()
	return p
}

// ticker wakes waiters now and then: deadlines and arrival times are checked
// on wake-up.
func (p *pipe) ticker() {
	t := time.NewTicker(2 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		p.mu.Lock()
		done := p.rclosed
		p.cond.Broadcast()
		p.mu.Unlock()
		if done {
			return
		}
	}
}

func (p *pipe) setStalled(on bool) {
	p.mu.Lock()
	p.stalled = on
	p.cond.Broadcast()
	p.mu.Unlock()
}

// run moves chunks from the send queue onto the wire at the bandwidth.
func (p *pipe) run() {
	next := time.Now() // when the wire is free
	var nextStall time.Time
	if p.o.StallEvery > 0 {
		nextStall = time.Now().Add(time.Duration(p.rand.ExpFloat64() * float64(p.o.StallEvery)))
	}
	for {
		p.mu.Lock()
		for (len(p.sendQ) == 0 || p.stalled) && !p.rclosed {
			p.cond.Wait()
		}
		if p.rclosed {
			p.mu.Unlock()
			return
		}
		c := p.sendQ[0]
		p.mu.Unlock()

		now := time.Now()
		if !nextStall.IsZero() && now.After(nextStall) {
			time.Sleep(p.o.StallFor)
			now = time.Now()
			nextStall = now.Add(time.Duration(p.rand.ExpFloat64() * float64(p.o.StallEvery)))
		}
		if next.Before(now) {
			next = now
		}
		if p.o.Bandwidth > 0 {
			next = next.Add(time.Duration(float64(len(c.b)) / float64(p.o.Bandwidth) * float64(time.Second)))
			if d := time.Until(next); d > 0 {
				time.Sleep(d)
			}
		}
		c.at = time.Now().Add(p.o.Latency)

		p.mu.Lock()
		if p.rclosed { // dropped meanwhile
			p.mu.Unlock()
			return
		}
		p.sendQ = p.sendQ[1:]
		p.queued -= len(c.b)
		p.wire = append(p.wire, c)
		p.cond.Broadcast()
		eof := c.eof
		p.mu.Unlock()
		if eof {
			return
		}
	}
}

const maxChunk = 16 << 10

func (p *pipe) write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		c := min(len(b), maxChunk)
		p.mu.Lock()
		for p.queued+c > p.o.Buffer && p.queued > 0 && !p.wclosed && !p.rclosed {
			p.cond.Wait()
		}
		if p.wclosed {
			p.mu.Unlock()
			return n, net.ErrClosed
		}
		if !p.rclosed {
			p.sendQ = append(p.sendQ, chunk{b: append([]byte(nil), b[:c]...)})
			p.queued += c
			p.cond.Broadcast()
		}
		p.mu.Unlock()
		n += c
		b = b[c:]
	}
	return n, nil
}

func (p *pipe) closeWrite() {
	p.mu.Lock()
	if !p.wclosed {
		p.wclosed = true
		p.sendQ = append(p.sendQ, chunk{eof: true})
		p.cond.Broadcast()
	}
	p.mu.Unlock()
}

func (p *pipe) read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if p.rclosed {
			return 0, net.ErrClosed
		}
		if len(p.wire) > 0 && !p.wire[0].at.After(time.Now()) && !p.stalled {
			c := &p.wire[0]
			if c.eof {
				return 0, io.EOF
			}
			n := copy(b, c.b)
			c.b = c.b[n:]
			if len(c.b) == 0 {
				p.wire = p.wire[1:]
			}
			p.delivered += int64(n)
			return n, nil
		}
		if !p.readDL.IsZero() && !time.Now().Before(p.readDL) {
			return 0, os.ErrDeadlineExceeded
		}
		p.cond.Wait()
	}
}

func (p *pipe) closeRead() {
	p.mu.Lock()
	p.rclosed = true
	p.sendQ, p.wire, p.queued = nil, nil, 0
	p.cond.Broadcast()
	p.mu.Unlock()
}

type end struct {
	r, w *pipe
	name string
	once sync.Once
}

func (e *end) Read(b []byte) (int, error)  { return e.r.read(b) }
func (e *end) Write(b []byte) (int, error) { return e.w.write(b) }

// Close sends EOF the other way (it travels like data, so a stalled
// direction does not deliver it) and stops reading.
func (e *end) Close() error {
	e.once.Do(func() {
		e.w.closeWrite()
		e.r.closeRead()
	})
	return nil
}

type addr string

func (a addr) Network() string { return "wantest" }
func (a addr) String() string  { return string(a) }

func (e *end) LocalAddr() net.Addr  { return addr(e.name) }
func (e *end) RemoteAddr() net.Addr { return addr("peer-of-" + e.name) }
func (e *end) SetDeadline(t time.Time) error {
	return e.SetReadDeadline(t)
}
func (e *end) SetReadDeadline(t time.Time) error {
	e.r.mu.Lock()
	e.r.readDL = t
	e.r.cond.Broadcast()
	e.r.mu.Unlock()
	return nil
}
func (e *end) SetWriteDeadline(time.Time) error { return errors.ErrUnsupported }
