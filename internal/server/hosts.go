package server

import (
	"context"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Other machines agents run on (like Paseo's hosts): the CLIs are started
// there over ssh, the conversation is driven and stored here. The remote
// needs claude and/or codex installed and signed in; Pocket AI only needs to
// be able to `ssh <target>` without a password.
//
// A machine belongs to whoever added it. The owner's are reached with this
// server's own keys (~/.ssh); everyone else's with a key of their own that
// lives in their folder (home/.ssh, made by the server), ssh running in
// their jail. Nobody sees or uses another person's machines.

func (s *Server) hostRoutes(r chi.Router) {
	r.Get("/hosts", s.handleListHosts)
	r.Post("/hosts", s.handleCreateHost)
	r.Patch("/hosts/{id}", s.handleUpdateHost)
	r.Delete("/hosts/{id}", s.handleDeleteHost)
	r.Post("/hosts/{id}/check", s.handleCheckHost)
	r.Get("/hosts/key", s.handleHostKey)
	r.Post("/fs/mkdir", s.handleMkdir)
	r.Post("/fs/clone", s.handleClone)
}

func remoteOf(h *store.Host) *agent.Remote {
	return &agent.Remote{Target: h.Target, Port: h.Port, Home: h.Home}
}

// host loads a host for running or browsing; id 0 is this server (nil).
// Callers check who may use it (checkHostUse, mayUseHost).
func (s *Server) host(ctx context.Context, id int64) (*store.Host, error) {
	if id == 0 {
		return nil, nil
	}
	h, err := s.store.GetHost(ctx, id)
	if err != nil {
		return nil, errNoHost
	}
	s.fillHost(ctx, h)
	return h, nil
}

var errNoHost = badRequest("that server is not set up in Pocket AI (any more)")

// hostUser is the person a machine belongs to.
func (s *Server) hostUser(ctx context.Context, h *store.Host) int64 {
	if h.UserID > 0 {
		return h.UserID
	}
	return s.store.OwnerID(ctx)
}

// fillHost sets how the machine is reached: from its person's own folder,
// or with the server's keys.
func (s *Server) fillHost(ctx context.Context, h *store.Host) {
	if uid := s.hostUser(ctx, h); s.jailed(ctx, uid) {
		h.Home = s.personHome(uid)
	}
}

// mayUseHost: a person's own machines. Without folders of their own
// (PEOPLE_DIR off) everyone shares the server's, as before.
func (s *Server) mayUseHost(ctx context.Context, uid int64, h *store.Host) bool {
	owner := s.hostUser(ctx, h)
	return owner == uid || (!s.jailed(ctx, uid) && !s.jailed(ctx, owner))
}

// hostsFor lists the machines a person may use.
func (s *Server) hostsFor(ctx context.Context, u *store.User) ([]*store.Host, error) {
	list, err := s.store.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := []*store.Host{}
	if u == nil || !u.MayRun() {
		return out, nil
	}
	for _, h := range list {
		if s.mayUseHost(ctx, u.ID, h) {
			s.fillHost(ctx, h)
			out = append(out, h)
		}
	}
	return out, nil
}

// manageHost loads a machine the person may change: their own, or for
// the server's machines an admin.
func (s *Server) manageHost(w http.ResponseWriter, r *http.Request) *store.Host {
	u := userFrom(r.Context())
	if !s.jailed(r.Context(), u.ID) && !s.requireAdmin(w, r) {
		return nil
	}
	if !s.requireRun(w, r) {
		return nil
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	h, err := s.host(r.Context(), id)
	if err == nil && (h == nil || !s.mayUseHost(r.Context(), u.ID, h)) {
		err = errNoHost
	}
	if err != nil {
		s.fail(w, err, "host")
		return nil
	}
	return h
}

// hostBin is the CLI to start on a host.
func hostBin(h *store.Host, provider string) string {
	if provider == "codex" {
		return firstNonEmpty(h.CodexBin, "codex")
	}
	return firstNonEmpty(h.ClaudeBin, "claude")
}

var sshTargetRe = regexp.MustCompile(`^[A-Za-z0-9._@%:\[\]-]+$`)

type hostIn struct {
	Name      *string `json:"name"`
	Target    *string `json:"target"`
	Port      *int    `json:"port"`
	ClaudeBin *string `json:"claude_bin"`
	CodexBin  *string `json:"codex_bin"`
}

func (in hostIn) apply(h *store.Host) error {
	if in.Name != nil {
		h.Name = strings.TrimSpace(*in.Name)
	}
	if in.Target != nil {
		h.Target = strings.TrimSpace(*in.Target)
	}
	if in.Port != nil {
		h.Port = *in.Port
	}
	if in.ClaudeBin != nil {
		h.ClaudeBin = strings.TrimSpace(*in.ClaudeBin)
	}
	if in.CodexBin != nil {
		h.CodexBin = strings.TrimSpace(*in.CodexBin)
	}
	if h.Target == "" {
		return badRequest("enter the ssh destination, e.g. me@box or a Host from ~/.ssh/config")
	}
	// a leading "-" would be read as an ssh option
	if strings.HasPrefix(h.Target, "-") || !sshTargetRe.MatchString(h.Target) {
		return badRequest("the ssh destination can only be user@host, host or an ~/.ssh/config Host name")
	}
	if h.Port < 0 || h.Port > 65535 {
		return badRequest("the port must be between 1 and 65535 (or empty)")
	}
	if h.Name == "" {
		h.Name = h.Target
		if i := strings.LastIndexByte(h.Name, '@'); i >= 0 {
			h.Name = h.Name[i+1:]
		}
	}
	if len([]rune(h.Name)) > 60 {
		return badRequest("keep the name under 60 characters")
	}
	return nil
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	list, err := s.hostsFor(r.Context(), userFrom(r.Context()))
	if err != nil {
		s.fail(w, err, "hosts")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	jailed := s.jailed(r.Context(), u.ID)
	if (!jailed && !s.requireAdmin(w, r)) || !s.requireRun(w, r) {
		return
	}
	var in hostIn
	if err := decode(r, &in); err != nil {
		s.fail(w, badRequest("bad body"), "host")
		return
	}
	h := &store.Host{}
	if err := in.apply(h); err != nil {
		s.fail(w, err, "host")
		return
	}
	if jailed {
		// theirs: reached with their own key, which has to exist first
		h.UserID = u.ID
		if _, err := s.personKey(r.Context(), u); err != nil {
			s.fail(w, err, "host")
			return
		}
	}
	s.fillHost(r.Context(), h)
	h.Info = s.checkHost(r.Context(), h)
	if err := s.store.CreateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishHosts()
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	h := s.manageHost(w, r)
	if h == nil {
		return
	}
	var in hostIn
	if err := decode(r, &in); err != nil {
		s.fail(w, badRequest("bad body"), "host")
		return
	}
	before := *h
	if err := in.apply(h); err != nil {
		s.fail(w, err, "host")
		return
	}
	if h.Target != before.Target || h.Port != before.Port || h.ClaudeBin != before.ClaudeBin || h.CodexBin != before.CodexBin {
		h.Info = s.checkHost(r.Context(), h)
		s.forgetHostLogins(h.ID) // another machine (or CLI) now
	}
	if err := s.store.UpdateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishHosts()
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	h := s.manageHost(w, r)
	if h == nil {
		return
	}
	id := h.ID
	s.rt.stopHost(id)
	s.forgetHostLogins(id)
	if err := s.store.DeleteHost(r.Context(), id); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishHosts()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckHost(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), id)
	if err == nil && h == nil {
		err = errNoHost
	}
	if err != nil {
		s.fail(w, err, "host")
		return
	}
	h.Info = s.checkHost(r.Context(), h)
	s.dropHostStatus(h.ID) // Accounts asks the machine again too (a CLI may be new there)
	if err := s.store.UpdateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishAccounts()
	writeJSON(w, http.StatusOK, h)
}

// handleHostKey shows the public ssh key(s) a person's machines are
// reached with: one goes into ~/.ssh/authorized_keys on the other machine.
// The owner's are this server's own; everyone else gets one of their own.
func (s *Server) handleHostKey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if s.jailed(r.Context(), u.ID) {
		if !s.requireRun(w, r) {
			return
		}
		key, err := s.personKey(r.Context(), u)
		if err != nil {
			s.fail(w, err, "key")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{"file": "your own key", "key": key}}, "user": "", "own": true})
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	keys := []map[string]string{}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		b, err := os.ReadFile(filepath.Join(s.cfg.Home, ".ssh", name+".pub"))
		if err != nil {
			continue
		}
		keys = append(keys, map[string]string{"file": "~/.ssh/" + name + ".pub", "key": strings.TrimSpace(string(b))})
	}
	user := os.Getenv("USER")
	if host, err := os.Hostname(); err == nil && user != "" {
		user += "@" + host
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "user": user})
}

// hostProbe prints what the check needs, one key=value per line.
const hostProbe = `echo "home=$HOME"
echo "os=$(uname -sm 2>/dev/null)"
echo "hostname=$(hostname 2>/dev/null || uname -n)"
probe() {
  p=$(command -v "$2" 2>/dev/null) || { echo "missing_$1=$2 is not on the PATH"; return; }
  echo "version_$1=$("$p" --version 2>&1 </dev/null | head -n 1)"
  if [ "$1" = claude ]; then
    st=$("$p" auth status 2>/dev/null </dev/null | tr -d '\n')
    case "$st" in *'"loggedIn": true'*|*'"loggedIn":true'*) ;;
      *) if [ "$(uname -s)" = Darwin ] && security find-generic-password -s "Claude Code-credentials" >/dev/null 2>&1; then
           # signed in on the Mac, but its login sits in the Keychain, which stays locked over ssh
           echo "missing_$1=signed in on this Mac, but the login is in the Keychain, which ssh cannot open: run claude setup-token there and add export CLAUDE_CODE_OAUTH_TOKEN=… to ~/.profile"
         else echo "missing_$1=installed but not signed in (run claude there once)"; fi;;
    esac
  else
    "$p" login status >/dev/null 2>&1 </dev/null || echo "missing_$1=installed but not signed in (run codex login there)"
  fi
}
probe claude "$1"
probe codex "$2"
`

// personKey is the public half of a person's own ssh key, made the first
// time (named after them and this box, so it is recognisable in
// authorized_keys).
func (s *Server) personKey(ctx context.Context, u *store.User) (string, error) {
	if err := s.ensurePerson(u.ID); err != nil {
		return "", err
	}
	name := strings.ToLower(keyNameRe.ReplaceAllString(firstNonEmpty(u.Name, "person"), "-"))
	name = strings.Trim(name, "-")
	if name == "" {
		name = "person-" + strconv.FormatInt(u.ID, 10)
	}
	host, _ := os.Hostname()
	return agent.SSHKey(s.personHome(u.ID), "pocket-ai-"+name+"@"+firstNonEmpty(host, "server"))
}

var keyNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// checkHost connects and looks at the CLIs.
func (s *Server) checkHost(ctx context.Context, h *store.Host) store.HostInfo {
	info := store.HostInfo{CheckedAt: store.Now(), Versions: map[string]string{}, Missing: map[string]string{}}
	out, errOut, err := remoteOf(h).Run(ctx, 40*time.Second, hostProbe, hostBin(h, "claude"), hostBin(h, "codex"))
	if err != nil && !strings.Contains(out, "home=") {
		info.Error = sshError(errOut, err)
		return info
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case k == "home":
			info.Home = v
		case k == "os":
			info.OS = v
		case k == "hostname":
			info.Hostname = v
		case strings.HasPrefix(k, "version_"):
			info.Versions[strings.TrimPrefix(k, "version_")] = v
		case strings.HasPrefix(k, "missing_"):
			info.Missing[strings.TrimPrefix(k, "missing_")] = v
		}
	}
	if info.Home == "" {
		info.Error = "the machine answered, but not like a shell would: " + oneLine(firstNonEmpty(errOut, out), 200)
		return info
	}
	for p := range info.Missing {
		delete(info.Versions, p)
	}
	info.OK = len(info.Versions) > len(info.Missing) || len(info.Missing) < 2
	if len(info.Missing) == 2 {
		info.OK = false
		info.Error = "neither Claude Code nor Codex can run there yet"
	}
	return info
}

// sshError turns ssh's complaint into something to act on.
func sshError(stderr string, err error) string {
	msg := oneLine(firstNonEmpty(stderr, err.Error()), 300)
	switch {
	case strings.Contains(stderr, "Permission denied"):
		return "ssh was refused (" + msg + "). Add the key shown under Servers (Key) to ~/.ssh/authorized_keys there."
	case strings.Contains(stderr, "Host key verification failed"), strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		return "the machine's host key changed (" + msg + "). If that is expected, remove the old line from known_hosts (~/.ssh on this server, or .ssh in your own folder)."
	case strings.Contains(stderr, "Could not resolve"), strings.Contains(stderr, "timed out"), strings.Contains(stderr, "No route"), strings.Contains(stderr, "Connection refused"):
		return "could not reach it: " + msg
	case strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "killed"):
		return "it did not answer in time"
	}
	return msg
}

// checkHostUse loads a host for a person who may run agents (id 0, this
// server, is nil and checked by the caller as before).
func (s *Server) checkHostUse(ctx context.Context, u *store.User, id int64) (*store.Host, error) {
	if id == 0 {
		return nil, nil
	}
	if !u.MayRun() {
		return nil, errForbidden("an admin has to allow you to run agents first")
	}
	h, err := s.host(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.mayUseHost(ctx, u.ID, h) {
		return nil, errNoHost // someone else's: not there for them
	}
	return h, nil
}

// ---------- folders on a host ----------

// remoteDirs lists the sub-folders of a folder ($1, empty = home) and says
// where it really is. Lines: path, home, "p=" places (top-level working
// folders the account owns or may write, like /work or /srv/app), then
// "d=name" / "g=name" (git).
const remoteDirs = `cd -- "$HOME" 2>/dev/null && home=$(pwd -P) || home=$HOME
p=$1
case "$p" in "") p=$HOME;; "~") p=$HOME;; "~/"*) p="$HOME/${p#\~/}";; esac
if ! cd -- "$p" 2>/dev/null; then
  # not there (yet): show the nearest folder that is, and say so
  case "$p" in /*) ;; *) echo "err=use an absolute folder path (or ~/…)"; exit 0;; esac
  echo "missing=$p"
  q=$p
  while [ "$q" != / ] && ! cd -- "$q" 2>/dev/null; do q=$(dirname -- "$q"); done
  [ "$q" = / ] && cd /
fi
echo "path=$(pwd -P)"
echo "home=$home"
n=0
for d in /* /srv/* /opt/* /mnt/* /media/* /data/* /var/www/* /Volumes/*; do
  [ "$n" -lt 12 ] || break
  [ -d "$d" ] && [ ! -L "$d" ] || continue
  case "$d" in /proc|/sys|/dev|/run|/tmp|/nix|/boot|/root|/lost+found|/home|/Users|/etc|/usr|/bin|/sbin|/lib|/lib32|/lib64|/libx32|/var|/private|/System|/Library|/Applications|/cores) continue;; esac
  if [ -O "$d" ] || { [ -w "$d" ] && [ ! -k "$d" ]; }; then echo "p=$d"; n=$((n+1)); fi
done
[ "$2" = 1 ] && set -- * .* || set -- *
for d in "$@"; do
  case "$d" in .|..) continue;; esac
  [ -d "$d" ] || continue
  if [ -e "$d/.git" ]; then echo "g=$d"; else echo "d=$d"; fi
done
`

// listRemoteDirs is listDirs for a host. Agents may start in any folder the
// ssh account can open there: that account's permissions are the boundary.
func (s *Server) listRemoteDirs(ctx context.Context, h *store.Host, p string, hidden bool) (*dirListing, error) {
	hiddenArg := "0"
	if hidden {
		hiddenArg = "1"
	}
	out, errOut, err := remoteOf(h).Run(ctx, 20*time.Second, remoteDirs, p, hiddenArg)
	if err != nil {
		return nil, badRequest(h.Name + ": " + sshError(errOut, err))
	}
	l := &dirListing{Dirs: []dirEntry{}}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "err":
			return nil, badRequest(v)
		case "path":
			l.Path = v
		case "home":
			l.Home = v
		case "missing":
			l.Missing = v
		case "p":
			l.Places = append(l.Places, v)
		case "d", "g":
			l.Dirs = append(l.Dirs, dirEntry{Name: v, Path: path.Join(l.Path, v), Git: k == "g"})
		}
	}
	if l.Path == "" {
		return nil, badRequest(h.Name + " did not answer like a shell would")
	}
	sort.Slice(l.Dirs, func(i, j int) bool { return strings.ToLower(l.Dirs[i].Name) < strings.ToLower(l.Dirs[j].Name) })
	l.Roots = []string{"/"}
	if parent := path.Dir(l.Path); parent != l.Path {
		l.Parent = parent
	}
	return l, nil
}

// checkRemoteDir makes sure a folder exists on the host; empty = the home folder.
func (s *Server) checkRemoteDir(ctx context.Context, h *store.Host, p string) (string, error) {
	l, err := s.listRemoteDirs(ctx, h, p, false)
	if err != nil {
		return "", err
	}
	return l.Path, nil
}
