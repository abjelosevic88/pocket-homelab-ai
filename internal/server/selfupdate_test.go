package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "pocket-ai_v0.2.0_linux_amd64/README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

type fakeCore struct {
	srv     *httptest.Server
	mu      sync.Mutex
	states  []string
	queries []string
}

func newFakeCore(t *testing.T, archive []byte, sha string, apply bool) *fakeCore {
	f := &fakeCore{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat_test" {
			http.Error(w, "no", 401)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/apps/self/update" && r.Method == "GET":
			f.mu.Lock()
			f.queries = append(f.queries, r.URL.RawQuery)
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"latest": "0.2.0", "program": "pocket-ai", "apply": apply, "target": "0.2.0",
				"file": map[string]any{"url": f.srv.URL + "/dl/pocket-ai.tar.gz", "sha256": sha, "size": len(archive)}})
		case r.URL.Path == "/api/v1/apps/self/update" && r.Method == "POST":
			var in struct{ State, Message string }
			json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.states = append(f.states, in.State+": "+in.Message)
			f.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// selfUpdateSetup: a server linked to the fake Core, an "installed" program
// that says it is 0.1.0, and a release whose program says 0.2.0.
func selfUpdateSetup(t *testing.T, badSHA, apply bool, managed string) (*Server, *fakeCore, string, *chan struct{}) {
	t.Helper()
	s, _, root := testServer(t)
	exe := filepath.Join(root, "bin", "pocket-ai")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\necho pocket-ai 0.1.0\n"), 0o755)
	archive := tarGz(t, "pocket-ai_v0.2.0_linux_amd64/pocket-ai", []byte("#!/bin/sh\necho pocket-ai 0.2.0\n"))
	sum := sha256.Sum256(archive)
	sha := hex.EncodeToString(sum[:])
	if badSHA {
		sha = strings.Repeat("0", 64)
	}
	f := newFakeCore(t, archive, sha, apply)
	mux := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl/pocket-ai.tar.gz" {
			w.Write(archive)
			return
		}
		mux.ServeHTTP(w, r)
	})
	tokFile := filepath.Join(root, "core.token")
	os.WriteFile(tokFile, []byte("pat_test"), 0o600)
	s.cfg.CoreURL = f.srv.URL
	s.core = core.New(f.srv.URL, core.Options{TokenFile: tokFile})

	oldV, oldT, oldR, oldH := Version, selfUpdateTarget, restartForUpdate, selfUpdateAllowHTTP
	t.Cleanup(func() { Version, selfUpdateTarget, restartForUpdate, selfUpdateAllowHTTP = oldV, oldT, oldR, oldH })
	Version, selfUpdateAllowHTTP = "0.1.0", true
	selfUpdateTarget = func() (string, string) { return exe, managed }
	restarted := make(chan struct{}, 1)
	restartForUpdate = func() error { restarted <- struct{}{}; return nil }
	return s, f, exe, &restarted
}

func waitSelfUpdate(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	time.Sleep(50 * time.Millisecond)
	for selfUpdateBusy.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSelfUpdateInstallsAndRestarts(t *testing.T) {
	s, f, exe, restarted := selfUpdateSetup(t, false, true, "")
	s.selfUpdateOnce(context.Background())
	waitSelfUpdate(t)
	select {
	case <-*restarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("no restart; reports %v", f.states)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "0.2.0") {
		t.Fatalf("program not replaced: %s", b)
	}
	if b, _ := os.ReadFile(exe + ".bak-0.1.0"); !strings.Contains(string(b), "0.1.0") {
		t.Fatalf("old version not kept: %s", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("left a .new file")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) != 2 || !strings.HasPrefix(f.states[0], "downloading") || f.states[1] != "installed: Installed 0.2.0; restarting now" {
		t.Fatalf("reports: %v", f.states)
	}
	if q := f.queries[0]; !strings.Contains(q, "version=0.1.0") || !strings.Contains(q, "os=") || strings.Contains(q, "managed") {
		t.Fatalf("query: %s", q)
	}
}

func TestSelfUpdateRefusesBadChecksum(t *testing.T) {
	s, f, exe, restarted := selfUpdateSetup(t, true, true, "")
	s.selfUpdateOnce(context.Background())
	waitSelfUpdate(t)
	select {
	case <-*restarted:
		t.Fatal("restarted after a bad download")
	default:
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "0.1.0") {
		t.Fatalf("program replaced: %s", b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) != 2 || !strings.HasPrefix(f.states[1], "failed: ") || !strings.Contains(f.states[1], "SHA-256") {
		t.Fatalf("reports: %v", f.states)
	}
}

func TestSelfUpdateManagedOnlyReports(t *testing.T) {
	s, f, exe, _ := selfUpdateSetup(t, false, true, "updated with the system (Pocket Box or Nix)")
	s.selfUpdateOnce(context.Background())
	waitSelfUpdate(t)
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "0.1.0") {
		t.Fatalf("program replaced: %s", b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) != 0 || !strings.Contains(f.queries[0], "managed=updated+with+the+system") {
		t.Fatalf("states %v query %v", f.states, f.queries)
	}
}

func TestSelfUpdateNoApplyNoChange(t *testing.T) {
	s, f, exe, _ := selfUpdateSetup(t, false, false, "")
	s.selfUpdateOnce(context.Background())
	waitSelfUpdate(t)
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "0.1.0") {
		t.Fatalf("program replaced: %s", b)
	}
	if len(f.states) != 0 {
		t.Fatalf("reports: %v", f.states)
	}
}
