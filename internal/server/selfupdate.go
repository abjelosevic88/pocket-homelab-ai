package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Updates through Pocket Core. Pocket AI runs as a program on the machine,
// not a container, so Core's installer cannot swap it. Instead it asks
// Core about once a minute (GET /api/v1/apps/self/update with its app
// token) whether to update. When someone presses Update on Core's Apps
// page, or Core's nightly pass does, the answer names the release file for
// this OS and CPU with its SHA-256 (from Core's signed app catalog). Pocket
// AI downloads it, checks the hash, runs the new program once (--version),
// keeps the old one as <program>.bak-<version>, moves the new one in place
// and restarts the way deploy/update.sh does: once no agent is working.

const selfUpdateEvery = time.Minute

// selfUpdateMaxSize bounds a release download.
const selfUpdateMaxSize = 512 << 20

var semverPlain = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

type selfUpdateFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type selfUpdateAnswer struct {
	Latest  string          `json:"latest"`
	Program string          `json:"program"`
	Apply   bool            `json:"apply"`
	Target  string          `json:"target"`
	File    *selfUpdateFile `json:"file"`
}

// selfUpdateBusy keeps two updates from running at once.
var selfUpdateBusy atomic.Bool

// restartForUpdate restarts the server once no agent is working (main's
// SIGUSR1 handler). Tests replace it.
var restartForUpdate = func() error { return syscall.Kill(os.Getpid(), syscall.SIGUSR1) }

func (s *Server) selfUpdateLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second): // registration first
	}
	t := time.NewTicker(selfUpdateEvery)
	defer t.Stop()
	for {
		s.selfUpdateOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// selfUpdateTarget is the program file to replace and, when it cannot be
// replaced from here, why (shown on Core's Apps page instead of Update).
var selfUpdateTarget = func() (exe, managed string) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	switch {
	case err != nil:
		return "", "Pocket AI cannot find its own program file"
	case !semverPlain.MatchString(Version):
		return exe, "a development build (" + Version + "); update it by hand"
	case strings.HasPrefix(exe, "/nix/store/"):
		return exe, "updated with the system (Pocket Box or Nix)"
	case runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") == "",
		runtime.GOOS == "darwin" && os.Getenv("XPC_SERVICE_NAME") == "":
		// Restarting = exiting; without a service manager nothing starts it again.
		return exe, "not running as a service (systemd or launchd) that starts it again; update it by hand"
	}
	if err := syscall.Access(filepath.Dir(exe), 2 /* W_OK */); err != nil {
		return exe, "Pocket AI cannot write to " + filepath.Dir(exe) + "; update it by hand"
	}
	return exe, ""
}

func (s *Server) selfUpdateOnce(ctx context.Context) {
	if !s.core.Configured() || s.core.Token() == "" || selfUpdateBusy.Load() {
		return
	}
	exe, managed := selfUpdateTarget()
	q := url.Values{"os": {runtime.GOOS}, "arch": {runtime.GOARCH}, "version": {Version}}
	if managed != "" {
		q.Set("managed", managed)
	}
	var ans selfUpdateAnswer
	if code, err := s.coreJSON(ctx, http.MethodGet, "/api/v1/apps/self/update?"+q.Encode(), nil, &ans); err != nil {
		if code != http.StatusNotFound { // a Core from before self-updates
			s.log.Debug("update check", "err", err)
		}
		return
	}
	if !ans.Apply || managed != "" || ans.File == nil {
		return
	}
	if !selfUpdateBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer selfUpdateBusy.Store(false)
		ctx := context.WithoutCancel(ctx)
		msg, err := s.applySelfUpdate(ctx, exe, ans)
		if err != nil {
			s.log.Warn("update failed", "to", ans.Target, "err", err)
			s.reportSelfUpdate(ctx, "failed", err.Error())
			return
		}
		s.log.Info("update installed", "from", Version, "to", ans.Target)
		s.reportSelfUpdate(ctx, "installed", msg)
		if err := restartForUpdate(); err != nil {
			s.log.Warn("restart for the update", "err", err)
		}
	}()
}

func (s *Server) reportSelfUpdate(ctx context.Context, state, message string) {
	if _, err := s.coreJSON(ctx, http.MethodPost, "/api/v1/apps/self/update", map[string]string{"state": state, "message": message}, nil); err != nil {
		s.log.Debug("update report", "err", err)
	}
}

// applySelfUpdate puts the release in place; the message says when it runs.
func (s *Server) applySelfUpdate(ctx context.Context, exe string, ans selfUpdateAnswer) (string, error) {
	if !semverPlain.MatchString(ans.Target) {
		return "", fmt.Errorf("not a version: %q", ans.Target)
	}
	program := ans.Program
	if program == "" {
		program = filepath.Base(exe)
	}
	want := strings.ToLower(strings.TrimSpace(ans.File.SHA256))
	if len(want) != 64 {
		return "", errors.New("the catalog gives no SHA-256 for the download")
	}
	s.reportSelfUpdate(ctx, "downloading", "")
	archive, err := downloadRelease(ctx, ans.File.URL, want)
	if err != nil {
		return "", err
	}
	bin, err := programFromTarGz(archive, program)
	if err != nil {
		return "", err
	}
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	defer os.Remove(tmp) // gone after the rename; left over only on failure
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, tmp, "--version").Output()
	if got := lastField(string(out)); err != nil || strings.TrimPrefix(got, "v") != ans.Target {
		return "", fmt.Errorf("the downloaded program does not run here or is not %s (got %q, %v)", ans.Target, strings.TrimSpace(string(out)), err)
	}
	bak := exe + ".bak-" + Version
	if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
		if err := copyFile(exe, bak); err != nil {
			return "", fmt.Errorf("keep the old version: %w", err)
		}
	}
	// The running server keeps the old file open until it restarts.
	if err := os.Rename(tmp, exe); err != nil {
		return "", fmt.Errorf("put the new version in place: %w", err)
	}
	if n := s.rt.busyCount() + len(s.inferenceSlots); n > 0 {
		return fmt.Sprintf("Installed %s; Pocket AI restarts into it once no agent is working (%d working now)", ans.Target, n), nil
	}
	return fmt.Sprintf("Installed %s; restarting now", ans.Target), nil
}

func downloadRelease(ctx context.Context, u, wantSHA string) ([]byte, error) {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" && !(pu.Scheme == "http" && selfUpdateAllowHTTP) {
		return nil, fmt.Errorf("refusing the download address %q", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pocket-ai/"+Version)
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, selfUpdateMaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if len(b) > selfUpdateMaxSize {
		return nil, errors.New("download: larger than 512 MB")
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("the download's SHA-256 is %s, the catalog says %s; not installing it", got, wantSHA)
	}
	return b, nil
}

// selfUpdateAllowHTTP lets tests serve downloads over plain HTTP.
var selfUpdateAllowHTTP = false

// programFromTarGz returns the regular file named program from the archive.
func programFromTarGz(archive []byte, program string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("release archive: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("release archive has no %s", program)
		}
		if err != nil {
			return nil, fmt.Errorf("release archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == program {
			return io.ReadAll(io.LimitReader(tr, selfUpdateMaxSize))
		}
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func lastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// coreJSON calls Pocket Core with the app's own token.
func (s *Server) coreJSON(ctx context.Context, method, p string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.CoreURL, "/")+p, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.core.Token())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("pocket core: HTTP %d", res.StatusCode)
	}
	if out != nil {
		return res.StatusCode, json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
	}
	return res.StatusCode, nil
}
