package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MCPServer is one MCP server (or claude.ai connector) of a live session, as
// the CLI reports it: what /mcp shows in the terminal.
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"` // connected | pending | needs-auth | failed | disabled
	Error  string `json:"error,omitempty"`
	Source string `json:"source,omitempty"` // claudeai | user | project | local | plugin …
	Tools  int    `json:"tools,omitempty"`
	// what this CLI can do with it
	CanToggle bool `json:"can_toggle,omitempty"`
	CanLogin  bool `json:"can_login,omitempty"`
	CanLogout bool `json:"can_logout,omitempty"`
}

// MCPLogin is a sign-in started for a server: AuthURL opens in the browser.
// With CallbackExpected the provider redirects to a localhost address on the
// machine the agent runs on, which a phone cannot reach: the person pastes
// the address the browser ended on, and MCPCallback finishes the sign-in.
type MCPLogin struct {
	AuthURL          string `json:"auth_url"`
	CallbackExpected bool   `json:"callback_expected"`
}

// MCPManager is a live session whose MCP servers can be seen and managed
// (Claude Code's /mcp; Codex's mcpServerStatus and OAuth login).
type MCPManager interface {
	MCPStatus(ctx context.Context) ([]MCPServer, error)
	// MCPReconnect reconnects one server ("" = all of them).
	MCPReconnect(ctx context.Context, name string) error
	MCPToggle(ctx context.Context, name string, on bool) error
	MCPLogin(ctx context.Context, name string) (MCPLogin, error)
	MCPCallback(ctx context.Context, name, callbackURL string) error
	MCPLogout(ctx context.Context, name string) error
}

var ErrMCPUnsupported = errors.New("this agent cannot do that with its MCP servers")

// finishLocalCallback opens a sign-in's localhost redirect on this machine,
// as the browser would have if it ran here.
func finishLocalCallback(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("paste the whole address the browser ended on (http://localhost:…)")
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		return errors.New("that is not the localhost address the sign-in redirected to")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return errors.New("the sign-in is not waiting any more — start it again")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 400 {
		return errors.New("the sign-in was refused: " + oneLine(string(body), 200))
	}
	return nil
}
