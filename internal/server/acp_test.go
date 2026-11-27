package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// fakeOpenCode answers ACP by method; a prompt gets one line of text back,
// with the OPENROUTER key it was given (so the test sees the keys arrive).
const fakeOpenCode = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"resume":{}}}}}' ;;
    *'"method":"session/new"'*|*'"method":"session/resume"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"fake-1","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"free/a","options":[{"value":"free/a","name":"Free A"},{"value":"google/gemini-4-neon","name":"Gemini 4 Neon"}]},{"id":"mode","category":"mode","type":"select","currentValue":"build","options":[{"value":"build","name":"build"},{"value":"plan","name":"plan"}]}]}}' ;;
    *'"method":"session/set_config_option"'*)
      printf '%s\n' "$line" >> "$FAKE_LOG"
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
    *'"method":"session/prompt"'*)
      printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello from fake, key '"$OPENROUTER_API_KEY"'"}}}}'
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":4}}}' ;;
    *'"id":'*'"method"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
  esac
done
`

func TestACPThread(t *testing.T) {
	s, h, root := testServer(t)
	bin := filepath.Join(root, "fake-opencode")
	if err := os.WriteFile(bin, []byte(fakeOpenCode), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_BIN", bin)
	t.Setenv("FAKE_LOG", filepath.Join(root, "fake.log"))
	owner, _ := person(t, s, h, "owner@example.test", "admin", true)

	// keys: unsafe names refused, secrets masked when read back
	owner.want("PUT", "/api/v1/agents/acp/opencode/env", map[string]any{"env": map[string]string{"PATH": "/evil"}}, 400, nil)
	var list []acpView
	owner.want("PUT", "/api/v1/agents/acp/opencode/env", map[string]any{"env": map[string]string{"OPENROUTER_API_KEY": "sk-or-123456789"}}, 200, &list)
	var oc acpView
	for _, v := range list {
		if v.ID == "opencode" {
			oc = v
		}
	}
	if !oc.Installed || !oc.Available || oc.Set["OPENROUTER_API_KEY"] != "••••6789" || oc.Models != 2 {
		t.Fatalf("opencode view %+v", oc)
	}
	other, _ := person(t, s, h, "other@example.test", "member", true)
	other.want("GET", "/api/v1/agents/acp", nil, 403, nil)

	// defaults for the agent are kept with the person's settings
	owner.want("PUT", "/api/v1/settings", map[string]any{"agents": map[string]any{"opencode": map[string]string{"model": "google/gemini-4-neon"}}}, 200, nil)

	var th struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Mode  string `json:"mode"`
	}
	owner.want("POST", "/api/v1/threads", map[string]any{"provider": "opencode", "text": "hi", "cwd": root}, 201, &th)
	if th.Model != "google/gemini-4-neon" || th.Mode != "ask" {
		t.Fatalf("thread model %q mode %q", th.Model, th.Mode)
	}
	var reply string
	for i := 0; i < 200 && reply == ""; i++ {
		items, _ := s.store.ListItems(context.Background(), th.ID)
		for _, it := range items {
			var a agent.Item
			_ = json.Unmarshal(it.Data, &a)
			if a.Kind == "assistant" {
				reply = a.Text
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reply != "Hello from fake, key sk-or-123456789" {
		t.Fatalf("reply %q", reply)
	}
	log, _ := os.ReadFile(filepath.Join(root, "fake.log"))
	if !strings.Contains(string(log), `"value":"google/gemini-4-neon"`) {
		t.Fatalf("model not switched: %s", log)
	}
	if strings.Contains(string(log), `"configId":"mode"`) {
		t.Fatalf("ask runs in build mode, which it already is: %s", log)
	}
	// ACP agents run here only
	owner.want("POST", "/api/v1/threads", map[string]any{"provider": "opencode", "text": "hi", "host_id": 5}, 400, nil)
}

// TestACPGoogleLoginAntigravity: "Sign in with Google" runs agy acp --login in
// a terminal, shows its link, hands it the pasted code, and is done when it exits.
func TestACPGoogleLoginAntigravity(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script (util-linux) not installed")
	}
	s, h, root := testServer(t)
	bin := filepath.Join(root, "agy")
	got := filepath.Join(root, "got")
	_ = os.WriteFile(bin, []byte(`#!/bin/sh
[ "$1 $2" = "acp --login" ] || exit 3
echo "Open https://accounts.google.com/o/oauth2/auth?client_id=x to sign in"
printf 'Paste the code: '
read code
echo "$code" > "`+got+`"
echo "Signed in."
`), 0o700)
	t.Setenv("ANTIGRAVITY_BIN", bin)
	owner, _ := person(t, s, h, "owner@example.test", "admin", true)
	var st struct {
		State    string `json:"state"`
		URL      string `json:"url"`
		NeedCode bool   `json:"need_code"`
	}
	owner.want("POST", "/api/v1/agents/acp/antigravity/login", nil, 200, &st)
	if st.State != "waiting" || !st.NeedCode || !strings.HasPrefix(st.URL, "https://accounts.google.com/") {
		t.Fatalf("start %+v", st)
	}
	owner.want("POST", "/api/v1/agents/acp/antigravity/login/code", map[string]string{"code": "4/abc-123"}, 200, &st)
	for i := 0; i < 100 && st.State != "done"; i++ {
		time.Sleep(50 * time.Millisecond)
		owner.want("GET", "/api/v1/agents/acp/antigravity/login", nil, 200, &st)
	}
	if st.State != "done" {
		t.Fatalf("state %+v", st)
	}
	if b, _ := os.ReadFile(got); strings.TrimSpace(string(b)) != "4/abc-123" {
		t.Fatalf("code %q", b)
	}
	// signing out forgets the token of the default login (in the test's own home)
	ag, _ := agent.ACPAgentByID("antigravity")
	if !strings.HasPrefix(ag.TokenPath(""), root) {
		t.Fatalf("the token path %s is outside the test's home", ag.TokenPath(""))
	}
	_ = os.MkdirAll(filepath.Dir(ag.TokenPath("")), 0o700)
	_ = os.WriteFile(ag.TokenPath(""), []byte(`{"id_token_email":"me@example.test"}`), 0o600)
	owner.want("POST", "/api/v1/agents/acp/antigravity/logout", nil, 200, nil)
	if _, err := os.Stat(ag.TokenPath("")); !os.IsNotExist(err) {
		t.Fatal("still signed in")
	}
}

// The scripts that run on a machine for Antigravity logins: add one, find
// them with who they are, copy a token in, read it back, sign out.
func TestAntigravityHostScripts(t *testing.T) {
	home := t.TempDir()
	ag, _ := agent.ACPAgentByID("antigravity")
	bin := filepath.Join(home, "agy")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\necho 1.5.18\n"), 0o700)

	dir := strings.TrimSpace(runLoginSh(t, home, hostAddLoginScript, "antigravity", "work"))
	if dir != filepath.Join(home, ".gemini-work") || !agent.ValidHostHome("antigravity", home, dir) {
		t.Fatalf("made %q", dir)
	}
	if agent.ValidHostHome("antigravity", home, filepath.Join(home, ".claude-work")) || agent.ValidHostHome("claude", home, dir) {
		t.Fatal("login folders of the CLIs are mixed up")
	}
	// the token goes in on stdin, private
	cmd := exec.Command("sh", "-c", writeTokenScript, "sh", dir, ag.TokenFile, ag.LoginPrefix())
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(`{"token":{"refresh_token":"r"},"id_token_email":"me@example.test"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	fi, err := os.Stat(filepath.Join(dir, ag.TokenFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", fi, err)
	}
	if got := runLoginSh(t, home, readTokenScript, dir, ag.TokenFile, ag.LoginPrefix()); !strings.Contains(got, `"refresh_token":"r"`) {
		t.Fatalf("read back %q", got)
	}
	// the default login (dir ""): ~/.gemini
	cmd = exec.Command("sh", "-c", writeTokenScript, "sh", "", ag.TokenFile, ag.LoginPrefix())
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(`{"id_token_email": "first@example.test"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	st := runLoginSh(t, home, hostStatusScript(), filepath.Join(home, "no-claude"), filepath.Join(home, "no-codex"), bin, ag.TokenFile)
	want := "antigravity_home=" + filepath.Join(home, ".gemini") + "\nantigravity_email=first@example.test\nantigravity_home=" + dir + "\nantigravity_email=me@example.test\n"
	if !strings.Contains(st, want) {
		t.Fatalf("status:\n%s", st)
	}
	// not installed: nothing said about it
	if st := runLoginSh(t, home, hostStatusScript(), "no-claude", "no-codex", filepath.Join(home, "no-agy"), ag.TokenFile); strings.Contains(st, "antigravity") {
		t.Fatalf("status without agy:\n%s", st)
	}
	if v := runLoginSh(t, home, hostProbe, "no-claude", "no-codex", bin); !strings.Contains(v, "version_antigravity=1.5.18") {
		t.Fatalf("probe:\n%s", v)
	}
}

// Antigravity logins on this server: the default one and more accounts, a
// sign-in copied from one to a new one, and a conversation started on it.
func TestAntigravityAccounts(t *testing.T) {
	s, h, root := testServer(t)
	ag, _ := agent.ACPAgentByID("antigravity")
	if !strings.HasPrefix(ag.TokenPath(""), root) {
		t.Fatalf("the token path %s is outside the test's home", ag.TokenPath(""))
	}
	bin := filepath.Join(root, "agy")
	argsLog := filepath.Join(root, "agy-args")
	// a fake agy: logs how it was started, answers ACP like the fake above
	_ = os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.5.18; exit 0; fi\necho \"$*\" >> "+argsLog+"\n"+strings.TrimPrefix(fakeOpenCode, "#!/bin/sh\n")), 0o700)
	t.Setenv("ANTIGRAVITY_BIN", bin)
	t.Setenv("FAKE_LOG", filepath.Join(root, "fake.log"))
	owner, _ := person(t, s, h, "owner@example.test", "admin", true)

	// signed in on the default login
	_ = os.MkdirAll(filepath.Dir(ag.TokenPath("")), 0o700)
	_ = os.WriteFile(ag.TokenPath(""), []byte(`{"token":{"refresh_token":"r"},"id_token_email":"me@example.test"}`), 0o600)
	var accts []accountView
	owner.want("GET", "/api/v1/accounts?refresh=1", nil, 200, &accts)
	var def *accountView
	for i := range accts {
		if accts[i].Provider == "antigravity" {
			def = &accts[i]
		}
	}
	if def == nil || def.ID != 0 || !def.SignedIn || def.Email != "me@example.test" {
		t.Fatalf("default antigravity login: %+v", def)
	}

	// one tap: the same account as a second login
	var cp struct {
		Email string `json:"email"`
		Dir   string `json:"dir"`
	}
	owner.want("POST", "/api/v1/agents/acp/antigravity/copy-login", map[string]any{"from": map[string]any{"host": 0}, "to": map[string]any{"host": 0, "name": "Work"}}, 200, &cp)
	if cp.Email != "me@example.test" || cp.Dir != filepath.Join(root, ".gemini-work") {
		t.Fatalf("copied %+v", cp)
	}
	if fi, err := os.Stat(filepath.Join(cp.Dir, ag.TokenFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token not copied privately: %v %v", fi, err)
	}
	owner.want("GET", "/api/v1/accounts", nil, 200, &accts)
	var work *accountView
	for i := range accts {
		if accts[i].Provider == "antigravity" && accts[i].ID != 0 {
			work = &accts[i]
		}
	}
	if work == nil || work.Label != "Work" || !work.SignedIn || work.Email != "me@example.test" {
		t.Fatalf("work login: %+v", work)
	}
	// Claude's lists never see it
	if list, _ := s.store.ListAccounts(context.Background()); len(list) != 0 {
		t.Fatalf("an Antigravity login among the CLI accounts: %+v", list[0])
	}
	// someone else gets neither the logins nor the copy
	other, _ := person(t, s, h, "other@example.test", "member", true)
	other.want("POST", "/api/v1/agents/acp/antigravity/copy-login", map[string]any{"from": map[string]any{"host": 0}, "to": map[string]any{"host": 0, "name": "Mine"}}, 403, nil)
	other.want("POST", "/api/v1/accounts", map[string]any{"provider": "antigravity", "label": "Mine"}, 403, nil)

	// a conversation on the second login is started on its folder
	var th struct {
		ID string `json:"id"`
	}
	owner.want("POST", "/api/v1/threads", map[string]any{"provider": "antigravity", "text": "hi", "cwd": root, "account_id": work.ID}, 201, &th)
	for i := 0; i < 200; i++ {
		if b, _ := os.ReadFile(argsLog); strings.Contains(string(b), "--gemini-dir "+cp.Dir) {
			break
		}
		if i == 199 {
			b, _ := os.ReadFile(argsLog)
			t.Fatalf("not started on the login folder: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// signing out forgets the token of that login only
	owner.want("POST", "/api/v1/accounts/"+strconv.FormatInt(work.ID, 10)+"/logout", nil, 200, nil)
	if _, err := os.Stat(filepath.Join(cp.Dir, ag.TokenFile)); !os.IsNotExist(err) {
		t.Fatal("the token is still there after signing out")
	}
	if _, ok := ag.LoginStatus(""); !ok {
		t.Fatal("signing one login out took the default one along")
	}
}
