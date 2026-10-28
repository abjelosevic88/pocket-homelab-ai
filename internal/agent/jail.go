package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The jail keeps the people on one server apart. Everyone but the owner of
// the server's own login has a folder of their own (PEOPLE_DIR/<id>: their
// home with their CLI logins, and their projects), and everything that runs
// for them — the agent, its sign-in, git in their folders — runs inside
// bubblewrap: their folder is all of the disk they see (plus the system
// read-only), other people's agents are invisible, and the server's
// environment (its secrets) never reaches them.
//
// What runs for a person is told by where it runs: a working folder or a
// HOME / CLAUDE_CONFIG_DIR / CODEX_HOME inside PEOPLE_DIR. So a command
// cannot slip out by forgetting to ask for the jail: their settings hooks,
// MCP servers, Codex notify and git hooks all live in that folder.

// JailConfig is set once at start (SetJail).
type JailConfig struct {
	People  string // PEOPLE_DIR, absolute
	Uploads string // DATA_DIR/uploads: a person's own uploads are readable inside
	Off     bool   // SANDBOX=off: people keep their folders but run without bubblewrap
	Bin     string // bubblewrap binary ("" = look for bwrap in PATH)
}

type jailState struct {
	mu      sync.Mutex
	cfg     JailConfig
	bin     string
	err     string // why bubblewrap does not work here ("" = it does)
	checked time.Time
}

var jail = &jailState{}

// SetJail configures the jail and checks that bubblewrap works.
func SetJail(c JailConfig) {
	jail.mu.Lock()
	jail.cfg = c
	jail.checked = time.Time{}
	jail.mu.Unlock()
	JailCheck(true)
}

// JailStatus says whether people's agents run sandboxed: on (bubblewrap
// works), off (SANDBOX=off) and why it does not work when it does not.
func JailStatus() (on, off bool, why string) {
	JailCheck(false)
	jail.mu.Lock()
	defer jail.mu.Unlock()
	return jail.err == "" && !jail.cfg.Off, jail.cfg.Off, jail.err
}

// PeopleDir is where people's folders live.
func PeopleDir() string {
	jail.mu.Lock()
	defer jail.mu.Unlock()
	return jail.cfg.People
}

// JailCheck (re)tests bubblewrap: at start, and again a few minutes after a
// failure so installing it takes effect without a restart.
func JailCheck(force bool) {
	jail.mu.Lock()
	cfg, checked, failing := jail.cfg, jail.checked, jail.err != ""
	jail.mu.Unlock()
	if cfg.People == "" || cfg.Off {
		return
	}
	if !force && !checked.IsZero() && (!failing || time.Since(checked) < 2*time.Minute) {
		return
	}
	bin, why := probeBwrap(cfg)
	jail.mu.Lock()
	jail.bin, jail.err, jail.checked = bin, why, time.Now()
	jail.mu.Unlock()
}

func probeBwrap(cfg JailConfig) (string, string) {
	bin := cfg.Bin
	if bin == "" {
		bin = "bwrap"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", "bubblewrap (bwrap) is not installed on the server"
	}
	dir, err := os.MkdirTemp(cfg.People, ".probe-")
	if err != nil {
		return "", "cannot use " + cfg.People + ": " + err.Error()
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// the probe folder is writable inside, a file next to it is not there
	outside := dir + ".outside"
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		return "", "cannot use " + cfg.People + ": " + err.Error()
	}
	defer os.Remove(outside)
	args := jailArgs(cfg, dir, dir, os.Getenv("PATH"), []string{"HOME=" + dir}, "/bin/sh", []string{"-c", `test -w "$HOME" && ! test -e "$1"`, "sh", outside})
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return path, "bubblewrap does not work here: " + oneLine(msg, 200)
	}
	return path, ""
}

// ErrJailUnavailable: something of a person's should run, but the sandbox
// does not work and SANDBOX=off was not chosen.
type ErrJailUnavailable struct{ Why string }

func (e *ErrJailUnavailable) Error() string {
	return "Agents of people other than the server's owner run in a sandbox, and it is unavailable: " + e.Why +
		". Install bubblewrap on the server (apt install bubblewrap), or set SANDBOX=off to run them without it."
}

// personDir is PEOPLE_DIR/<id> when the command runs for a person.
func personDir(dir string, env []string) string {
	jail.mu.Lock()
	people := jail.cfg.People
	jail.mu.Unlock()
	if people == "" {
		return ""
	}
	under := func(p string) string {
		if p == "" || !filepath.IsAbs(p) {
			return ""
		}
		rel, err := filepath.Rel(people, filepath.Clean(p))
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return ""
		}
		first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if strings.HasPrefix(first, ".") {
			return ""
		}
		return filepath.Join(people, first)
	}
	if p := under(dir); p != "" {
		return p
	}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		switch k {
		case "HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME":
			if p := under(v); p != "" {
				return p
			}
		}
	}
	return ""
}

// PersonHome is the home of the person the first of paths belongs to ("" =
// none does): sh scripts on people's repos run in their jail from there.
func PersonHome(paths ...string) string {
	for _, p := range paths {
		if d := personDir(p, nil); d != "" {
			return filepath.Join(d, "home")
		}
	}
	return ""
}

// IsPersonPath reports whether p lies in a person's folder.
func IsPersonPath(p string) bool { return personDir(p, nil) != "" }

// Confined reports whether p, with every symlink followed, stays inside the
// person's folder it is in (true for paths outside people's folders). The
// server reads people's session files and lists their folders; a symlink
// they made must not lead it into someone else's files.
func Confined(p string) bool {
	root := personDir(p, nil)
	if root == "" {
		return true
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		// not there yet: judge the part that is
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		if _, statErr := os.Lstat(p); statErr == nil {
			return false // there, but a broken or looping link
		}
		return Confined(parent)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	return real == rootReal || strings.HasPrefix(real, rootReal+string(filepath.Separator))
}

// jailEnvKeep: what of the server's environment a person's process gets.
var jailEnvKeep = map[string]bool{"PATH": true, "LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NIX_SSL_CERT_FILE": true, "GIT_SSL_CAINFO": true, "LOCALE_ARCHIVE": true}

// cleanEnv is the environment of a person's process: harmless server
// variables, then env, then HOME in their folder.
func cleanEnv(person string, env []string) []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if jailEnvKeep[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, e)
		}
	}
	out = append(out, env...)
	return append(out, "HOME="+filepath.Join(person, "home"), "USER=pocket", "LOGNAME=pocket",
		"TMPDIR=/tmp", "DISABLE_AUTOUPDATER=1")
}

// LocalCmd is exec.Command for anything that may belong to a person: it
// runs bin in dir with env added, inside the jail when it is theirs.
// ctx may be nil.
func LocalCmd(ctx context.Context, dir string, env []string, bin string, args ...string) (*exec.Cmd, error) {
	mk := func(name string, a ...string) *exec.Cmd {
		if ctx != nil {
			return exec.CommandContext(ctx, name, a...)
		}
		return exec.Command(name, a...)
	}
	person := personDir(dir, env)
	if person == "" {
		cmd := mk(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		return cmd, nil
	}
	if err := os.MkdirAll(filepath.Join(person, "home"), 0o700); err != nil {
		return nil, err
	}
	if personDir(dir, nil) != person {
		dir = filepath.Join(person, "home") // the server's folders are not there for them
	}
	full := cleanEnv(person, env)
	JailCheck(false)
	jail.mu.Lock()
	cfg, bwrap, why := jail.cfg, jail.bin, jail.err
	jail.mu.Unlock()
	if cfg.Off {
		cmd := mk(bin, args...)
		cmd.Dir = dir
		cmd.Env = full
		return cmd, nil
	}
	if why != "" {
		return nil, &ErrJailUnavailable{Why: why}
	}
	path := ""
	for _, e := range full {
		if strings.HasPrefix(e, "PATH=") {
			path = strings.TrimPrefix(e, "PATH=")
		}
	}
	cmd := mk(bwrap, jailArgs(cfg, person, dir, path, full, bin, args)...)
	cmd.Env = full // bwrap itself needs PATH only; the rest is passed on by --setenv as well
	return cmd, nil
}

// jailArgs builds the bubblewrap command line: the system read-only, the
// person's folder read-write at its own path (so paths in session files
// mean the same inside and out), fresh /tmp, /dev and /proc, own pid space.
func jailArgs(cfg JailConfig, person, dir, path string, env []string, bin string, args []string) []string {
	a := []string{"--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--hostname", "pocket", "--clearenv"}
	for _, sys := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/nix", "/opt", "/run/current-system", "/run/wrappers", "/run/systemd/resolve", "/var/lib/pocket-box-ca"} {
		fi, err := os.Lstat(sys)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(sys); err == nil {
				a = append(a, "--symlink", t, sys)
			}
			continue
		}
		a = append(a, "--ro-bind", sys, sys)
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--dir", "/run/user")
	// where the CLI and the tools on PATH really are, when that is outside
	// the system folders (~/.local/bin/claude → a versions folder)
	var inPath []string
	seen := map[string]bool{}
	roBind := func(p string) {
		if p == "" || seen[p] || underSystem(p) || personDir(p, nil) != "" {
			return
		}
		seen[p] = true
		a = append(a, "--ro-bind", p, p)
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		if underSystem(real) {
			inPath = append(inPath, real)
		}
	}
	// the CLI's own folder comes along (Codex keeps its helpers and bundled
	// tools next to bin/, Claude Code its versions)
	binDir := func(real string) string {
		d := filepath.Dir(real)
		if filepath.Base(d) == "bin" {
			d = filepath.Dir(d)
		}
		return d
	}
	runBin := bin
	if filepath.IsAbs(bin) || strings.Contains(bin, "/") {
		if real, err := filepath.EvalSymlinks(bin); err == nil {
			runBin = real
			roBind(binDir(real))
		}
	} else if found, err := exec.LookPath(bin); err == nil {
		if real, err := filepath.EvalSymlinks(found); err == nil {
			runBin = real
			roBind(binDir(real))
		}
	}
	a = append(a, "--bind", person, person)
	if cfg.Uploads != "" {
		if up := filepath.Join(cfg.Uploads, filepath.Base(person)); dirExists(up) {
			a = append(a, "--ro-bind", up, up)
		}
	}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "PATH" {
			continue
		}
		a = append(a, "--setenv", k, v)
	}
	a = append(a, "--setenv", "PATH", strings.Join(append(inPath, "/usr/local/bin", "/usr/bin", "/bin"), ":"))
	a = append(a, "--chdir", dir, "--", runBin)
	return append(a, args...)
}

// underSystem: inside a folder the jail shows read-only anyway.
func underSystem(p string) bool {
	for _, sys := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/nix", "/opt", "/run/current-system", "/run/wrappers"} {
		if p == sys || strings.HasPrefix(p, sys+"/") {
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// IsJailUnavailable reports whether err is a missing sandbox.
func IsJailUnavailable(err error) bool {
	var e *ErrJailUnavailable
	return errors.As(err, &e)
}

var errOutside = errors.New("a link out of its person's folder")

// OpenConfined is os.Open for files the server reads out of CLI folders:
// in a person's folder, only what stays in it (Confined).
func OpenConfined(p string) (*os.File, error) {
	if !Confined(p) {
		return nil, &os.PathError{Op: "open", Path: p, Err: errOutside}
	}
	return os.Open(p)
}

// ReadConfined is os.ReadFile with the same rule.
func ReadConfined(p string) ([]byte, error) {
	if !Confined(p) {
		return nil, &os.PathError{Op: "read", Path: p, Err: errOutside}
	}
	return os.ReadFile(p)
}

// WriteConfined is os.WriteFile that will not follow a person's link out
// of their folder.
func WriteConfined(p string, data []byte, perm os.FileMode) error {
	if !Confined(p) {
		return &os.PathError{Op: "write", Path: p, Err: errOutside}
	}
	return os.WriteFile(p, data, perm)
}
