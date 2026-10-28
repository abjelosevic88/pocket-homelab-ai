package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AccountHome is the config folder of one CLI login. ID 0 is the server's
// own login (~/.claude, ~/.codex); the others are accounts added in Settings,
// each in a folder of its own (CLAUDE_CONFIG_DIR / CODEX_HOME).
type AccountHome struct {
	ID  int64
	Dir string
}

// Homes are the config folders of every login, per CLI.
type Homes struct {
	Claude []AccountHome
	Codex  []AccountHome
}

func (h Homes) each(fn func(provider string, a AccountHome)) {
	for _, a := range h.Claude {
		if a.Dir != "" {
			fn("claude", a)
		}
	}
	for _, a := range h.Codex {
		if a.Dir != "" {
			fn("codex", a)
		}
	}
}

// Of returns the folders of one CLI.
func (h Homes) Of(provider string) []AccountHome {
	if provider == "codex" {
		return h.Codex
	}
	return h.Claude
}

// AccountEnv points a CLI at an account's folder. The server's own login
// (dir "") needs nothing: the CLI finds its default folder by itself.
func AccountEnv(provider, dir string) []string {
	if dir == "" {
		return nil
	}
	if provider == "codex" {
		return []string{"CODEX_HOME=" + dir}
	}
	return []string{"CLAUDE_CONFIG_DIR=" + dir}
}

// SessionPath finds the file of a session in an account folder.
func SessionPath(provider, dir, sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\") || strings.Contains(sessionID, "..") {
		return "", errors.New("bad session id")
	}
	if provider == "claude" {
		return FindClaude(dir, sessionID)
	}
	found := ""
	stop := errors.New("found")
	_ = filepath.WalkDir(filepath.Join(dir, "sessions"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, "-"+sessionID+".jsonl") {
			found = path
			return stop
		}
		return nil
	})
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

// CopySession puts a session's file into another account folder (same path
// below the folder), so that account can resume it. Claude's sub-agent logs
// (a folder named after the session) come along. An existing copy is
// replaced only by a longer one: the session may have gone on there.
func CopySession(provider, sessionID, fromDir, toDir string) (string, error) {
	src, err := SessionPath(provider, fromDir, sessionID)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(fromDir, src)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("session file outside its folder")
	}
	dst := filepath.Join(toDir, rel)
	if fi, err := os.Stat(dst); err == nil && fi.Size() >= FileSize(src) {
		return dst, nil
	}
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	if provider == "claude" {
		side := strings.TrimSuffix(src, ".jsonl")
		if fi, err := os.Stat(side); err == nil && fi.IsDir() {
			_ = filepath.WalkDir(side, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				r, _ := filepath.Rel(fromDir, path)
				return copyFile(path, filepath.Join(toDir, r))
			})
		}
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	if !Confined(src) || !Confined(dst) {
		return &os.PathError{Op: "copy", Path: dst, Err: errOutside}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := OpenConfined(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".pocket-copy"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// SharedSetup is what a new account folder borrows from the server's own
// login (symlinks, so a change shows up for every account). Logins, history
// and the CLI's own state stay per account.
var SharedSetup = map[string][]string{
	"claude": {"settings.json", "CLAUDE.md", "skills", "agents", "commands", "plugins", "hooks", "output-styles"},
	"codex":  {"config.toml", "AGENTS.md", "prompts", "skills", "rules"},
}

// ShareSetup links the shared parts of defDir into dir (missing ones skipped,
// anything already in dir left alone).
func ShareSetup(provider, defDir, dir string) {
	for _, name := range SharedSetup[provider] {
		src := filepath.Join(defDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(dir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		_ = os.Symlink(src, dst)
	}
}

// CodexAccount asks Codex who is signed in (env points it at an account
// folder; on another machine when remote is given).
func CodexAccount(ctx context.Context, bin string, env []string, remote ...*Remote) (email, plan string, signedIn bool, err error) {
	dir := os.TempDir()
	if len(remote) > 0 && remote[0] != nil {
		dir = "" // its home
	}
	c, err := startRPC(bin, dir, env, remote...)
	if err != nil {
		return "", "", false, err
	}
	defer c.close()
	if err := c.initialize(ctx, Version); err != nil {
		return "", "", false, err
	}
	res, err := c.call(ctx, "account/read", map[string]any{})
	if err != nil {
		return "", "", false, err
	}
	var r struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", "", false, err
	}
	if r.Account == nil {
		return "", "", false, nil
	}
	plan = strings.ToUpper(r.Account.PlanType)
	if plan == "" {
		plan = r.Account.Type
	}
	return r.Account.Email, plan, true, nil
}
