package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Connector is a claude.ai connector (Notion, Gmail, …) the login brings
// along. Claude Code loads them for every session of a claude.ai login.
type Connector struct {
	Name   string `json:"name"`   // "Notion" (Claude Code calls it "claude.ai Notion")
	Status string `json:"status"` // connected | needs-auth | failed | pending | disabled
}

const claudeAIPrefix = "claude.ai "

// connectorTool is the tool-name prefix of a connector's tools.
var mcpNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func connectorTool(name string) string {
	return "mcp__" + mcpNameRe.ReplaceAllString(claudeAIPrefix+name, "_") + "__"
}

// Pocket Ask reads from connectors; it never changes anything there. The
// permission prompt carries no MCP annotations, so the verb in the tool
// name decides: the first word that is a known read or write verb.
var (
	readVerbs  = map[string]bool{"search": true, "fetch": true, "get": true, "list": true, "query": true, "read": true, "view": true, "download": true, "find": true, "lookup": true, "describe": true, "retrieve": true}
	writeVerbs = map[string]bool{"create": true, "update": true, "delete": true, "move": true, "duplicate": true, "restore": true, "set": true, "send": true, "spawn": true, "stop": true, "cancel": true, "resolve": true, "upload": true, "convert": true, "add": true, "remove": true, "edit": true, "post": true, "publish": true, "write": true, "archive": true, "reply": true, "draft": true, "invite": true, "share": true, "save": true, "import": true, "export": true, "run": true, "execute": true}
)

// ConnectorReadTool reports whether tool is a read-only tool of one of the
// allowed connectors (and of which).
func ConnectorReadTool(tool string, allowed []string) (connector string, ok bool) {
	for _, name := range allowed {
		rest, found := strings.CutPrefix(tool, connectorTool(name))
		if !found {
			continue
		}
		for _, word := range strings.FieldsFunc(strings.ToLower(rest), func(r rune) bool { return r == '-' || r == '_' }) {
			if writeVerbs[word] {
				return name, false
			}
			if readVerbs[word] {
				return name, true
			}
		}
		return name, false
	}
	return "", false
}

type mcpStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func parseMCPStatus(body json.RawMessage) []mcpStatus {
	var r struct {
		Servers []mcpStatus `json:"mcpServers"`
	}
	_ = json.Unmarshal(body, &r)
	return r.Servers
}

func claudeAIConnectors(list []mcpStatus) []Connector {
	out := []Connector{}
	for _, m := range list {
		if name, ok := strings.CutPrefix(m.Name, claudeAIPrefix); ok {
			out = append(out, Connector{Name: name, Status: m.Status})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// settling: some connector is still connecting, or (want) one we wait for
// is not listed yet; Claude Code fetches the list from claude.ai first.
func settling(list []Connector, want map[string]bool) bool {
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.Name] = true
		if c.Status == "pending" && (want == nil || want[c.Name]) {
			return true
		}
	}
	for n := range want {
		if !seen[n] {
			return true
		}
	}
	return false
}

// ListClaudeConnectors asks Claude Code which claude.ai connectors the login
// in env has, and whether each is signed in. No prompt, no session file.
func ListClaudeConnectors(ctx context.Context, bin, cwd string, env []string) ([]Connector, error) {
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd, err := LocalCmd(ctx, cwd, env, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence", "--permission-prompt-tool", "stdio", "--tools", "", "--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "", "--settings", `{"disableAllHooks":true}`)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	ask := func(n int) {
		_, _ = fmt.Fprintf(stdin, `{"type":"control_request","request_id":"st-%d","request":{"subtype":"mcp_status"}}`+"\n", n)
	}
	_, _ = stdin.Write([]byte(`{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}` + "\n"))
	ask(0)
	start := time.Now()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	var last []Connector
	for n := 1; sc.Scan(); {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || !strings.HasPrefix(m.Response.RequestID, "st-") {
			continue
		}
		last = claudeAIConnectors(parseMCPStatus(m.Response.Response))
		// an empty list early on: not fetched from claude.ai yet
		if !settling(last, nil) && (len(last) > 0 || time.Since(start) > 6*time.Second) {
			return last, nil
		}
		// connectors sign in in the background; a few seconds as a rule
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
			ask(n)
			n++
		}
	}
	if last != nil {
		return last, nil // some still connecting: report them as they are
	}
	if ctx.Err() != nil {
		return nil, errors.New("Claude Code did not answer in time")
	}
	return nil, errors.New("Claude Code did not list its connectors")
}

// control sends a control request and waits for its answer.
func (s *claudeSession) control(ctx context.Context, prefix string, req map[string]any) (controlReply, error) {
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("%s-%d", prefix, s.reqSeq)
	ch := make(chan controlReply, 1)
	s.waiters[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, id)
		s.mu.Unlock()
	}()
	if err := s.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return controlReply{}, err
	}
	select {
	case rep := <-ch:
		return rep, nil
	case <-s.done:
		return controlReply{}, ErrClosed
	case <-ctx.Done():
		return controlReply{}, ctx.Err()
	}
}

// setupConnectors runs before the first message of a Pocket Ask process: the
// login's claude.ai connectors connect in the background, so it waits for the
// allowed ones (a first turn would otherwise run without their tools), turns
// every other connector off, and says when an allowed one is not signed in.
func (s *claudeSession) setupConnectors() {
	defer close(s.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	allowed := map[string]bool{}
	for _, n := range s.opts.Connectors {
		allowed[n] = true
	}
	var list []Connector
	deadline := time.Now().Add(12 * time.Second)
	for {
		rep, err := s.control(ctx, "mcp", map[string]any{"subtype": "mcp_status"})
		if err != nil || !rep.ok {
			if s.opts.Log != nil {
				s.opts.Log.Warn("connectors: no status", "err", err, "reply", rep.err)
			}
			return
		}
		list = claudeAIConnectors(parseMCPStatus(rep.body))
		if !settling(list, allowed) || time.Now().After(deadline) {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	found := map[string]bool{}
	for _, c := range list {
		found[c.Name] = true
		if allowed[c.Name] {
			if c.Status == "disabled" {
				_, _ = s.control(ctx, "mcp", map[string]any{"subtype": "mcp_toggle", "serverName": claudeAIPrefix + c.Name, "enabled": true})
			}
			switch c.Status {
			case "needs-auth":
				s.notice("warn", c.Name+" is not connected for this Claude login. Connect it on claude.ai (Settings → Connectors) with the same account, then ask again.")
			case "failed":
				s.notice("warn", c.Name+" could not be reached just now; its tools are missing from this answer.")
			case "pending":
				s.notice("warn", c.Name+" is still connecting; its tools may be missing from this first answer.")
			}
			continue
		}
		if c.Status != "disabled" {
			_, _ = s.control(ctx, "mcp", map[string]any{"subtype": "mcp_toggle", "serverName": claudeAIPrefix + c.Name, "enabled": false})
		}
	}
	for _, n := range s.opts.Connectors {
		if !found[n] {
			s.notice("warn", n+" is not one of this Claude login's claude.ai connectors.")
		}
	}
}
