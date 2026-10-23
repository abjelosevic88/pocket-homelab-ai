package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Remote is another machine the CLIs run on, reached with the server's own
// ssh (keys and ~/.ssh/config of the user Pocket AI runs as). The stdio
// protocols of both CLIs go through the ssh connection unchanged.
type Remote struct {
	Target string // ssh destination: a Host from ~/.ssh/config or user@host
	Port   int
}

// sshArgs are the options of every connection: never prompt, learn a new
// host key once, notice a dead link.
func (r *Remote) sshArgs() []string {
	a := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=20", "-o", "ServerAliveCountMax=6", "-o", "LogLevel=ERROR"}
	if r.Port > 0 {
		a = append(a, "-p", strconv.Itoa(r.Port))
	}
	return append(a, r.Target, "--")
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

// Command runs bin in dir: here (r nil) or on the remote machine over ssh.
// env is added to the local environment; on a remote only env itself is set.
func Command(r *Remote, dir string, env []string, bin string, args ...string) *exec.Cmd {
	if r == nil {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		return cmd
	}
	cmd := exec.Command("ssh", append(r.sshArgs(), remoteCommand(remoteScript(dir, env, bin, args)))...)
	cmd.Env = os.Environ()
	return cmd
}

// Run runs a short sh script on the remote machine (the same profile and
// PATH as an agent gets) and returns its output.
func (r *Remote) Run(ctx context.Context, timeout time.Duration, script string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := `[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1 </dev/null; [ -r "$HOME/.profile" ] && . "$HOME/.profile" >/dev/null 2>&1 </dev/null; ` +
		remotePath + "; set --"
	for _, a := range args {
		full += " " + shq(a)
	}
	full += "\n" + script
	cmd := exec.CommandContext(ctx, "ssh", append(r.sshArgs(), remoteCommand(full))...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), strings.TrimSpace(errb.String()), err
}
