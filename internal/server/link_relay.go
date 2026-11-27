package server

// The server's view of internal/relay (docs/machine-link-impl.md, "Server
// side API"). machine_link.go is written against linkConn and startRelay (a
// fake in its tests); startRelay is relay.Start behind a thin adapter that
// maps the relay's errors onto the sentinels here.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay"
)

// linkConn is the part of *relay.Client the server uses.
type linkConn interface {
	Hello() linkHello
	Exec(ctx context.Context, req linkExecRequest) (linkExecResult, error)
	Status(ctx context.Context) (relayStatus, error)
	RTT() time.Duration    // last ping round trip
	Done() <-chan struct{} // closed when the link is dead or closed
	Err() error
	Close() error
	// phase 2:
	OpenHold(ctx context.Context, slot string) (net.Conn, error)
	Bind(ctx context.Context, port int) error
	Unbind(ctx context.Context, port int) error
	Sync(ctx context.Context, ports []int) (bound []int, failed map[int]string, err error)
}

// linkHello mirrors relay.Hello: the relay's POCKET-RELAY line.
type linkHello struct {
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Generation int64  `json:"generation"`
	Test       bool   `json:"test"`
	Sum        string `json:"sum,omitempty"` // the relay binary's content hash
}

// linkExecRequest mirrors relay.ExecRequest.
type linkExecRequest struct {
	Script         string
	Args           []string
	Stdin          io.Reader // nil: /dev/null; else sent, then its end
	Stdout, Stderr io.Writer
}

// linkExecResult mirrors relay.ExecResult.
type linkExecResult struct {
	Code    int
	Signal  string
	Started bool
}

// relayStatus mirrors relay.Status.
type relayStatus struct {
	PID     int            `json:"pid"`
	UptimeS int64          `json:"uptime_s"`
	Streams map[string]int `json:"streams"`
	Ports   []int          `json:"ports"`
}

// relayDialOptions mirrors relay.DialOptions.
type relayDialOptions struct {
	Installation string
	Entry        int64
	Generation   int64
	Token        string
	Test         bool
	Tools        func(port int) (net.Conn, error) // phase 2: dial the local target registered for port
	OnDead       func(err error)
}

var (
	// errRelayNotSent mirrors relay.ErrNotSent: the exec stream could not be
	// opened, so the command never reached the machine.
	errRelayNotSent = errors.New("relay: not sent")
	// errRelayUnknown mirrors relay.ErrUnknown: the request went out, no
	// exit came back.
	errRelayUnknown = errors.New("relay: result unknown")
	// errRelayMissing: this build cannot start relays yet.
	errRelayMissing = errors.New("this Pocket AI build cannot start the link yet")
)

// relayRefused mirrors *relay.Refused: the relay would not start (the
// fence has a newer generation, another machine entry owns the account,
// the link was switched off there, or the old relay would not end).
type relayRefused struct {
	Reason     string // newer | other-entry | off | busy
	Entry      int64
	Generation int64
	Msg        string
}

func (e *relayRefused) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	switch e.Reason {
	case "newer":
		return "a newer link to this machine has started since"
	case "other-entry":
		return "another machine entry in this Pocket AI owns the link to that account"
	case "off":
		return "the link to this machine was switched off since"
	case "busy":
		return "the previous relay there would not end"
	}
	return "the relay refused to start (" + e.Reason + ")"
}

// relayHandshakeError: the handshake or the version check failed (stray
// output before the link started, another protocol, another build). It will
// not fix itself, so the machine falls back at once.
type relayHandshakeError struct{ Msg string }

func (e *relayHandshakeError) Error() string { return e.Msg }

// relayAvailable: the relay can be started from this build (tests turn it
// off to see the button's reason).
var relayAvailable = true

// startRelay runs cmd (the ssh process, not started yet), does the
// handshake and returns the live link. Refusals come back as *relayRefused,
// a failed handshake or version check as *relayHandshakeError.
var startRelay = func(ctx context.Context, cmd *exec.Cmd, o relayDialOptions) (linkConn, error) {
	c, err := relay.Start(ctx, cmd, relay.DialOptions{Installation: o.Installation, Entry: o.Entry,
		Generation: o.Generation, Token: o.Token, Test: o.Test, Tools: o.Tools, OnDead: o.OnDead})
	if err != nil {
		return nil, relayErr(err)
	}
	return relayConn{c}, nil
}

// relayErr maps the relay's errors onto the server's.
func relayErr(err error) error {
	var ref *relay.Refused
	var hs *relay.HandshakeError
	switch {
	case errors.As(err, &ref):
		return &relayRefused{Reason: ref.Reason, Entry: ref.Entry, Generation: ref.Generation, Msg: ref.Msg}
	case errors.As(err, &hs):
		return &relayHandshakeError{Msg: err.Error()}
	case errors.Is(err, relay.ErrNotSent):
		return fmt.Errorf("%w: %v", errRelayNotSent, err)
	case errors.Is(err, relay.ErrUnknown):
		return fmt.Errorf("%w: %v", errRelayUnknown, err)
	}
	return err
}

// relayConn is a *relay.Client as a linkConn.
type relayConn struct{ c *relay.Client }

func (r relayConn) Hello() linkHello {
	h := r.c.Hello()
	return linkHello{PID: h.PID, Version: h.Version, OS: h.OS, Arch: h.Arch, Generation: h.Generation, Test: h.Test, Sum: h.Sum}
}

func (r relayConn) Exec(ctx context.Context, req linkExecRequest) (linkExecResult, error) {
	res, err := r.c.Exec(ctx, relay.ExecRequest{Script: req.Script, Args: req.Args, Stdin: req.Stdin, Stdout: req.Stdout, Stderr: req.Stderr})
	out := linkExecResult{Code: res.Code, Signal: res.Signal, Started: res.Started}
	if err != nil {
		return out, relayErr(err)
	}
	return out, nil
}

func (r relayConn) Status(ctx context.Context) (relayStatus, error) {
	st, err := r.c.Status(ctx)
	if err != nil {
		return relayStatus{}, err
	}
	return relayStatus{PID: st.PID, UptimeS: int64(st.Uptime.Seconds()), Streams: st.Streams, Ports: st.Ports}, nil
}

func (r relayConn) RTT() time.Duration    { return r.c.RTT() }
func (r relayConn) Done() <-chan struct{} { return r.c.Done() }
func (r relayConn) Err() error            { return r.c.Err() }
func (r relayConn) Close() error          { return r.c.Close() }
func (r relayConn) OpenHold(ctx context.Context, slot string) (net.Conn, error) {
	return r.c.OpenHold(ctx, slot)
}
func (r relayConn) Bind(ctx context.Context, port int) error   { return r.c.Bind(ctx, port) }
func (r relayConn) Unbind(ctx context.Context, port int) error { return r.c.Unbind(ctx, port) }
func (r relayConn) Sync(ctx context.Context, ports []int) ([]int, map[int]string, error) {
	return r.c.Sync(ctx, ports)
}
