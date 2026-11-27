package server

// Settings → Machines: the newest Claude Code and Codex, and Update on each
// machine. Both CLIs update themselves (claude update, codex update; native
// and standalone installs, package-manager installs go through their
// manager), so Update runs that there and checks the machine again.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// cliPackages: where the newest release of each CLI is published.
var cliPackages = map[string]string{"claude": "@anthropic-ai/claude-code", "codex": "@openai/codex"}

var (
	cliLatestMu sync.Mutex
	cliLatest   = map[string]string{}
	cliLatestAt time.Time
	cliUpdating sync.Map // "host:provider" → true while an update runs
	npmHTTP     = &http.Client{Timeout: 10 * time.Second}
	npmRegistry = "https://registry.npmjs.org/"
)

// latestCLIs is the newest version of each CLI, asked at most once an hour
// (refresh: now); one that cannot be asked keeps what was known.
func latestCLIs(ctx context.Context, refresh bool) (map[string]string, time.Time) {
	cliLatestMu.Lock()
	defer cliLatestMu.Unlock()
	if !refresh && len(cliLatest) == len(cliPackages) && time.Since(cliLatestAt) < time.Hour {
		return copyMap(cliLatest), cliLatestAt
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for id, pkg := range cliPackages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := npmLatest(ctx, pkg); err == nil {
				mu.Lock()
				cliLatest[id] = v
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	cliLatestAt = time.Now()
	return copyMap(cliLatest), cliLatestAt
}

func npmLatest(ctx context.Context, pkg string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, npmRegistry+pkg+"/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	res, err := npmHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New(res.Status)
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.Version == "" {
		return "", errors.New("no version")
	}
	return out.Version, nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Server) handleCLILatest(w http.ResponseWriter, r *http.Request) {
	v, at := latestCLIs(r.Context(), queryBool(r, "refresh"))
	writeJSON(w, http.StatusOK, map[string]any{"versions": v, "checked_at": store.FormatTime(at)})
}

// cliUpdateScript runs the CLI's own update: $1 the CLI.
const cliUpdateScript = `p=$(command -v "$1" 2>/dev/null) || { echo "$1 is not on the PATH there"; exit 127; }
"$p" update </dev/null 2>&1`

// cliInstallScript: $1 the CLI, $2 its installer. Updates it when it is
// there, else installs it with the vendor's script (which checks the
// download's checksum itself) and says where it went.
const cliInstallScript = `if p=$(command -v "$1" 2>/dev/null); then "$p" update </dev/null 2>&1; exit $?; fi
command -v curl >/dev/null 2>&1 || { echo "curl is not installed there"; exit 127; }
t=$(mktemp -d) || exit 3
curl -fsSL "$2" -o "$t/install.sh" && bash "$t/install.sh" </dev/null 2>&1; rc=$?
rm -rf "$t"
[ $rc = 0 ] || exit $rc
PATH="$HOME/.local/bin:$PATH"; command -v "$1" >/dev/null 2>&1 || { echo "installed, but $1 is not on the PATH there"; exit 127; }`

// handleCLIUpdate updates Claude Code or Codex on a machine (0 = this
// server, for an admin; another machine for whoever may manage it).
func (s *Server) handleCLIUpdate(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	ag, _ := agent.ACPAgentByID(provider)
	if _, ok := cliPackages[provider]; !ok && !(ag.Logins && ag.InstallScript != "") {
		writeErr(w, http.StatusBadRequest, "that agent is not installed or updated from here")
		return
	}
	// an agent with an installer (Antigravity CLI): installed when it is not
	// there yet, else its own update
	script, args := cliUpdateScript, []string{}
	if ag.InstallScript != "" {
		script, args = cliInstallScript, []string{ag.InstallScript}
	}
	var h *store.Host
	if chi.URLParam(r, "id") == "0" {
		if !s.requireAdmin(w, r) || !s.requireRun(w, r) {
			return
		}
	} else if h = s.manageHost(w, r); h == nil {
		return
	}
	key := chi.URLParam(r, "id") + ":" + provider
	if _, busy := cliUpdating.LoadOrStore(key, true); busy {
		writeErr(w, http.StatusConflict, "an update is already running there")
		return
	}
	defer cliUpdating.Delete(key)

	ctx := context.WithoutCancel(r.Context()) // a closed page does not cut an install in half
	var out string
	var err error
	if h == nil {
		bin := s.cfg.ClaudeBin
		if provider == "codex" {
			bin = s.cfg.CodexBin
		}
		if ag.ID != "" {
			bin = ag.Command()
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(c, "sh", append([]string{"-c", script, "sh", bin}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+s.cfg.Home)
		cmd.Dir = s.cfg.Home
		b, e := cmd.CombinedOutput()
		out, err = string(b), e
	} else {
		var stderr string
		out, stderr, err = remoteOf(h).Run(ctx, 5*time.Minute, script, append([]string{hostBin(h, provider)}, args...)...)
		if err != nil && strings.TrimSpace(out) == "" {
			out = sshError(stderr, err)
		}
	}

	version := ""
	if h == nil {
		for _, p := range s.providers(ctx, true) {
			if p.ID == provider && p.Available {
				version = p.Version
			}
		}
	} else {
		h.Info = s.checkHost(ctx, h)
		s.dropHostStatus(h.ID)
		forgetHolder(h.ID)
		_ = s.store.UpdateHost(ctx, h)
		version = h.Info.Versions[provider]
	}
	s.rt.publishAccounts()
	s.touch()
	res := map[string]any{"ok": err == nil, "version": version, "output": lastLines(out, 4)}
	if err != nil {
		res["error"] = "the update did not finish: " + oneLine(firstNonEmpty(lastLines(out, 3), err.Error()), 300)
	}
	s.log.Info("cli update", "host", chi.URLParam(r, "id"), "provider", provider, "version", version, "ok", err == nil)
	writeJSON(w, http.StatusOK, res)
}
