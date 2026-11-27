package server

// Shared AI for the other agents (ai_share.go has Claude Code and Codex).
// The rule is the same for every agent: what the owner lends goes through
// Pocket AI. The person's agent gets a key that only the relay takes, never
// the owner's sign-in or API key.
//
//	Antigravity  CLOUD_CODE_URL → /gca (Google's Code Assist service, with
//	             the owner's Google token). It also renews its sign-in and
//	             reads who it is straight from Google: HTTPS_PROXY points at
//	             the relay, which answers the renewal with the share key and
//	             passes on only the few Google calls it needs (a certificate
//	             of Pocket AI's own, trusted through SSL_CERT_FILE by that
//	             agent alone). Everything else is tunnelled untouched, with
//	             no sign-in of the owner's on it.
//	Gemini CLI   GOOGLE_GEMINI_BASE_URL → /gemini (the owner's Gemini API key)
//	Qwen Code    OPENAI_BASE_URL → /openai/qwen (the owner's key and endpoint)
//	OpenCode     each provider's baseURL → /oc/<provider> (the owner's keys;
//	             its free models need none, but they count all the same)

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Upstreams of the other agents (vars for tests).
var (
	shareGCAUpstream    = "https://daily-cloudcode-pa.googleapis.com" // Antigravity's Code Assist service
	shareGeminiUpstream = "https://generativelanguage.googleapis.com"
	// shareGoogleHosts: the Google hosts the relay answers for Antigravity
	// itself; "" = the real host.
	shareGoogleHosts = map[string]string{}
)

// ocProviders: OpenCode's providers the relay passes on: the key it reads,
// the provider's API and how the key goes on ("google" = x-goog-api-key).
var ocProviders = map[string]struct{ key, base, auth string }{
	"opencode":   {"", "https://opencode.ai/zen/v1", "keep"}, // free models: no key of the owner's
	"google":     {"GOOGLE_GENERATIVE_AI_API_KEY", "https://generativelanguage.googleapis.com/v1beta", "google"},
	"openrouter": {"OPENROUTER_API_KEY", "https://openrouter.ai/api/v1", "bearer"},
	"deepseek":   {"DEEPSEEK_API_KEY", "https://api.deepseek.com/v1", "bearer"},
	"moonshotai": {"MOONSHOT_API_KEY", "https://api.moonshot.ai/v1", "bearer"},
	"xai":        {"XAI_API_KEY", "https://api.x.ai/v1", "bearer"},
	"mistral":    {"MISTRAL_API_KEY", "https://api.mistral.ai/v1", "bearer"},
	"groq":       {"GROQ_API_KEY", "https://api.groq.com/openai/v1", "bearer"},
}

// shareable: the agents whose logins (or keys) can be lent.
func shareable(provider string) bool {
	switch provider {
	case "claude", "codex", "antigravity", "gemini", "qwen", "opencode":
		return true
	}
	return false
}

// sharePlanKnown: the agent's provider says how full the plan is, so a
// share of the week can be measured.
func sharePlanKnown(provider string) bool { return provider == "claude" || provider == "codex" }

// ---------- the owner's side ----------

// lentACPHome: where the lent login of an agent keeps its sign-in ("" for
// agents that run on keys).
func (s *Server) lentACPHome(ctx context.Context, provider string, from int64) (string, error) {
	ag, ok := agent.ACPAgentByID(provider)
	if !ok {
		return "", errors.New("that agent cannot be shared")
	}
	if !ag.Logins {
		if from != 0 {
			return "", errors.New("that login cannot be shared")
		}
		return "", nil
	}
	if from == 0 {
		return ag.LoginHome(""), nil
	}
	a, err := s.store.GetAccount(ctx, from)
	if err != nil || a.Provider != provider || a.SharedFrom != nil || agent.IsPersonPath(a.Home) {
		return "", errors.New("that login cannot be shared")
	}
	return a.Home, nil
}

// lentACPReady says whether the owner has something to lend for the agent
// ("" = yes, else why not).
func (s *Server) lentACPReady(ctx context.Context, provider string, from int64) string {
	ag, _ := agent.ACPAgentByID(provider)
	if _, err := exec.LookPath(ag.Command()); err != nil {
		return "not installed on this server"
	}
	env := s.store.ACPEnv(ctx, provider)
	switch provider {
	case "antigravity":
		home, err := s.lentACPHome(ctx, provider, from)
		if err != nil {
			return err.Error()
		}
		if _, _, err := readAgyToken(home); err != nil {
			return "not signed in on this server"
		}
	case "gemini":
		if env["GEMINI_API_KEY"] == "" {
			return "needs your Gemini API key (Settings → Agents); Google sign-ins no longer work in Gemini CLI"
		}
	case "qwen":
		if env["OPENAI_API_KEY"] == "" || env["OPENAI_BASE_URL"] == "" {
			return "needs your API key and endpoint (Settings → Agents)"
		}
	}
	return ""
}

func readAgyToken(home string) (string, time.Time, error) {
	b, err := os.ReadFile(filepath.Join(home, "antigravity-cli", "antigravity-oauth-token"))
	if err != nil {
		return "", time.Time{}, err
	}
	var t struct {
		Token struct {
			AccessToken string `json:"access_token"`
			Expiry      string `json:"expiry"`
		} `json:"token"`
	}
	if json.Unmarshal(b, &t) != nil || t.Token.AccessToken == "" {
		return "", time.Time{}, errors.New("no sign-in in it")
	}
	exp, err := time.Parse(time.RFC3339Nano, t.Token.Expiry)
	if err != nil {
		exp = time.Now().Add(time.Hour)
	}
	return t.Token.AccessToken, exp, nil
}

// lentACPCred is the owner's credential for a request of the agent: the
// Google token (Antigravity) or the API key; sub names OpenCode's provider.
func (s *Server) lentACPCred(ctx context.Context, provider string, from int64, sub string) (string, error) {
	owner := s.ownerName(ctx)
	if why := s.lentACPReady(ctx, provider, from); why != "" {
		return "", errors.New(owner + "'s " + providerName(provider) + " cannot be used right now: " + why + ".")
	}
	env := s.store.ACPEnv(ctx, provider)
	switch provider {
	case "antigravity":
		home, _ := s.lentACPHome(ctx, provider, from)
		tok, exp, err := readAgyToken(home)
		lk := limitsKey(provider, from)
		s.share.mu.Lock()
		s.share.init()
		stale := s.share.stale[lk]
		s.share.mu.Unlock()
		if err == nil && (stale || time.Until(exp) < 3*time.Minute) {
			s.renewAgy(ctx, from, home)
			tok, _, err = readAgyToken(home)
		}
		if err != nil {
			return "", errors.New(owner + "'s Antigravity is not signed in on this server.")
		}
		return tok, nil
	case "gemini":
		return env["GEMINI_API_KEY"], nil
	case "qwen":
		return env["OPENAI_API_KEY"], nil
	case "opencode":
		p, ok := ocProviders[sub]
		if !ok {
			return "", errors.New("OpenCode's " + sub + " is not shared")
		}
		if p.key == "" {
			return "", nil
		}
		if env[p.key] == "" {
			return "", errors.New(owner + " has no " + sub + " key in OpenCode.")
		}
		return env[p.key], nil
	}
	return "", errors.New("that agent cannot be shared")
}

// renewAgy lets Antigravity renew the lent sign-in: a tiny question on it
// (at most once a minute). Its print mode only knows ~/.gemini, so another
// login folder is shown to it as that of a throwaway home.
func (s *Server) renewAgy(ctx context.Context, from int64, home string) {
	s.share.renewMu.Lock()
	defer s.share.renewMu.Unlock()
	lk := limitsKey("antigravity", from)
	s.share.mu.Lock()
	last := s.share.renewed[lk]
	s.share.mu.Unlock()
	if time.Since(last) < time.Minute {
		return
	}
	dir, err := os.MkdirTemp("", "pocket-share-agy-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	env := os.Environ()
	if from != 0 {
		h := filepath.Join(dir, "home")
		_ = os.MkdirAll(h, 0o700)
		_ = os.Symlink(home, filepath.Join(h, ".gemini"))
		env = append(env, "HOME="+h)
	}
	ag, _ := agent.ACPAgentByID("antigravity")
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, ag.Command(), "-p", "Reply with the single word: ok")
	cmd.Dir, cmd.Env = dir, env
	_, err = cmd.CombinedOutput()
	s.share.mu.Lock()
	s.share.renewed[lk] = time.Now()
	if err == nil {
		delete(s.share.stale, lk)
	}
	s.share.mu.Unlock()
	if err != nil {
		s.log.Warn("shared AI: renewing the lent Antigravity login failed", "from", from, "err", err)
	}
}

// ---------- the person's side ----------

// shareACPEnv sets up a lent agent's folder and returns its environment.
func (s *Server) shareACPEnv(ctx context.Context, a *store.Account, tok, addr string) []string {
	base := "http://" + addr
	env := []string{agent.SharedEnv + "=1"}
	switch a.Provider {
	case "antigravity":
		if err := writeAgyPlaceholder(a.Home, tok); err != nil {
			s.log.Warn("shared AI: Antigravity folder", "err", err)
		}
		bundle, err := s.shareCABundle(a.Home)
		if err != nil {
			s.log.Warn("shared AI: certificate", "err", err)
		}
		proxy := "http://share:" + tok + "@" + addr
		env = append(env, "CLOUD_CODE_URL="+base+"/gca", "HTTPS_PROXY="+proxy, "https_proxy="+proxy,
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost", "SSL_CERT_FILE="+bundle)
	case "gemini":
		set := []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}}}` + "\n")
		for _, d := range []string{a.Home, filepath.Join(a.Home, ".gemini")} {
			_ = os.MkdirAll(d, 0o700)
			_ = os.WriteFile(filepath.Join(d, "settings.json"), set, 0o600)
		}
		env = append(env, "GEMINI_CLI_HOME="+a.Home, "GEMINI_API_KEY="+tok, "GOOGLE_GEMINI_BASE_URL="+base+"/gemini")
	case "qwen":
		env = append(env, "OPENAI_API_KEY="+tok, "OPENAI_BASE_URL="+base+"/openai/qwen")
		if m := s.store.ACPEnv(ctx, "qwen")["OPENAI_MODEL"]; m != "" {
			env = append(env, "OPENAI_MODEL="+m)
		}
	case "opencode":
		owner := s.store.ACPEnv(ctx, "opencode")
		cfg := map[string]any{}
		ag, _ := agent.ACPAgentByID("opencode")
		for _, e := range ag.Env { // its permission settings stay
			if v, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
				_ = json.Unmarshal([]byte(v), &cfg)
			}
		}
		prov := map[string]any{}
		for id, p := range ocProviders {
			if p.key != "" && owner[p.key] == "" {
				continue
			}
			opts := map[string]any{"baseURL": base + "/oc/" + id}
			if p.key == "" {
				opts["apiKey"] = tok // its free models: the relay knows whose they are by this
			}
			prov[id] = map[string]any{"options": opts}
			if p.key != "" {
				env = append(env, p.key+"="+tok)
			}
		}
		cfg["provider"] = prov
		b, _ := json.Marshal(cfg)
		env = append(env, "OPENCODE_CONFIG_CONTENT="+string(b))
	}
	return env
}

// writeAgyPlaceholder gives a lent Antigravity login a sign-in that is only
// the share key: Antigravity renews it at the relay, which hands back the
// share key again.
func writeAgyPlaceholder(home, tok string) error {
	dir := filepath.Join(home, "antigravity-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, "antigravity-oauth-token")
	if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(tok)) {
		return nil
	}
	b, _ := json.Marshal(map[string]any{
		"token":          map[string]any{"access_token": tok, "token_type": "Bearer", "refresh_token": "pocket-ai-shared", "expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano), "expires_in": 3599},
		"auth_method":    "consumer",
		"id_token_email": "shared via Pocket AI",
	})
	return os.WriteFile(p, b, 0o600)
}

// ---------- the relay's own certificate (Antigravity's HTTPS proxy) ----------

type shareCA struct {
	once  sync.Once
	err   error
	cert  *x509.Certificate
	pem   []byte
	key   *ecdsa.PrivateKey
	mu    sync.Mutex
	leafs map[string]*tls.Certificate
}

var shareCerts shareCA

// loadCA reads (or makes, the first time) the certificate authority only
// lent agents trust: DATA_DIR/share-ca.
func (s *Server) loadCA() error {
	shareCerts.once.Do(func() {
		dir := filepath.Join(s.cfg.DataDir, "share-ca")
		certP, keyP := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
		cb, cerr := os.ReadFile(certP)
		kb, kerr := os.ReadFile(keyP)
		if cerr != nil || kerr != nil {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				shareCerts.err = err
				return
			}
			t := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Pocket AI shared logins"},
				IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(20, 0, 0),
				KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
			der, err := x509.CreateCertificate(rand.Reader, t, t, &key.PublicKey, key)
			if err != nil {
				shareCerts.err = err
				return
			}
			kder, _ := x509.MarshalECPrivateKey(key)
			cb = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			kb = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
			if err := os.MkdirAll(dir, 0o700); err == nil {
				_ = os.WriteFile(keyP, kb, 0o600)
				_ = os.WriteFile(certP, cb, 0o644)
			}
		}
		cblk, _ := pem.Decode(cb)
		kblk, _ := pem.Decode(kb)
		if cblk == nil || kblk == nil {
			shareCerts.err = errors.New("the shared logins' certificate is damaged: remove " + dir)
			return
		}
		cert, err := x509.ParseCertificate(cblk.Bytes)
		if err != nil {
			shareCerts.err = err
			return
		}
		key, err := x509.ParseECPrivateKey(kblk.Bytes)
		if err != nil {
			shareCerts.err = err
			return
		}
		shareCerts.cert, shareCerts.key, shareCerts.pem, shareCerts.leafs = cert, key, cb, map[string]*tls.Certificate{}
	})
	return shareCerts.err
}

// leafFor is the relay's certificate for one Google host.
func leafFor(host string) (*tls.Certificate, error) {
	shareCerts.mu.Lock()
	defer shareCerts.mu.Unlock()
	if c := shareCerts.leafs[host]; c != nil && time.Until(c.Leaf.NotAfter) > time.Hour {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	t := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, t, shareCerts.cert, &key.PublicKey, shareCerts.key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	shareCerts.leafs[host] = c
	return c, nil
}

// systemRoots finds the system's certificate bundle.
func systemRoots() []byte {
	for _, p := range []string{os.Getenv("SSL_CERT_FILE"), "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem", "/etc/ssl/ca-bundle.pem"} {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return b
		}
	}
	return nil
}

// shareCABundle writes the system's certificates plus the relay's into the
// lent login's folder (inside the person's jail) and returns its path.
func (s *Server) shareCABundle(home string) (string, error) {
	if err := s.loadCA(); err != nil {
		return "", err
	}
	p := filepath.Join(home, "pocket-ai-ca.pem")
	want := append(append(systemRoots(), '\n'), shareCerts.pem...)
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, want) {
		return p, nil
	}
	return p, os.WriteFile(p, want, 0o600)
}

// ---------- Antigravity's HTTPS proxy ----------

// shareGoogleAllowed: the Google calls the relay passes on for Antigravity
// with the owner's token. Anything else on a Google host gets no token of
// the owner's: their token has wide access, so the list stays narrow.
func shareGoogleAllowed(host, method, path string) bool {
	switch host {
	case "www.googleapis.com":
		return method == http.MethodGet && (path == "/oauth2/v2/userinfo" || path == "/oauth2/v1/userinfo")
	case "aicode.googleapis.com":
		return method == http.MethodGet && strings.HasPrefix(path, "/v1alpha:fetchConfig")
	case "daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com":
		return strings.HasPrefix(path, "/v1internal:")
	}
	return false
}

// handleShareConnect is the HTTPS proxy of a lent Antigravity login. The
// share key comes as the proxy password.
func (s *Server) handleShareConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := ""
	if v, ok := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic "); ok {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			_, tok, _ = strings.Cut(string(b), ":")
		}
	}
	sh, err := s.store.GetAIShareByToken(ctx, tok)
	var a *store.Account
	if err == nil {
		a, err = s.store.GetAccount(ctx, sh.AccountID)
	}
	if tok == "" || err != nil || a.SharedFrom == nil || a.Provider != "antigravity" {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Pocket AI"`)
		http.Error(w, "This shared login is no longer shared with you.", http.StatusProxyAuthRequired)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		http.Error(w, "only HTTPS", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	google := host == "oauth2.googleapis.com" || host == "play.googleapis.com" || host == "www.googleapis.com" || host == "aicode.googleapis.com" ||
		host == "daily-cloudcode-pa.googleapis.com" || host == "cloudcode-pa.googleapis.com"
	if !google { // not ours to look at: straight through, no sign-in of the owner's on it
		up, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
		if err != nil {
			return
		}
		defer up.Close()
		go func() { _, _ = io.Copy(up, brw); _ = up.(*net.TCPConn).CloseWrite() }()
		_, _ = io.Copy(conn, up)
		return
	}
	if err := s.loadCA(); err != nil {
		return
	}
	leaf, err := leafFor(host)
	if err != nil {
		return
	}
	tc := tls.Server(&bufConn{Conn: conn, r: brw.Reader}, &tls.Config{Certificates: []tls.Certificate{*leaf}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	defer tc.Close()
	tr := bufio.NewReader(tc)
	for {
		_ = tc.SetReadDeadline(time.Now().Add(5 * time.Minute))
		req, err := http.ReadRequest(tr)
		if err != nil {
			return
		}
		if !s.shareGoogle(tc, req, host, a, tok) {
			return
		}
	}
}

// bufConn reads what the hijacked reader already holds first.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// shareGoogle answers one request Antigravity made to a Google host through
// the proxy; false = close the connection.
func (s *Server) shareGoogle(w io.Writer, req *http.Request, host string, a *store.Account, tok string) bool {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 32<<20))
	req.Body.Close()
	reply := func(code int, ctype string, b []byte) bool {
		fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", code, http.StatusText(code), ctype, len(b))
		_, err := w.Write(b)
		return err == nil
	}
	switch {
	case host == "oauth2.googleapis.com" && req.Method == http.MethodPost && req.URL.Path == "/token":
		// its sign-in renews to the share key again
		b, _ := json.Marshal(map[string]any{"access_token": tok, "expires_in": 3599, "token_type": "Bearer",
			"scope": "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email openid"})
		return reply(http.StatusOK, "application/json", b)
	case host == "play.googleapis.com":
		return reply(http.StatusOK, "application/json", []byte("{}")) // usage logs: not sent on as the owner
	case !shareGoogleAllowed(host, req.Method, req.URL.Path):
		return reply(http.StatusForbidden, "application/json", []byte(`{"error":{"code":403,"message":"Pocket AI does not pass this on for a shared login.","status":"PERMISSION_DENIED"}}`))
	}
	ctx := context.Background()
	cred, err := s.lentACPCred(ctx, "antigravity", *a.SharedFrom, "")
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 401, "message": err.Error(), "status": "UNAUTHENTICATED"}})
		return reply(http.StatusUnauthorized, "application/json", b)
	}
	target := "https://" + host
	if t := shareGoogleHosts[host]; t != "" {
		target = t
	}
	up, err := http.NewRequestWithContext(ctx, req.Method, target+req.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return false
	}
	copyShareHeaders(up.Header, req.Header)
	up.Header.Set("Authorization", "Bearer "+cred)
	res, err := shareClient.Do(up)
	if err != nil {
		return reply(http.StatusBadGateway, "application/json", []byte(`{"error":{"code":502,"message":"Google could not be reached"}}`))
	}
	out, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	res.Body.Close()
	return reply(res.StatusCode, res.Header.Get("Content-Type"), out)
}

// copyShareHeaders copies a request's headers without its sign-in and the
// connection's own.
func copyShareHeaders(dst, src http.Header) {
	for k, v := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "X-Api-Key", "X-Goog-Api-Key", "Host", "Content-Length", "Connection", "Accept-Encoding",
			"Proxy-Connection", "Proxy-Authorization", "Keep-Alive", "Te", "Upgrade":
			continue
		}
		dst[k] = v
	}
}
