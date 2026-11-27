package server

// ACP agents (Gemini CLI, OpenCode, Qwen Code, …): their keys, install and
// Google sign-in, for Settings → Agents. The driver is internal/agent/acp.go.

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func (s *Server) acpRoutes(r chi.Router) {
	r.Get("/agents/acp", s.handleACPList)
	r.Put("/agents/acp/{id}/env", s.handleACPEnv)
	r.Post("/agents/acp/{id}/install", s.handleACPInstall)
	r.Post("/agents/acp/{id}/login", s.handleACPLogin)
	r.Get("/agents/acp/{id}/login", s.handleACPLoginState)
	r.Post("/agents/acp/{id}/login/code", s.handleACPLoginCode)
	r.Delete("/agents/acp/{id}/login", s.handleACPLoginCancel)
	r.Post("/agents/acp/{id}/logout", s.handleACPLogout)
}

// acpEnv is what an ACP agent is given in its environment (its keys).
func (s *Server) acpEnv(ctx context.Context, id string) []string {
	m := s.store.ACPEnv(ctx, id)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		if v := m[k]; v != "" && validEnvName(k) {
			out = append(out, k+"="+v)
		}
	}
	return out
}

var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// validEnvName: a key an agent may be given. Not anything that changes how
// programs run (PATH, HOME, LD_*, NODE_OPTIONS, …): API keys, endpoints, models.
func validEnvName(k string) bool {
	if !envNameRe.MatchString(k) {
		return false
	}
	for _, suffix := range []string{"_API_KEY", "_BASE_URL", "_MODEL", "_API_BASE", "_ENDPOINT", "_PROJECT", "_LOCATION", "_REGION"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// forgetProviders makes the next provider list ask the CLIs again.
func (s *Server) forgetProviders() {
	s.provMu.Lock()
	s.provList = nil
	s.provMu.Unlock()
}

// probeACP probes every ACP agent (into out, from index from).
func (s *Server) probeACP(ctx context.Context, out []agent.Provider, from int, done chan<- struct{}) {
	for i, ag := range agent.ACPAgents {
		go func(i int, ag agent.ACPAgent) {
			out[from+i] = agent.ProbeACP(ctx, ag, "", filepath.Join(s.cfg.DataDir, "acp-probe", ag.ID), s.acpEnv(ctx, ag.ID))
			done <- struct{}{}
		}(i, ag)
	}
}

// acpView is an ACP agent as Settings shows it.
type acpView struct {
	agent.ACPAgent
	Installed      bool              `json:"installed"`
	Version        string            `json:"version,omitempty"`
	Available      bool              `json:"available"`
	Error          string            `json:"error,omitempty"`
	Account        string            `json:"account,omitempty"`
	Models         int               `json:"models"`
	GoogleSignedIn bool              `json:"google_signed_in"`
	CanSignOut     bool              `json:"can_sign_out"`
	SelfUpdates    bool              `json:"self_updates"`
	Set            map[string]string `json:"set"` // key → shown value (secrets masked)
}

func mask(v string) string {
	if len(v) <= 8 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}

func (s *Server) handleACPList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	provs := s.providers(ctx, queryBool(r, "refresh"))
	out := []acpView{}
	for _, ag := range agent.ACPAgents {
		v := acpView{ACPAgent: ag, Set: map[string]string{}}
		if ag.Keys == nil {
			v.Keys = []agent.ACPKey{}
		}
		_, err := exec.LookPath(ag.Command())
		v.Installed = err == nil
		for _, p := range provs {
			if p.ID == ag.ID {
				v.Version, v.Available, v.Error, v.Account, v.Models = p.Version, p.Available, p.Error, p.Account, len(p.Models)
			}
		}
		v.GoogleSignedIn = ag.GoogleSignedIn() || ag.LoginArgs != nil && v.Available
		v.CanSignOut = ag.GoogleLogin && ag.LoginArgs == nil // Antigravity: /logout in agy itself
		v.SelfUpdates = ag.InstallScript != ""
		secret := map[string]bool{}
		for _, k := range ag.Keys {
			secret[k.Name] = k.Secret
		}
		for k, val := range s.store.ACPEnv(ctx, ag.ID) {
			if val == "" {
				continue
			}
			if sec, listed := secret[k]; sec || !listed && strings.HasSuffix(k, "_API_KEY") {
				val = mask(val)
			}
			v.Set[k] = val
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) acpAgent(w http.ResponseWriter, r *http.Request) (agent.ACPAgent, bool) {
	if !s.requireAdmin(w, r) || !s.requireRun(w, r) {
		return agent.ACPAgent{}, false
	}
	if u := userFrom(r.Context()); !s.mayUseServerLogin(r.Context(), u) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return agent.ACPAgent{}, false
	}
	ag, ok := agent.ACPAgentByID(chi.URLParam(r, "id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such agent")
	}
	return ag, ok
}

// handleACPEnv sets an agent's keys: {"env": {"NAME": "value"}}; "" removes one.
func (s *Server) handleACPEnv(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.acpAgent(w, r)
	if !ok {
		return
	}
	var in struct {
		Env map[string]string `json:"env"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := r.Context()
	cur := s.store.ACPEnv(ctx, ag.ID)
	for k, v := range in.Env {
		k = strings.TrimSpace(k)
		if !validEnvName(k) {
			writeErr(w, http.StatusBadRequest, k+" is not a key Pocket AI hands to agents (names end in _API_KEY, _BASE_URL, _MODEL, …)")
			return
		}
		v = strings.TrimSpace(v)
		if strings.ContainsAny(v, "\r\n\x00") || len(v) > 4096 {
			writeErr(w, http.StatusBadRequest, "that value cannot be used")
			return
		}
		if v == "" {
			delete(cur, k)
		} else {
			cur[k] = v
		}
	}
	if err := s.store.SetACPEnv(ctx, ag.ID, cur); err != nil {
		s.fail(w, err, "acp env")
		return
	}
	s.forgetProviders()
	s.rt.stopProvider(ag.ID) // idle conversations start again with the new keys
	s.log.Info("acp keys changed", "agent", ag.ID, "keys", len(cur))
	s.handleACPList(w, r)
}

var acpInstalling sync.Map

// handleACPInstall installs (or updates) an agent from npm.
func (s *Server) handleACPInstall(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.acpAgent(w, r)
	if !ok {
		return
	}
	if _, busy := acpInstalling.LoadOrStore(ag.ID, true); busy {
		writeErr(w, http.StatusConflict, "it is being installed already")
		return
	}
	defer acpInstalling.Delete(ag.ID)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 8*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if ag.InstallScript != "" {
		// the vendor's installer (it checks the download's checksum itself)
		dir, err := os.MkdirTemp("", "pocket-acp-install-")
		if err != nil {
			s.fail(w, err, "install")
			return
		}
		defer os.RemoveAll(dir)
		cmd = exec.CommandContext(ctx, "sh", "-c", `curl -fsSL "$1" -o "$2/install.sh" && bash "$2/install.sh"`, "sh", ag.InstallScript, dir)
	} else {
		npm, err := exec.LookPath("npm")
		if err != nil {
			writeErr(w, http.StatusBadGateway, "npm (Node.js) is not installed on the server, so "+ag.Name+" cannot be installed from here")
			return
		}
		cmd = exec.CommandContext(ctx, npm, "install", "-g", ag.Package+"@latest")
	}
	cmd.Env = append(os.Environ(), "HOME="+s.cfg.Home)
	cmd.Dir = s.cfg.Home
	b, err := cmd.CombinedOutput()
	s.forgetProviders()
	res := map[string]any{"ok": err == nil, "output": lastLines(string(b), 4)}
	if err != nil {
		res["error"] = "the install did not finish: " + oneLine(firstNonEmpty(lastLines(string(b), 3), err.Error()), 300)
	} else if _, lerr := exec.LookPath(ag.Command()); lerr != nil {
		res["ok"], res["error"] = false, "installed, but "+ag.Bin+" is not on Pocket AI's PATH ("+os.Getenv("PATH")+")"
	}
	s.log.Info("acp install", "agent", ag.ID, "ok", err == nil)
	s.rt.publishAccounts()
	writeJSON(w, http.StatusOK, res)
}

// ---------- Gemini CLI: signing in with Google ----------
//
// Gemini CLI signs in only in a terminal: with NO_BROWSER it prints a URL,
// asks for the code Google shows, saves ~/.gemini/oauth_creds.json and goes
// on into its chat. It runs under script(1) for a terminal; once the file
// is there the sign-in is done and the CLI is stopped.

func acpLoginKey(id string) string { return "acp-" + id }

func (s *Server) handleACPLogin(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.acpAgent(w, r)
	if !ok {
		return
	}
	if !ag.GoogleLogin {
		writeErr(w, http.StatusBadRequest, ag.Name+" has no Google sign-in")
		return
	}
	script, err := exec.LookPath("script")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "script (util-linux) is not installed on the server, so Pocket AI cannot run the sign-in")
		return
	}
	bin, err := exec.LookPath(ag.Command())
	if err != nil {
		writeErr(w, http.StatusBadRequest, ag.Name+" is not installed")
		return
	}
	if ag.LoginArgs == nil {
		if err := agent.SetGeminiAuth(ag.ACPHome(), "oauth-personal"); err != nil {
			s.fail(w, err, "gemini settings")
			return
		}
	}
	creds := filepath.Join(ag.ACPHome(), "oauth_creds.json")
	var before time.Time
	if fi, err := os.Stat(creds); err == nil {
		before = fi.ModTime()
	}
	key := acpLoginKey(ag.ID)
	s.cancelLogin(key)
	ctx, cancel := context.WithTimeout(s.rt.ctx, loginExpiry)
	dir, _ := os.MkdirTemp("", "pocket-gemini-login-")
	line := shellQuote(bin)
	for _, a := range ag.LoginArgs { // Antigravity: agy acp --login signs in and exits
		line += " " + shellQuote(a)
	}
	cmd := exec.CommandContext(ctx, script, "-qfec", line, "/dev/null")
	cmd.Dir = firstNonEmpty(dir, s.cfg.Home)
	cmd.Env = append(os.Environ(), "NO_BROWSER=true", "TERM=dumb", "NO_COLOR=1", "BROWSER=true")
	l, err := s.beginLogin(ctx, cancel, key, ag.ID, cmd, func(ok bool) {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		if ok {
			s.forgetProviders()
			s.rt.stopProvider(ag.ID)
			s.log.Info("signed in with google", "agent", ag.ID)
		}
		s.rt.publishAccounts()
	})
	if err != nil {
		s.fail(w, err, "login")
		return
	}
	if ag.LoginArgs == nil { // Gemini CLI goes on into its chat: done once the file is there
		l.succeedWhen = func() bool {
			fi, err := os.Stat(creds)
			return err == nil && fi.Size() > 0 && fi.ModTime().After(before)
		}
		go l.watchSuccess(ctx)
	}
	writeJSON(w, http.StatusOK, l.waitStarted())
}

func (s *Server) handleACPLoginState(w http.ResponseWriter, r *http.Request) {
	if ag, ok := s.acpAgent(w, r); ok {
		writeLoginState(w, r, s.login(acpLoginKey(ag.ID)))
	}
}

func (s *Server) handleACPLoginCode(w http.ResponseWriter, r *http.Request) {
	if ag, ok := s.acpAgent(w, r); ok {
		sendLoginCode(w, r, s.login(acpLoginKey(ag.ID)))
	}
}

func (s *Server) handleACPLoginCancel(w http.ResponseWriter, r *http.Request) {
	if ag, ok := s.acpAgent(w, r); ok {
		s.cancelLogin(acpLoginKey(ag.ID))
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleACPLogout forgets the Google sign-in.
func (s *Server) handleACPLogout(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.acpAgent(w, r)
	if !ok {
		return
	}
	if ag.LoginArgs != nil {
		writeErr(w, http.StatusBadRequest, "Sign "+ag.Name+" out with /logout in "+ag.Bin+" itself")
		return
	}
	if ag.GoogleLogin {
		for _, f := range []string{"oauth_creds.json", "google_accounts.json"} {
			if err := os.Remove(filepath.Join(ag.ACPHome(), f)); err != nil && !os.IsNotExist(err) {
				s.fail(w, err, "sign out")
				return
			}
		}
	}
	s.forgetProviders()
	s.rt.stopProvider(ag.ID)
	s.rt.publishAccounts()
	s.handleACPList(w, r)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
