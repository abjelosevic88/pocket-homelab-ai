package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestAskApprovalIsEnforcedByServer(t *testing.T) {
	s, h, root := testServer(t)
	a, u := person(t, s, h, "ask@example.test", "member", false)
	b, _ := person(t, s, h, "other-ask@example.test", "member", false)
	th := thread(t, s, u.ID, root)
	th.Source = "ask"
	_, _ = s.store.DB.Exec(`UPDATE threads SET source='ask' WHERE id=?`, th.ID)
	_, _ = s.store.DB.Exec(`INSERT INTO ask_contexts VALUES(?,?)`, th.ID, `{"app":"notes","url":"https://notes.test/notes/1"}`)
	sealed, _ := s.seal("pcma_test_connection")
	_, _ = s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	var sent atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("Authorization") != "Bearer pcma_test_connection" {
			t.Error("wrong Core credential")
		}
		var rpc struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		w.Header().Set("Content-Type", "application/json")
		if rpc.Method == "tools/list" {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"pocket_notes_get","annotations":{"readOnlyHint":true}},{"name":"pocket_notes_update","description":"Update this note","annotations":{"readOnlyHint":false}}]}}`))
			return
		}
		sent.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"saved"}]}}`))
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &run{threadID: th.ID, userID: u.ID, pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}, lastUsed: time.Now()}
	s.rt.mu.Lock()
	s.rt.runs[th.ID] = r
	s.rt.mu.Unlock()
	defer func() { s.rt.mu.Lock(); delete(s.rt.runs, th.ID); s.rt.mu.Unlock() }()
	var opts agent.Options
	if err := s.prepareAsk(ctx, th, &runSink{rt: s.rt, r: r}, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.Restricted || opts.Mode != "default" {
		t.Fatal("Ask was not restricted")
	}
	raw, _ := os.ReadFile(opts.MCPConfig)
	if strings.Contains(string(raw), "pcma_test") {
		t.Fatal("Core credential exposed to CLI")
	}
	var conf struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal(raw, &conf)
	server := conf.Servers["pocket"]
	call := func(name string) int {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"id": 1, "data": map[string]string{"title": "New"}}}})
		req, _ := http.NewRequest("POST", server.URL, bytes.NewReader(body))
		req.Header.Set("Authorization", server.Headers["Authorization"])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if call("pocket_notes_get") != 200 || sent.Load() != 1 {
		t.Fatal("read did not reach Core")
	}
	for _, decision := range []string{"allow", "deny"} {
		before := sent.Load()
		done := make(chan int, 1)
		go func() { done <- call("pocket_notes_update") }()
		id := ""
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			r.mu.Lock()
			for pending := range r.pending {
				id = pending
			}
			r.mu.Unlock()
			if id != "" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if id == "" {
			t.Fatal("write failed to ask for approval")
		}
		if sent.Load() != before {
			t.Fatal("write ran before approval")
		}
		b.want("POST", "/api/v1/threads/"+th.ID+"/requests/"+id, map[string]string{"decision": "allow"}, 403, nil)
		a.want("POST", "/api/v1/threads/"+th.ID+"/requests/"+id, map[string]string{"decision": "allow_session"}, 400, nil)
		a.want("POST", "/api/v1/threads/"+th.ID+"/requests/"+id, map[string]string{"decision": decision}, 204, nil)
		select {
		case code := <-done:
			if code != 200 {
				t.Fatal(code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("approval did not finish")
		}
		want := before
		if decision == "allow" {
			want++
		}
		if sent.Load() != want {
			t.Fatal("incorrect dispatch after approval")
		}
	}
	a.want("PATCH", "/api/v1/threads/"+th.ID, map[string]string{"mode": "bypassPermissions"}, 400, nil)
	a.want("POST", "/api/v1/threads/"+th.ID+"/fork", map[string]any{}, 400, nil)
	a.want("POST", "/api/v1/ask/connect", map[string]string{"token": "pcma_other"}, 403, nil)
	b.want("GET", "/api/v1/ask/threads/"+th.ID, nil, 404, nil)
	b.want("GET", "/api/v1/ask-context/"+th.ID, nil, 404, nil)
	if call("Bash") != 200 || sent.Load() != 2 {
		t.Fatal("unknown tool reached Core")
	}
}
