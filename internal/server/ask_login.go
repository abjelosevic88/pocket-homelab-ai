package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Pocket Ask runs Claude Code in a folder of its own, signed in with the
// login of the account it uses: that folder's .credentials.json is a link to
// the account's file, so there is one login, and signing in again in
// Settings → Machines reaches Ask too.
//
// Claude Code saves a renewed login as a new file, which replaces the link
// with a copy that then lives on by itself: the account is signed in again,
// the copy is not, and Ask fails with "this login reached its time limit".
// shareLogin runs before every Ask run and puts the link back; a copy that
// holds the newer sign-in (Ask renewed it last) goes to the account first, so
// nothing is lost either way.

var shareLoginMu sync.Mutex

func shareLogin(src, dest string) error {
	shareLoginMu.Lock()
	defer shareLoginMu.Unlock()
	fi, err := os.Lstat(dest)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		if to, _ := os.Readlink(dest); to == src {
			return nil
		}
	case fi.Mode().IsRegular():
		if newerLogin(dest, src) {
			if err := copyLogin(dest, src); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(src); err != nil {
		return nil // the account is not signed in: nothing to link to yet
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(src, dest)
}

type claudeLogin struct {
	RefreshToken          string `json:"refreshToken"`
	ExpiresAt             int64  `json:"expiresAt"`
	RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"`
	LoginEnded            string `json:"loginEnded"`
}

// readLogin reads a Claude Code login file; ok = it can still sign in.
func readLogin(path string) (l claudeLogin, ok bool) {
	var f struct {
		OAuth claudeLogin `json:"claudeAiOauth"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &f) != nil {
		return l, false
	}
	l = f.OAuth
	live := l.RefreshTokenExpiresAt == 0 || l.RefreshTokenExpiresAt > time.Now().UnixMilli()
	return l, l.RefreshToken != "" && l.LoginEnded == "" && live
}

// newerLogin: a can sign in and b cannot, or a is a later sign-in, or the
// same sign-in renewed later (its refresh token is the one still good).
func newerLogin(a, b string) bool {
	la, okA := readLogin(a)
	if !okA {
		return false
	}
	lb, okB := readLogin(b)
	switch {
	case !okB:
		return true
	case la.RefreshTokenExpiresAt != lb.RefreshTokenExpiresAt:
		return la.RefreshTokenExpiresAt > lb.RefreshTokenExpiresAt
	default:
		return la.ExpiresAt > lb.ExpiresAt
	}
}

// copyLogin writes from's login over to (through a link, if to is one),
// readable by the owner only.
func copyLogin(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(to); err == nil {
		to = real
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(to), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Chmod(0600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), to)
}
