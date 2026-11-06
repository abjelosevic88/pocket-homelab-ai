package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A Pocket app's web view signs in to Pocket AI through /app-signin: Allow,
// a one-time code to the app's pocket<app>:// callback, traded with the
// PKCE verifier for a session.
func TestAppSignin(t *testing.T) {
	s, h, _ := testServer(t)
	_, u := person(t, s, h, "reader@example.test", "member", false)
	ts := httptest.NewServer(h)
	defer ts.Close()
	s.cfg.BaseURL = ts.URL
	sid, err := s.store.CreateSession(context.Background(), u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	cb := "pocketbookshelf://ai-signed-in"
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{"challenge": {challenge}, "device": {"iPhone"}, "callback": {cb}}
	get := func(v url.Values, cookie bool) (*http.Response, string) {
		req, _ := http.NewRequest("GET", ts.URL+"/app-signin?"+v.Encode(), nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
		}
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	if res, _ := get(q, false); res.StatusCode != 302 || !strings.Contains(res.Header.Get("Location"), "next=%2Fapp-signin%3F") {
		t.Fatalf("signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, body := get(q, true); res.StatusCode != 200 || !strings.Contains(body, "Pocket Bookshelf") {
		t.Fatalf("question: %d %s", res.StatusCode, body)
	}
	bad := url.Values{"challenge": {challenge}, "callback": {"https://evil.example/x"}}
	if res, _ := get(bad, true); res.StatusCode != 400 {
		t.Fatalf("foreign callback: %d", res.StatusCode)
	}
	allow := func() string {
		req, _ := http.NewRequest("POST", ts.URL+"/app-signin", strings.NewReader(q.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", ts.URL)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != http.StatusSeeOther || loc == nil || !strings.HasPrefix(loc.String(), cb+"?code=") {
			t.Fatalf("allow: %d %v", res.StatusCode, loc)
		}
		return loc.Query().Get("code")
	}
	exchange := func(code, v string) (int, map[string]any) {
		res, err := http.Post(ts.URL+"/api/v1/auth/app/exchange", "application/json", strings.NewReader(`{"code":"`+code+`","verifier":"`+v+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if st, _ := exchange(allow(), strings.Repeat("x", 50)); st != 401 {
		t.Fatalf("wrong verifier: %d", st)
	}
	st, out := exchange(allow(), verifier)
	session, _ := out["session"].(string)
	if st != 200 || session == "" || out["cookie"] != sessionCookie {
		t.Fatalf("exchange: %d %v", st, out)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("session: %v %d", err, res.StatusCode)
	}
	res.Body.Close()
}
