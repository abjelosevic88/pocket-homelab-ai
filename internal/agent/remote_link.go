package agent

// A machine's one link (docs/machine-link-plan.md): a relay started over ssh
// that carries short commands (and, from phase 2, agents and their tools)
// inside one connection. The server's machineLink implements RemoteLink;
// Remote uses it when it is set and says it may carry a call.
//
// At most once: a command that went out over the link and whose end never
// came back may have run. It is run again over plain ssh only when it
// provably never reached the machine (ErrLinkNotSent), or when its call site
// marked it safe to repeat (Repeatable, StatusScript). Otherwise the caller
// gets "the result is unknown".

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
)

// LinkUse is what a call would carry over the link.
type LinkUse int

const (
	// LinkStatus: read-only status scripts (the logins check, the machine
	// check). Phase 1, mode "status".
	LinkStatus LinkUse = iota + 1
	// LinkCommands: every other short command and upload. Phase 4, mode "agents".
	LinkCommands
	// LinkAgents: agents' streams and their tools ports. Phase 2, mode "agents".
	LinkAgents
)

// RemoteLink is a machine's link as the agent package uses it.
type RemoteLink interface {
	// Exec runs script like Remote.Run would, over the link: stdin (nil:
	// none) is sent and then its end; stdout and stderr are written as they
	// come. Errors: ErrLinkNotSent (it never reached the machine) or
	// ErrLinkUnknown (it went out, no end came back); a non-zero exit is a
	// code, not an error.
	Exec(ctx context.Context, script string, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, err error)
	// UseFor: the link is up and its machine's mode lets it carry kind.
	UseFor(kind LinkUse) bool
	// phase 2:
	OpenHold(ctx context.Context, slot string) (net.Conn, error)
	BindTools(ctx context.Context, slot string, remotePort int, localAddr string) error
	UnbindTools(ctx context.Context, slot string, remotePort int) error
}

var (
	// ErrLinkNotSent: the command never reached the machine (the stream
	// could not be opened): safe to run anywhere.
	ErrLinkNotSent = errors.New("the command did not reach the machine's link")
	// ErrLinkUnknown: the command went out, and the link dropped before its
	// end came back: it may have run.
	ErrLinkUnknown = errors.New("the link dropped while the command ran")
)

// LinkUnknownError is what a caller gets for a command that is not safe to
// repeat when the link dropped while it ran.
type LinkUnknownError struct{ Machine string }

func (e *LinkUnknownError) Error() string {
	return "the result is unknown: the link to " + e.Machine + " dropped while it ran"
}

func (e *LinkUnknownError) Unwrap() error { return ErrLinkUnknown }

// LinkExitError: a command over the link ended with a non-zero code (what
// *exec.ExitError is for a command over ssh).
type LinkExitError struct {
	Code   int
	Signal string
}

func (e *LinkExitError) Error() string {
	if e.Signal != "" {
		return "signal: " + e.Signal
	}
	return "exit status " + strconv.Itoa(e.Code)
}

// ExitCode is the command's exit code.
func (e *LinkExitError) ExitCode() int { return e.Code }

type linkMarks int

const (
	markRepeatable linkMarks = 1 << iota
	markStatus
	markPlain
)

type linkMarkKey struct{}

func withMark(ctx context.Context, m linkMarks) context.Context {
	prev, _ := ctx.Value(linkMarkKey{}).(linkMarks)
	return context.WithValue(ctx, linkMarkKey{}, prev|m)
}

func linkMarksOf(ctx context.Context) linkMarks {
	if ctx == nil {
		return 0
	}
	m, _ := ctx.Value(linkMarkKey{}).(linkMarks)
	return m
}

// Repeatable marks a call as safe to run twice: when the link drops while
// it runs, it is run again over plain ssh.
func Repeatable(ctx context.Context) context.Context { return withMark(ctx, markRepeatable) }

// StatusScript marks a read-only status script (the logins check, the
// machine check): repeatable, and it goes over the link already in mode
// "status".
func StatusScript(ctx context.Context) context.Context {
	return withMark(ctx, markRepeatable|markStatus)
}

// PlainSSH marks a call that must never go over the link: the pocket-hold
// upload (the relay needs it first), the fenced relay end, the cleanup and
// forward restore of a fallback.
func PlainSSH(ctx context.Context) context.Context { return withMark(ctx, markPlain) }

// IsRepeatable: ctx was marked Repeatable or StatusScript.
func IsRepeatable(ctx context.Context) bool { return linkMarksOf(ctx)&markRepeatable != 0 }

// IsStatusScript: ctx was marked StatusScript.
func IsStatusScript(ctx context.Context) bool { return linkMarksOf(ctx)&markStatus != 0 }

// IsPlainSSH: ctx was marked PlainSSH.
func IsPlainSSH(ctx context.Context) bool { return linkMarksOf(ctx)&markPlain != 0 }

// linkFor is the link a call with ctx goes over, nil for plain ssh.
func (r *Remote) linkFor(ctx context.Context) RemoteLink {
	if r.Link == nil || IsPlainSSH(ctx) {
		return nil
	}
	kind := LinkCommands
	if IsStatusScript(ctx) {
		kind = LinkStatus
	}
	if !r.Link.UseFor(kind) {
		return nil
	}
	return r.Link
}

// overLink runs a whole script (already with its profile and arguments) over
// the link when the call goes there. done false: run it over plain ssh
// instead (it is not for the link, it provably never reached the machine,
// or it is safe to repeat); out and errb are empty again then.
func (r *Remote) overLink(ctx context.Context, script string, stdin io.Reader, out, errb *bytes.Buffer) (done bool, err error) {
	l := r.linkFor(ctx)
	if l == nil {
		return false, nil
	}
	// input that can be read again from where it started: a retry over ssh
	// sends all of it
	rewind := func() bool { return stdin == nil }
	if s, ok := stdin.(io.Seeker); ok {
		if at, err := s.Seek(0, io.SeekCurrent); err == nil {
			rewind = func() bool {
				_, err := s.Seek(at, io.SeekStart)
				return err == nil
			}
		}
	}
	code, err := l.Exec(ctx, script, nil, stdin, out, errb)
	switch {
	case err == nil:
		if code != 0 {
			return true, &LinkExitError{Code: code}
		}
		return true, nil
	case errors.Is(err, ErrLinkNotSent):
		if !rewind() {
			return true, err // its input was read and cannot be had again
		}
		out.Reset()
		errb.Reset()
		return false, nil
	case errors.Is(err, ErrLinkUnknown):
		if IsRepeatable(ctx) && rewind() {
			out.Reset()
			errb.Reset()
			return false, nil
		}
		return true, &LinkUnknownError{Machine: r.Target}
	}
	return true, err
}

// LinkCommand is the ssh command that starts a machine's relay: the same
// ssh, sandbox and options as every connection to it (a person's own key in
// their jail), but never a shared ControlMaster, ssh keep-alives every 5 s
// (3 missed: dead). The relay itself starts in the login shell's own
// environment, without the profile and PATH preamble: every command it runs
// brings that preamble along (RemoteScriptText), as over ssh, so it finds
// exactly the environment a command over ssh finds (with the preamble
// applied twice, PATH would differ). stderr is the caller's to keep apart.
func (r *Remote) LinkCommand(ctx context.Context, script string, args ...string) (*exec.Cmd, error) {
	argv := []string{"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
	argv = append(argv, r.sshArgs()...) // -T and BatchMode=yes come with these
	bare := "set --"
	for _, a := range args {
		bare += " " + shq(a)
	}
	if testRemoteHome != "" {
		bare = "HOME=" + shq(testRemoteHome) + "; export HOME; " + bare
	}
	return r.ssh(ctx, append(argv, remoteCommand(bare+"\n"+script)))
}
