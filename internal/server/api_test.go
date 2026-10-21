package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// testServer runs without real CLIs: the binaries point nowhere, so nothing
// can start an agent by accident.
func testServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AUTH_MODE", "local")
	t.Setenv("TZ", "Europe/Sarajevo")
	t.Setenv("CORE_URL", "")
	t.Setenv("BASE_URL", "https://ai.example")
	t.Setenv("AGENT_HOME", root)
	t.Setenv("ROOTS", root)
	t.Setenv("CLAUDE_BIN", filepath.Join(root, "no-claude"))
	t.Setenv("CODEX_BIN", filepath.Join(root, "no-codex"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(cfg, st, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Router(), root
}

type client struct {
	t     *testing.T
	h     http.Handler
	token string
}

func (c *client) do(method, path, ctype string, body []byte) (int, []byte) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (c *client) call(method, path string, body any) (int, []byte) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	return c.do(method, path, "application/json", b)
}

func (c *client) want(method, path string, body any, code int, out any) {
	c.t.Helper()
	got, b := c.call(method, path, body)
	if got != code {
		c.t.Fatalf("%s %s: got %d want %d: %s", method, path, got, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: decode: %v (%s)", method, path, err, b)
		}
	}
}

func person(t *testing.T, s *Server, h http.Handler, email, role string, canRun bool) (*client, *store.User) {
	t.Helper()
	u := &store.User{Email: email, Name: email, Role: role, CanRun: canRun}
	if err := s.store.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.store.CreateAPIToken(context.Background(), u.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, h: h, token: tok}, u
}

// thread puts a thread with a timeline straight into the store (no CLI).
func thread(t *testing.T, s *Server, uid int64, cwd string) *store.Thread {
	t.Helper()
	th := &store.Thread{ID: uuid.NewString(), UserID: uid, Provider: "claude", Mode: "default", Cwd: cwd, Title: "Check the disks", ProviderSessionID: "sess-" + uuid.NewString()}
	if err := s.store.CreateThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
	for _, it := range []agent.Item{{Key: "u1", Kind: "user", Text: "how full is the zpool"}, {Key: "a1", Kind: "assistant", Text: "The zpool is 61% full."}} {
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(context.Background(), th.ID, it.Key, it.Kind, data, it.Text); err != nil {
			t.Fatal(err)
		}
	}
	return th
}

func TestEverythingIsPerPerson(t *testing.T) {
	s, h, root := testServer(t)
	alice, au := person(t, s, h, "alice@example.com", "admin", false)
	bob, bu := person(t, s, h, "bob@example.com", "admin", false)

	th := thread(t, s, au.ID, root)
	var rc store.Recipe
	alice.want("POST", "/api/v1/recipes", map[string]any{"name": "Disks", "prompt": "check {{pool}}"}, 201, &rc)
	var sc store.Schedule
	alice.want("POST", "/api/v1/schedules", map[string]any{"name": "Nightly", "prompt": "check", "spec": "daily 03:00"}, 201, &sc)
	var ws store.Workspace
	alice.want("POST", "/api/v1/workspaces", map[string]any{"path": root}, 201, &ws)

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "dot.png")
	_, _ = fw.Write(png)
	_ = mw.Close()
	code, b := alice.do("POST", "/api/v1/uploads", mw.FormDataContentType(), buf.Bytes())
	if code != 201 {
		t.Fatalf("upload: %d %s", code, b)
	}
	var ups []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &ups)

	// Bob is an admin too, and still sees none of it.
	for _, p := range []struct{ method, path string }{
		{"GET", "/api/v1/threads/" + th.ID},
		{"PATCH", "/api/v1/threads/" + th.ID},
		{"DELETE", "/api/v1/threads/" + th.ID},
		{"POST", "/api/v1/threads/" + th.ID + "/messages"},
		{"POST", "/api/v1/threads/" + th.ID + "/interrupt"},
		{"POST", "/api/v1/threads/" + th.ID + "/fork"},
		{"GET", "/api/v1/threads/" + th.ID + "/export.md"},
		{"GET", "/api/v1/threads/" + th.ID + "/log-image?key=u-x&n=0"},
		{"GET", "/api/v1/threads/" + th.ID + "/events"},
		{"PUT", "/api/v1/recipes/" + itoa(rc.ID)},
		{"DELETE", "/api/v1/recipes/" + itoa(rc.ID)},
		{"POST", "/api/v1/recipes/" + itoa(rc.ID) + "/run"},
		{"PUT", "/api/v1/schedules/" + itoa(sc.ID)},
		{"DELETE", "/api/v1/schedules/" + itoa(sc.ID)},
		{"POST", "/api/v1/schedules/" + itoa(sc.ID) + "/run"},
		{"PUT", "/api/v1/workspaces/" + itoa(ws.ID)},
		{"DELETE", "/api/v1/workspaces/" + itoa(ws.ID)},
		{"GET", "/api/v1/uploads/" + ups[0].ID},
		{"DELETE", "/api/v1/uploads/" + ups[0].ID},
	} {
		body := map[string]any{"text": "hi", "name": "x", "prompt": "x", "spec": "daily 03:00", "path": root}
		if code, b := bob.call(p.method, p.path, body); code != http.StatusNotFound {
			t.Errorf("bob %s %s: got %d want 404 (%s)", p.method, p.path, code, b)
		}
	}
	var list []store.Thread
	bob.want("GET", "/api/v1/threads", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("bob sees %d threads", len(list))
	}
	var hits []store.SearchHit
	bob.want("GET", "/api/v1/search?q=zpool", nil, 200, &hits)
	if len(hits) != 0 {
		t.Fatalf("bob finds alice's messages: %+v", hits)
	}
	alice.want("GET", "/api/v1/search?q=zpool", nil, 200, &hits)
	if len(hits) != 1 || hits[0].ThreadID != th.ID {
		t.Fatalf("alice's search: %+v", hits)
	}
	var rs []store.Recipe
	bob.want("GET", "/api/v1/recipes", nil, 200, &rs)
	for _, r := range rs {
		if r.ID == rc.ID {
			t.Fatal("bob sees alice's recipe")
		}
	}
	var ss []store.Schedule
	bob.want("GET", "/api/v1/schedules", nil, 200, &ss)
	if len(ss) != 0 {
		t.Fatal("bob sees alice's schedule")
	}
	_ = bu
}

func TestMembersNeedPermissionToRun(t *testing.T) {
	s, h, root := testServer(t)
	_, admin := person(t, s, h, "admin@example.com", "admin", false)
	member, _ := person(t, s, h, "kid@example.com", "member", false)
	_ = admin
	var me store.User
	member.want("GET", "/api/v1/auth/me", nil, 200, &me)
	if me.CanRun {
		t.Fatal("a member may run by default")
	}
	for _, p := range []struct{ method, path string }{
		{"POST", "/api/v1/threads"}, {"POST", "/api/v1/uploads"}, {"GET", "/api/v1/history"},
		{"POST", "/api/v1/history/import"}, {"GET", "/api/v1/fs/dirs"}, {"POST", "/api/v1/schedules"},
	} {
		if code, b := member.call(p.method, p.path, map[string]any{"text": "rm -rf", "cwd": root}); code != http.StatusForbidden {
			t.Errorf("member %s %s: got %d want 403 (%s)", p.method, p.path, code, b)
		}
	}
}

func TestFoldersStayInsideRoots(t *testing.T) {
	s, _, root := testServer(t)
	inside := filepath.Join(root, "proj")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.checkDir(inside); err != nil {
		t.Fatalf("inside: %v", err)
	}
	for _, p := range []string{"/etc", root + "/../", "relative/path", filepath.Join(root, "missing")} {
		if _, err := s.checkDir(p); err == nil {
			t.Errorf("%s was allowed", p)
		}
	}
	// a symlink out of the root does not count as inside
	link := filepath.Join(root, "out")
	if err := os.Symlink("/etc", link); err == nil {
		if _, err := s.checkDir(link); err == nil {
			t.Error("symlink to /etc was allowed")
		}
	}
}

func TestUnavailableProviderIsAReadableError(t *testing.T) {
	s, h, root := testServer(t)
	c, _ := person(t, s, h, "a@example.com", "admin", false)
	code, b := c.call("POST", "/api/v1/threads", map[string]any{"provider": "claude", "text": "hello", "cwd": root})
	if code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", code, b)
	}
}

func TestScheduleSpecs(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Sarajevo")
	from := time.Date(2026, 10, 21, 17, 40, 0, 0, loc) // a Wednesday
	cases := []struct {
		spec string
		want string
	}{
		{"daily 08:00", "2026-10-22 08:00"},
		{"weekdays 18:30", "2026-10-21 18:30"},
		{"weekly mon 08:00", "2026-10-26 08:00"},
		{"weekly sat,sun 10:15", "2026-10-24 10:15"},
		{"every 6h", "2026-10-21 23:40"},
		{"*/15 9-17 * * mon-fri", "2026-10-21 17:45"},
		{"0 3 1 * *", "2026-11-01 03:00"},
	}
	for _, c := range cases {
		sp, err := ParseSpec(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := sp.Next(from, loc).In(loc).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s: got %s want %s", c.spec, got, c.want)
		}
	}
	for _, bad := range []string{"", "every 1m", "daily 25:00", "weekly funday 08:00", "* * *", "61 * * * *"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRecipePlaceholders(t *testing.T) {
	got, err := fill("check {{ app }} on {{host}}", map[string]string{"app": "jellyfin", "host": "nas"})
	if err != nil || got != "check jellyfin on nas" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := fill("check {{app}}", nil); err == nil {
		t.Fatal("missing var accepted")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
