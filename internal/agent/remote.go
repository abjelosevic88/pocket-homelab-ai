package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Remote is another machine the CLIs run on, reached with the server's own
// ssh (keys and ~/.ssh/config of the user Pocket AI runs as). The stdio
// protocols of both CLIs go through the ssh connection unchanged.
type Remote struct {
	Target string // ssh destination: a Host from ~/.ssh/config or user@host
	Port   int
	// Forwards are ssh -R specs ("127.0.0.1:41234:127.0.0.1:38211") an agent's
	// connection carries: the Pocket app tools, whose endpoint is on this
	// server's loopback, reached from the remote machine's loopback.
	Forwards []string
	// Home is a person's home (PEOPLE_DIR/<id>/home) when the machine is
	// theirs: ssh then runs in their jail with their own key, config and
	// known hosts (home/.ssh), never the server's.
	Home string
}

// PersonKey is a person's ssh key, made by the server (SSHKey).
const PersonKey = "id_ed25519"

// sshArgs are the options of every connection: never prompt, learn a new
// host key once, notice a dead link.
func (r *Remote) sshArgs() []string {
	a := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=20", "-o", "ServerAliveCountMax=6", "-o", "LogLevel=ERROR"}
	if r.Port > 0 {
		a = append(a, "-p", strconv.Itoa(r.Port))
	}
	if r.Home != "" {
		// only what is in their folder: ssh would read the server account's
		// ~/.ssh (its home comes from passwd, not $HOME) with SANDBOX=off
		dot := filepath.Join(r.Home, ".ssh")
		conf := filepath.Join(dot, "config")
		if _, err := os.Stat(conf); err != nil {
			conf = "none"
		}
		a = append([]string{"-F", conf}, a...)
		a = append(a, "-i", filepath.Join(dot, PersonKey), "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
			"-o", "UserKnownHostsFile="+filepath.Join(dot, "known_hosts"))
	}
	return append(a, r.Target, "--")
}

// ssh makes the ssh command: here for the server's machines, in the
// person's jail for theirs. ctx may be nil.
func (r *Remote) ssh(ctx context.Context, argv []string) (*exec.Cmd, error) {
	if r.Home != "" {
		return LocalCmd(ctx, r.Home, []string{"HOME=" + r.Home}, "ssh", argv...)
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, "ssh", argv...)
	} else {
		cmd = exec.Command("ssh", argv...)
	}
	cmd.Env = os.Environ()
	return cmd, nil
}

// mux: a person's ssh runs in a jail that ends with it, so a shared
// connection would not outlive the command anyway.
func (r *Remote) mux() []string {
	if r.Home != "" {
		return nil
	}
	return muxArgs()
}

// remotePath: the login shell over ssh is not interactive, so rc files that
// put the CLIs on PATH are not read. These are where installers put them.
const remotePath = `PATH="$HOME/.local/bin:$HOME/bin:$HOME/.npm-global/bin:$HOME/.bun/bin:$HOME/.volta/bin:$HOME/.nix-profile/bin:/etc/profiles/per-user/$USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"; export PATH`

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string { return shq(s) }

func shq(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteCommand wraps a POSIX sh script so that it survives whatever login
// shell the remote account has (bash, zsh, fish): the script travels as
// base64 inside one single-quoted word without backslashes or quotes.
func remoteCommand(script string) string {
	b := base64.StdEncoding.EncodeToString([]byte(script))
	return `exec /bin/sh -c 'eval "$(echo ` + b + ` | base64 -d 2>/dev/null || echo ` + b + ` | base64 -D)"'`
}

// script builds the remote sh script: profile, PATH, folder, then exec.
func remoteScript(dir string, env []string, bin string, args []string) string {
	var b strings.Builder
	b.WriteString(`[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1 </dev/null; [ -r "$HOME/.profile" ] && . "$HOME/.profile" >/dev/null 2>&1 </dev/null; `)
	b.WriteString(remotePath + "; ")
	if dir != "" {
		b.WriteString("cd -- " + shq(dir) + " || exit 1; ")
	}
	b.WriteString("exec")
	if len(env) > 0 {
		b.WriteString(" env")
		for _, e := range env {
			b.WriteString(" " + shq(e))
		}
	}
	b.WriteString(" " + shq(bin))
	for _, a := range args {
		b.WriteString(" " + shq(a))
	}
	return b.String()
}

// Command runs bin in dir: here (r nil; a person's things in their jail,
// jail.go) or on the remote machine over ssh. env is added to the local
// environment; on a remote only env itself is set.
func Command(r *Remote, dir string, env []string, bin string, args ...string) (*exec.Cmd, error) {
	if r == nil {
		return LocalCmd(nil, dir, env, bin, args...)
	}
	var argv []string
	if len(r.Forwards) > 0 {
		// a forward that cannot be made fails the start instead of leaving the tools dead
		argv = append(argv, "-o", "ExitOnForwardFailure=yes")
		for _, f := range r.Forwards {
			argv = append(argv, "-R", f)
		}
	}
	argv = append(argv, r.sshArgs()...)
	return r.ssh(nil, append(argv, remoteCommand(remoteScript(dir, env, bin, args))))
}

// Run runs a short sh script on the remote machine (the same profile and
// PATH as an agent gets) and returns its output. Short commands share one
// ssh connection per machine (a fresh one costs a full handshake, ~1.5 s to a
// far-away box; a shared one two round trips), and a connection that could
// not be made at all is tried again: a home line drops outgoing connections
// for a few seconds now and then ("No route to host"). Agents do not share
// it (see Command): a lost link must not take every running session along.
func (r *Remote) Run(ctx context.Context, timeout time.Duration, script string, args ...string) (string, string, error) {
	return r.RunInput(ctx, timeout, nil, script, args...)
}

// RunInput is Run with stdin (an upload): never tried twice, the input is
// read once.
func (r *Remote) RunInput(ctx context.Context, timeout time.Duration, stdin io.Reader, script string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := `[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1 </dev/null; [ -r "$HOME/.profile" ] && . "$HOME/.profile" >/dev/null 2>&1 </dev/null; ` +
		remotePath + "; set --"
	for _, a := range args {
		full += " " + shq(a)
	}
	full += "\n" + script
	argv := append(r.mux(), r.sshArgs()...)
	argv = append(argv, remoteCommand(full))
	for try := 0; ; try++ {
		cmd, err := r.ssh(ctx, argv)
		if err != nil {
			return "", "", err
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		cmd.Stdin = stdin
		err = cmd.Run()
		stderr := strings.TrimSpace(errb.String())
		if err == nil || try >= 2 || stdin != nil || !notConnected(err, stderr) {
			return out.String(), stderr, err
		}
		select { // 1 s, then 3 s: blips last a few seconds
		case <-ctx.Done():
			return out.String(), stderr, err
		case <-time.After(time.Duration(1+2*try) * time.Second):
		}
	}
}

// notConnected: ssh gave up before the script started (exit 255 with a
// connect error), so running it again cannot do anything twice.
func notConnected(err error, stderr string) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 255 {
		return false
	}
	for _, m := range []string{"No route to host", "Network is unreachable", "Connection refused", "Could not resolve hostname", "Connection timed out", "Operation timed out"} {
		if strings.Contains(stderr, m) {
			return true
		}
	}
	return false
}

// muxArgs share one connection per machine between short commands. The
// shared link notices a dead network within ~15 s (agents: 2 min), and closes
// 5 minutes after its last use. They come first: ssh keeps the first value.
// Put writes data to path on the remote machine (mode 600, replaced in one
// step: a half-written file never takes its place).
func (r *Remote) Put(ctx context.Context, timeout time.Duration, path string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	script := "set -e; umask 077; t=\"$1.part.$$\"; cat > \"$t\"; mv -f -- \"$t\" \"$1\""
	argv := append(r.mux(), r.sshArgs()...)
	argv = append(argv, remoteCommand("set -- "+shq(path)+"\n"+script))
	cmd, err := r.ssh(ctx, argv)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(data)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func muxArgs() []string {
	dir := muxDir()
	if dir == "" {
		return nil
	}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(dir, "%C"), "-o", "ControlPersist=300",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
}

var muxDirOnce = sync.OnceValue(func() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "pocket-ai-ssh-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil || os.Chmod(dir, 0o700) != nil {
		return "" // no private folder for the sockets: connect every time
	}
	return dir
})

func muxDir() string { return muxDirOnce() }

// Shell is an interactive login shell on the remote machine for a terminal:
// ssh with a tty, in dir when one is given. TERM travels with the tty.
func (r *Remote) Shell(dir string) (*exec.Cmd, error) {
	a := r.sshArgs()
	for i, o := range a {
		if o == "-T" {
			a[i] = "-tt"
			break
		}
	}
	if dir != "" {
		a = append(a, remoteCommand(`[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1 </dev/null; `+remotePath+"; cd -- "+shq(dir)+` || cd; exec "${SHELL:-/bin/sh}" -l`))
	} else {
		a = a[:len(a)-1] // no command: sshd starts the account's login shell itself
	}
	term := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	if r.Home != "" {
		return LocalCmd(nil, r.Home, append([]string{"HOME=" + r.Home}, term...), "ssh", a...)
	}
	cmd := exec.Command("ssh", a...)
	cmd.Env = append(os.Environ(), term...)
	return cmd, nil
}
