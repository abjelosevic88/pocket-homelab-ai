package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A machine can have more than one login per CLI: besides the default
// (~/.claude, ~/.codex) a folder like ~/.claude-max that a small script
// starts the CLI on (CLAUDE_CONFIG_DIR / CODEX_HOME). Pocket AI finds them
// all and runs a thread on whichever one it was given (Thread.HostHome).

// RemoteHomesSh defines `homes claude|codex|antigravity` for remote scripts:
// the CLI's login folders there, the default first.
const RemoteHomesSh = `homes() {
  if [ "$1" = claude ]; then d0="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; p=.claude; m='.claude.json .credentials.json projects settings.json'
  elif [ "$1" = antigravity ]; then d0="$HOME/.gemini"; p=.gemini; m='antigravity-cli'
  else d0="${CODEX_HOME:-$HOME/.codex}"; p=.codex; m='auth.json config.toml sessions'; fi
  printf '%s\n' "$d0"
  for d in "$HOME/$p"-* "$HOME/$p"_*; do
    [ -d "$d" ] && [ ! -L "$d" ] && [ "$d" != "$d0" ] || continue
    for f in $m; do if [ -e "$d/$f" ]; then printf '%s\n' "$d"; break; fi; done
  done
}
`

// RemoteCodexSh defines, for remote scripts that have RemoteHomesSh too,
// `codex_ask BIN` and `codex_tell`: who is signed in to each Codex login
// folder there, asked of its app-server (account/read) inside the script's
// own ssh connection. A connection per folder was six at once for someone
// with five logins, and machines behind a NAT with few ports per destination
// lost some of them ("Connection timed out", shown as a dead agent).
//
// codex_ask starts every folder's app-server at once and returns; codex_tell
// waits (at most ~20 s) and prints, per folder, "codex_home=<dir>" and then
// "codex_status=<the reply to account/read>" or "codex_error=<why not>".
// On a Mac with pocket-gui the app-server starts in the GUI session, as an
// agent's does (the keychain is unlocked there).
func RemoteCodexSh() string {
	q := func(v any) string {
		b, _ := json.Marshal(v)
		return shq(string(b))
	}
	ask := q(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]any{"name": "pocket-ai", "title": "Pocket AI", "version": Version}}}) + " " +
		q(map[string]any{"method": "initialized"}) + " " +
		q(map[string]any{"id": 2, "method": "account/read", "params": map[string]any{}})
	return guiBridge + `
codex_ask() {
  ct=$(mktemp -d 2>/dev/null) || { ct="${TMPDIR:-/tmp}/pocket-codex.$$"; mkdir -p "$ct"; }
  homes codex >"$ct/list"
  cn=0
  while IFS= read -r d; do
    cn=$((cn+1))
    (
      trap '' PIPE
      f="$ct/$cn"
      mkfifo "$f.in" || exit 1
      if [ $cn = 1 ]; then set -- "$1" app-server; else set -- env "CODEX_HOME=$d" "$1" app-server; fi
      ${g:+"$g" run --} "$@" <"$f.in" >"$f.out" 2>"$f.err" &
      p=$!
      exec 3>"$f.in"
      printf '%s\n' ` + ask + ` >&3 2>/dev/null
      i=0
      while [ $i -lt 40 ] && ! grep -q '"id":2[,}].*}$' "$f.out" 2>/dev/null && kill -0 "$p" 2>/dev/null; do sleep 0.5; i=$((i+1)); done
      exec 3>&-
      kill "$p" 2>/dev/null
      grep '"id":2[,}].*}$' "$f.out" 2>/dev/null | head -n 1 >"$f.res"
    ) &
  done <"$ct/list"
}
codex_tell() {
  wait
  cn=0
  while IFS= read -r d; do
    cn=$((cn+1))
    echo "codex_home=$d"
    if [ -s "$ct/$cn.res" ]; then printf 'codex_status='; cat "$ct/$cn.res"
    else printf 'codex_error=%s\n' "$(tail -n 3 "$ct/$cn.err" 2>/dev/null | tr '\n' ' ')"; fi
  done <"$ct/list"
  rm -rf "$ct"
}
`
}

var homeBaseRe = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`^\.claude[-_][A-Za-z0-9._-]{1,60}$`),
	"codex":  regexp.MustCompile(`^\.codex[-_][A-Za-z0-9._-]{1,60}$`),
	// Antigravity CLI: ~/.gemini-work (agy acp --gemini-dir)
	"antigravity": regexp.MustCompile(`^\.gemini[-_][A-Za-z0-9._-]{1,60}$`),
}

// ValidHostHome: dir is an extra login folder of the CLI right in the
// machine's home (the only place they are looked for).
func ValidHostHome(provider, home, dir string) bool {
	re := homeBaseRe[provider]
	if re == nil || dir == "" || strings.Contains(dir, "..") || !re.MatchString(filepath.Base(dir)) {
		return false
	}
	return home == "" || filepath.Dir(dir) == strings.TrimRight(home, "/")
}

// HomeEnv points the CLI at a login folder ("" = the default: nothing).
func HomeEnv(provider, dir string) []string {
	switch {
	case dir == "" || IsACP(provider): // an ACP agent is given its folder as a flag (Options.LoginDir)
		return nil
	case provider == "codex":
		return []string{"CODEX_HOME=" + dir}
	}
	return []string{"CLAUDE_CONFIG_DIR=" + dir}
}

// HomeLabel names a login folder: "claude-max" for ~/.claude-max.
func HomeLabel(dir string) string { return strings.TrimPrefix(filepath.Base(dir), ".") }

// MirrorHomes are the login folders of a mirror (MirrorSessions), as homes
// whose ids say which folder over there each one is: ids[id] is "" for the
// default, else that folder (under remoteHome).
func MirrorHomes(dir, remoteHome string) (Homes, map[int64]string) {
	var h Homes
	ids := map[int64]string{}
	n := int64(0)
	for _, kind := range []string{"claude", "codex"} {
		ents, _ := os.ReadDir(filepath.Join(dir, kind))
		names := []string{}
		for _, e := range ents {
			if e.IsDir() && (e.Name() == "default" || homeBaseRe[kind].MatchString(e.Name())) {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			n++
			a := AccountHome{ID: n, Dir: filepath.Join(dir, kind, name)}
			if name != "default" {
				ids[n] = strings.TrimRight(remoteHome, "/") + "/" + name
			} else {
				ids[n] = ""
			}
			if kind == "claude" {
				h.Claude = append(h.Claude, a)
			} else {
				h.Codex = append(h.Codex, a)
			}
		}
	}
	return h, ids
}
