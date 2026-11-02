package server

import (
	"errors"
	"net/http"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The MCP panel: what /mcp is in the terminal. The thread's live process
// (started if none runs) reports its MCP servers and reconnects, turns off
// and on, signs in and out the ones it was asked to.

// mcpSession is the thread's live session, started if none runs.
func (s *Server) mcpSession(w http.ResponseWriter, r *http.Request) (*store.Thread, agent.MCPManager) {
	if !s.requireRun(w, r) {
		return nil, nil
	}
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return nil, nil
	}
	if t.Source == "ask" {
		writeErr(w, http.StatusBadRequest, "Pocket Ask chats keep the connectors they were set up with")
		return nil, nil
	}
	run, err := s.rt.ensure(t)
	if err != nil {
		s.fail(w, err, "start agent")
		return nil, nil
	}
	m, ok := run.sess.(agent.MCPManager)
	if !ok {
		writeErr(w, http.StatusBadRequest, "this agent cannot list its MCP servers")
		return nil, nil
	}
	return t, m
}

// GET /threads/{id}/mcp
func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	_, m := s.mcpSession(w, r)
	if m == nil {
		return
	}
	list, err := m.MCPStatus(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": list})
}

// POST /threads/{id}/mcp {action, server, url}: reconnect (server "" = all),
// enable, disable, login, callback (url: where the sign-in redirected), logout.
func (s *Server) handleMCPAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
		Server string `json:"server"`
		URL    string `json:"url"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Server == "" && in.Action != "reconnect" {
		writeErr(w, http.StatusBadRequest, "which server?")
		return
	}
	t, m := s.mcpSession(w, r)
	if m == nil {
		return
	}
	ctx := r.Context()
	var err error
	out := map[string]any{}
	switch in.Action {
	case "reconnect":
		err = m.MCPReconnect(ctx, in.Server)
	case "enable", "disable":
		err = m.MCPToggle(ctx, in.Server, in.Action == "enable")
	case "login":
		var l agent.MCPLogin
		if l, err = m.MCPLogin(ctx, in.Server); err == nil {
			out["login"] = l
		}
	case "callback":
		if in.URL == "" {
			writeErr(w, http.StatusBadRequest, "paste the address the browser ended on")
			return
		}
		err = m.MCPCallback(ctx, in.Server, in.URL)
	case "logout":
		err = m.MCPLogout(ctx, in.Server)
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	s.log.Info("mcp action", "thread", t.ID, "action", in.Action, "server", in.Server, "err", err)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, agent.ErrMCPUnsupported) {
			code = http.StatusBadRequest
		}
		writeErr(w, code, err.Error())
		return
	}
	if list, e := m.MCPStatus(ctx); e == nil {
		out["servers"] = list
	}
	writeJSON(w, http.StatusOK, out)
}
