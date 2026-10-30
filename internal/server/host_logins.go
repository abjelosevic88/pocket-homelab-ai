package server

// Sign-ins on other machines: a thread on a Server (hosts.go) runs on that
// machine's own Claude Code / Codex login (~/.claude, ~/.codex there). These
// show it next to the logins here and sign it in or out from the browser:
// the same `claude auth login` / `codex login --device-auth` as accounts.go,
// started over ssh.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) hostLoginRoutes(r chi.Router) {
	r.Get("/hosts/logins", s.handleHostLogins)
	r.Post("/hosts/{id}/login/{provider}", s.handleHostLoginStart)
	r.Get("/hosts/{id}/login/{provider}", s.handleHostLoginState)
	r.Post("/hosts/{id}/login/{provider}/code", s.handleHostLoginCode)
	r.Delete("/hosts/{id}/login/{provider}", s.handleHostLoginCancel)
	r.Post("/hosts/{id}/logout/{provider}", s.handleHostLogout)
}

// hostLoginView is one login of one CLI on a machine: the default
// (~/.claude, ~/.codex) or another login folder there (~/.claude-max, made
// for a script that sets CLAUDE_CONFIG_DIR / CODEX_HOME).
type hostLoginView struct {
	HostID   int64  `json:"host_id"`
	Host     string `json:"host"`
	Target   string `json:"target"`
	Provider string `json:"provider"`
	Home     string `json:"home"` // its folder over there
	// Dir is the login folder a thread names to run on it (Thread.HostHome):
	// "" for the default one.
	Dir   string `json:"dir"`
	Label string `json:"label,omitempty"` // "claude-max"; "" for the default
	// Missing: the CLI cannot run there (not installed), so there is nothing to sign in.
	Missing bool `json:"missing,omitempty"`
	acctStatus
}

func hostLoginKey(id int64, provider, dir string) string {
	k := "h" + strconv.FormatInt(id, 10) + "/" + provider
	if dir != "" {
		k += "|" + dir
	}
	return k
}

// hostStatusScript prints the home folder, then for each CLI ($1 claude,
// $2 codex) whether it is there and its login folders; for Claude with
// `claude auth status` of each.
const hostStatusScript = agent.RemoteHomesSh + `echo "home=$HOME"
if p=$(command -v "$1" 2>/dev/null); then
  first=1
  homes claude | while IFS= read -r d; do
    echo "claude_home=$d"
    printf 'claude_status='
    # the default as the CLI finds it (CLAUDE_CONFIG_DIR set to ~/.claude
    # would read ~/.claude/.claude.json instead of ~/.claude.json)
    if [ $first = 1 ]; then "$p" auth status; else CLAUDE_CONFIG_DIR="$d" "$p" auth status; fi 2>/dev/null </dev/null | tr -d '\n'; echo
    first=0
  done
else echo "claude_missing=1"; fi
if command -v "$2" >/dev/null 2>&1; then
  homes codex | while IFS= read -r d; do echo "codex_home=$d"; done
else echo "codex_missing=1"; fi
`

type hostLoginCache struct {
	at    time.Time
	views []hostLoginView
}

var (
	hostLoginsMu    sync.Mutex
	hostLoginsCache = map[int64]hostLoginCache{}
)

// hostLogins asks a machine about all of its logins (cached; after 10
// minutes the cached answer is still given while a fresh one is asked).
func (s *Server) hostLogins(ctx context.Context, h *store.Host, refresh bool) []hostLoginView {
	hostLoginsMu.Lock()
	c, ok := hostLoginsCache[h.ID]
	hostLoginsMu.Unlock()
	if ok && !refresh {
		if time.Since(c.at) >= 10*time.Minute {
			// the picker shows what was known; asking takes seconds (ssh and
			// the CLIs there), so it is done in the background for next time
			s.refreshHostLogins(h)
		}
		return append([]hostLoginView(nil), c.views...)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	now := time.Now()
	stamp := func(v *hostLoginView) {
		v.at, v.CheckedAt = now, store.FormatTime(now)
		v.Missing = strings.HasPrefix(v.Error, notInstalled)
	}
	base := func(provider, home string) hostLoginView {
		return hostLoginView{HostID: h.ID, Host: h.Name, Target: h.Target, Provider: provider, Home: home}
	}
	res, errOut, err := remoteOf(h).Run(ctx, 45*time.Second, hostStatusScript, hostBin(h, "claude"), hostBin(h, "codex"))
	if err != nil && !strings.Contains(res, "home=") {
		msg := sshError(errOut, err)
		out := []hostLoginView{base("claude", "~/.claude"), base("codex", "~/.codex")}
		for i := range out {
			out[i].acctStatus = acctStatus{Error: msg}
			stamp(&out[i])
		}
		return out // not cached: the machine may be back in a minute
	}
	var claude, codex []hostLoginView
	claudeMissing, codexMissing := false, false
	for _, line := range strings.Split(res, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "claude_home":
			claude = append(claude, base("claude", v))
		case "claude_status":
			if n := len(claude); n > 0 {
				claude[n-1].acctStatus = parseClaudeStatus([]byte(v), nil)
			}
		case "codex_home":
			codex = append(codex, base("codex", v))
		case "claude_missing":
			claudeMissing = true
		case "codex_missing":
			codexMissing = true
		}
	}
	if claudeMissing || len(claude) == 0 {
		v := base("claude", "~/.claude")
		if claudeMissing {
			v.acctStatus = acctStatus{Error: notInstalled + hostBin(h, "claude") + " is not on the PATH there"}
		}
		claude = []hostLoginView{v}
	}
	if codexMissing || len(codex) == 0 {
		v := base("codex", "~/.codex")
		if codexMissing {
			v.acctStatus = acctStatus{Error: notInstalled + hostBin(h, "codex") + " is not on the PATH there"}
		}
		codex = []hostLoginView{v}
	} else {
		// its app-server says who (one more ssh connection per folder)
		var wg sync.WaitGroup
		for i := range codex {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				dir := ""
				if i > 0 {
					dir = codex[i].Home
				}
				codex[i].acctStatus = s.remoteCodexStatus(ctx, h, dir)
			}(i)
		}
		wg.Wait()
	}
	out := append(claude, codex...)
	for i := range out {
		// the first of each CLI is the default login: threads name it ""
		if i != 0 && i != len(claude) {
			out[i].Dir, out[i].Label = out[i].Home, agent.HomeLabel(out[i].Home)
		}
		stamp(&out[i])
	}
	hostLoginsMu.Lock()
	hostLoginsCache[h.ID] = hostLoginCache{at: now, views: out}
	hostLoginsMu.Unlock()
	return append([]hostLoginView(nil), out...)
}

var hostLoginsRefreshing sync.Map // host id → a background ask is running

func (s *Server) refreshHostLogins(h *store.Host) {
	if _, busy := hostLoginsRefreshing.LoadOrStore(h.ID, true); busy {
		return
	}
	go func() {
		defer hostLoginsRefreshing.Delete(h.ID)
		s.hostLogins(s.rt.ctx, h, true)
	}()
}

const notInstalled = "Not installed: "

func (s *Server) remoteCodexStatus(ctx context.Context, h *store.Host, dir string) acctStatus {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	email, plan, signedIn, err := agent.CodexAccount(ctx, hostBin(h, "codex"), agent.HomeEnv("codex", dir), remoteOf(h))
	if err != nil {
		return acctStatus{Error: oneLine(err.Error(), 300)}
	}
	st := acctStatus{SignedIn: signedIn, Email: email, Plan: plan}
	if signedIn {
		st.ident = loginIdent("codex", email, plan)
	}
	return st
}

// forgetHostLogins drops what is known about a machine's logins and any
// sign-in there (it was changed or removed).
func (s *Server) forgetHostLogins(id int64) {
	hostLoginsMu.Lock()
	views := hostLoginsCache[id].views
	hostLoginsMu.Unlock()
	for _, p := range []string{"claude", "codex"} {
		s.cancelLogin(hostLoginKey(id, p, ""))
	}
	for _, v := range views {
		s.cancelLogin(hostLoginKey(id, v.Provider, v.Dir))
	}
	s.dropHostStatus(id)
}

func (s *Server) dropHostStatus(id int64) {
	hostLoginsMu.Lock()
	defer hostLoginsMu.Unlock()
	delete(hostLoginsCache, id)
}

// handleHostLogins lists the logins of the person's machines.
func (s *Server) handleHostLogins(w http.ResponseWriter, r *http.Request) {
	out := []hostLoginView{}
	list, err := s.hostsFor(r.Context(), userFrom(r.Context()))
	if err != nil {
		s.fail(w, err, "hosts")
		return
	}
	if only := r.URL.Query().Get("host"); only != "" { // one machine (the Add dialog asks fresh)
		var keep []*store.Host
		for _, h := range list {
			if strconv.FormatInt(h.ID, 10) == only {
				keep = append(keep, h)
			}
		}
		list = keep
	}
	res := make([][]hostLoginView, len(list))
	var wg sync.WaitGroup
	for i, h := range list {
		wg.Add(1)
		go func(i int, h *store.Host) {
			defer wg.Done()
			res[i] = s.hostLogins(r.Context(), h, queryBool(r, "refresh"))
		}(i, h)
	}
	wg.Wait()
	for _, v := range res {
		out = append(out, v...)
	}
	writeJSON(w, http.StatusOK, out)
}

// loginHost is the machine, CLI and login folder (?dir=, "" = the default)
// of a request, for someone who may run there.
func (s *Server) loginHost(w http.ResponseWriter, r *http.Request) (*store.Host, string, string) {
	provider := chi.URLParam(r, "provider")
	if provider != "claude" && provider != "codex" {
		writeErr(w, http.StatusBadRequest, "provider must be claude or codex")
		return nil, "", ""
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id <= 0 {
		writeErr(w, http.StatusNotFound, "that server is not set up in Pocket AI")
		return nil, "", ""
	}
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), id)
	if err != nil {
		s.fail(w, err, "host")
		return nil, "", ""
	}
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir != "" && !agent.ValidHostHome(provider, h.Info.Home, dir) {
		writeErr(w, http.StatusBadRequest, dir+" is not a "+provider+" login folder on "+h.Name)
		return nil, "", ""
	}
	return h, provider, dir
}

func (s *Server) handleHostLoginStart(w http.ResponseWriter, r *http.Request) {
	h, provider, dir := s.loginHost(w, r)
	if h == nil {
		return
	}
	key := hostLoginKey(h.ID, provider, dir)
	s.cancelLogin(key)
	ctx, cancel := context.WithTimeout(s.rt.ctx, loginExpiry)
	bin, args := loginArgs(provider, hostBin(h, "claude"), hostBin(h, "codex"))
	cmd, err := agent.Command(remoteOf(h), "", append(append([]string{}, loginEnv...), agent.HomeEnv(provider, dir)...), bin, args...)
	if err != nil {
		cancel()
		s.fail(w, err, "login")
		return
	}
	l, err := s.beginLogin(ctx, cancel, key, provider, cmd, func(ok bool) {
		s.dropHostStatus(h.ID) // the next list asks the machine again
		go s.afterHostLogin(h.ID)
	})
	if err != nil {
		s.fail(w, err, "login")
		return
	}
	s.log.Info("sign-in started on a server", "host", h.Name, "provider", provider, "login", firstNonEmpty(dir, "default"))
	writeJSON(w, http.StatusOK, l.waitStarted())
}

// afterHostLogin looks at the machine again: its logins and the Servers
// check, which says "not signed in" until now.
func (s *Server) afterHostLogin(id int64) {
	ctx := s.rt.ctx
	h, err := s.host(ctx, id)
	if err != nil || h == nil {
		return
	}
	s.hostLogins(ctx, h, true)
	h.Info = s.checkHost(ctx, h)
	if s.store.UpdateHost(ctx, h) == nil {
		s.rt.publishHosts()
	}
	s.rt.publishAccounts()
}

func (s *Server) handleHostLoginState(w http.ResponseWriter, r *http.Request) {
	if h, p, dir := s.loginHost(w, r); h != nil {
		writeLoginState(w, r, s.login(hostLoginKey(h.ID, p, dir)))
	}
}

func (s *Server) handleHostLoginCode(w http.ResponseWriter, r *http.Request) {
	if h, p, dir := s.loginHost(w, r); h != nil {
		sendLoginCode(w, r, s.login(hostLoginKey(h.ID, p, dir)))
	}
}

func (s *Server) handleHostLoginCancel(w http.ResponseWriter, r *http.Request) {
	if h, p, dir := s.loginHost(w, r); h != nil {
		s.cancelLogin(hostLoginKey(h.ID, p, dir))
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleHostLogout signs a machine's own login out.
func (s *Server) handleHostLogout(w http.ResponseWriter, r *http.Request) {
	h, provider, dir := s.loginHost(w, r)
	if h == nil {
		return
	}
	script := `"$1" auth logout`
	if provider == "codex" {
		script = `"$1" logout`
	}
	out, errOut, err := remoteOf(h).Run(r.Context(), 30*time.Second, inLogin(provider, dir, script+" </dev/null 2>&1"), hostBin(h, provider))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "sign out failed: "+oneLine(stripANSI(firstNonEmpty(out, sshError(errOut, err))), 300))
		return
	}
	s.rt.stopHost(h.ID)
	s.afterHostLogin(h.ID)
	for _, v := range s.hostLogins(r.Context(), h, false) {
		if v.Provider == provider && v.Dir == dir {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
