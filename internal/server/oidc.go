package server

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Sign-in with an OpenID Connect provider (Pocket Core, PocketID, …):
// authorization code flow with PKCE and the client secret, the ID token's
// RS256 signature checked against the provider's published keys. Users are
// matched by verified email; API tokens are untouched, so apps and scripts
// keep working.
//
// On a box with Pocket Core nothing needs configuring: the app asks for a
// relying-party client when it registers and uses the one Core issues.
// Explicit OIDC_* variables take precedence when set.

const oidcCookie = "pai_oidc"

type oidcProvider struct {
	mu        sync.Mutex
	disc      *oidcDiscovery
	discAt    time.Time
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
	issuerURL string
}

type oidcDiscovery struct {
	Issuer   string `json:"issuer"`
	Auth     string `json:"authorization_endpoint"`
	Token    string `json:"token_endpoint"`
	UserInfo string `json:"userinfo_endpoint"`
	JWKS     string `json:"jwks_uri"`
}

var oidcHTTP = &http.Client{Timeout: 15 * time.Second}

// oidcSettings returns issuer, client id, secret and button label: explicit
// OIDC_* env wins, else the client Core issued at registration.
func (s *Server) oidcSettings() (issuer, clientID, secret, name string) {
	if s.cfg.OIDCIssuer != "" && s.cfg.OIDCClientID != "" {
		return s.cfg.OIDCIssuer, s.cfg.OIDCClientID, s.cfg.OIDCClientSecret, s.cfg.OIDCName
	}
	if s.core.Configured() && s.cfg.BaseURL != "" {
		if o := s.core.OIDC(); o.Issuer != "" && o.ClientID != "" {
			return strings.TrimRight(o.Issuer, "/"), o.ClientID, o.ClientSecret, "Pocket Core"
		}
	}
	return "", "", "", ""
}

// oidcFromCore reports whether the active settings came from Core rather
// than the environment; Core vouches for the person, so accounts are always
// created on first sign-in.
func (s *Server) oidcFromCore() bool {
	return !(s.cfg.OIDCIssuer != "" && s.cfg.OIDCClientID != "")
}

func (s *Server) oidcEnabled() bool {
	issuer, clientID, _, _ := s.oidcSettings()
	return issuer != "" && clientID != ""
}

func (s *Server) oidcName() string {
	_, _, _, name := s.oidcSettings()
	return name
}

func (s *Server) oidcCallbackURL() string { return s.cfg.BaseURL + "/api/v1/auth/oidc/callback" }

// provider returns the cached provider for the current issuer, rebuilding it
// when the issuer changed (Core's values arrive after start).
func (s *Server) provider(issuer string) *oidcProvider {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidc == nil || s.oidc.issuerURL != issuer {
		s.oidc = &oidcProvider{issuerURL: issuer}
	}
	return s.oidc
}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := oidcHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// discovery is cached for an hour.
func (p *oidcProvider) discovery(ctx context.Context) (*oidcDiscovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disc != nil && time.Since(p.discAt) < time.Hour {
		return p.disc, nil
	}
	var d oidcDiscovery
	if err := getJSON(ctx, p.issuerURL+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if d.Issuer != p.issuerURL || d.Auth == "" || d.Token == "" || d.JWKS == "" {
		return nil, fmt.Errorf("the provider's discovery document does not match the issuer %q (says %q)", p.issuerURL, d.Issuer)
	}
	p.disc, p.discAt = &d, time.Now()
	return p.disc, nil
}

// key returns the signing key with that id, refetching the key set when it is
// unknown (the provider rotated) but at most once a minute.
func (p *oidcProvider) key(ctx context.Context, jwksURL, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if time.Since(p.keysAt) < time.Minute && p.keys != nil {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, jwksURL, &set); err != nil {
		return nil, err
	}
	p.keys, p.keysAt = map[string]*rsa.PublicKey{}, time.Now()
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		p.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(p.keys) == 1 {
		for _, k := range p.keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

type idClaims struct {
	Iss           string          `json:"iss"`
	Sub           string          `json:"sub"`
	AuthRevision  int64           `json:"auth_revision"`
	Aud           json.RawMessage `json:"aud"`
	Exp           int64           `json:"exp"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
	Name          string          `json:"name"`
}

func (c *idClaims) hasAudience(id string) bool {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == id
	}
	var many []string
	_ = json.Unmarshal(c.Aud, &many)
	for _, a := range many {
		if a == id {
			return true
		}
	}
	return false
}

// verifyIDToken checks an RS256 ID token's signature, issuer, audience, expiry and nonce.
func verifyIDToken(ctx context.Context, p *oidcProvider, d *oidcDiscovery, clientID, raw, nonce string) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed ID token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed ID token header")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &head) != nil || head.Alg != "RS256" {
		return nil, fmt.Errorf("ID token algorithm %q is not RS256", head.Alg)
	}
	key, err := p.key(ctx, d.JWKS, head.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed ID token signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return nil, errors.New("ID token signature does not verify")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed ID token payload")
	}
	var c idClaims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, errors.New("malformed ID token payload")
	}
	switch {
	case c.Iss != d.Issuer:
		return nil, errors.New("ID token from another issuer")
	case !c.hasAudience(clientID):
		return nil, errors.New("ID token for another client")
	case time.Now().Unix() > c.Exp+60:
		return nil, errors.New("ID token expired")
	case nonce == "" || c.Nonce != nonce:
		return nil, errors.New("ID token nonce mismatch")
	}
	return &c, nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// safeNext keeps the return path on this site.
func safeNext(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.HasPrefix(p, "/api/") {
		return "/"
	}
	return p
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// handleOIDCStart sends the browser to the provider.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	issuer, clientID, _, name := s.oidcSettings()
	if issuer == "" || clientID == "" {
		writeErr(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	d, err := s.provider(issuer).discovery(r.Context())
	if err != nil {
		s.log.Warn("oidc discovery", "err", err)
		http.Redirect(w, r, "/login?error="+url.QueryEscape("Cannot reach "+name+" right now"), http.StatusFound)
		return
	}
	state, nonce, verifier := store.RandomToken(16), store.RandomToken(16), store.RandomToken(32)
	next := safeNext(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: strings.Join([]string{state, nonce, verifier, base64.RawURLEncoding.EncodeToString([]byte(next))}, "."),
		Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {s.oidcCallbackURL()},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(d.Auth, "?") {
		sep = "&"
	}
	http.Redirect(w, r, d.Auth+sep+q.Encode(), http.StatusFound)
}

// handleOIDCCallback finishes the sign-in and starts a normal session.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(msg string, err error) {
		if err != nil {
			s.log.Warn("oidc sign-in failed", "msg", msg, "err", err)
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
	}
	issuer, clientID, secret, name := s.oidcSettings()
	if issuer == "" || clientID == "" {
		fail("Single sign-on is not configured", nil)
		return
	}
	c, err := r.Cookie(oidcCookie)
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies})
	if err != nil {
		fail("The sign-in took too long — try again", nil)
		return
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 4 {
		fail("The sign-in took too long — try again", nil)
		return
	}
	state, nonce, verifier := parts[0], parts[1], parts[2]
	nb, _ := base64.RawURLEncoding.DecodeString(parts[3])
	next := safeNext(string(nb))
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(firstNonEmpty(q.Get("error_description"), e), nil)
		return
	}
	if q.Get("state") == "" || q.Get("state") != state {
		fail("The sign-in could not be matched to this browser — try again", nil)
		return
	}
	p := s.provider(issuer)
	d, err := p.discovery(ctx)
	if err != nil {
		fail("Cannot reach "+name, err)
		return
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {s.oidcCallbackURL()},
		"client_id": {clientID}, "code_verifier": {verifier},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	res, err := oidcHTTP.Do(req)
	if err != nil {
		fail("Cannot reach "+name, err)
		return
	}
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || tok.IDToken == "" {
		fail(name+" refused the sign-in", fmt.Errorf("token endpoint HTTP %d: %s %s", res.StatusCode, tok.Error, tok.Desc))
		return
	}
	claims, err := verifyIDToken(ctx, p, d, clientID, tok.IDToken, nonce)
	if err != nil {
		fail("The answer from "+name+" did not check out", err)
		return
	}
	// some providers keep the email out of the ID token
	if (claims.Email == "" || claims.EmailVerified == nil) && d.UserInfo != "" && tok.AccessToken != "" {
		var ui idClaims
		ureq, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.UserInfo, nil)
		ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		if ures, err := oidcHTTP.Do(ureq); err == nil {
			if ures.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(ures.Body, 1<<20)).Decode(&ui)
			}
			ures.Body.Close()
		}
		if ui.Sub == claims.Sub {
			claims.Email, claims.EmailVerified = firstNonEmpty(claims.Email, ui.Email), ui.EmailVerified
			claims.Name = firstNonEmpty(claims.Name, ui.Name)
		}
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || claims.EmailVerified == nil || !*claims.EmailVerified {
		fail("Your "+name+" account needs a verified email address", nil)
		return
	}
	u, err := s.identityUser(ctx, claims.Iss, claims.Sub, email, claims.Name, (s.cfg.OIDCAutoCreate || s.oidcFromCore()))
	if err != nil {
		fail("No Pocket AI account for "+email+" or identity could not be linked", err)
		return
	}

	id, err := s.store.CreateSession(ctx, u.ID, s.cfg.SessionTTL)
	if err != nil {
		fail("Could not start a session", err)
		return
	}
	if err := s.core.TrackSession(ctx, id, claims.Iss, claims.Sub, claims.AuthRevision); err != nil {
		_ = s.store.DeleteSession(ctx, id)
		fail("Could not start a revocable session", err)
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	s.log.Info("signed in", "email", email, "via", "oidc")
	http.Redirect(w, r, next, http.StatusFound)
}
