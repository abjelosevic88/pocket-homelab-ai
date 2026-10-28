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

// hostLoginView is a machine's own login of one CLI.
type hostLoginView struct {
	HostID   int64  `json:"host_id"`
	Host     string `json:"host"`
	Target   string `json:"target"`
	Provider string `json:"provider"`
	Home     string `json:"home"` // its folder over there
	// Missing: the CLI cannot run there (not installed), so there is nothing to sign in.
	Missing bool `json:"missing,omitempty"`
	acctStatus
}

func hostLoginKey(id int64, provider string) string {
	return "h" + strconv.FormatInt(id, 10) + "/" + provider
}

// hostStatusScript prints the home folder, then for each CLI ($1 claude,
// $2 codex) whether it is there and, for Claude, `claude auth status`.
const hostStatusScript = `echo "home=$HOME"
if p=$(command -v "$1" 2>/dev/null); then
  printf 'claude_status='; "$p" auth status 2>/dev/null </dev/null | tr -d '\n'; echo
else echo "claude_missing=1"; fi
command -v "$2" >/dev/null 2>&1 || echo "codex_missing=1"
`

// hostLogins asks a machine about both of its logins (cached 10 minutes).
func (s *Server) hostLogins(ctx context.Context, h *store.Host, refresh bool) []hostLoginView {
	out := []hostLoginView{
		{HostID: h.ID, Host: h.Name, Target: h.Target, Provider: "claude", Home: "~/.claude"},
		{HostID: h.ID, Host: h.Name, Target: h.Target, Provider: "codex", Home: "~/.codex"},
	}
	s.accts.mu.Lock()
	fresh := !refresh
	for i := range out {
		st, ok := s.accts.status[hostLoginKey(h.ID, out[i].Provider)]
		if !ok || time.Since(st.at) >= 10*time.Minute {
			fresh = false
		}
		out[i].acctStatus = st
		if st.home != "" {
			out[i].Home = st.home
		}
	}
	s.accts.mu.Unlock()
	if fresh {
		for i := range out {
			out[i].Missing = strings.HasPrefix(out[i].Error, notInstalled)
		}
		return out
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
	defer cancel()
	now := time.Now()
	res, errOut, err := remoteOf(h).Run(ctx, 30*time.Second, hostStatusScript, hostBin(h, "claude"), hostBin(h, "codex"))
	if err != nil && !strings.Contains(res, "home=") {
		msg := sshError(errOut, err)
		for i := range out {
			out[i].acctStatus = acctStatus{Error: msg, at: now, CheckedAt: store.FormatTime(now)}
		}
		return out // not cached: the machine may be back in a minute
	}
	kv := map[string]string{}
	for _, line := range strings.Split(res, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	if home := kv["home"]; home != "" {
		out[0].Home, out[1].Home = home+"/.claude", home+"/.codex"
	}
	// claude
	if kv["claude_missing"] != "" {
		out[0].acctStatus = acctStatus{Error: notInstalled + hostBin(h, "claude") + " is not on the PATH there"}
	} else {
		out[0].acctStatus = parseClaudeStatus([]byte(kv["claude_status"]), nil)
	}
	// codex: its app-server says who (one more ssh connection)
	if kv["codex_missing"] != "" {
		out[1].acctStatus = acctStatus{Error: notInstalled + hostBin(h, "codex") + " is not on the PATH there"}
	} else {
		out[1].acctStatus = s.remoteCodexStatus(ctx, h)
	}
	s.accts.mu.Lock()
	for i := range out {
		out[i].at = now
		out[i].CheckedAt = store.FormatTime(now)
		out[i].Missing = strings.HasPrefix(out[i].Error, notInstalled)
		out[i].home = out[i].Home
		s.accts.status[hostLoginKey(h.ID, out[i].Provider)] = out[i].acctStatus
	}
	s.accts.mu.Unlock()
	return out
}

const notInstalled = "Not installed: "

func (s *Server) remoteCodexStatus(ctx context.Context, h *store.Host) acctStatus {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	email, plan, signedIn, err := agent.CodexAccount(ctx, hostBin(h, "codex"), nil, remoteOf(h))
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
	for _, p := range []string{"claude", "codex"} {
		s.cancelLogin(hostLoginKey(id, p))
	}
	s.dropHostStatus(id)
}

func (s *Server) dropHostStatus(id int64) {
	s.accts.mu.Lock()
	defer s.accts.mu.Unlock()
	for _, p := range []string{"claude", "codex"} {
		delete(s.accts.status, hostLoginKey(id, p))
	}
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

// loginHost is the machine and CLI of a request, for someone who may run there.
func (s *Server) loginHost(w http.ResponseWriter, r *http.Request) (*store.Host, string) {
	provider := chi.URLParam(r, "provider")
	if provider != "claude" && provider != "codex" {
		writeErr(w, http.StatusBadRequest, "provider must be claude or codex")
		return nil, ""
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id <= 0 {
		writeErr(w, http.StatusNotFound, "that server is not set up in Pocket AI")
		return nil, ""
	}
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), id)
	if err != nil {
		s.fail(w, err, "host")
		return nil, ""
	}
	return h, provider
}

func (s *Server) handleHostLoginStart(w http.ResponseWriter, r *http.Request) {
	h, provider := s.loginHost(w, r)
	if h == nil {
		return
	}
	key := hostLoginKey(h.ID, provider)
	s.cancelLogin(key)
	ctx, cancel := context.WithTimeout(s.rt.ctx, loginExpiry)
	bin, args := loginArgs(provider, hostBin(h, "claude"), hostBin(h, "codex"))
	cmd, err := agent.Command(remoteOf(h), "", loginEnv, bin, args...)
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
	s.log.Info("sign-in started on a server", "host", h.Name, "provider", provider)
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
	if h, p := s.loginHost(w, r); h != nil {
		writeLoginState(w, r, s.login(hostLoginKey(h.ID, p)))
	}
}

func (s *Server) handleHostLoginCode(w http.ResponseWriter, r *http.Request) {
	if h, p := s.loginHost(w, r); h != nil {
		sendLoginCode(w, r, s.login(hostLoginKey(h.ID, p)))
	}
}

func (s *Server) handleHostLoginCancel(w http.ResponseWriter, r *http.Request) {
	if h, p := s.loginHost(w, r); h != nil {
		s.cancelLogin(hostLoginKey(h.ID, p))
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleHostLogout signs a machine's own login out.
func (s *Server) handleHostLogout(w http.ResponseWriter, r *http.Request) {
	h, provider := s.loginHost(w, r)
	if h == nil {
		return
	}
	script := `"$1" auth logout`
	if provider == "codex" {
		script = `"$1" logout`
	}
	out, errOut, err := remoteOf(h).Run(r.Context(), 30*time.Second, script+" </dev/null 2>&1", hostBin(h, provider))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "sign out failed: "+oneLine(stripANSI(firstNonEmpty(out, sshError(errOut, err))), 300))
		return
	}
	s.rt.stopHost(h.ID)
	s.afterHostLogin(h.ID)
	for _, v := range s.hostLogins(r.Context(), h, false) {
		if v.Provider == provider {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
