package server

import (
	"strings"
	"testing"
)

func TestLoginError(t *testing.T) {
	out := "Open this link in a browser to log in with Google:\n\n  https://accounts.google.com/o/oauth2/auth?x=1\n\nThen paste the authorization code, or the full URL of the page you land on, and press Enter:\n" +
		"Error: couldn't log in: token exchange failed: Post \"https://oauth2.googleapis.com/token\": dial tcp 142.251.127.95:443: connect: connection refused\n"
	got := loginError(out)
	if strings.Contains(got, "accounts.google.com") || !strings.Contains(got, "connection refused") || !strings.HasPrefix(got, "Could not reach Google") {
		t.Fatalf("network: %q", got)
	}
	if got := loginError("link\nError: login cancelled: no authorization code was entered\n"); got != "login cancelled: no authorization code was entered" {
		t.Fatalf("plain: %q", got)
	}
	if got := loginError("one\ntwo\nthree\nfour"); got != "two · three · four" {
		t.Fatalf("fallback: %q", got)
	}
}
