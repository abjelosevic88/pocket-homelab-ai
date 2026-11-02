package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// CodexCommands are the slash commands Pocket AI runs for Codex itself: the
// app-server has no command list of its own (the terminal's are built into
// its TUI). /mcp opens the MCP panel; the others are handled in command.
var CodexCommands = []Skill{
	{Name: "mcp", Description: "Manage MCP servers: status, reconnect, sign in", Invoke: "/mcp"},
	{Name: "compact", Description: "Free up context by summarizing the conversation so far", Invoke: "/compact"},
	{Name: "review", Description: "Review the uncommitted changes (or follow your instructions)", ArgHint: "[instructions]", Invoke: "/review"},
	{Name: "init", Description: "Create an AGENTS.md file with instructions for Codex", Invoke: "/init"},
}

const codexInitPrompt = "Generate a file named AGENTS.md that serves as a contributor guide for this repository. " +
	"Look at the repository first (structure, build and test commands, conventions), then write a concise, well-organised guide: " +
	"project structure, build/test/dev commands, coding style and naming, testing guidelines, and commit/PR conventions. " +
	"Keep it short and specific to this repository. If an AGENTS.md exists already, improve it instead."

// command runs a Codex slash command typed as a message; handled is false
// for anything else (it goes to the agent as text).
func (s *codexSession) command(in Input) (handled bool, err error) {
	text := strings.TrimSpace(in.Text)
	if !strings.HasPrefix(text, "/") || len(in.Images) > 0 {
		return false, nil
	}
	name, args, _ := strings.Cut(text[1:], " ")
	args = strings.TrimSpace(args)
	switch name {
	case "init":
		in.Text = codexInitPrompt
		if args != "" {
			in.Text += "\n\n" + args
		}
		return true, s.Send(in)
	case "compact":
		return true, s.commandTurn("thread/compact/start", map[string]any{"threadId": s.threadID})
	case "review":
		target := map[string]any{"type": "uncommittedChanges"}
		if args != "" {
			target = map[string]any{"type": "custom", "instructions": args}
		}
		return true, s.commandTurn("review/start", map[string]any{"threadId": s.threadID, "target": target, "delivery": "inline"})
	}
	return false, nil
}

// commandTurn starts a turn the app-server runs on its own (compaction, a
// review); its notifications report it like any other turn.
func (s *codexSession) commandTurn(method string, params map[string]any) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("wait until the agent is done with this turn")
	}
	s.running, s.turnFrom = true, time.Now()
	s.tokensBase = s.tokensNow
	s.cost = 0
	s.mu.Unlock()
	s.sink.TurnStarted()
	if _, err := s.c.call(s.ctx, method, params); err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.sink.TurnDone(TurnInfo{OK: false, Error: err.Error()})
		return err
	}
	return nil
}

func (s *codexSession) MCPStatus(ctx context.Context) ([]MCPServer, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := []MCPServer{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"threadId": s.threadID, "detail": "toolsAndAuthOnly"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := s.c.call(ctx, "mcpServerStatus/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Data []struct {
				Name          string                     `json:"name"`
				RuntimeStatus *string                    `json:"runtimeStatus"`
				AuthStatus    string                     `json:"authStatus"`
				PluginID      *string                    `json:"pluginId"`
				Tools         map[string]json.RawMessage `json:"tools"`
				ToolsError    *string                    `json:"toolsError"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, err
		}
		s.mu.Lock()
		for _, v := range r.Data {
			st := "pending"
			if v.RuntimeStatus != nil {
				switch *v.RuntimeStatus {
				case "connected":
					st = "connected"
				case "authenticationRequired":
					st = "needs-auth"
				case "failed", "cancelled":
					st = "failed"
				case "disabled":
					st = "disabled"
				}
			} else if v.AuthStatus == "notLoggedIn" {
				st = "needs-auth"
			}
			m := MCPServer{Name: v.Name, Status: st, Tools: len(v.Tools),
				CanLogin: v.AuthStatus == "notLoggedIn" || v.AuthStatus == "oAuth"}
			if v.PluginID != nil {
				m.Source = "plugin"
			}
			m.Error = s.mcpErrs[v.Name]
			if m.Error == "" && v.ToolsError != nil {
				m.Error = *v.ToolsError
			}
			out = append(out, m)
		}
		s.mu.Unlock()
		if r.NextCursor == nil || *r.NextCursor == "" {
			break
		}
		cursor = *r.NextCursor
	}
	return out, nil
}

// MCPReconnect reloads the MCP configuration: Codex reconnects every server.
func (s *codexSession) MCPReconnect(ctx context.Context, _ string) error {
	_, err := s.c.call(ctx, "config/mcpServer/reload", map[string]any{})
	return err
}

func (s *codexSession) MCPToggle(context.Context, string, bool) error { return ErrMCPUnsupported }
func (s *codexSession) MCPLogout(context.Context, string) error       { return ErrMCPUnsupported }

func (s *codexSession) MCPLogin(ctx context.Context, name string) (MCPLogin, error) {
	res, err := s.c.call(ctx, "mcpServer/oauth/login", map[string]any{"name": name, "threadId": s.threadID, "timeoutSecs": 600})
	if err != nil {
		return MCPLogin{}, err
	}
	var r struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	_ = json.Unmarshal(res, &r)
	if r.AuthorizationURL == "" {
		return MCPLogin{}, errors.New("Codex did not give a sign-in address")
	}
	// Codex listens on localhost of the machine it runs on for the redirect
	return MCPLogin{AuthURL: r.AuthorizationURL, CallbackExpected: true}, nil
}

func (s *codexSession) MCPCallback(ctx context.Context, _ string, callbackURL string) error {
	if s.opts.Remote != nil {
		return errors.New("this agent runs on another machine: finish the sign-in in a browser there")
	}
	if err := finishLocalCallback(ctx, callbackURL); err != nil {
		return err
	}
	_, _ = s.c.call(ctx, "config/mcpServer/reload", map[string]any{})
	return nil
}
