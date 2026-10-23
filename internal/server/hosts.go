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
// there over ssh with this server's own keys, the conversation is driven and
// stored here. The remote needs claude and/or codex installed and signed in;
// Pocket AI only needs to be able to `ssh <target>` without a password.

func (s *Server) hostRoutes(r chi.Router) {
	r.Get("/hosts", s.handleListHosts)
	r.Post("/hosts", s.handleCreateHost)
	r.Patch("/hosts/{id}", s.handleUpdateHost)
	r.Delete("/hosts/{id}", s.handleDeleteHost)
	r.Post("/hosts/{id}/check", s.handleCheckHost)
	r.Get("/hosts/key", s.handleHostKey)
}

func remoteOf(h *store.Host) *agent.Remote {
	return &agent.Remote{Target: h.Target, Port: h.Port}
}

// host loads a host for running or browsing; id 0 is this server (nil).
func (s *Server) host(ctx context.Context, id int64) (*store.Host, error) {
	if id == 0 {
		return nil, nil
	}
	h, err := s.store.GetHost(ctx, id)
	if err != nil {
		return nil, badRequest("that server is not set up in Pocket AI (any more)")
	}
	return h, nil
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
	u := userFrom(r.Context())
	if !u.MayRun() {
		writeJSON(w, http.StatusOK, []*store.Host{})
		return
	}
	list, err := s.store.ListHosts(r.Context())
	if err != nil {
		s.fail(w, err, "hosts")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
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
	h.Info = s.checkHost(r.Context(), h)
	if err := s.store.CreateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishHosts()
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	h, err := s.store.GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, err, "host")
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
	}
	if err := s.store.UpdateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	s.rt.publishHosts()
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	s.rt.stopHost(id)
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
	h, err := s.store.GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, err, "host")
		return
	}
	h.Info = s.checkHost(r.Context(), h)
	if err := s.store.UpdateHost(r.Context(), h); err != nil {
		s.fail(w, err, "host")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleHostKey shows this server's public ssh keys: one goes into
// ~/.ssh/authorized_keys on the other machine.
func (s *Server) handleHostKey(w http.ResponseWriter, r *http.Request) {
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
    case "$st" in *'"loggedIn": true'*|*'"loggedIn":true'*) ;; *) echo "missing_$1=installed but not signed in (run claude there once)";; esac
  else
    "$p" login status >/dev/null 2>&1 </dev/null || echo "missing_$1=installed but not signed in (run codex login there)"
  fi
}
probe claude "$1"
probe codex "$2"
`

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
		return "ssh was refused (" + msg + "). Add this server's public key to ~/.ssh/authorized_keys there."
	case strings.Contains(stderr, "Host key verification failed"), strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		return "the machine's host key changed (" + msg + "). If that is expected, remove the old line from ~/.ssh/known_hosts on this server."
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
	return s.host(ctx, id)
}

// ---------- folders on a host ----------

// remoteDirs lists the sub-folders of a folder ($1, empty = home) and says
// where it really is. Lines: path, home, then "d name" / "g name" (git).
const remoteDirs = `cd -- "$HOME" 2>/dev/null && home=$(pwd -P) || home=$HOME
p=$1
case "$p" in "") p=$HOME;; "~") p=$HOME;; "~/"*) p="$HOME/${p#\~/}";; esac
cd -- "$p" 2>/dev/null || { echo "err=the folder $p does not exist there"; exit 0; }
echo "path=$(pwd -P)"
echo "home=$home"
[ "$2" = 1 ] && set -- * .* || set -- *
for d in "$@"; do
  case "$d" in .|..) continue;; esac
  [ -d "$d" ] || continue
  if [ -e "$d/.git" ]; then echo "g=$d"; else echo "d=$d"; fi
done
`

// listRemoteDirs is listDirs for a host. Agents may start anywhere in the
// remote account's home (like ROOTS=~ here).
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
		case "d", "g":
			l.Dirs = append(l.Dirs, dirEntry{Name: v, Path: path.Join(l.Path, v), Git: k == "g"})
		}
	}
	if l.Path == "" {
		return nil, badRequest(h.Name + " did not answer like a shell would")
	}
	if !within(l.Path, l.Home) {
		return nil, badRequest(l.Path + " is outside the home folder on " + h.Name + " (" + l.Home + ")")
	}
	sort.Slice(l.Dirs, func(i, j int) bool { return strings.ToLower(l.Dirs[i].Name) < strings.ToLower(l.Dirs[j].Name) })
	l.Roots = []string{l.Home}
	if parent := path.Dir(l.Path); parent != l.Path && within(parent, l.Home) {
		l.Parent = parent
	}
	return l, nil
}

// checkRemoteDir makes sure a folder exists on the host, inside its home;
// empty = the home folder.
func (s *Server) checkRemoteDir(ctx context.Context, h *store.Host, p string) (string, error) {
	l, err := s.listRemoteDirs(ctx, h, p, false)
	if err != nil {
		return "", err
	}
	return l.Path, nil
}
