package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Signing a Pocket app's web view in to Pocket AI (Pocket Bookshelf on iOS
// shows its book chats in a WKWebView). The app opens /app-signin in the
// system's sign-in sheet (ASWebAuthenticationSession), which shares Safari's
// cookies and can use passkeys — a web view can do neither, so Pocket Core's
// sign-in through a passkey-only provider never finished in it. Signed in,
// the person allows the app here; it gets a one-time code at its callback
// (pocket<app>://…) and trades it, with the verifier only it knows (PKCE,
// S256), for a Pocket AI session it puts in its web view.

// appCallbackRe: a Pocket app's own address for the answer.
var appCallbackRe = regexp.MustCompile(`^pocket([a-z]{2,20})://[a-z-]{1,30}$`)

// appName is what the question calls the app ("pocketbookshelf" → Pocket Bookshelf).
func appName(callback string) string {
	m := appCallbackRe.FindStringSubmatch(callback)
	if m == nil {
		return ""
	}
	return "Pocket " + strings.ToUpper(m[1][:1]) + m[1][1:]
}

var appChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

type appCode struct {
	userID    int64
	device    string
	challenge string
	callback  string
	expires   time.Time
}

var appCodes = struct {
	sync.Mutex
	m map[string]appCode
}{m: map[string]appCode{}}

func appDevice(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		s = "iPhone"
	}
	return s
}

var appSigninPage = template.Must(template.New("app").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Allow {{.App}}</title>
<style>
:root { color-scheme: light dark; --accent: #7c3aed; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; font: 16px/1.5 -apple-system, system-ui, sans-serif; background: Canvas; color: CanvasText; }
main { width: min(380px, 100% - 40px); text-align: center; }
img { width: 64px; height: 64px; border-radius: 16px; }
h1 { font-size: 22px; margin: 14px 0 6px; }
p { margin: 0 0 22px; opacity: .75; }
button, a.btn { display: block; width: 100%; box-sizing: border-box; font: inherit; font-weight: 600; padding: 13px; border-radius: 12px; border: 0; cursor: pointer; text-decoration: none; }
button { background: var(--accent); color: #fff; margin-bottom: 10px; }
a.btn { color: var(--accent); background: transparent; }
</style></head><body><main>
<img src="/apple-touch-icon.png" alt="" onerror="this.remove()">
<h1>Open Pocket AI in {{.App}}?</h1>
<p>{{.App}} on <b>{{.Device}}</b> shows your Pocket AI chats in the app, signed in as <b>{{.Who}}</b>. Signing out of Pocket AI there ends it.</p>
<form method="post" action="/app-signin">
<input type="hidden" name="challenge" value="{{.Challenge}}"><input type="hidden" name="device" value="{{.Device}}"><input type="hidden" name="callback" value="{{.Callback}}">
<button type="submit">Allow</button>
</form>
<a class="btn" href="{{.Cancel}}">Cancel</a>
</main></body></html>`))

// GET /app-signin?challenge=&device=&callback= : sign in first if needed, then ask.
func (s *Server) handleAppSignin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !appChallenge.MatchString(q.Get("challenge")) || appName(q.Get("callback")) == "" {
		http.Error(w, "This page is for a Pocket app.", http.StatusBadRequest)
		return
	}
	u, _ := s.resolveUser(r)
	if u == nil {
		back := "/app-signin?" + url.Values{"challenge": {q.Get("challenge")}, "device": {appDevice(q.Get("device"))}, "callback": {q.Get("callback")}}.Encode()
		if s.oidcEnabled() {
			http.Redirect(w, r, "/api/v1/auth/oidc/start?next="+url.QueryEscape(back), http.StatusFound)
		} else {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(back), http.StatusFound)
		}
		return
	}
	who := u.Name
	if who == "" {
		who = u.Email
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = appSigninPage.Execute(w, map[string]string{"App": appName(q.Get("callback")), "Device": appDevice(q.Get("device")), "Who": who,
		"Challenge": q.Get("challenge"), "Callback": q.Get("callback"), "Cancel": q.Get("callback") + "?error=cancelled"})
}

// POST /app-signin (the Allow button) → <callback>?code=
func (s *Server) handleAppSigninAllow(w http.ResponseWriter, r *http.Request) {
	callback := r.PostFormValue("callback")
	if appName(callback) == "" {
		http.Error(w, "This page is for a Pocket app.", http.StatusBadRequest)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" && s.cfg.BaseURL != "" && !strings.EqualFold(o, originOf(s.cfg.BaseURL)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	u, _ := s.resolveUser(r)
	challenge := r.PostFormValue("challenge")
	if u == nil || !appChallenge.MatchString(challenge) {
		http.Redirect(w, r, callback+"?error=signin", http.StatusSeeOther)
		return
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	code := hex.EncodeToString(b)
	appCodes.Lock()
	for k, c := range appCodes.m {
		if time.Now().After(c.expires) {
			delete(appCodes.m, k)
		}
	}
	appCodes.m[code] = appCode{userID: u.ID, device: appDevice(r.PostFormValue("device")), challenge: challenge, callback: callback, expires: time.Now().Add(3 * time.Minute)}
	appCodes.Unlock()
	http.Redirect(w, r, callback+"?code="+code, http.StatusSeeOther)
}

// POST /api/v1/auth/app/exchange {code, verifier} → {cookie, session, expires_at}: a
// Pocket AI session for the app's web view.
func (s *Server) handleAppExchange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	appCodes.Lock()
	c, ok := appCodes.m[in.Code]
	delete(appCodes.m, in.Code) // one try
	appCodes.Unlock()
	sum := sha256.Sum256([]byte(in.Verifier))
	if !ok || time.Now().After(c.expires) || subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.challenge)) != 1 {
		writeErr(w, http.StatusUnauthorized, "That sign-in has expired. Try again.")
		return
	}
	id, err := s.store.CreateSession(r.Context(), c.userID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.log.Info("app signed in", "app", appName(c.callback), "device", c.device, "user", c.userID)
	writeJSON(w, http.StatusOK, map[string]any{"cookie": sessionCookie, "session": id, "secure": s.cfg.SecureCookies,
		"expires_at": time.Now().Add(s.cfg.SessionTTL).UTC().Format(time.RFC3339)})
}

func originOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
