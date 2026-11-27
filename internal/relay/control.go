package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// msg is one control message: a request ({"id","op",…}) or a reply
// ({"re","ok",…}).
type msg struct {
	ID      int64          `json:"id,omitempty"`
	Op      string         `json:"op,omitempty"`
	Re      int64          `json:"re,omitempty"`
	OK      *bool          `json:"ok,omitempty"`
	Error   string         `json:"error,omitempty"`
	Port    int            `json:"port,omitempty"`
	Ports   []int          `json:"ports,omitempty"` // sync request; status reply
	Bound   []int          `json:"bound,omitempty"`
	Failed  map[int]string `json:"failed,omitempty"`
	PID     int            `json:"pid,omitempty"`
	Uptime  float64        `json:"uptime_s,omitempty"`
	Streams map[string]int `json:"streams,omitempty"`
}

func reply(m msg, err error) msg {
	ok := err == nil
	r := msg{Re: m.ID, OK: &ok}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// control is one end of the control stream: requests both ways, our pings
// and the liveness verdict. Received output of other streams never counts:
// only answers to our own pings prove the link works both ways.
type control struct {
	s      net.Conn
	handle func(msg) msg // requests other than ping; nil: refuse them

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan msg

	lastAnswer atomic.Int64 // unix nanos of the last answer to one of our pings
	rtt        atomic.Int64

	ping, dead time.Duration
	died       chan struct{}
	dieOnce    sync.Once
	err        error
}

func newControl(s net.Conn, ping, dead time.Duration, handle func(msg) msg) *control {
	if ping <= 0 {
		ping = DefaultPing
	}
	if dead <= 0 {
		dead = DefaultDead
	}
	c := &control{s: s, handle: handle, pending: map[int64]chan msg{}, ping: ping, dead: dead, died: make(chan struct{})}
	c.lastAnswer.Store(time.Now().UnixNano())
	return c
}

// run reads the stream and pings until the link is found dead or the stream
// ends; then died is closed and err says why.
func (c *control) run() {
	go c.pinger()
	r := bufio.NewReaderSize(c.s, 64<<10)
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			select {
			case <-c.died:
				return // the verdict is in already
			default:
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				c.die(errors.New("control message too large"))
			} else {
				c.die(fmt.Errorf("link closed: %w", err))
			}
			return
		}
		var m msg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Op == "ping":
			go c.send(reply(m, nil))
		case m.Op != "":
			go func() {
				if c.handle == nil {
					c.send(reply(m, fmt.Errorf("unknown op %q", m.Op)))
					return
				}
				r := c.handle(m)
				r.Re = m.ID
				c.send(r)
			}()
		case m.Re != 0:
			c.mu.Lock()
			ch := c.pending[m.Re]
			delete(c.pending, m.Re)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

// pinger pings every c.ping and checks that some ping was answered within
// c.dead. Each ping goes out on its own: a stalled write must not hold the
// verdict back.
func (c *control) pinger() {
	tick := time.NewTicker(c.ping)
	defer tick.Stop()
	check := time.NewTicker(max(c.dead/60, 10*time.Millisecond))
	defer check.Stop()
	for {
		select {
		case <-c.died:
			return
		case <-tick.C:
			go func() {
				sent := time.Now()
				if _, err := c.call(context.Background(), msg{Op: "ping"}, c.dead); err == nil {
					now := time.Now()
					c.lastAnswer.Store(now.UnixNano())
					c.rtt.Store(int64(now.Sub(sent)))
				}
			}()
		case <-check.C:
			if since := time.Since(time.Unix(0, c.lastAnswer.Load())); since > c.dead {
				c.die(fmt.Errorf("link dead: no answer to pings for %s", since.Round(100*time.Millisecond)))
				return
			}
		}
	}
}

func (c *control) die(err error) {
	c.dieOnce.Do(func() {
		c.err = err
		close(c.died)
		// a stream's Close does not end our own pending read: wake it
		_ = c.s.SetReadDeadline(time.Now())
		// a stream's Close queues its FIN behind whatever the session still
		// has to send, which a dead link never takes: never wait for it
		// (the relay used to hang here instead of leaving)
		_ = c.s.SetWriteDeadline(time.Now())
		go func() { _ = c.s.Close() }()
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
}

func (c *control) send(m msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.s.Write(append(b, '\n'))
	return err
}

var errLinkDown = errors.New("the link is down")

// call sends a request and waits for its reply (up to timeout, and ctx).
func (c *control) call(ctx context.Context, m msg, timeout time.Duration) (msg, error) {
	ch := make(chan msg, 1)
	c.mu.Lock()
	select {
	case <-c.died:
		c.mu.Unlock()
		return msg{}, errLinkDown
	default:
	}
	c.nextID++
	m.ID = c.nextID
	c.pending[m.ID] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
	}
	go func() {
		if err := c.send(m); err != nil {
			c.die(fmt.Errorf("link closed: %w", err))
		}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return msg{}, errLinkDown
		}
		if r.OK == nil || !*r.OK {
			if r.Error == "" {
				r.Error = "refused"
			}
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-ctx.Done():
		forget()
		return msg{}, ctx.Err()
	case <-t.C:
		forget()
		return msg{}, fmt.Errorf("%s: no reply in %s", m.Op, timeout)
	case <-c.died:
		forget()
		return msg{}, errLinkDown
	}
}

func (c *control) RTT() time.Duration { return time.Duration(c.rtt.Load()) }
