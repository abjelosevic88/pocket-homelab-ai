// Package config loads runtime configuration from environment variables.
//
// Every option has a sensible default so that `docker run` with a single
// volume works out of the box. See docs/configuration.md for the full list.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings.
type Config struct {
	Host           string
	Port           int
	BaseURL        string
	DataDir        string
	DBPath         string
	LogLevel       string
	LogFormat      string // text | json
	TrustProxy     bool
	MetricsEnabled bool
	Timezone       string // IANA; the box default for people who did not choose one

	AuthMode             string // local | proxy | none
	AuthProxyHeader      string
	AuthProxyEmailHeader string
	SessionTTL           time.Duration
	SecureCookies        bool
	AdminEmail           string
	AdminPassword        string
	AdminName            string

	// Sign in with an OpenID Connect provider. On a box with Pocket Core
	// none of these are needed: the app uses the client Core issued.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCName         string
	OIDCAutoCreate   bool

	// Pocket Core: when CORE_URL is set the app registers itself on start,
	// publishes events and accepts the tokens Core hands to paired phones.
	CoreURL             string
	CoreRegistrationKey string
	CoreAppURL          string

	// Notifications: finished turns, questions, failed runs (each person
	// can set their own route).
	NotifyURL   string // Apprise route, ntfy topic URL or webhook
	NotifyKind  string // apprise | ntfy | webhook
	APNSTeamID  string
	APNSKeyID   string
	APNSKeyFile string
	APNSTopic   string

	// The agents.
	Home        string        // the server user's home (default folder, ~ expansion)
	Roots       []string      // folders agents may start in (and the folder picker shows)
	ClaudeBin   string        // claude executable
	CodexBin    string        // codex executable
	ClaudeHome  string        // ~/.claude (session history)
	CodexHome   string        // ~/.codex (rollout files, for "Running now")
	PaseoHome   string        // ~/.paseo (to name sessions Paseo drives); "" = off
	IdleTimeout time.Duration // stop a CLI process after this long without a turn
	MaxLive     int           // most CLI processes alive at once (oldest idle ones are stopped)
	UploadMax   int64         // bytes per uploaded image

	// People other than the owner: a folder each here, their agents in a
	// bubblewrap jail (agent/jail.go); SandboxOff keeps the folders but
	// runs them without bubblewrap.
	PeopleDir  string
	SandboxOff bool
	BwrapBin   string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := env(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	dataDir := env("DATA_DIR", "./data")
	c := &Config{
		Host:           env("HOST", "127.0.0.1"),
		Port:           envInt("PORT", 8195),
		BaseURL:        strings.TrimRight(env("BASE_URL", ""), "/"),
		DataDir:        dataDir,
		DBPath:         env("DB_PATH", dataDir+"/pocket-ai.db"),
		LogLevel:       strings.ToLower(env("LOG_LEVEL", "info")),
		LogFormat:      strings.ToLower(env("LOG_FORMAT", "text")),
		TrustProxy:     envBool("TRUST_PROXY", true),
		MetricsEnabled: envBool("METRICS_ENABLED", true),
		Timezone:       env("TZ", "UTC"),

		AuthMode:             strings.ToLower(env("AUTH_MODE", "local")),
		AuthProxyHeader:      env("AUTH_PROXY_HEADER", "Remote-User"),
		AuthProxyEmailHeader: env("AUTH_PROXY_EMAIL_HEADER", "Remote-Email"),
		SessionTTL:           envDuration("SESSION_TTL", 720*time.Hour),
		SecureCookies:        envBool("SECURE_COOKIES", false),
		AdminEmail:           strings.ToLower(env("ADMIN_EMAIL", "")),
		AdminPassword:        env("ADMIN_PASSWORD", ""),
		AdminName:            env("ADMIN_NAME", "Admin"),

		OIDCIssuer:       strings.TrimRight(env("OIDC_ISSUER", ""), "/"),
		OIDCClientID:     env("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: env("OIDC_CLIENT_SECRET", ""),
		OIDCName:         env("OIDC_NAME", "single sign-on"),
		OIDCAutoCreate:   envBool("OIDC_AUTO_CREATE", true),

		CoreURL:             strings.TrimRight(env("CORE_URL", ""), "/"),
		CoreRegistrationKey: env("CORE_REGISTRATION_KEY", ""),
		CoreAppURL:          strings.TrimRight(env("CORE_APP_URL", ""), "/"),

		NotifyURL:   env("NOTIFY_URL", ""),
		NotifyKind:  strings.ToLower(env("NOTIFY_KIND", "apprise")),
		APNSTeamID:  env("APNS_TEAM_ID", ""),
		APNSKeyID:   env("APNS_KEY_ID", ""),
		APNSKeyFile: env("APNS_KEY_FILE", ""),
		APNSTopic:   env("APNS_TOPIC", "org.bjelke.pocketai"),

		ClaudeBin:   env("CLAUDE_BIN", "claude"),
		CodexBin:    env("CODEX_BIN", "codex"),
		IdleTimeout: envDuration("IDLE_TIMEOUT", 20*time.Minute),
		MaxLive:     envInt("MAX_LIVE", 12),
		UploadMax:   int64(envInt("UPLOAD_MAX_MB", 20)) << 20,
	}
	home, _ := os.UserHomeDir()
	c.Home = env("AGENT_HOME", home)
	if c.Home == "" {
		c.Home = "/"
	}
	c.ClaudeHome = env("CLAUDE_HOME", filepath.Join(c.Home, ".claude"))
	c.CodexHome = env("CODEX_HOME", filepath.Join(c.Home, ".codex"))
	c.PaseoHome = env("PASEO_HOME", filepath.Join(c.Home, ".paseo"))
	c.PeopleDir = env("PEOPLE_DIR", filepath.Join(dataDir, "people"))
	if c.PeopleDir != "" {
		if abs, err := filepath.Abs(c.PeopleDir); err == nil {
			c.PeopleDir = abs
		}
	}
	switch v := strings.ToLower(env("SANDBOX", "on")); v {
	case "on", "auto", "true", "1":
	case "off", "false", "0":
		c.SandboxOff = true
	default:
		return nil, fmt.Errorf("SANDBOX must be on or off (got %q)", v)
	}
	c.BwrapBin = env("BWRAP_BIN", "")
	for _, r := range strings.Split(env("ROOTS", c.Home), ",") {
		r = strings.TrimSpace(r)
		if strings.HasPrefix(r, "~") {
			r = filepath.Join(c.Home, strings.TrimPrefix(r, "~"))
		}
		if r != "" {
			c.Roots = append(c.Roots, filepath.Clean(r))
		}
	}
	if c.CoreAppURL == "" {
		c.CoreAppURL = c.BaseURL
	}
	switch c.AuthMode {
	case "local", "proxy", "none":
	default:
		return nil, fmt.Errorf("AUTH_MODE must be one of local, proxy, none (got %q)", c.AuthMode)
	}
	if c.AuthMode == "none" {
		// No login in front of a shell on the server: loopback only.
		for _, h := range strings.Split(c.Host, ",") {
			if h = strings.TrimSpace(h); h != "127.0.0.1" && h != "localhost" && h != "::1" {
				return nil, fmt.Errorf("AUTH_MODE=none runs agents for anyone who can reach the port; it is only allowed with HOST=127.0.0.1 (got %q)", c.Host)
			}
		}
	}
	if c.Port <= 0 || c.Port > 65535 {
		return nil, fmt.Errorf("PORT out of range: %d", c.Port)
	}
	if c.OIDCIssuer != "" && (c.OIDCClientID == "" || c.BaseURL == "") {
		return nil, fmt.Errorf("OIDC_ISSUER needs OIDC_CLIENT_ID and BASE_URL (the callback is BASE_URL/api/v1/auth/oidc/callback)")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return nil, fmt.Errorf("TZ %q is not a known time zone", c.Timezone)
	}
	return c, nil
}

// Addr returns host:port of the first listen address.
func (c *Config) Addr() string { return c.Addrs()[0] }

// Addrs returns host:port for every HOST (comma-separated, e.g.
// "127.0.0.1,172.17.0.1" to answer on loopback and the docker bridge only).
func (c *Config) Addrs() []string {
	var out []string
	for _, h := range strings.Split(c.Host, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, fmt.Sprintf("%s:%d", h, c.Port))
		}
	}
	if len(out) == 0 {
		out = []string{fmt.Sprintf("0.0.0.0:%d", c.Port)}
	}
	return out
}
