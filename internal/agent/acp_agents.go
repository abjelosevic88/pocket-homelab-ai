package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ACPAgent is a coding agent CLI Pocket AI drives over the Agent Client
// Protocol (acp.go).
type ACPAgent struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Bin     string   `json:"bin"`
	Args    []string `json:"-"`       // what starts it in ACP mode
	Package string   `json:"package"` // npm package (install / update)
	Site    string   `json:"site"`
	// Keys are the environment variables it can be given (API keys, an
	// endpoint): set in Settings → Agents, kept by Pocket AI, handed to the
	// agent when it starts.
	Keys []ACPKey `json:"keys"`
	// GoogleLogin: it can sign in with a Google account (Gemini CLI,
	// Antigravity CLI).
	GoogleLogin bool `json:"google_login"`
	// LoginArgs sign it in with a pasted code and exit (Antigravity: acp
	// --login); nil = Gemini CLI's way (its chat, until the credentials file
	// is there).
	LoginArgs []string `json:"-"`
	// InstallScript: installed by the vendor's script instead of npm; such
	// agents update themselves.
	InstallScript string `json:"install_script,omitempty"`
	// Note says how it is set up, for Settings.
	Note string `json:"note"`
	// Modes: Pocket AI's own permission modes for it, when the agent's are
	// not enough (OpenCode's build mode never asks); nil = the agent's own.
	Modes []ACPMode `json:"-"`
	// DefaultMode: the mode new conversations take ("" = the agent's current).
	DefaultMode string `json:"-"`
	// Env is always given to it (on top of the keys).
	Env []string `json:"-"`
}

// ACPMode is a Pocket AI mode on an ACP agent: the agent's own mode it runs
// in, and whether Pocket AI approves what it asks without asking the person.
type ACPMode struct {
	Mode
	ACP       string
	AutoAllow bool
}

// mode resolves a Pocket AI mode to the agent's mode and auto-approval.
func (a ACPAgent) mode(id string) (acp string, autoAllow bool) {
	for _, m := range a.Modes {
		if m.ID == id {
			return m.ACP, m.AutoAllow
		}
	}
	return id, false
}

// ACPKey is one environment variable an agent can be given.
type ACPKey struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Help   string `json:"help,omitempty"`
	URL    string `json:"url,omitempty"` // where to get one
	Secret bool   `json:"secret"`
}

// ACPAgents are the ACP agents Pocket AI knows, in the order it shows them.
var ACPAgents = []ACPAgent{
	{
		ID: "antigravity", Name: "Antigravity CLI", Bin: "agy", Args: []string{"acp"},
		InstallScript: "https://antigravity.google/cli/install.sh", Site: "https://antigravity.google/docs/cli/acp",
		GoogleLogin: true, LoginArgs: []string{"acp", "--login"},
		// agy starts in auto, which needs its terminal sandbox: ask first instead
		DefaultMode: "default",
		Note:        "Google's agent on your Google AI plan (Free, Pro or Ultra): Gemini 4 Neon and more. Sign in with Google; it updates itself.",
		Keys:        []ACPKey{},
	},
	{
		ID: "gemini", Name: "Gemini CLI", Bin: "gemini", Args: []string{"--acp"}, Package: "@google/gemini-cli",
		Site: "https://github.com/google-gemini/gemini-cli", GoogleLogin: true, DefaultMode: "default",
		Note: "Google's earlier agent, now for Gemini API keys (and Gemini Code Assist Standard/Enterprise sign-ins); personal Google accounts and Google AI plans moved to Antigravity CLI.",
		Keys: []ACPKey{
			{Name: "GEMINI_API_KEY", Label: "Gemini API key", URL: "https://aistudio.google.com/apikey", Secret: true,
				Help: "Used when there is no Google sign-in."},
		},
	},
	{
		ID: "opencode", Name: "OpenCode", Bin: "opencode", Args: []string{"acp"}, Package: "opencode-ai",
		Site: "https://opencode.ai",
		// OpenCode asks only what its config says to ask; Pocket AI makes it
		// ask about edits, commands and fetches, and answers for it in Full access.
		Env: []string{`OPENCODE_CONFIG_CONTENT={"permission":{"edit":"ask","bash":"ask","webfetch":"ask"}}`},
		Modes: []ACPMode{
			{Mode: Mode{ID: "ask", Name: "Ask first", Description: "Asks before changing files, running commands or fetching pages."}, ACP: "build"},
			{Mode: Mode{ID: "build", Name: "Full access", Description: "Changes files and runs commands without asking."}, ACP: "build", AutoAllow: true},
			{Mode: Mode{ID: "plan", Name: "Plan", Description: "Read only: looks and plans, changes nothing."}, ACP: "plan"},
		},
		DefaultMode: "ask",
		Note:        "Works with many model providers: free OpenCode models out of the box; every key below adds that provider's models (Google AI = Gemini 4 Neon, Gemini 3 and more).",
		Keys: []ACPKey{
			{Name: "GOOGLE_GENERATIVE_AI_API_KEY", Label: "Google AI (Gemini) API key", URL: "https://aistudio.google.com/apikey", Secret: true},
			{Name: "OPENROUTER_API_KEY", Label: "OpenRouter API key", URL: "https://openrouter.ai/keys", Secret: true, Help: "Hundreds of models behind one key."},
			{Name: "DEEPSEEK_API_KEY", Label: "DeepSeek API key", URL: "https://platform.deepseek.com/api_keys", Secret: true},
			{Name: "MOONSHOT_API_KEY", Label: "Moonshot (Kimi) API key", URL: "https://platform.moonshot.ai/console/api-keys", Secret: true},
			{Name: "XAI_API_KEY", Label: "xAI (Grok) API key", URL: "https://console.x.ai", Secret: true},
			{Name: "MISTRAL_API_KEY", Label: "Mistral API key", URL: "https://console.mistral.ai/api-keys", Secret: true},
			{Name: "GROQ_API_KEY", Label: "Groq API key", URL: "https://console.groq.com/keys", Secret: true},
		},
	},
	{
		ID: "qwen", Name: "Qwen Code", Bin: "qwen", Args: []string{"--acp"}, Package: "@qwen-code/qwen-code",
		Site: "https://github.com/QwenLM/qwen-code", DefaultMode: "default",
		Note: "Alibaba's agent for Qwen models, through any OpenAI-compatible endpoint (Alibaba Cloud Model Studio, OpenRouter, a local server).",
		Keys: []ACPKey{
			{Name: "OPENAI_API_KEY", Label: "API key", Secret: true, URL: "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key"},
			{Name: "OPENAI_BASE_URL", Label: "Endpoint", Help: "e.g. https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
			{Name: "OPENAI_MODEL", Label: "Model", Help: "e.g. qwen3-coder-plus"},
		},
	},
}

// Command is the program to run: <ID>_BIN in the environment (GEMINI_BIN,
// OPENCODE_BIN, QWEN_BIN) or its name on the PATH.
func (a ACPAgent) Command() string {
	if v := os.Getenv(strings.ToUpper(a.ID) + "_BIN"); v != "" {
		return v
	}
	return a.Bin
}

// ACPAgentByID finds an ACP agent.
func ACPAgentByID(id string) (ACPAgent, bool) {
	for _, a := range ACPAgents {
		if a.ID == id {
			return a, true
		}
	}
	return ACPAgent{}, false
}

// IsACP: provider is one of the ACP agents.
func IsACP(provider string) bool {
	_, ok := ACPAgentByID(provider)
	return ok
}

// KnownProvider: Claude Code, Codex or an ACP agent.
func KnownProvider(provider string) bool {
	return provider == "claude" || provider == "codex" || IsACP(provider)
}

// ProviderIDs lists every provider id, Claude Code and Codex first.
func ProviderIDs() []string {
	out := []string{"claude", "codex"}
	for _, a := range ACPAgents {
		out = append(out, a.ID)
	}
	return out
}

// ACPHome is the folder the agent keeps its own settings and sign-ins in.
func (a ACPAgent) ACPHome() string {
	h, _ := os.UserHomeDir()
	switch a.ID {
	case "gemini":
		return filepath.Join(h, ".gemini")
	case "qwen":
		return filepath.Join(h, ".qwen")
	case "opencode":
		return filepath.Join(h, ".local", "share", "opencode")
	case "antigravity":
		return filepath.Join(h, ".gemini", "antigravity-cli")
	}
	return ""
}

// GoogleSignedIn: Gemini CLI has a Google sign-in saved.
func (a ACPAgent) GoogleSignedIn() bool {
	if !a.GoogleLogin || a.LoginArgs != nil {
		return false // Antigravity keeps it in the keyring: only a start tells
	}
	return fileExists(filepath.Join(a.ACPHome(), "oauth_creds.json"))
}

// GoogleAccount is the Google account Gemini CLI signed in with, if it says.
func (a ACPAgent) GoogleAccount() string {
	b, err := os.ReadFile(filepath.Join(a.ACPHome(), "google_accounts.json"))
	if err != nil {
		return ""
	}
	var g struct {
		Active string `json:"active"`
	}
	_ = json.Unmarshal(b, &g)
	return g.Active
}

// prepare readies the agent's own settings for a start with env. Gemini CLI
// starts on the sign-in its settings name and waits for a browser when that
// one is a Google sign-in it does not have: name the one that is there.
func (a ACPAgent) prepare(env []string) {
	if a.ID != "gemini" {
		return
	}
	switch {
	case a.GoogleSignedIn():
		_ = SetGeminiAuth(a.ACPHome(), "oauth-personal")
	case envHas(env, "GEMINI_API_KEY") || geminiAuth(a.ACPHome()) == "oauth-personal":
		// a key, or nothing at all: it says what is missing at once
		_ = SetGeminiAuth(a.ACPHome(), "gemini-api-key")
	}
}

// geminiAuth is the sign-in Gemini CLI's settings name.
func geminiAuth(home string) string {
	var m struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	b, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	_ = json.Unmarshal(b, &m)
	return m.Security.Auth.SelectedType
}

var geminiSettingsMu sync.Mutex

// SetGeminiAuth names the sign-in Gemini CLI uses in its settings
// (security.auth.selectedType), keeping everything else in the file.
func SetGeminiAuth(home, typ string) error {
	geminiSettingsMu.Lock()
	defer geminiSettingsMu.Unlock()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	p := filepath.Join(home, "settings.json")
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return err // a settings file Pocket AI does not understand stays as it is
		}
	}
	sec, _ := m["security"].(map[string]any)
	if sec == nil {
		sec = map[string]any{}
	}
	auth, _ := sec["auth"].(map[string]any)
	if auth == nil {
		auth = map[string]any{}
	}
	if auth["selectedType"] == typ {
		return nil
	}
	auth["selectedType"] = typ
	sec["auth"] = auth
	m["security"] = sec
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// authMethod picks the sign-in to tell the agent about, from what is set up:
// a Google sign-in, else an API key in env. "" = none.
func (a ACPAgent) authMethod(env []string, methods []acpAuthMethod) string {
	has := func(id string) bool {
		for _, m := range methods {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if a.ID == "gemini" {
		if a.GoogleSignedIn() && has("oauth-personal") {
			return "oauth-personal"
		}
		if envHas(env, "GEMINI_API_KEY") && has("gemini-api-key") {
			return "gemini-api-key"
		}
		return ""
	}
	if a.ID == "qwen" && envHas(env, "OPENAI_API_KEY") && has("openai") {
		return "openai"
	}
	return ""
}

func envHas(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") && len(e) > len(name)+1 {
			return true
		}
	}
	return os.Getenv(name) != ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// probed keeps the last probe of each ACP agent (its modes check threads).
var probed sync.Map // id → Provider

// ACPProbed is the last probe of an ACP agent.
func ACPProbed(id string) (Provider, bool) {
	v, ok := probed.Load(id)
	if !ok {
		return Provider{}, false
	}
	return v.(Provider), true
}

// ProbeACP asks an ACP agent for its version, then opens a session in an
// empty folder (no prompt) for its models and modes. A missing sign-in
// leaves it unavailable, with what to do in Error.
func ProbeACP(ctx context.Context, ag ACPAgent, bin, dir string, env []string) Provider {
	bin = firstNonEmpty(bin, ag.Command())
	p := Provider{ID: ag.ID, Name: ag.Name, Models: []Model{}, Efforts: []string{}, Modes: []Mode{}, Speeds: []Speed{}}
	defer func() { probed.Store(ag.ID, p) }()
	v, err := cliVersion(ctx, bin)
	if err != nil {
		p.Error = ag.Bin + " is not installed on the server. Install it in Settings → Agents."
		return p
	}
	p.Version = strings.TrimSpace(strings.TrimPrefix(v, ag.Bin))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		p.Error = err.Error()
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	a, err := openACP(ctx, Options{Cwd: dir, Env: env, Bin: bin}, ag, nil)
	if err != nil {
		p.Error = oneLine(err.Error(), 300)
		return p
	}
	defer a.c.close()
	res, err := a.open(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, env)
	if err != nil {
		p.Error = oneLine(err.Error(), 300)
		return p
	}
	var info acpSessionInfo
	_ = json.Unmarshal(res, &info)
	p.Available = true
	p.Models, p.DefaultModel = info.models()
	p.Modes, p.DefaultMode = info.modes()
	if ag.Modes != nil {
		p.Modes = nil
		for _, m := range ag.Modes {
			p.Modes = append(p.Modes, m.Mode)
		}
	}
	if ag.DefaultMode != "" {
		p.DefaultMode = ag.DefaultMode
	}
	if p.Models == nil {
		p.Models = []Model{}
	}
	if p.Modes == nil {
		p.Modes = []Mode{}
	}
	if p.DefaultModel == "" {
		p.DefaultModel = "default"
		p.Models = append([]Model{{ID: "default", Name: "Default", Efforts: []string{}}}, p.Models...)
	}
	if ag.ID == "gemini" && ag.GoogleSignedIn() {
		p.Account = firstNonEmpty(ag.GoogleAccount(), "Google account")
	}
	if ag.LoginArgs != nil {
		p.Account = "Google account"
	}
	if a.caps.Session.Close != nil && info.SessionID != "" {
		cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = a.c.call(cctx, "session/close", map[string]any{"sessionId": info.SessionID})
		ccancel()
	}
	return p
}
