package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// mcpControl sends an MCP control request and waits for Claude Code's
// answer. Pocket Ask chats keep the connectors they were set up with.
func (s *claudeSession) mcpControl(ctx context.Context, req map[string]any) (json.RawMessage, error) {
	if s.opts.Restricted {
		return nil, errors.New("Pocket Ask chats keep the connectors they were set up with")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep, err := s.control(ctx, "mcp", req)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, errors.New("Claude Code did not answer in time")
	}
	if err != nil {
		return nil, err
	}
	if !rep.ok {
		return nil, errors.New(firstNonEmpty(rep.err, "Claude Code refused the request"))
	}
	return rep.body, nil
}

func (s *claudeSession) MCPStatus(ctx context.Context) ([]MCPServer, error) {
	body, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_status"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Servers []struct {
			Name      string            `json:"name"`
			Status    string            `json:"status"`
			Error     string            `json:"error"`
			Scope     string            `json:"scope"`
			Source    string            `json:"source"`
			Transport string            `json:"transport"`
			Tools     []json.RawMessage `json:"tools"`
			Config    struct {
				Type string `json:"type"`
			} `json:"config"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]MCPServer, 0, len(r.Servers))
	for _, v := range r.Servers {
		transport := firstNonEmpty(v.Transport, v.Config.Type)
		remote := transport == "http" || transport == "sse" || transport == "claudeai-proxy" || transport == "ws"
		out = append(out, MCPServer{Name: v.Name, Status: v.Status, Error: v.Error, Source: firstNonEmpty(v.Source, v.Scope),
			Tools: len(v.Tools), CanToggle: true, CanLogin: remote || v.Status == "needs-auth", CanLogout: remote})
	}
	return out, nil
}

func (s *claudeSession) MCPReconnect(ctx context.Context, name string) error {
	if name != "" {
		_, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_reconnect", "serverName": name})
		return err
	}
	list, err := s.MCPStatus(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range list {
		if v.Status == "disabled" {
			continue
		}
		if _, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_reconnect", "serverName": v.Name}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *claudeSession) MCPToggle(ctx context.Context, name string, on bool) error {
	_, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_toggle", "serverName": name, "enabled": on})
	return err
}

func (s *claudeSession) MCPLogin(ctx context.Context, name string) (MCPLogin, error) {
	body, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_authenticate", "serverName": name})
	if err != nil {
		return MCPLogin{}, err
	}
	var r struct {
		AuthURL          string `json:"authUrl"`
		CallbackExpected bool   `json:"callbackExpected"`
	}
	_ = json.Unmarshal(body, &r)
	if r.AuthURL == "" {
		return MCPLogin{}, errors.New("Claude Code did not give a sign-in address (is the server already signed in?)")
	}
	return MCPLogin{AuthURL: r.AuthURL, CallbackExpected: r.CallbackExpected}, nil
}

func (s *claudeSession) MCPCallback(ctx context.Context, name, callbackURL string) error {
	_, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_oauth_callback_url", "serverName": name, "callbackUrl": callbackURL})
	return err
}

func (s *claudeSession) MCPLogout(ctx context.Context, name string) error {
	_, err := s.mcpControl(ctx, map[string]any{"subtype": "mcp_clear_auth", "serverName": name})
	return err
}
