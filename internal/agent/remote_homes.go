package agent

import (
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

// RemoteHomesSh defines `homes claude|codex` for remote scripts: the CLI's
// login folders there, the default first.
const RemoteHomesSh = `homes() {
  if [ "$1" = claude ]; then d0="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; p=.claude; m='.claude.json .credentials.json projects settings.json'
  else d0="${CODEX_HOME:-$HOME/.codex}"; p=.codex; m='auth.json config.toml sessions'; fi
  printf '%s\n' "$d0"
  for d in "$HOME/$p"-* "$HOME/$p"_*; do
    [ -d "$d" ] && [ ! -L "$d" ] && [ "$d" != "$d0" ] || continue
    for f in $m; do if [ -e "$d/$f" ]; then printf '%s\n' "$d"; break; fi; done
  done
}
`

var homeBaseRe = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`^\.claude[-_][A-Za-z0-9._-]{1,60}$`),
	"codex":  regexp.MustCompile(`^\.codex[-_][A-Za-z0-9._-]{1,60}$`),
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
	case dir == "":
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
