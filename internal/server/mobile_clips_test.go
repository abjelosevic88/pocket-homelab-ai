package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestLatestClipSkipsImagesAndPinnedOrder(t *testing.T) {
	s, h, _ := testServer(t)
	a, u := person(t, s, h, "clips@test.local", "member", false)
	a.want("GET", "/api/v1/mobile/clips/latest", nil, http.StatusConflict, nil)

	sealed, _ := s.seal("pcma_clips")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	rows := []any{
		// Pinned first, as Clips lists them, but copied long ago.
		map[string]any{"id": 1, "kind": "text", "content": "old pinned", "pinned": true, "last_copied_at": "2026-01-01T10:00:00Z"},
		map[string]any{"id": 3, "kind": "image", "content": "shot.png", "last_copied_at": "2026-10-27T18:00:00.9Z"},
		map[string]any{"id": 2, "kind": "link", "content": "https://example.com", "title": "Example", "source_device": "MacBook", "last_copied_at": "2026-10-27T18:00:00.53Z"},
		map[string]any{"id": 4, "kind": "text", "content": "same second, earlier", "last_copied_at": "2026-10-27T18:00:00.5Z"},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&rpc)
		if rpc.Params.Name != "pocket_clips_list" {
			t.Errorf("called %q", rpc.Params.Name)
		}
		result := toolResult(map[string]any{"result": map[string]any{"clips": rows}}, false)
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})

	var got latestClip
	a.want("GET", "/api/v1/mobile/clips/latest", nil, 200, &got)
	if got.Content != "https://example.com" || got.Kind != "link" || got.Device != "MacBook" {
		t.Fatalf("latest clip = %+v", got)
	}

	rows = rows[2:3]
	rows[0] = map[string]any{"id": 3, "kind": "image", "content": "shot.png", "last_copied_at": "2026-10-27T18:00:00Z"}
	a.want("GET", "/api/v1/mobile/clips/latest", nil, http.StatusNotFound, nil)
}
