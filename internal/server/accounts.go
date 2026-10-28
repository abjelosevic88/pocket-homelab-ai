package server

// Accounts: more than one Claude Code / Codex login on the server. Each
// added account is a CLI config folder of its own (CLAUDE_CONFIG_DIR /
// CODEX_HOME) next to the server's own login (account 0, ~/.claude and
// ~/.codex). A thread runs on one account; switching moves the session file
// into the other folder and resumes it there.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) accountRoutes(r chi.Router) {
	r.Get("/accounts", s.handleListAccounts)
	r.Post("/accounts", s.handleCreateAccount)
	r.Patch("/accounts/{id}", s.handlePatchAccount)
	r.Delete("/accounts/{id}", s.handleDeleteAccount)
	r.Post("/accounts/{id}/login", s.handleStartLogin)
	r.Get("/accounts/{id}/login", s.handleLoginState)
	r.Post("/accounts/{id}/login/code", s.handleLoginCode)
	r.Delete("/accounts/{id}/login", s.handleCancelLogin)
	r.Post("/accounts/{id}/logout", s.handleAccountLogout)
}

// accountView is an account as the web app sees it.
type accountView struct {
	ID       int64  `json:"id"` // 0 = the server's own login
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Home     string `json:"home"`
	UserID   *int64 `json:"user_id"`
	Default  bool   `json:"default"`
	acctStatus
}

// acctStatus is what the CLI says about a login (cached).
type acctStatus struct {
	SignedIn  bool   `json:"signed_in"`
	Email     string `json:"email,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Org       string `json:"org,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	// SameAs names another login that is signed in to the same account.
	SameAs string `json:"same_as,omitempty"`
	ident  string // who the login really is ("" = unknown)
	home   string // another machine's login folder (host_logins.go)
	at     time.Time
}

// loginIdent is who a login is signed in as: the same email in two orgs
// (personal + team) is two accounts.
func loginIdent(provider, email, org string) string {
	if email == "" {
		return ""
	}
	return provider + "|" + strings.ToLower(email) + "|" + org
}

type accountState struct {
	mu     sync.Mutex
	status map[string]acctStatus // provider/id
	logins map[string]*loginRun  // loginKey(account) | hostLoginKey(host, provider)
}

func acctKey(provider string, id int64) string { return provider + "/" + strconv.FormatInt(id, 10) }

// loginKey names the sign-in of an account.
func loginKey(id int64) string { return "a" + strconv.FormatInt(id, 10) }

// defaultHome is the folder of the server's own login.
func (s *Server) defaultHome(provider string) string {
	if provider == "codex" {
		return s.cfg.CodexHome
	}
	return s.cfg.ClaudeHome
}

// homes lists the folders of every login, for reading session files.
func (s *Server) homes(ctx context.Context) agent.Homes {
	h := agent.Homes{Claude: []agent.AccountHome{{Dir: s.cfg.ClaudeHome}}, Codex: []agent.AccountHome{{Dir: s.cfg.CodexHome}}}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.Provider == "codex" {
			h.Codex = append(h.Codex, agent.AccountHome{ID: a.ID, Dir: a.Home})
		} else {
			h.Claude = append(h.Claude, agent.AccountHome{ID: a.ID, Dir: a.Home})
		}
	}
	return h
}

// accountHome is the folder of a thread's account ("" when the account is
// gone: the server's own login takes over).
func (s *Server) accountHome(ctx context.Context, provider string, id int64) (dir string, env []string) {
	if id == 0 {
		return s.defaultHome(provider), nil
	}
	a, err := s.store.GetAccount(ctx, id)
	if err != nil || a.Provider != provider {
		return s.defaultHome(provider), nil
	}
	return a.Home, agent.AccountEnv(provider, a.Home)
}

func (s *Server) accountEnv(ctx context.Context, provider string, id int64) []string {
	_, env := s.accountHome(ctx, provider, id)
	return env
}

// mayUseAccount: the owner runs on accounts nobody owns and their own;
// anyone else only on their own, in their own folder (people.go).
func (s *Server) mayUseAccount(ctx context.Context, u *store.User, a *store.Account) bool {
	if u == nil {
		return false
	}
	if s.jailed(ctx, u.ID) {
		return a.UserID != nil && *a.UserID == u.ID && s.ownFolder(ctx, u.ID, a.Home)
	}
	if agent.IsPersonPath(a.Home) {
		return false // someone else's folder
	}
	return a.UserID == nil || *a.UserID == u.ID
}

// mayUseServerLogin: the server's own login (account 0) is the owner's.
func (s *Server) mayUseServerLogin(ctx context.Context, u *store.User) bool {
	return u != nil && !s.jailed(ctx, u.ID)
}

// checkAccount accepts an account of the provider the person may use (0 =
// the server's login, the owner's alone).
func (s *Server) checkAccount(ctx context.Context, u *store.User, provider string, id int64) error {
	if id == 0 {
		if !s.mayUseServerLogin(ctx, u) {
			return badRequest(signInFirst(provider))
		}
		return nil
	}
	a, err := s.store.GetAccount(ctx, id)
	if err != nil || a.Provider != provider || !s.mayUseAccount(ctx, u, a) {
		return badRequest("that account is not available")
	}
	return nil
}

func signInFirst(provider string) string {
	name := "Claude Code"
	if provider == "codex" {
		name = "Codex"
	}
	return "Sign in to " + name + " with your own account first: Settings → Agents → Accounts → Add " + name + " login."
}

// usableAccount is the account a person runs on when theirs is not given or
// gone: their saved default if they may still use it, else their first own
// login of the provider (anyone but the owner), else the server's login
// (the owner). err when they have none.
func (s *Server) usableAccount(ctx context.Context, u *store.User, provider string, saved int64) (int64, error) {
	if s.checkAccount(ctx, u, provider, saved) == nil {
		return saved, nil
	}
	if s.mayUseServerLogin(ctx, u) {
		return 0, nil
	}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.Provider == provider && s.mayUseAccount(ctx, u, a) {
			return a.ID, nil
		}
	}
	return 0, badRequest(signInFirst(provider))
}

// accountsFor lists the logins a person may run on (admins also see the
// ones that belong to someone else when manage is set, to manage them).
func (s *Server) accountsFor(ctx context.Context, u *store.User, manage, refresh bool) []accountView {
	var out []accountView
	if s.mayUseServerLogin(ctx, u) || (manage && u.Role == "admin") {
		out = append(out, accountView{Provider: "claude", Label: "Server login", Home: s.cfg.ClaudeHome, Default: true},
			accountView{Provider: "codex", Label: "Server login", Home: s.cfg.CodexHome, Default: true})
	}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if s.mayUseAccount(ctx, u, a) || (manage && u.Role == "admin") {
			out = append(out, accountView{ID: a.ID, Provider: a.Provider, Label: a.Label, Home: a.Home, UserID: a.UserID})
		}
	}
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(v *accountView) {
			defer wg.Done()
			v.acctStatus = s.accountStatus(ctx, v.Provider, v.ID, v.Home, refresh)
		}(&out[i])
	}
	wg.Wait()
	for i := range out {
		for j := range out {
			if j != i && out[i].ident != "" && out[i].ident == out[j].ident {
				out[i].SameAs = out[j].Label
				break
			}
		}
	}
	return out
}

// accountStatus asks the CLI whether the login works (cached 10 minutes).
func (s *Server) accountStatus(ctx context.Context, provider string, id int64, home string, refresh bool) acctStatus {
	k := acctKey(provider, id)
	s.accts.mu.Lock()
	cur, ok := s.accts.status[k]
	s.accts.mu.Unlock()
	if ok && !refresh && time.Since(cur.at) < 10*time.Minute && !loginChanged(provider, home, cur.at) {
		return cur
	}
	var env []string
	if id != 0 {
		env = agent.AccountEnv(provider, home)
		if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
			st := acctStatus{Error: "Its folder " + home + " is missing on the server. Remove the account and add it again.", at: time.Now()}
			st.CheckedAt = store.FormatTime(st.at)
			return st
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var st acctStatus
	if provider == "codex" {
		st = s.codexStatus(ctx, env)
	} else {
		st = s.claudeStatus(ctx, env)
	}
	st.at = time.Now()
	st.CheckedAt = store.FormatTime(st.at)
	s.accts.mu.Lock()
	s.accts.status[k] = st
	s.accts.mu.Unlock()
	return st
}

// loginChanged reports whether the CLI wrote its login since t: a sign-in
// from a terminal (or a token refresh) is noticed right away. Only the
// file's time is looked at, never its contents.
func loginChanged(provider, home string, t time.Time) bool {
	name := ".credentials.json"
	if provider == "codex" {
		name = "auth.json"
	}
	fi, err := os.Stat(filepath.Join(home, name))
	return err == nil && fi.ModTime().After(t)
}

func (s *Server) claudeStatus(ctx context.Context, env []string) acctStatus {
	cmd, err := agent.LocalCmd(ctx, s.cfg.Home, env, s.cfg.ClaudeBin, "auth", "status")
	if err != nil {
		return acctStatus{Error: err.Error(), at: time.Now()}
	}
	out, err := cmd.Output()
	return parseClaudeStatus(out, err)
}

// parseClaudeStatus reads the JSON of `claude auth status`.
func parseClaudeStatus(out []byte, err error) acctStatus {
	var r struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
		AuthMethod       string `json:"authMethod"`
		OrgID            string `json:"orgId"`
		OrgName          string `json:"orgName"`
	}
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		if err == nil {
			err = jerr
		}
		return acctStatus{Error: "claude auth status: " + err.Error()}
	}
	plan := r.SubscriptionType
	if plan == "" && r.AuthMethod != "claude.ai" {
		plan = r.AuthMethod
	}
	st := acctStatus{SignedIn: r.LoggedIn, Email: r.Email, Plan: plan, Org: r.OrgName}
	if r.LoggedIn {
		st.ident = loginIdent("claude", r.Email, r.OrgID)
	}
	return st
}

func (s *Server) codexStatus(ctx context.Context, env []string) acctStatus {
	email, plan, signedIn, err := agent.CodexAccount(ctx, s.cfg.CodexBin, env)
	if err != nil {
		return acctStatus{Error: err.Error()}
	}
	st := acctStatus{SignedIn: signedIn, Email: email, Plan: plan}
	if signedIn {
		st.ident = loginIdent("codex", email, plan) // no workspace id: the plan tells personal from team
	}
	return st
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !u.MayRun() {
		writeJSON(w, http.StatusOK, []accountView{})
		return
	}
	writeJSON(w, http.StatusOK, s.accountsFor(r.Context(), u, queryBool(r, "manage"), queryBool(r, "refresh")))
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// handleCreateAccount adds a login: a new config folder next to the
// server's own, sharing its settings, skills and plugins (symlinks).
func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !u.MayRun() {
		writeErr(w, http.StatusForbidden, "an admin has to allow you to run agents first")
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		UserID   *int64 `json:"user_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Provider != "claude" && in.Provider != "codex" {
		writeErr(w, http.StatusBadRequest, "provider must be claude or codex")
		return
	}
	in.Label = oneLine(in.Label, 60)
	if in.Label == "" {
		writeErr(w, http.StatusBadRequest, "give the account a name")
		return
	}
	if u.Role != "admin" || (in.UserID == nil && s.jailed(r.Context(), u.ID)) {
		in.UserID = &u.ID // people add logins for themselves
	}
	if err := s.checkOwner(r.Context(), in.UserID); err != nil {
		s.fail(w, err, "account")
		return
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(in.Label), "-"), "-")
	if slug == "" {
		slug = "account"
	}
	var home string
	person := in.UserID != nil && s.jailed(r.Context(), *in.UserID)
	if person {
		// in their own folder, with their own settings (people.go)
		if err := s.ensurePerson(*in.UserID); err != nil {
			s.fail(w, err, "create account folder")
			return
		}
		home = s.newAccountHome(*in.UserID, in.Provider, slug)
	} else {
		base := filepath.Join(s.cfg.Home, "."+in.Provider+"-"+slug)
		home = base
		for i := 2; ; i++ {
			if _, err := os.Lstat(home); os.IsNotExist(err) {
				break
			}
			home = fmt.Sprintf("%s-%d", base, i)
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		s.fail(w, err, "create account folder")
		return
	}
	if !person {
		agent.ShareSetup(in.Provider, s.defaultHome(in.Provider), home)
		if in.Provider == "claude" {
			s.copyClaudeMCP(home)
		}
	}
	a := &store.Account{Provider: in.Provider, Label: in.Label, Home: home, UserID: in.UserID}
	if err := s.store.CreateAccount(r.Context(), a); err != nil {
		_ = os.Remove(home)
		s.fail(w, err, "create account")
		return
	}
	s.log.Info("account added", "provider", a.Provider, "label", a.Label, "home", home)
	s.rt.publishAccounts()
	writeJSON(w, http.StatusCreated, accountView{ID: a.ID, Provider: a.Provider, Label: a.Label, Home: a.Home, UserID: a.UserID})
}

// copyClaudeMCP gives a new Claude folder the MCP servers of the server's
// own login (they live in ~/.claude.json, which also holds the login, so it
// cannot be shared).
func (s *Server) copyClaudeMCP(home string) {
	b, err := os.ReadFile(filepath.Join(s.cfg.Home, ".claude.json"))
	if err != nil {
		return
	}
	var cfg struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil || len(cfg.MCPServers) == 0 {
		return
	}
	out, _ := json.Marshal(map[string]any{"mcpServers": cfg.MCPServers, "hasCompletedOnboarding": true})
	dst := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		_ = os.WriteFile(dst, out, 0o600)
	}
}

// giveAccount checks (and does) what handing a login to someone else needs:
// a login in a person's folder stays theirs (its settings are theirs), one
// in the owner's home moves into the new person's folder.
func (s *Server) giveAccount(ctx context.Context, a *store.Account, uid *int64) error {
	if agent.IsPersonPath(a.Home) {
		if uid == nil || !s.jailed(ctx, *uid) || !s.ownFolder(ctx, *uid, a.Home) {
			return badRequest("this login lives in its person's own folder and stays theirs; add a new login instead")
		}
		return nil
	}
	if uid == nil || !s.jailed(ctx, *uid) {
		return nil
	}
	if err := s.ensurePerson(*uid); err != nil {
		return err
	}
	s.rt.stopAccount(a.ID)
	dst := s.newAccountHome(*uid, a.Provider, filepath.Base(a.Home))
	if err := os.Rename(a.Home, dst); err != nil {
		return fmt.Errorf("move the login into their folder: %w", err)
	}
	unshareSetup(dst)
	a.Home = dst
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE accounts SET home=? WHERE id=?`, dst, a.ID); err != nil {
		return err
	}
	s.accts.mu.Lock()
	delete(s.accts.status, acctKey(a.Provider, a.ID))
	s.accts.mu.Unlock()
	return nil
}

func (s *Server) checkOwner(ctx context.Context, uid *int64) error {
	if uid == nil {
		return nil
	}
	if _, err := s.store.GetUser(ctx, *uid); err != nil {
		return badRequest("that person does not exist")
	}
	return nil
}

// adminAccount is the account of the request for someone who may manage
// it: an admin, or the person it belongs to.
func (s *Server) adminAccount(w http.ResponseWriter, r *http.Request) *store.Account {
	u := userFrom(r.Context())
	a, err := s.store.GetAccount(r.Context(), idParam(r, "id"))
	if err != nil {
		if u.Role != "admin" && !u.MayRun() {
			writeErr(w, http.StatusForbidden, "forbidden")
			return nil
		}
		s.fail(w, err, "account")
		return nil
	}
	if u.Role == "admin" || (u.MayRun() && a.UserID != nil && *a.UserID == u.ID) {
		return a
	}
	writeErr(w, http.StatusNotFound, "account not found")
	return nil
}

func (s *Server) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
	a := s.adminAccount(w, r)
	if a == nil {
		return
	}
	var in struct {
		Label  *string          `json:"label"`
		UserID *json.RawMessage `json:"user_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Label != nil {
		if l := oneLine(*in.Label, 60); l != "" {
			a.Label = l
		}
	}
	if in.UserID != nil {
		if !s.requireAdmin(w, r) {
			return
		}
		var uid *int64
		if err := json.Unmarshal(*in.UserID, &uid); err != nil {
			writeErr(w, http.StatusBadRequest, "user_id must be a number or null")
			return
		}
		if err := s.checkOwner(r.Context(), uid); err != nil {
			s.fail(w, err, "account")
			return
		}
		if err := s.giveAccount(r.Context(), a, uid); err != nil {
			s.fail(w, err, "account")
			return
		}
		a.UserID = uid
	}
	if err := s.store.UpdateAccount(r.Context(), a); err != nil {
		s.fail(w, err, "update account")
		return
	}
	s.rt.publishAccounts()
	writeJSON(w, http.StatusOK, a)
}

// handleDeleteAccount forgets an account. Its folder (login and session
// files) stays on disk; its threads continue on the server's own login.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	a := s.adminAccount(w, r)
	if a == nil {
		return
	}
	s.cancelLogin(loginKey(a.ID))
	if err := s.store.DeleteAccount(r.Context(), a.ID); err != nil {
		s.fail(w, err, "delete account")
		return
	}
	s.rt.stopAccount(a.ID)
	s.rt.dropLimits(limitsKey(a.Provider, a.ID))
	s.accts.mu.Lock()
	delete(s.accts.status, acctKey(a.Provider, a.ID))
	s.accts.mu.Unlock()
	s.rt.publishAccounts()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAccountLogout(w http.ResponseWriter, r *http.Request) {
	a := s.adminAccount(w, r)
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bin, args := s.cfg.ClaudeBin, []string{"auth", "logout"}
	if a.Provider == "codex" {
		bin, args = s.cfg.CodexBin, []string{"logout"}
	}
	cmd, err := agent.LocalCmd(ctx, s.cfg.Home, agent.AccountEnv(a.Provider, a.Home), bin, args...)
	if err != nil {
		s.fail(w, err, "sign out")
		return
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "sign out failed: "+oneLine(stripANSI(string(out)), 300))
		return
	}
	s.rt.stopAccount(a.ID)
	st := s.accountStatus(r.Context(), a.Provider, a.ID, a.Home, true)
	s.rt.publishAccounts()
	writeJSON(w, http.StatusOK, st)
}

// ---------- signing in from the browser ----------
//
// Claude Code: `claude auth login` prints a URL; the person signs in there
// and pastes the code it shows back (written to the CLI's stdin).
// Codex: `codex login --device-auth` prints a URL and a one-time code; the
// CLI finishes by itself once the code is entered on that page.

type loginRun struct {
	mu       sync.Mutex
	provider string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	out      strings.Builder
	URL      string `json:"url"`
	Code     string `json:"code,omitempty"` // Codex: the code to type on the page
	NeedCode bool   `json:"need_code"`      // Claude: paste the code from the page here
	State    string `json:"state"`          // starting | waiting | checking | done | failed
	Error    string `json:"error,omitempty"`
	Sent     bool   `json:"code_sent,omitempty"` // a code was pasted
	done     chan struct{}
}

func (l *loginRun) snapshot() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{"url": l.URL, "code": l.Code, "need_code": l.NeedCode, "state": l.State, "error": l.Error, "code_sent": l.Sent}
}

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`)
	urlRe       = regexp.MustCompile(`https://[^\s"'<>]+`)
	deviceRe    = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,6}\b`)
	loginExpiry = 15 * time.Minute
)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func (s *Server) handleStartLogin(w http.ResponseWriter, r *http.Request) {
	a := s.adminAccount(w, r)
	if a == nil {
		return
	}
	key := loginKey(a.ID)
	s.cancelLogin(key)
	ctx, cancel := context.WithTimeout(s.rt.ctx, loginExpiry)
	bin, args := loginArgs(a.Provider, s.cfg.ClaudeBin, s.cfg.CodexBin)
	// nothing to open on a server
	cmd, err := agent.LocalCmd(ctx, a.Home, append(agent.AccountEnv(a.Provider, a.Home), loginEnv...), bin, args...)
	if err != nil {
		cancel()
		s.fail(w, err, "login")
		return
	}
	l, err := s.beginLogin(ctx, cancel, key, a.Provider, cmd, func(ok bool) {
		s.accountStatus(s.rt.ctx, a.Provider, a.ID, a.Home, true)
		s.rt.publishAccounts()
		if ok {
			s.rt.refreshLimitsNow()
		}
	})
	if err != nil {
		s.fail(w, err, "login")
		return
	}
	writeJSON(w, http.StatusOK, l.waitStarted())
}

// loginEnv: a server (or another machine over ssh) has no browser to open.
var loginEnv = []string{"BROWSER=true", "NO_COLOR=1"}

// loginArgs is the sign-in command of a CLI.
func loginArgs(provider, claudeBin, codexBin string) (string, []string) {
	if provider == "codex" {
		return codexBin, []string{"login", "--device-auth"}
	}
	return claudeBin, []string{"auth", "login", "--claudeai"}
}

// beginLogin starts a sign-in command (here, or ssh to another machine) and
// follows its output for the URL and code. after runs once it has ended,
// before anyone waiting is told.
// The command is stopped when ctx ends (cancel is called when it exits).
func (s *Server) beginLogin(ctx context.Context, cancel context.CancelFunc, key, provider string, cmd *exec.Cmd, after func(ok bool)) (*loginRun, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	l := &loginRun{provider: provider, cmd: cmd, State: "starting", done: make(chan struct{})}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	l.stdin = stdin
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s.accts.mu.Lock()
	s.accts.logins[key] = l
	s.accts.mu.Unlock()
	go func() { // the whole group (ssh commands are not tied to ctx at all)
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		case <-l.done:
		}
	}()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		sc.Split(scanChunks)
		for sc.Scan() {
			l.feed(stripANSI(sc.Text()))
		}
	}()
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		expired := ctx.Err() == context.DeadlineExceeded
		cancel()
		l.mu.Lock()
		switch {
		case err == nil:
			l.State = "done"
		case expired:
			l.State, l.Error = "failed", "The sign-in expired. Start again."
		case l.State == "cancelled":
		default:
			l.State, l.Error = "failed", firstNonEmpty(lastLines(l.out.String(), 3), err.Error())
		}
		l.mu.Unlock()
		after(err == nil) // first: whoever waits for "done" then sees the new login
		close(l.done)
	}()
	return l, nil
}

// waitStarted waits (briefly) for the URL so the first answer can show it.
func (l *loginRun) waitStarted() map[string]any {
	deadline := time.After(20 * time.Second)
	for {
		l.mu.Lock()
		ready := l.URL != "" && (l.provider != "codex" || l.Code != "")
		l.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-l.done:
		case <-deadline:
		case <-time.After(150 * time.Millisecond):
			continue
		}
		break
	}
	return l.snapshot()
}

// scanChunks splits on newlines but also hands over a trailing prompt that
// has no newline yet ("Paste code here if prompted >").
func scanChunks(data []byte, atEOF bool) (int, []byte, error) {
	if i := strings.IndexAny(string(data), "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	if strings.HasSuffix(strings.TrimSpace(string(data)), ">") {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (l *loginRun) feed(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out.WriteString(line + "\n")
	if l.URL == "" {
		if u := urlRe.FindString(line); u != "" {
			l.URL = strings.TrimRight(u, ".,)")
			l.State = "waiting"
			l.NeedCode = l.provider == "claude"
		}
	}
	if l.provider == "codex" && l.Code == "" {
		if c := deviceRe.FindString(line); c != "" {
			l.Code = c
		}
	}
	low := strings.ToLower(line)
	if l.provider == "claude" && l.Sent && strings.Contains(low, "paste code") {
		l.State = "waiting" // asked again: the code was not accepted
		if l.Error == "" {
			l.Error = "That code was not accepted. Copy it again (or start over) and paste it here."
		}
	}
	if l.provider == "claude" && l.Sent && (strings.Contains(low, "invalid") || strings.Contains(low, "error") || strings.Contains(low, "failed")) {
		l.Error = strings.TrimSpace(line)
	}
}

func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " · ")
}

func (s *Server) login(key string) *loginRun {
	s.accts.mu.Lock()
	defer s.accts.mu.Unlock()
	return s.accts.logins[key]
}

func (s *Server) handleLoginState(w http.ResponseWriter, r *http.Request) {
	if a := s.adminAccount(w, r); a != nil {
		writeLoginState(w, r, s.login(loginKey(a.ID)))
	}
}

// writeLoginState answers where a sign-in is (nil = none). With ?wait=1 it
// long-polls: answers when the sign-in ends or after 25 s.
func writeLoginState(w http.ResponseWriter, r *http.Request, l *loginRun) {
	if l == nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "idle"})
		return
	}
	if queryBool(r, "wait") {
		select {
		case <-l.done:
		case <-time.After(25 * time.Second):
		case <-r.Context().Done():
		}
	}
	writeJSON(w, http.StatusOK, l.snapshot())
}

// handleLoginCode hands the code shown after a Claude sign-in to the CLI.
func (s *Server) handleLoginCode(w http.ResponseWriter, r *http.Request) {
	if a := s.adminAccount(w, r); a != nil {
		sendLoginCode(w, r, s.login(loginKey(a.ID)))
	}
}

func sendLoginCode(w http.ResponseWriter, r *http.Request, l *loginRun) {
	if l == nil {
		writeErr(w, http.StatusConflict, "start the sign-in first")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Code) == "" {
		writeErr(w, http.StatusBadRequest, "paste the code")
		return
	}
	code := strings.TrimSpace(in.Code)
	if strings.ContainsAny(code, "\r\n") {
		writeErr(w, http.StatusBadRequest, "the code is one line")
		return
	}
	l.mu.Lock()
	if l.State != "waiting" {
		l.mu.Unlock()
		writeErr(w, http.StatusConflict, "this sign-in is not waiting for a code")
		return
	}
	l.Sent, l.State, l.Error = true, "checking", ""
	l.mu.Unlock()
	if _, err := io.WriteString(l.stdin, code+"\n"); err != nil {
		writeErr(w, http.StatusConflict, "the sign-in has ended; start again")
		return
	}
	select {
	case <-l.done:
	case <-time.After(30 * time.Second):
	case <-r.Context().Done():
	}
	writeJSON(w, http.StatusOK, l.snapshot())
}

func (s *Server) handleCancelLogin(w http.ResponseWriter, r *http.Request) {
	a := s.adminAccount(w, r)
	if a == nil {
		return
	}
	s.cancelLogin(loginKey(a.ID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cancelLogin(key string) {
	s.accts.mu.Lock()
	l := s.accts.logins[key]
	delete(s.accts.logins, key)
	s.accts.mu.Unlock()
	if l == nil {
		return
	}
	l.mu.Lock()
	running := l.State == "starting" || l.State == "waiting" || l.State == "checking"
	if running {
		l.State = "cancelled"
	}
	l.mu.Unlock()
	if running && l.cmd.Process != nil {
		_ = syscall.Kill(-l.cmd.Process.Pid, syscall.SIGTERM)
	}
}

// ---------- moving a session between accounts ----------

// ensureSession makes sure the account folder has the thread's session file
// before the CLI resumes it there; it is copied from whichever login has
// the latest version (the thread may have switched accounts).
func (s *Server) ensureSession(ctx context.Context, t *store.Thread, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	dir, _ := s.accountHome(ctx, t.Provider, t.AccountID)
	have := int64(-1)
	if p, err := agent.SessionPath(t.Provider, dir, sessionID); err == nil {
		have = agent.FileSize(p)
	}
	from, best := "", have
	for _, h := range s.homes(ctx).Of(t.Provider) {
		if h.Dir == dir {
			continue
		}
		if p, err := agent.SessionPath(t.Provider, h.Dir, sessionID); err == nil {
			if n := agent.FileSize(p); n > best {
				from, best = h.Dir, n
			}
		}
	}
	if from == "" {
		return nil // already there (or nowhere: the CLI says so)
	}
	if _, err := agent.CopySession(t.Provider, sessionID, from, dir); err != nil {
		return fmt.Errorf("could not move the conversation to this account: %w", err)
	}
	s.log.Info("session copied to account", "thread", t.ID, "provider", t.Provider, "account", t.AccountID, "from", from, "to", dir)
	return nil
}

// accountLabel names a thread's account for notices.
func (s *Server) accountLabel(ctx context.Context, provider string, id int64) string {
	if id == 0 {
		return "the server login"
	}
	if a, err := s.store.GetAccount(ctx, id); err == nil {
		return a.Label
	}
	return "account " + strconv.FormatInt(id, 10)
}

// homesFor lists the folders of the logins a person may use (what "On this
// server" and Today read).
func (s *Server) homesFor(ctx context.Context, u *store.User) agent.Homes {
	var h agent.Homes
	if s.mayUseServerLogin(ctx, u) {
		h = agent.Homes{Claude: []agent.AccountHome{{Dir: s.cfg.ClaudeHome}}, Codex: []agent.AccountHome{{Dir: s.cfg.CodexHome}}}
	}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if !s.mayUseAccount(ctx, u, a) {
			continue
		}
		if a.Provider == "codex" {
			h.Codex = append(h.Codex, agent.AccountHome{ID: a.ID, Dir: a.Home})
		} else {
			h.Claude = append(h.Claude, agent.AccountHome{ID: a.ID, Dir: a.Home})
		}
	}
	return h
}

// accountIDsFor is the set of "provider/id" a person may use.
func (s *Server) accountIDsFor(ctx context.Context, u *store.User) map[string]bool {
	out := map[string]bool{}
	h := s.homesFor(ctx, u)
	for _, a := range h.Claude {
		out[acctKey("claude", a.ID)] = true
	}
	for _, a := range h.Codex {
		out[acctKey("codex", a.ID)] = true
	}
	return out
}

// askHomes are the Claude folders Pocket Ask keeps per person (one per login).
func (s *Server) askHomes(uid int64) agent.Homes {
	base := filepath.Join(s.askBase(context.Background(), uid), "ask-claude", strconv.FormatInt(uid, 10))
	h := agent.Homes{Claude: []agent.AccountHome{{Dir: base}}}
	more, _ := filepath.Glob(base + "-a*")
	for _, dir := range more {
		if id, err := strconv.ParseInt(strings.TrimPrefix(dir, base+"-a"), 10, 64); err == nil {
			h.Claude = append(h.Claude, agent.AccountHome{ID: id, Dir: dir})
		}
	}
	return h
}

// sessionAccount finds the login a CLI session belongs to: the account
// folder with the latest copy of its file, among those the person may use.
func (s *Server) sessionAccount(ctx context.Context, u *store.User, provider, sessionID string) (int64, string) {
	id, dir, best := int64(0), s.defaultHome(provider), int64(-1)
	if !s.mayUseServerLogin(ctx, u) {
		dir = filepath.Join(s.personHome(u.ID), "."+provider) // never the server's folder
	}
	for _, h := range s.homesFor(ctx, u).Of(provider) {
		if p, err := agent.SessionPath(provider, h.Dir, sessionID); err == nil {
			if n := agent.FileSize(p); n > best {
				id, dir, best = h.ID, h.Dir, n
			}
		}
	}
	return id, dir
}

// signedIn is the last known sign-in state of an account (unknown = yes:
// let the CLI say otherwise).
func (s *Server) signedIn(provider string, id int64) bool {
	s.accts.mu.Lock()
	defer s.accts.mu.Unlock()
	st, ok := s.accts.status[acctKey(provider, id)]
	return !ok || st.SignedIn || st.Error != ""
}
