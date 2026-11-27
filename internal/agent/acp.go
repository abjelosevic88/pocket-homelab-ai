package agent

// The Agent Client Protocol (agentclientprotocol.com): JSON-RPC 2.0 over the
// agent's stdio, spoken by Gemini CLI, OpenCode, Qwen Code and more. One
// driver serves them all; acp_agents.go lists which ones Pocket AI offers.
//
// Pocket AI is the client. It does not offer the agent its file system or a
// terminal (the agent uses its own tools in the folder), answers permission
// requests, and turns session/update notifications into timeline items.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// acpMarks: what a holder would recognise turns by. ACP agents are not held
// (a restart of Pocket AI ends them; the next message loads the session
// again), so these only satisfy openLink.
var acpMarks = marks{`"method":"session/prompt"`, `"stopReason"`, nil}

// acpForceLoad: tests take the session/load way with agents that resume.
var acpForceLoad = false

// ErrACPBusy: an ACP agent takes no message while a turn runs (there is no
// steering in the protocol); the message waits for the turn to end.
var ErrACPBusy = errors.New("this agent takes the next message when its turn has ended")

// acpCaps is what the agent said it can do in initialize.
type acpCaps struct {
	LoadSession bool `json:"loadSession"`
	Prompt      struct {
		Image bool `json:"image"`
	} `json:"promptCapabilities"`
	MCP struct {
		HTTP bool `json:"http"`
		SSE  bool `json:"sse"`
	} `json:"mcpCapabilities"`
	Session struct {
		Resume *struct{} `json:"resume"`
		Fork   *struct{} `json:"fork"`
		Close  *struct{} `json:"close"`
	} `json:"sessionCapabilities"`
}

type acpAuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpSessionInfo is what session/new (load, resume, fork) answers: the modes
// and models, in the older modes/models form and/or as config options.
type acpSessionInfo struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models *struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID     string  `json:"modelId"`
			Name        string  `json:"name"`
			Description *string `json:"description"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
	// Antigravity says the plan of the account ("Google AI Pro")
	Meta struct {
		Antigravity struct {
			Tier struct {
				Name string `json:"name"`
			} `json:"tier"`
		} `json:"antigravity"`
	} `json:"_meta"`
}

type acpConfigOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	CurrentValue any    `json:"currentValue"`
	Options      []struct {
		Value       string `json:"value"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"options"`
}

// option finds the config option of a category ("mode", "model").
func (in *acpSessionInfo) option(category string) *acpConfigOption {
	for i := range in.ConfigOptions {
		if o := &in.ConfigOptions[i]; o.Category == category && o.Type == "select" {
			return o
		}
	}
	return nil
}

// modes and models as Pocket AI lists them, with the current ones.
func (in *acpSessionInfo) modes() (list []Mode, current string) {
	if o := in.option("mode"); o != nil {
		for _, v := range o.Options {
			list = append(list, Mode{ID: v.Value, Name: firstNonEmpty(v.Name, v.Value), Description: v.Description})
		}
		current, _ = o.CurrentValue.(string)
		return list, current
	}
	if in.Modes != nil {
		for _, m := range in.Modes.AvailableModes {
			list = append(list, Mode{ID: m.ID, Name: firstNonEmpty(m.Name, m.ID), Description: m.Description})
		}
		current = in.Modes.CurrentModeID
	}
	return list, current
}

func (in *acpSessionInfo) models() (list []Model, current string) {
	if o := in.option("model"); o != nil {
		for _, v := range o.Options {
			list = append(list, Model{ID: v.Value, Name: firstNonEmpty(v.Name, v.Value), Description: v.Description, Efforts: []string{}})
		}
		current, _ = o.CurrentValue.(string)
		return list, current
	}
	if in.Models != nil {
		for _, m := range in.Models.AvailableModels {
			d := ""
			if m.Description != nil {
				d = *m.Description
			}
			list = append(list, Model{ID: m.ModelID, Name: firstNonEmpty(m.Name, m.ModelID), Description: d, Efforts: []string{}})
		}
		current = in.Models.CurrentModelID
	}
	return list, current
}

// ---------- the connection ----------

// acpConn is a started agent after initialize.
type acpConn struct {
	c       *rpc
	ag      ACPAgent
	caps    acpCaps
	methods []acpAuthMethod
}

func openACP(ctx context.Context, o Options, ag ACPAgent, sink Sink) (*acpConn, error) {
	bin := firstNonEmpty(o.Bin, ag.Command())
	o.Hold = nil // never held: see acpMarks
	o.Env = append(append([]string{}, ag.Env...), o.Env...)
	ag.prepare(o.Env)
	args := append(append([]string{}, ag.Args...), ag.loginArgs(o.LoginDir)...)
	c, err := openRPCWith(ctx, o, bin, args, acpMarks, sink)
	if err != nil {
		return nil, err
	}
	c.v2 = true
	ictx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := c.call(ictx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "pocket-ai", "title": "Pocket AI", "version": Version},
	})
	if err != nil {
		c.close()
		return nil, fmt.Errorf("%s: %w%s", ag.Name, err, stderrHint(c.errb))
	}
	var r struct {
		Caps    acpCaps         `json:"agentCapabilities"`
		Methods []acpAuthMethod `json:"authMethods"`
	}
	_ = json.Unmarshal(res, &r)
	return &acpConn{c: c, ag: ag, caps: r.Caps, methods: r.Methods}, nil
}

// isAuthError: the agent wants a sign-in before it opens a session.
func isAuthError(err error) bool {
	var re *rpcError
	if errors.As(err, &re) && re.Code == -32000 {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "auth") || strings.Contains(low, "api key") || strings.Contains(low, "sign in") || strings.Contains(low, "log in")
}

// open runs a session call (session/new, load, resume, fork). When the
// agent wants a sign-in first, it is told which one Pocket AI set up
// (authenticate) and asked once more.
func (a *acpConn) open(ctx context.Context, method string, params map[string]any, env []string) (json.RawMessage, error) {
	res, err := a.c.call(ctx, method, params)
	if err == nil || !isAuthError(err) {
		return res, err
	}
	id := a.ag.authMethod(env, a.methods)
	if id == "" {
		return nil, &ACPSignInError{Agent: a.ag, Why: err.Error()}
	}
	if _, aerr := a.c.call(ctx, "authenticate", map[string]any{"methodId": id}); aerr != nil {
		return nil, &ACPSignInError{Agent: a.ag, Why: aerr.Error()}
	}
	res, err = a.c.call(ctx, method, params)
	if err != nil && isAuthError(err) {
		return nil, &ACPSignInError{Agent: a.ag, Why: err.Error()}
	}
	return res, err
}

// ACPSignInError: the agent has no sign-in or key Pocket AI can use.
type ACPSignInError struct {
	Agent ACPAgent
	Why   string
}

func (e *ACPSignInError) Error() string {
	if e.Agent.Logins { // its logins, here and on machines, are on the Machines page
		return e.Agent.Name + " is not signed in on this login (" + oneLine(e.Why, 200) + "). Sign it in, or copy a sign-in to it, in Settings → Machines."
	}
	return e.Agent.Name + " is not signed in (" + oneLine(e.Why, 200) + "). Set it up in Settings → Agents → " + e.Agent.Name + "."
}

// mcpServers turns Pocket AI's MCP config (a file here, or the JSON itself)
// into ACP's list. Only what the agent supports goes along.
func (a *acpConn) mcpServers(config string) []map[string]any {
	out := []map[string]any{}
	if strings.TrimSpace(config) == "" {
		return out
	}
	raw := []byte(config)
	if !strings.HasPrefix(strings.TrimSpace(config), "{") {
		var err error
		if raw, err = ReadConfined(config); err != nil {
			return out
		}
	}
	var cfg struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return out
	}
	for name, s := range cfg.Servers {
		switch {
		case s.URL != "" && (a.caps.MCP.HTTP || (s.Type == "sse" && a.caps.MCP.SSE)):
			typ := "http"
			if s.Type == "sse" {
				typ = "sse"
			}
			hs := []map[string]string{}
			for k, v := range s.Headers {
				hs = append(hs, map[string]string{"name": k, "value": v})
			}
			out = append(out, map[string]any{"type": typ, "name": name, "url": s.URL, "headers": hs})
		case s.Command != "":
			env := []map[string]string{}
			for k, v := range s.Env {
				env = append(env, map[string]string{"name": k, "value": v})
			}
			args := s.Args
			if args == nil {
				args = []string{}
			}
			out = append(out, map[string]any{"name": name, "command": s.Command, "args": args, "env": env})
		}
	}
	return out
}

// ---------- session ----------

type acpSession struct {
	opts Options
	sink Sink
	a    *acpConn
	c    *rpc
	ctx  context.Context
	info acpSessionInfo

	mu        sync.Mutex
	sessionID string
	running   bool
	turnFrom  time.Time
	turnN     int
	pending   map[string]acpPending
	// loading: session/load replays the conversation, which the timeline
	// has already; it is dropped until the marker after the answer.
	loading bool
	// what streams now: the item key, its kind and the text so far
	textKey, textKind string
	seg               int
	text              map[string]*strings.Builder
	tools             map[string]*Tool
	// usage: the session's cost so far (usage_update), and where the turn began
	cost, costFrom float64
	// the system prompt goes in front of the first message of a new session
	preamble string
	closed   bool
	exited   chan struct{}
	// epoch makes the item keys this process invents unique in the thread:
	// a later process counts its turns from one again
	epoch int64
}

type acpPending struct {
	rpcID   json.RawMessage
	options []acpPermOption
}

type acpPermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// StartACP starts an ACP agent and opens (resumes, forks) its session.
func StartACP(ctx context.Context, ag ACPAgent, o Options, sink Sink) (Session, error) {
	a, err := openACP(ctx, o, ag, sink)
	if err != nil {
		return nil, err
	}
	s := &acpSession{opts: o, sink: sink, a: a, c: a.c, ctx: ctx, pending: map[string]acpPending{},
		text: map[string]*strings.Builder{}, tools: map[string]*Tool{}, exited: make(chan struct{}), epoch: time.Now().UnixMilli()}
	a.c.onNotify = s.notify
	a.c.onRequest = s.request
	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	params := map[string]any{"cwd": o.Cwd, "mcpServers": a.mcpServers(o.MCPConfig)}
	method := "session/new"
	switch {
	case o.ResumeID != "" && o.Fork:
		if a.caps.Session.Fork == nil {
			a.c.close()
			return nil, fmt.Errorf("%s cannot copy a conversation; start a new one instead", ag.Name)
		}
		method = "session/fork"
		params["sessionId"] = o.ResumeID
	case o.ResumeID != "" && a.caps.Session.Resume != nil && !acpForceLoad:
		method = "session/resume"
		params["sessionId"] = o.ResumeID
	case o.ResumeID != "" && a.caps.LoadSession:
		method = "session/load"
		params["sessionId"] = o.ResumeID
		s.loading = true
	case o.ResumeID != "":
		a.c.close()
		return nil, fmt.Errorf("%s cannot continue an earlier conversation once it has stopped; start a new one", ag.Name)
	}
	res, err := a.open(ictx, method, params, o.Env)
	if method == "session/load" {
		// everything the load replayed is queued before this step
		a.c.enqueue(rpcIn{fn: func() {
			s.mu.Lock()
			s.loading = false
			s.mu.Unlock()
		}})
	}
	if err != nil {
		a.c.close()
		var se *ACPSignInError
		if errors.As(err, &se) {
			return nil, err
		}
		return nil, fmt.Errorf("%s %s: %w%s", ag.Name, method, err, stderrHint(a.c.errb))
	}
	_ = json.Unmarshal(res, &s.info)
	s.sessionID = s.info.SessionID
	if s.sessionID == "" {
		s.sessionID = o.ResumeID
	}
	if s.sessionID == "" || (o.Fork && s.sessionID == o.ResumeID) {
		a.c.close()
		return nil, fmt.Errorf("%s did not open a session", ag.Name)
	}
	if (o.ResumeID == "" || o.Fork) && strings.TrimSpace(o.SystemPrompt) != "" {
		s.preamble = o.SystemPrompt
	}
	// the thread's mode and model, when they are not what the agent began with
	if o.Mode != "" {
		if err := s.setMode(ictx, o.Mode); err != nil {
			s.notice("warn", "Could not switch "+ag.Name+" to mode "+o.Mode+": "+err.Error())
		} else {
			acp, _ := ag.mode(o.Mode)
			s.setCurrentMode(acp)
		}
	}
	if _, cur := s.info.models(); o.Model != "" && o.Model != "default" && o.Model != cur {
		if err := s.setModel(ictx, o.Model); err != nil {
			s.notice("warn", "Could not switch "+ag.Name+" to model "+o.Model+": "+err.Error())
		}
	}
	sink.SessionID(s.sessionID)
	s.watch(ctx)
	return s, nil
}

func (s *acpSession) setMode(ctx context.Context, mode string) error {
	mode, _ = s.a.ag.mode(mode)
	if _, cur := s.info.modes(); mode == cur {
		return nil
	}
	if o := s.info.option("mode"); o != nil {
		_, err := s.c.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.sessionID, "configId": o.ID, "value": mode})
		return err
	}
	if s.info.Modes == nil {
		return errors.New("it has no modes")
	}
	_, err := s.c.call(ctx, "session/set_mode", map[string]any{"sessionId": s.sessionID, "modeId": mode})
	return err
}

func (s *acpSession) setModel(ctx context.Context, model string) error {
	if o := s.info.option("model"); o != nil {
		_, err := s.c.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.sessionID, "configId": o.ID, "value": model})
		return err
	}
	if s.info.Models == nil {
		return errors.New("it does not let a client choose the model")
	}
	_, err := s.c.call(ctx, "session/set_model", map[string]any{"sessionId": s.sessionID, "modelId": model})
	return err
}

func (s *acpSession) notice(level, text string) {
	s.sink.Item(Item{Key: fmt.Sprintf("acp-%d", time.Now().UnixNano()), Kind: "notice", Level: level, Text: text})
}

// watch reports the agent's end to the sink.
func (s *acpSession) watch(ctx context.Context) {
	c, o, sink := s.c, s.opts, s.sink
	go func() {
		lost := ""
		select {
		case <-c.done:
			lost = lostLink(o.Remote, c.err, c.errb.String())
		case <-ctx.Done():
			c.close()
		}
		s.mu.Lock()
		running, turnFrom := s.running, s.turnFrom
		s.running = false
		s.closed = true
		s.mu.Unlock()
		if running {
			why := s.a.ag.Name + " exited" + stderrHint(c.errb)
			if lost != "" {
				why = lost
			}
			sink.TurnDone(TurnInfo{OK: false, DurationMS: time.Since(turnFrom).Milliseconds(), Error: why})
		}
		close(s.exited)
		if lost != "" {
			sink.Exited(errors.New(lost))
		} else {
			sink.Exited(nil)
		}
	}()
}

func (s *acpSession) Done() <-chan struct{} { return s.exited }

func (s *acpSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	go s.c.close()
	return nil
}

func (s *acpSession) SetMode(mode string) error {
	s.mu.Lock()
	s.opts.Mode = mode
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	err := s.setMode(ctx, mode)
	if err == nil {
		acp, _ := s.a.ag.mode(mode)
		s.mu.Lock()
		s.setCurrentMode(acp)
		s.mu.Unlock()
	}
	return err
}

// setCurrentMode notes the agent's mode now (called with mu held).
func (s *acpSession) setCurrentMode(acp string) {
	if o := s.info.option("mode"); o != nil {
		o.CurrentValue = acp
	} else if s.info.Modes != nil {
		s.info.Modes.CurrentModeID = acp
	}
}

func (s *acpSession) Send(in Input) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrACPBusy
	}
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	prompt := []map[string]any{}
	if s.preamble != "" {
		prompt = append(prompt, map[string]any{"type": "text", "text": "<instructions from Pocket AI, the app this conversation runs in>\n" + s.preamble + "\n</instructions>"})
		s.preamble = ""
	}
	s.mu.Unlock()
	text := in.Text
	if strings.TrimSpace(text) == "" && len(in.Images) > 0 {
		text = "(see the attached image)"
	}
	prompt = append(prompt, map[string]any{"type": "text", "text": text})
	for _, im := range in.Images {
		if !s.a.caps.Prompt.Image {
			prompt = append(prompt, map[string]any{"type": "text", "text": "(an image is attached at " + im.Path + ")"})
			continue
		}
		data, err := os.ReadFile(im.Path)
		if err != nil {
			return fmt.Errorf("read image: %w", err)
		}
		prompt = append(prompt, map[string]any{"type": "image", "mimeType": im.Mime, "data": base64.StdEncoding.EncodeToString(data)})
	}
	s.mu.Lock()
	s.running = true
	s.turnFrom = time.Now()
	s.turnN++
	s.costFrom = s.cost
	s.textKey, s.textKind = "", ""
	turn := s.turnN
	s.mu.Unlock()
	s.sink.TurnStarted()
	go s.prompt(turn, prompt)
	return nil
}

// prompt runs one turn: session/prompt answers when it has ended. The end
// is handled in the dispatcher's order, after the turn's last updates.
func (s *acpSession) prompt(turn int, prompt []map[string]any) {
	res, err := s.c.call(s.ctx, "session/prompt", map[string]any{"sessionId": s.sessionID, "prompt": prompt})
	if errors.Is(err, ErrClosed) {
		return // watch reports it
	}
	s.c.enqueue(rpcIn{fn: func() { s.endTurn(turn, res, err) }})
}

func (s *acpSession) endTurn(turn int, res json.RawMessage, err error) {
	s.mu.Lock()
	if !s.running || s.turnN != turn {
		s.mu.Unlock()
		return
	}
	s.running = false
	items := s.flushText()
	dur := time.Since(s.turnFrom).Milliseconds()
	cost := s.cost - s.costFrom
	for k, p := range s.pending { // a permission still open: the turn is over
		_ = s.c.reply(p.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		delete(s.pending, k)
	}
	s.tools = map[string]*Tool{}
	s.mu.Unlock()
	for _, it := range items {
		s.sink.Item(it)
	}
	t := TurnInfo{DurationMS: dur, CostUSD: cost}
	if err != nil {
		t.Error = err.Error()
		if isAuthError(err) {
			t.Error = (&ACPSignInError{Agent: s.a.ag, Why: err.Error()}).Error()
		}
		s.sink.TurnDone(t)
		return
	}
	var r struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens      int64 `json:"inputTokens"`
			OutputTokens     int64 `json:"outputTokens"`
			CachedReadTokens int64 `json:"cachedReadTokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(res, &r)
	if r.Usage != nil {
		t.InputTokens, t.OutputTokens, t.CachedTokens = r.Usage.InputTokens+r.Usage.CachedReadTokens, r.Usage.OutputTokens, r.Usage.CachedReadTokens
	}
	switch r.StopReason {
	case "end_turn", "":
		t.OK = true
	case "cancelled":
		t.Interrupted = true
	case "max_tokens":
		t.Error = "The answer reached the model's length limit."
	case "max_turn_requests":
		t.Error = "The agent reached its limit of steps for one turn."
	case "refusal":
		t.Error = "The model declined to go on."
	default:
		t.Error = r.StopReason
	}
	s.sink.TurnDone(t)
}

func (s *acpSession) Interrupt() error {
	s.mu.Lock()
	running := s.running
	for k, p := range s.pending {
		_ = s.c.reply(p.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		delete(s.pending, k)
	}
	s.mu.Unlock()
	if !running {
		return nil
	}
	return s.c.send(map[string]any{"method": "session/cancel", "params": map[string]any{"sessionId": s.sessionID}})
}

func (s *acpSession) Respond(id string, r Response) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("that request is no longer waiting")
	}
	want := []string{"allow_once", "allow_always"}
	switch r.Decision {
	case "allow_session":
		want = []string{"allow_always", "allow_once"}
	case "deny", "defer":
		want = []string{"reject_once", "reject_always"}
	}
	for _, k := range want {
		for _, o := range p.options {
			if o.Kind == k {
				return s.c.reply(p.rpcID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}})
			}
		}
	}
	return s.c.reply(p.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
}

// request handles agent→client calls: permissions. Files and terminals were
// not offered, so those are refused.
func (s *acpSession) request(id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		_ = s.c.replyError(id, "Pocket AI does not handle "+method)
		return
	}
	var p struct {
		ToolCall acpToolCall     `json:"toolCall"`
		Options  []acpPermOption `json:"options"`
	}
	_ = json.Unmarshal(params, &p)
	key := "rq-" + strings.Trim(string(id), `"`)
	s.mu.Lock()
	// the tool it is about, as far as it is known
	t := s.tools[p.ToolCall.ID]
	if t == nil {
		t = &Tool{Status: "running"}
		s.tools[p.ToolCall.ID] = t
	}
	p.ToolCall.apply(t)
	tc := *t
	_, auto := s.a.ag.mode(s.opts.Mode)
	if !auto {
		s.pending[key] = acpPending{rpcID: id, options: p.Options}
	}
	s.mu.Unlock()
	if auto { // Full access: Pocket AI says yes for the person
		for _, k := range []string{"allow_once", "allow_always"} {
			for _, o := range p.Options {
				if o.Kind == k {
					_ = s.c.reply(id, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}})
					return
				}
			}
		}
		_ = s.c.reply(id, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		return
	}
	r := Request{ID: key, Type: "permission", Status: "pending", ToolName: tc.Name, Input: tc.Input}
	r.Title = tc.Title
	if r.Title == "" {
		r.Title = "Use " + firstNonEmpty(tc.Name, "a tool")
	}
	if tc.Kind == "bash" && !strings.HasPrefix(strings.ToLower(r.Title), "run") {
		r.Title = "Run " + r.Title
	}
	for _, o := range p.Options {
		if o.Kind == "allow_always" {
			r.CanAllowSession = true
		}
	}
	if p.ToolCall.ID != "" {
		s.sink.Item(Item{Key: "tc-" + p.ToolCall.ID, Kind: "tool", Tool: &tc})
	}
	s.sink.Request(r)
}

// acpToolCall is a tool_call / tool_call_update (every field optional).
type acpToolCall struct {
	ID        string            `json:"toolCallId"`
	Title     *string           `json:"title"`
	Kind      *string           `json:"kind"`
	Status    *string           `json:"status"`
	Content   []json.RawMessage `json:"content"`
	RawInput  json.RawMessage   `json:"rawInput"`
	RawOutput json.RawMessage   `json:"rawOutput"`
	Locations []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

var acpKinds = map[string][2]string{ // ACP kind → Pocket AI tool kind, name
	"read": {"read", "Read"}, "edit": {"edit", "Edit"}, "delete": {"edit", "Delete"}, "move": {"edit", "Move"},
	"search": {"search", "Search"}, "execute": {"bash", "Shell"}, "think": {"other", "Think"},
	"fetch": {"web", "Fetch"}, "switch_mode": {"other", "Mode"}, "other": {"other", "Tool"},
}

// apply merges what the update says into t.
func (u *acpToolCall) apply(t *Tool) {
	if u.Kind != nil {
		k := acpKinds[*u.Kind]
		if k[0] == "" {
			k = acpKinds["other"]
		}
		t.Kind, t.Name = k[0], k[1]
	}
	if t.Kind == "" {
		t.Kind, t.Name = "other", "Tool"
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) != "" {
		t.Title = oneLine(*u.Title, 160)
		if t.Kind == "other" && !strings.Contains(*u.Title, " ") && t.Name == "Tool" {
			t.Name = *u.Title // a bare tool name: "todowrite", "pocket_notes_search"
		}
	}
	if (t.Kind == "edit" || t.Kind == "read") && t.Title != "" && !strings.Contains(t.Title, " ") && strings.ContainsAny(t.Title, "/.") {
		t.Title = t.Name + " " + filepath.Base(t.Title) // a bare path
	}
	if t.Title == "" && len(u.Locations) > 0 {
		t.Title = t.Name + " " + u.Locations[0].Path
	}
	if u.Status != nil {
		switch *u.Status {
		case "pending", "in_progress":
			t.Status = "running"
		case "completed":
			t.Status = "done"
		case "failed":
			t.Status = "error"
		}
	}
	if t.Status == "" {
		t.Status = "running"
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "{}" && string(u.RawInput) != "null" {
		t.Input = u.RawInput
	}
	if len(u.Content) > 0 {
		var out []string
		diff := ""
		for _, raw := range u.Content {
			var c struct {
				Type    string `json:"type"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Path    string  `json:"path"`
				OldText *string `json:"oldText"`
				NewText string  `json:"newText"`
			}
			if json.Unmarshal(raw, &c) != nil {
				continue
			}
			switch c.Type {
			case "content":
				if c.Content.Type == "text" && c.Content.Text != "" {
					out = append(out, c.Content.Text)
				}
			case "diff":
				old := ""
				if c.OldText != nil {
					old = *c.OldText
				}
				diff += simpleDiff(c.Path, old, c.NewText)
			}
		}
		if len(out) > 0 {
			t.Output = clip(strings.Join(out, "\n"), 60000)
		}
		if diff != "" {
			t.Diff = clip(diff, 60000)
			if t.Kind == "other" {
				t.Kind, t.Name = "edit", "Edit"
			}
		}
	}
	if t.Output == "" && len(u.RawOutput) > 0 && t.Status != "running" {
		var o struct {
			Output string `json:"output"`
		}
		if json.Unmarshal(u.RawOutput, &o) == nil && o.Output != "" {
			t.Output = clip(o.Output, 60000)
		}
	}
	if t.Kind == "bash" && len(t.Input) > 0 {
		var in struct {
			Command any `json:"command"`
		}
		if json.Unmarshal(t.Input, &in) == nil {
			switch c := in.Command.(type) {
			case string:
				if c != "" {
					t.Title = oneLine(c, 160)
				}
			case []any:
				parts := []string{}
				for _, x := range c {
					parts = append(parts, fmt.Sprint(x))
				}
				if len(parts) > 0 {
					t.Title = oneLine(strings.Join(parts, " "), 160)
				}
			}
		}
	}
}

// todos reads a to-do tool's list (OpenCode todowrite, Gemini write_todos).
func acpTodos(input json.RawMessage) ([]Todo, bool) {
	var in struct {
		Todos []struct {
			Content     string `json:"content"`
			Description string `json:"description"`
			Status      string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Todos) == 0 {
		return nil, false
	}
	out := []Todo{}
	for _, t := range in.Todos {
		out = append(out, Todo{Text: firstNonEmpty(t.Content, t.Description), Status: todoStatus(t.Status)})
	}
	return out, true
}

func todoStatus(s string) string {
	switch s {
	case "in_progress", "inProgress", "in-progress":
		return "in_progress"
	case "completed", "done":
		return "completed"
	}
	return "pending"
}

// flushText ends the streamed text item: its whole text, to write once mu
// is let go (called with mu held).
func (s *acpSession) flushText() []Item {
	if s.textKey == "" {
		return nil
	}
	var out []Item
	if b := s.text[s.textKey]; b != nil {
		out = append(out, Item{Key: s.textKey, Kind: s.textKind, Text: b.String()})
		delete(s.text, s.textKey)
	}
	s.textKey, s.textKind = "", ""
	return out
}

func (s *acpSession) notify(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var u struct {
		Kind      string          `json:"sessionUpdate"`
		MessageID string          `json:"messageId"`
		Content   json.RawMessage `json:"content"` // a block (chunks) or a list (tool calls)
		Entries   []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"entries"`
		Used *int64 `json:"used"`
		Size int64  `json:"size"`
		Cost *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if json.Unmarshal(p.Update, &u) != nil {
		return
	}
	// what to tell the sink, once mu is let go
	var out []Item
	var delta [2]string
	var ctxNow *Context
	defer func() {
		for _, it := range out {
			s.sink.Item(it)
		}
		if delta[0] != "" {
			s.sink.Delta(delta[0], "text", delta[1])
		}
		if ctxNow != nil {
			s.sink.Context(*ctxNow)
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loading {
		return
	}
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &block) != nil || block.Type != "text" || block.Text == "" {
			return
		}
		kind := "assistant"
		if u.Kind == "agent_thought_chunk" {
			kind = "thinking"
		}
		key := ""
		if u.MessageID != "" {
			key = kind[:1] + "-" + u.MessageID
		}
		if key == "" || key != s.textKey {
			if key == "" && s.textKey != "" && s.textKind == kind {
				key = s.textKey // the same run of text, no ids
			} else {
				out = append(out, s.flushText()...)
				if key == "" {
					s.seg++
					key = fmt.Sprintf("%s-%x-%d-%d", kind[:1], s.epoch, s.turnN, s.seg)
				}
			}
		}
		b := s.text[key]
		if b == nil {
			b = &strings.Builder{}
			s.text[key] = b
			s.textKey, s.textKind = key, kind
			b.WriteString(block.Text)
			out = append(out, Item{Key: key, Kind: kind, Text: block.Text})
			return
		}
		b.WriteString(block.Text)
		delta = [2]string{key, block.Text}
	case "tool_call", "tool_call_update":
		var tc acpToolCall
		if json.Unmarshal(p.Update, &tc) != nil || tc.ID == "" {
			return
		}
		out = append(out, s.flushText()...)
		t := s.tools[tc.ID]
		if t == nil {
			t = &Tool{}
			s.tools[tc.ID] = t
		}
		tc.apply(t)
		if todos, ok := acpTodos(t.Input); ok {
			out = append(out, Item{Key: "todos-" + tc.ID, Kind: "todos", Todos: todos})
			return
		}
		cp := *t
		out = append(out, Item{Key: "tc-" + tc.ID, Kind: "tool", Tool: &cp})
	case "plan":
		if len(u.Entries) == 0 {
			return
		}
		todos := []Todo{}
		for _, e := range u.Entries {
			todos = append(todos, Todo{Text: e.Content, Status: todoStatus(e.Status)})
		}
		out = append(out, Item{Key: fmt.Sprintf("plan-%x-%d", s.epoch, s.turnN), Kind: "todos", Todos: todos})
	case "current_mode_update":
		var m struct {
			ID string `json:"currentModeId"`
		}
		if json.Unmarshal(p.Update, &m) == nil && m.ID != "" {
			s.setCurrentMode(m.ID)
		}
	case "usage_update":
		if u.Cost != nil && (u.Cost.Currency == "" || u.Cost.Currency == "USD") {
			s.cost = u.Cost.Amount
		}
		if u.Used != nil {
			ctxNow = &Context{Tokens: *u.Used, Window: u.Size}
		}
	}
}
