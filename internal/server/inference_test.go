package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestInferenceScopeAndIsolation(t *testing.T) {
	s, h, root := testServer(t)
	personal, user := person(t, s, h, "inference@example.test", "member", true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		scope, audience, kind := core.InferenceScope, "ai", "delegated"
		switch input.Token {
		case "pcx_wrong_scope":
			scope = "threads:read"
		case "pcx_wrong_audience":
			audience = "money"
		case "pcx_not_delegated":
			kind = "user"
		}
		json.NewEncoder(w).Encode(map[string]any{"active": input.Token != "pcx_revoked", "kind": kind, "scope": scope, "audience": audience,
			"user": map[string]any{"id": 123, "sub": "inference-person", "email": "inference@example.test", "name": "Inference"}})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	// Exercise the real CLI adapter with a deterministic process, never a paid model.
	bin := filepath.Join(root, "fake-claude")
	script := `#!/usr/bin/env python3
import sys,json
args=sys.argv[1:]
if args == ['--version']:
    print('test (Claude Code)')
    sys.exit(0)
assert '--restricted' in args and '--no-session-persistence' in args
assert args[args.index('--tools')+1] == ''
assert json.loads(args[args.index('--mcp-config')+1]) == {'mcpServers':{}}
assert args[args.index('--setting-sources')+1] == ''
for line in sys.stdin:
    m=json.loads(line)
    if m.get('type')=='control_request' and m.get('request_id')=='probe':
        print(json.dumps({'type':'control_response','response':{'request_id':'probe','response':{'models':[{'value':'default','displayName':'Default'},{'value':'sonnet','displayName':'Test Sonnet'},{'value':'opus','displayName':'Test Opus'}]}}}),flush=True)
        continue
    if m.get('type')=='user':
        print(json.dumps({'type':'assistant','message':{'id':'answer','content':[{'type':'text','text':'{"ok":true}'}]}}),flush=True)
        print(json.dumps({'type':'result','subtype':'success','usage':{'input_tokens':3,'output_tokens':4}}),flush=True)
        break
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.cfg.ClaudeBin = bin
	for _, token := range []string{"", personal.token, "pcx_wrong_scope", "pcx_wrong_audience", "pcx_not_delegated", "pcx_revoked"} {
		c := &client{t: t, h: h, token: token}
		code, body := c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private","json":true}`))
		if code != 401 && code != 403 {
			t.Fatalf("%q bypassed inference auth: %d %s", token, code, body)
		}
		code, body = c.do("GET", "/api/v1/inference/models", "", nil)
		if code != 401 && code != 403 {
			t.Fatalf("%q bypassed model auth: %d %s", token, code, body)
		}

	}
	c := &client{t: t, h: h, token: "pcx_valid"}
	code, body := c.do("POST", "/api/v1/inference", "application/json", []byte(`{"system":"Return JSON","prompt":"private","json":true}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/sonnet`) || !strings.Contains(string(body), `"input_tokens":3`) {
		t.Fatalf("inference: %d %s", code, body)
	}

	code, body = c.do("GET", "/api/v1/inference/models", "", nil)
	var catalog core.InferenceModels
	if err := json.Unmarshal(body, &catalog); err != nil || code != 200 || len(catalog.Models) != 2 || catalog.DefaultModel != "sonnet" || catalog.DefaultName != "Test Sonnet" {
		t.Fatalf("model discovery: %d %s", code, body)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private","model":"opus"}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/opus`) {
		t.Fatalf("override: %d %s", code, body)
	}
	settingsBefore, _ := s.store.GetSettings(t.Context(), user.ID)
	if settingsBefore.Claude.Model != "" && settingsBefore.Claude.Model != "default" {
		t.Fatal("override mutated global model")
	}
	settingsBefore.Claude.Model = "opus"
	if err := s.store.SaveSettings(t.Context(), user.ID, settingsBefore); err != nil {
		t.Fatal(err)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private"}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/opus`) {
		t.Fatalf("follow changed default: %d %s", code, body)
	}
	for _, model := range []string{"unknown-model", "--help", "opus;echo-secret"} {
		input, _ := json.Marshal(map[string]string{"prompt": "private", "model": model})
		code, body = c.do("POST", "/api/v1/inference", "application/json", input)
		if code != 400 {
			t.Fatalf("accepted unlisted model %q: %d %s", model, code, body)
		}
	}

	// A personal central default takes precedence over chat defaults, but an app
	// override still wins. All changes go through the real settings API.
	code, body = personal.do("GET", "/api/v1/settings/app-models", "", nil)
	if code != 200 || !strings.Contains(string(body), `"claude_default_model":"opus"`) {
		t.Fatalf("personal catalog: %d %s", code, body)
	}
	code, body = personal.do("PUT", "/api/v1/settings", "application/json", []byte(`{"app_model":"sonnet"}`))
	if code != 200 || !strings.Contains(string(body), `"app_model":"sonnet"`) {
		t.Fatalf("save central default: %d %s", code, body)
	}
	saved, _ := s.store.GetSettings(t.Context(), user.ID)
	if saved.AppModel != "sonnet" || saved.Claude.Model != "opus" {
		t.Fatalf("central preference changed chat defaults: %+v", saved)
	}
	code, body = c.do("GET", "/api/v1/inference/models", "", nil)
	if code != 200 || !strings.Contains(string(body), `"default_model":"sonnet"`) {
		t.Fatalf("apps did not see central default: %d %s", code, body)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private"}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/sonnet`) {
		t.Fatalf("central default not used: %d %s", code, body)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private","model":"opus"}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/opus`) {
		t.Fatalf("per-app override lost: %d %s", code, body)
	}
	code, body = personal.do("PUT", "/api/v1/settings", "application/json", []byte(`{"enter_sends":false}`))
	if code != 200 || !strings.Contains(string(body), `"app_model":"sonnet"`) {
		t.Fatalf("partial update lost central preference: %d %s", code, body)
	}
	code, body = personal.do("PUT", "/api/v1/settings", "application/json", []byte(`{"app_model":"not-offered"}`))
	if code != 400 {
		t.Fatalf("accepted unavailable central default: %d %s", code, body)
	}
	code, body = personal.do("PUT", "/api/v1/settings", "application/json", []byte(`{"app_model":""}`))
	if code != 200 {
		t.Fatalf("clear central preference: %d %s", code, body)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private"}`))
	if code != 200 || !strings.Contains(string(body), `pocket-ai/opus`) {
		t.Fatalf("cleared preference did not follow Claude: %d %s", code, body)
	}
	personal.do("PUT", "/api/v1/settings", "application/json", []byte(`{"app_model":"sonnet"}`))
	for _, path := range []string{"/api/v1/threads", "/api/v1/settings", "/api/v1/accounts", "/api/v1/settings/app-models"} {
		code, body := c.do("GET", path, "", nil)
		if code != 401 {
			t.Fatalf("inference token reached %s: %d %s", path, code, body)
		}
	}
	code, _ = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private","cwd":"/","tools":["Bash"]}`))
	if code != 400 {
		t.Fatalf("accepted arbitrary execution options: %d", code)
	}

	otherClient, other := person(t, s, h, "another@example.test", "member", true)
	code, body = otherClient.do("GET", "/api/v1/settings", "", nil)
	if code != 200 || !strings.Contains(string(body), `"app_model":""`) {
		t.Fatalf("central default leaked to another person: %d %s", code, body)
	}
	account := &store.Account{Provider: "claude", Label: "Other person's account", Home: root, UserID: &other.ID}
	if err := s.store.CreateAccount(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.store.GetSettings(t.Context(), user.ID)
	settings.Claude.Account = account.ID
	if err := s.store.SaveSettings(t.Context(), user.ID, settings); err != nil {
		t.Fatal(err)
	}
	code, body = c.do("POST", "/api/v1/inference", "application/json", []byte(`{"prompt":"private"}`))
	if code != 403 {
		t.Fatalf("used another person's Claude account: %d %s", code, body)
	}
	code, body = c.do("GET", "/api/v1/inference/models", "", nil)
	if code != 403 {
		t.Fatalf("listed another person's account models: %d %s", code, body)
	}

}
