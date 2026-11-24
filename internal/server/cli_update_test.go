package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLILatestAndUpdate(t *testing.T) {
	s, h, root := testServer(t)
	admin, _ := person(t, s, h, "admin@example.com", "admin", true)
	other, _ := person(t, s, h, "bob@example.com", "user", true)

	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/@anthropic-ai/claude-code/latest":
			_, _ = w.Write([]byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.331"}`))
		case "/@openai/codex/latest":
			_, _ = w.Write([]byte(`{"name":"@openai/codex","version":"0.172.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer reg.Close()
	old := npmRegistry
	npmRegistry = reg.URL + "/"
	defer func() { npmRegistry = old }()

	var latest struct {
		Versions map[string]string `json:"versions"`
	}
	admin.want("GET", "/api/v1/cli/latest?refresh=1", nil, 200, &latest)
	if latest.Versions["claude"] != "2.1.331" || latest.Versions["codex"] != "0.172.0" {
		t.Fatalf("latest = %v", latest.Versions)
	}

	// a stand-in CLI: "update" leaves a mark
	mark := filepath.Join(root, "updated")
	bin := filepath.Join(root, "fake-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = update ]; then echo \"Successfully updated\"; touch "+mark+"; exit 0; fi\necho '2.1.331 (Claude Code)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.cfg.ClaudeBin = bin

	other.want("POST", "/api/v1/hosts/0/update/claude", nil, 403, nil)
	admin.want("POST", "/api/v1/hosts/0/update/nope", nil, 400, nil)
	var res struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	admin.want("POST", "/api/v1/hosts/0/update/claude", nil, 200, &res)
	if !res.OK || !strings.Contains(res.Output, "Successfully updated") {
		t.Fatalf("update = %+v", res)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("claude update did not run")
	}
}
