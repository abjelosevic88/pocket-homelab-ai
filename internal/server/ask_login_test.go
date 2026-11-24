package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLogin(t *testing.T, path, refresh string, expires, refreshExpires int64, ended string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "a", "refreshToken": refresh, "expiresAt": expires,
		"refreshTokenExpiresAt": refreshExpires, "loginEnded": ended}})
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func refreshOf(t *testing.T, path string) string {
	t.Helper()
	l, _ := readLogin(path)
	return l.RefreshToken
}

func TestShareLogin(t *testing.T) {
	now := time.Now().UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	setup := func(t *testing.T) (src, dest string) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "acct"), 0700)
		os.MkdirAll(filepath.Join(dir, "ask"), 0700)
		return filepath.Join(dir, "acct", ".credentials.json"), filepath.Join(dir, "ask", ".credentials.json")
	}
	isLink := func(t *testing.T, src, dest string) {
		t.Helper()
		if to, err := os.Readlink(dest); err != nil || to != src {
			t.Fatalf("dest is not a link to src: %q %v", to, err)
		}
	}

	t.Run("links a new folder", func(t *testing.T) {
		src, dest := setup(t)
		writeLogin(t, src, "r1", now+day, now+30*day, "")
		if err := shareLogin(src, dest); err != nil {
			t.Fatal(err)
		}
		isLink(t, src, dest)
	})

	t.Run("ended copy is replaced by the account's login", func(t *testing.T) {
		src, dest := setup(t)
		writeLogin(t, src, "fresh", now+day, now+13*day, "")
		writeLogin(t, dest, "", 0, now-2*day, "time_limit")
		if err := shareLogin(src, dest); err != nil {
			t.Fatal(err)
		}
		isLink(t, src, dest)
		if refreshOf(t, src) != "fresh" {
			t.Fatal("account login changed")
		}
	})

	t.Run("copy renewed by Ask goes to the account first", func(t *testing.T) {
		src, dest := setup(t)
		writeLogin(t, src, "old", now-time.Hour.Milliseconds(), now+10*day, "")
		writeLogin(t, dest, "renewed", now+8*time.Hour.Milliseconds(), now+10*day, "")
		if err := shareLogin(src, dest); err != nil {
			t.Fatal(err)
		}
		isLink(t, src, dest)
		if refreshOf(t, src) != "renewed" {
			t.Fatal("renewed login not kept")
		}
		if fi, _ := os.Stat(src); fi.Mode().Perm() != 0600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	})

	t.Run("account not signed in keeps a working copy", func(t *testing.T) {
		src, dest := setup(t)
		writeLogin(t, dest, "only", now+day, now+10*day, "")
		if err := shareLogin(src, dest); err != nil {
			t.Fatal(err)
		}
		isLink(t, src, dest)
		if refreshOf(t, src) != "only" {
			t.Fatal("copy not moved to the account")
		}
	})

	t.Run("nothing anywhere", func(t *testing.T) {
		src, dest := setup(t)
		if err := shareLogin(src, dest); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatal("made something out of nothing")
		}
	})
}
