package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLink is a machine link: what it carries, and what Exec does.
type fakeLink struct {
	use   map[LinkUse]bool
	exec  func(script string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
	calls int
}

func (f *fakeLink) Exec(_ context.Context, script string, _ []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	f.calls++
	return f.exec(script, stdin, stdout, stderr)
}
func (f *fakeLink) UseFor(k LinkUse) bool                                { return f.use[k] }
func (f *fakeLink) OpenHold(context.Context, string) (net.Conn, error)   { return nil, errors.New("no") }
func (f *fakeLink) BindTools(context.Context, string, int, string) error { return errors.New("no") }
func (f *fakeLink) UnbindTools(context.Context, string, int) error       { return errors.New("no") }

// linkFakeSSH puts an ssh on PATH that runs the remote command here and counts
// its calls; it returns the file it counts in.
func linkFakeSSH(t *testing.T) func() int {
	t.Helper()
	dir := t.TempDir()
	mark := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho x >> " + mark + "\nfor a; do last=$a; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return func() int {
		b, _ := os.ReadFile(mark)
		return strings.Count(string(b), "x")
	}
}

func TestRunGoesOverLinkByMark(t *testing.T) {
	sshCalls := linkFakeSSH(t)
	l := &fakeLink{use: map[LinkUse]bool{LinkStatus: true}, exec: func(script string, _ io.Reader, out, _ io.Writer) (int, error) {
		if !strings.Contains(script, "PATH=") || strings.Contains(script, "$1") && !strings.Contains(script, "set -- a\n") {
			t.Errorf("not the whole script: %q", script)
		}
		_, _ = io.WriteString(out, "via link\n")
		return 0, nil
	}}
	r := &Remote{Target: "box", Link: l}
	ctx := context.Background()

	out, _, err := r.Run(StatusScript(ctx), 10*time.Second, `echo "$1 via ssh"`, "a")
	if err != nil || out != "via link\n" || sshCalls() != 0 {
		t.Fatalf("status script: %q %v ssh=%d", out, err, sshCalls())
	}
	// mode "status" carries status scripts only
	out, _, err = r.Run(ctx, 10*time.Second, `echo "$1 via ssh"`, "a")
	if err != nil || out != "a via ssh\n" || sshCalls() != 1 || l.calls != 1 {
		t.Fatalf("plain command: %q %v ssh=%d link=%d", out, err, sshCalls(), l.calls)
	}
	// mode "agents" carries every command, but never a PlainSSH one
	l.use[LinkCommands] = true
	if out, _, _ = r.Run(ctx, 10*time.Second, `echo x`); out != "via link\n" {
		t.Fatalf("agents mode: %q", out)
	}
	out, _, err = r.Run(PlainSSH(StatusScript(ctx)), 10*time.Second, `echo "$1 via ssh"`, "b")
	if err != nil || out != "b via ssh\n" || l.calls != 2 {
		t.Fatalf("PlainSSH went over the link: %q %v link=%d", out, err, l.calls)
	}
	if err := r.Put(PlainSSH(ctx), 10*time.Second, filepath.Join(t.TempDir(), "f"), []byte("data")); err != nil || l.calls != 2 {
		t.Fatalf("PlainSSH Put: %v link=%d", err, l.calls)
	}
}

func TestLinkAtMostOnce(t *testing.T) {
	sshCalls := linkFakeSSH(t)
	ctx := context.Background()
	var fail error
	l := &fakeLink{use: map[LinkUse]bool{LinkStatus: true, LinkCommands: true}, exec: func(_ string, _ io.Reader, out, _ io.Writer) (int, error) {
		_, _ = io.WriteString(out, "half")
		return 0, fail
	}}
	r := &Remote{Target: "box", Link: l}

	// never reached the machine: plain ssh, as if the link were not there
	fail = ErrLinkNotSent
	out, _, err := r.Run(ctx, 10*time.Second, `echo ssh`)
	if err != nil || out != "ssh\n" || sshCalls() != 1 {
		t.Fatalf("not sent: %q %v ssh=%d", out, err, sshCalls())
	}
	// went out, no end came: not repeatable → unknown, never run twice
	fail = ErrLinkUnknown
	_, _, err = r.Run(ctx, 10*time.Second, `echo ssh`)
	var unk *LinkUnknownError
	if !errors.As(err, &unk) || !errors.Is(err, ErrLinkUnknown) || sshCalls() != 1 {
		t.Fatalf("unknown: %v ssh=%d", err, sshCalls())
	}
	if want := "the result is unknown: the link to box dropped while it ran"; err.Error() != want {
		t.Fatalf("message %q", err)
	}
	// repeatable: run again over ssh, with nothing of the first try's output
	out, _, err = r.Run(Repeatable(ctx), 10*time.Second, `echo ssh`)
	if err != nil || out != "ssh\n" || sshCalls() != 2 {
		t.Fatalf("repeatable: %q %v ssh=%d", out, err, sshCalls())
	}
	// an upload that never reached the machine is sent whole over ssh
	l.exec = func(_ string, in io.Reader, _, _ io.Writer) (int, error) {
		buf := make([]byte, 2)
		_, _ = in.Read(buf) // read a little before it failed
		return 0, ErrLinkNotSent
	}
	dst := filepath.Join(t.TempDir(), "up")
	if err := r.Put(ctx, 10*time.Second, dst, []byte("whole file")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "whole file" || sshCalls() != 3 {
		t.Fatalf("upload: %q ssh=%d", b, sshCalls())
	}
	// an upload whose end did not come: unknown, not sent twice
	l.exec = func(string, io.Reader, io.Writer, io.Writer) (int, error) { return 0, ErrLinkUnknown }
	if err := r.Put(ctx, 10*time.Second, dst, []byte("again")); !errors.Is(err, ErrLinkUnknown) || sshCalls() != 3 {
		t.Fatalf("upload unknown: %v ssh=%d", err, sshCalls())
	}
	// a non-zero exit over the link is the command's own
	l.exec = func(_ string, _ io.Reader, _, errw io.Writer) (int, error) {
		_, _ = io.WriteString(errw, "no such file\n")
		return 3, nil
	}
	_, stderr, err := r.Run(ctx, 10*time.Second, `exit 3`)
	var xe *LinkExitError
	if !errors.As(err, &xe) || xe.ExitCode() != 3 || stderr != "no such file" || sshCalls() != 3 {
		t.Fatalf("exit: %v %q ssh=%d", err, stderr, sshCalls())
	}
}

func TestLinkMarks(t *testing.T) {
	ctx := context.Background()
	if IsRepeatable(ctx) || IsStatusScript(ctx) || IsPlainSSH(ctx) {
		t.Fatal("unmarked context is marked")
	}
	s := StatusScript(ctx)
	if !IsRepeatable(s) || !IsStatusScript(s) || IsPlainSSH(s) {
		t.Fatal("a status script is repeatable")
	}
	p := PlainSSH(Repeatable(ctx))
	if !IsRepeatable(p) || !IsPlainSSH(p) || IsStatusScript(p) {
		t.Fatal("marks add up")
	}
}

func TestLinkCommand(t *testing.T) {
	r := &Remote{Target: "me@box", Port: 2222}
	cmd, err := r.LinkCommand(context.Background(), `exec "$1" relay`, "/home/me/.pocket-ai/bin/pocket-hold-x")
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-o ControlMaster=no", "-o ControlPath=none", "-o ServerAliveInterval=5", "-o ServerAliveCountMax=3", "-T", "BatchMode=yes", "-p 2222", "me@box --"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
	// ssh keeps the first value of an option: the link's keep-alives win
	if strings.Index(a, "ServerAliveInterval=5") > strings.Index(a, "ServerAliveInterval=20") {
		t.Error("the link's ServerAliveInterval comes after the default one")
	}
	if strings.Contains(a, "ControlMaster=auto") {
		t.Error("a shared connection for the link")
	}
}
