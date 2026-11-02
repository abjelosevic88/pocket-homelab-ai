package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Files the agent sent the person (Claude Code's SendUserFile). The CLI only
// hands over the path; Pocket AI copies the file as soon as it is sent (the
// agent may change or delete it later, a worktree may be cleaned up) and
// serves the copy: DATA_DIR/sent/<thread>/<item>-<n>.

// sentMax: bigger files are not copied (the card says so).
const sentMax = 100 << 20

// sentRead prints a file as base64 (one way for this server, a person's
// jail and a machine over ssh). $1 path, $2 the most bytes.
const sentRead = `[ -f "$1" ] || { echo "it is not there any more" >&2; exit 3; }
n=$(wc -c < "$1") || exit 3
[ "$n" -le "$2" ] || { echo "it is too big to show here ($n bytes)" >&2; exit 4; }
base64 < "$1"
`

var errSentGone = errors.New("sent file gone")

// sentLocks: one copy of a file at a time (the save after the tool result and
// the first look at it can race).
var sentLocks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func sentLock(k string) func() {
	sentLocks.Lock()
	l := sentLocks.m[k]
	if l == nil {
		l = &sync.Mutex{}
		sentLocks.m[k] = l
	}
	sentLocks.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		sentLocks.Lock()
		delete(sentLocks.m, k)
		sentLocks.Unlock()
	}
}

func (s *Server) sentDir(threadID string) string {
	return filepath.Join(s.cfg.DataDir, "sent", threadID)
}

func (s *Server) sentPath(threadID, key string, n int) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(s.sentDir(threadID), hex.EncodeToString(h[:12])+"-"+strconv.Itoa(n))
}

// keepSent copies what a SendUserFile call sent, right after it is done.
func (s *Server) keepSent(threadID string, it agent.Item) {
	if it.Tool == nil || it.Tool.Kind != "file" || it.Tool.Status != "done" {
		return
	}
	ctx, cancel := context.WithTimeout(s.rt.ctx, 5*time.Minute)
	defer cancel()
	t, err := s.store.GetThreadAny(ctx, threadID)
	if err != nil {
		return
	}
	for n, f := range it.Tool.Files {
		if _, err := s.sentCopy(ctx, t, it.Key, n, f); err != nil {
			s.log.Warn("sent file: no copy", "thread", threadID, "file", f.Path, "err", err)
		}
	}
}

// sentCopy returns the copy of a sent file, making it first when there is none.
func (s *Server) sentCopy(ctx context.Context, t *store.Thread, key string, n int, f agent.SentFile) (string, error) {
	p := s.sentPath(t.ID, key, n)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	defer sentLock(p)()
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	data, err := s.readSent(ctx, t, f.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// readSent reads a file where the thread's agent runs: on this server (in
// its person's jail when they have one) or on its machine over ssh.
func (s *Server) readSent(ctx context.Context, t *store.Thread, path string) ([]byte, error) {
	if path == "" {
		return nil, errSentGone
	}
	if !strings.HasPrefix(path, "/") {
		if t.Cwd == "" {
			return nil, errSentGone
		}
		path = filepath.Join(t.Cwd, path)
	}
	max := strconv.Itoa(sentMax)
	var out, errOut string
	var err error
	h, herr := s.host(ctx, t.HostID)
	if herr != nil {
		return nil, herr
	}
	if h != nil {
		out, errOut, err = remoteOf(h).Run(ctx, 3*time.Minute, sentRead, path, max)
	} else {
		dir := ""
		if s.jailed(ctx, t.UserID) {
			dir = s.personHome(t.UserID)
		}
		cmd, cerr := agent.LocalCmd(ctx, dir, nil, "sh", "-c", sentRead, "sh", path, max)
		if cerr != nil {
			return nil, cerr
		}
		var ob, eb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &ob, &eb
		err = cmd.Run()
		out, errOut = ob.String(), strings.TrimSpace(eb.String())
	}
	if err != nil {
		if errOut == "" {
			return nil, err
		}
		return nil, errors.New(errOut)
	}
	return base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, out))
}

// handleSentFile serves a file the agent sent.
// GET /threads/{id}/sent-file?key=<tool item>&n=<index>[&download=1]
func (s *Server) handleSentFile(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessView)
	if t == nil {
		return
	}
	key := r.URL.Query().Get("key")
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	st, gerr := s.store.GetItem(r.Context(), t.ID, key)
	if key == "" || err != nil || gerr != nil {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	var it agent.Item
	if json.Unmarshal(st.Data, &it) != nil || it.Tool == nil || it.Tool.Kind != "file" || n < 0 || n >= len(it.Tool.Files) {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	f := it.Tool.Files[n]
	p, err := s.sentCopy(r.Context(), t, key, n, f)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Pocket AI has no copy of "+f.Name+": "+err.Error())
		return
	}
	fh, err := os.Open(p)
	if err != nil {
		s.fail(w, err, "sent file")
		return
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		s.fail(w, err, "sent file")
		return
	}
	ctype, active := sentType(f)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	if active {
		// The agent's page runs as nobody: no cookies, no Pocket AI API.
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads")
	}
	disp := "inline"
	if r.URL.Query().Get("download") == "1" || ctype == "application/octet-stream" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(f.Name))
	http.ServeContent(w, r, f.Name, fi.ModTime(), fh)
}

// sentType: what a sent file is served as; active = it can run scripts
// (HTML, SVG, XML) and is fenced off.
func sentType(f agent.SentFile) (ctype string, active bool) {
	ext := strings.ToLower(filepath.Ext(f.Name))
	m := f.Mime
	if m == "" {
		m = mime.TypeByExtension(ext)
	}
	if base, _, err := mime.ParseMediaType(m); err == nil {
		m = base
	}
	switch {
	case m == "text/html" || ext == ".html" || ext == ".htm":
		return "text/html; charset=utf-8", true
	case m == "image/svg+xml" || ext == ".svg":
		return "image/svg+xml", true
	case strings.HasSuffix(m, "xml") || ext == ".xml" || ext == ".xhtml":
		return "text/plain; charset=utf-8", false
	case strings.HasPrefix(m, "image/"), strings.HasPrefix(m, "audio/"), strings.HasPrefix(m, "video/"), m == "application/pdf":
		return m, false
	case strings.HasPrefix(m, "text/"), m == "application/json", m == "application/x-ndjson", m == "application/yaml", m == "application/toml",
		m == "application/javascript", m == "application/x-sh", textExt(ext):
		return "text/plain; charset=utf-8", false
	case m != "":
		return m, false
	}
	return "application/octet-stream", false
}

func textExt(ext string) bool {
	switch ext {
	case ".md", ".markdown", ".txt", ".log", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini", ".conf", ".env",
		".go", ".py", ".js", ".mjs", ".ts", ".tsx", ".jsx", ".css", ".sh", ".zsh", ".bash", ".sql", ".rs", ".rb", ".java", ".kt",
		".swift", ".c", ".h", ".cpp", ".hpp", ".cs", ".php", ".lua", ".nix", ".diff", ".patch":
		return true
	}
	return false
}
