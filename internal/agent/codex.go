package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// CodexModes are the approval/sandbox pairs offered for Codex.
var CodexModes = []Mode{
	{ID: "untrusted", Name: "Ask first", Description: "Asks before running anything that is not a plain read."},
	{ID: "on-request", Name: "Sandboxed", Description: "Works inside the folder freely; asks before leaving the sandbox."},
	{ID: "read-only", Name: "Read only", Description: "Can look but not change anything; asks before writing."},
	{ID: "full-access", Name: "Full access", Description: "No sandbox, never asks. Runs anything as the server user."},
}

// codexSandboxPolicy is the per-turn form of the sandbox for a mode.
func codexSandboxPolicy(mode string) map[string]any {
	_, sb := codexPolicy(mode)
	switch sb {
	case "danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	case "read-only":
		return map[string]any{"type": "readOnly"}
	}
	return map[string]any{"type": "workspaceWrite"}
}

func codexPolicy(mode string) (approval, sandbox string) {
	switch mode {
	case "untrusted":
		return "untrusted", "workspace-write"
	case "read-only":
		return "on-request", "read-only"
	case "full-access":
		return "never", "danger-full-access"
	default:
		return "on-request", "workspace-write"
	}
}

// rpc is a JSON-RPC client over a codex app-server process.
type rpc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex
	next  atomic.Int64
	mu    sync.Mutex
	calls map[int64]chan rpcReply
	done  chan struct{}
	err   error // how the process ended, once done is closed
	errb  *tail
	// onNotify / onRequest are called in order from a dispatcher goroutine
	// (not the reader), so handlers may make calls of their own.
	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	qmu   sync.Mutex
	qcond *sync.Cond
	queue []rpcIn
	qdone bool
}

type rpcIn struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

func (c *rpc) dispatch() {
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 && !c.qdone {
			c.qcond.Wait()
		}
		if len(c.queue) == 0 && c.qdone {
			c.qmu.Unlock()
			return
		}
		m := c.queue[0]
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		if len(m.id) > 0 {
			if c.onRequest != nil {
				c.onRequest(m.id, m.method, m.params)
			} else {
				_ = c.replyError(m.id, "not supported")
			}
		} else if c.onNotify != nil {
			c.onNotify(m.method, m.params)
		}
	}
}

func (c *rpc) enqueue(m rpcIn) {
	c.qmu.Lock()
	c.queue = append(c.queue, m)
	c.qmu.Unlock()
	c.qcond.Signal()
}

type rpcReply struct {
	Result json.RawMessage
	Err    error
}

func startRPC(bin, cwd string, env []string, remote ...*Remote) (*rpc, error) {
	if bin == "" {
		bin = "codex"
	}
	var r *Remote
	if len(remote) > 0 {
		r = remote[0]
	}
	cmd, err := Command(r, cwd, env, bin, "app-server")
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c := &rpc{cmd: cmd, calls: map[int64]chan rpcReply{}, done: make(chan struct{}), errb: &tail{n: 8 << 10}}
	c.qcond = sync.NewCond(&c.qmu)
	cmd.Stderr = c.errb
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.stdin = in
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s app-server: %w", bin, err)
	}
	go c.read(out)
	go c.dispatch()
	return c, nil
}

func (c *rpc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *rpc) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.next.Add(1)
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	c.calls[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
	}()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.Result, r.Err
	case <-c.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *rpc) reply(id json.RawMessage, result any) error {
	return c.send(map[string]any{"id": id, "result": result})
}

func (c *rpc) replyError(id json.RawMessage, msg string) error {
	return c.send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": msg}})
}

func (c *rpc) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method != "":
			c.enqueue(rpcIn{id: m.ID, method: m.Method, params: m.Params})
		case len(m.ID) > 0:
			var id int64
			_ = json.Unmarshal(m.ID, &id)
			c.mu.Lock()
			ch := c.calls[id]
			c.mu.Unlock()
			if ch != nil {
				rep := rpcReply{Result: m.Result}
				if m.Error != nil {
					rep.Err = errors.New(m.Error.Message)
				}
				ch <- rep
			}
		}
	}
	c.err = c.cmd.Wait()
	close(c.done)
	c.qmu.Lock()
	c.qdone = true
	c.qmu.Unlock()
	c.qcond.Broadcast()
}

func (c *rpc) close() {
	c.wmu.Lock()
	_ = c.stdin.Close()
	c.wmu.Unlock()
	if c.cmd.Process == nil {
		return
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

func (c *rpc) initialize(ctx context.Context, version string) error {
	return c.initializeWithCapabilities(ctx, version, nil)
}

func (c *rpc) initializeWithCapabilities(ctx context.Context, version string, capabilities map[string]any) error {
	params := map[string]any{"clientInfo": map[string]any{"name": "pocket-ai", "title": "Pocket AI", "version": version}}
	if capabilities != nil {
		params["capabilities"] = capabilities
	}
	_, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	return c.send(map[string]any{"method": "initialized"})
}

// ---------- session ----------

type codexSession struct {
	opts     Options
	sink     Sink
	c        *rpc
	threadID string
	ctx      context.Context

	mu         sync.Mutex
	turnID     string
	running    bool
	turnFrom   time.Time
	pending    map[string]codexPending
	tokensBase struct{ in, out, cached int64 }
	tokensNow  struct{ in, out, cached int64 }
	closed     bool
	exited     chan struct{}
}

type codexPending struct {
	rpcID     json.RawMessage
	method    string
	params    json.RawMessage
	questions []Question
}

// Version is reported to the CLIs as the client version.
var Version = "dev"

// StartCodex starts `codex app-server` and opens (or resumes, or forks) a thread.
func StartCodex(ctx context.Context, o Options, sink Sink) (Session, error) {
	c, err := startRPC(o.Bin, o.Cwd, o.Env, o.Remote)
	if err != nil {
		return nil, err
	}
	s := &codexSession{opts: o, sink: sink, c: c, ctx: ctx, pending: map[string]codexPending{}, exited: make(chan struct{})}
	c.onNotify = s.notify
	c.onRequest = s.request
	ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := c.initialize(ictx, Version); err != nil {
		c.close()
		return nil, fmt.Errorf("codex: %w%s", err, stderrHint(c.errb))
	}
	approval, sandbox := codexPolicy(o.Mode)
	params := map[string]any{"cwd": o.Cwd, "approvalPolicy": approval, "sandbox": sandbox}
	if o.Model != "" && o.Model != "default" {
		params["model"] = o.Model
	}
	if strings.TrimSpace(o.SystemPrompt) != "" {
		params["developerInstructions"] = o.SystemPrompt
	}
	if o.MCPConfig != "" {
		config, err := codexPocketConfig(o.MCPConfig)
		if err != nil {
			c.close()
			return nil, err
		}
		params["config"] = config
	}
	if o.Speed != "" {
		params["serviceTier"] = o.Speed
	}
	method := "thread/start"
	if o.ResumeID != "" {
		params["threadId"] = o.ResumeID
		params["excludeTurns"] = true
		method = "thread/resume"
		if o.Fork {
			method = "thread/fork"
		}
	}
	res, err := c.call(ictx, method, params)
	if err != nil {
		c.close()
		return nil, fmt.Errorf("codex %s: %w%s", method, err, stderrHint(c.errb))
	}
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(res, &r)
	if o.Fork && (r.Thread.ID == "" || r.Thread.ID == o.ResumeID) {
		c.close()
		return nil, fmt.Errorf("codex: the agent did not create a separate session")
	}
	s.threadID = r.Thread.ID
	if s.threadID == "" {
		s.threadID = o.ResumeID
	}
	sink.SessionID(s.threadID)
	go func() {
		lost := ""
		select {
		case <-c.done:
			lost = lostLink(o.Remote, c.err, c.errb.String())
		case <-ctx.Done():
			c.close()
		}
		s.mu.Lock()
		running := s.running
		turnID, turnFrom := s.turnID, s.turnFrom
		s.running = false
		s.closed = true
		s.mu.Unlock()
		if running {
			why := "Codex exited" + stderrHint(c.errb)
			if lost != "" {
				why = lost
			}
			sink.TurnDone(TurnInfo{ID: turnID, OK: false, DurationMS: time.Since(turnFrom).Milliseconds(), Error: why})
		}
		close(s.exited)
		if lost != "" {
			sink.Exited(errors.New(lost))
		} else {
			sink.Exited(nil)
		}
	}()
	return s, nil
}

func stderrHint(t *tail) string {
	if s := t.String(); s != "" {
		return ": " + oneLine(lastLines(s, 3), 300)
	}
	return ""
}

func (s *codexSession) Done() <-chan struct{} { return s.exited }

func (s *codexSession) Close() error {
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

func (s *codexSession) Send(in Input) error {
	input := []map[string]any{}
	text := in.Text
	if strings.TrimSpace(text) == "" && len(in.Images) > 0 {
		text = "(see the attached image)"
	}
	input = append(input, map[string]any{"type": "text", "text": text})
	for _, im := range in.Images {
		if s.opts.Remote == nil {
			input = append(input, map[string]any{"type": "localImage", "path": im.Path})
			continue
		}
		// the file is here, the agent is not: send the picture itself
		data, err := os.ReadFile(im.Path)
		if err != nil {
			return fmt.Errorf("read image: %w", err)
		}
		input = append(input, map[string]any{"type": "image", "url": "data:" + im.Mime + ";base64," + base64.StdEncoding.EncodeToString(data)})
	}
	params := map[string]any{"threadId": s.threadID, "input": input}
	s.mu.Lock()
	mode := s.opts.Mode
	s.mu.Unlock()
	approval, _ := codexPolicy(mode)
	params["approvalPolicy"] = approval
	params["sandboxPolicy"] = codexSandboxPolicy(mode)
	if s.opts.Effort != "" {
		params["effort"] = s.opts.Effort
	}
	if s.opts.Speed != "" {
		params["serviceTier"] = s.opts.Speed
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		// Steer the running turn with the extra message.
		_, err := s.c.call(s.ctx, "turn/steer", map[string]any{"threadId": s.threadID, "input": input, "expectedTurnId": s.turnID})
		return err
	}
	s.running = true
	s.turnFrom = time.Now()
	s.tokensBase = s.tokensNow
	s.mu.Unlock()
	s.sink.TurnStarted()
	res, err := s.c.call(s.ctx, "turn/start", params)
	if err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.sink.TurnDone(TurnInfo{OK: false, Error: err.Error()})
		return err
	}
	var r struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &r)
	s.mu.Lock()
	if s.turnID == "" || r.Turn.ID != "" {
		s.turnID = r.Turn.ID
	}
	s.mu.Unlock()
	return nil
}

// SetMode takes effect with the next turn (the policy is sent on turn/start).
func (s *codexSession) SetMode(mode string) error {
	s.mu.Lock()
	s.opts.Mode = mode
	s.mu.Unlock()
	return nil
}

func (s *codexSession) Interrupt() error {
	s.mu.Lock()
	tid := s.turnID
	s.mu.Unlock()
	if tid == "" {
		return nil
	}
	_, err := s.c.call(s.ctx, "turn/interrupt", map[string]any{"threadId": s.threadID, "turnId": tid})
	return err
}

func (s *codexSession) Respond(id string, r Response) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("that request is no longer waiting")
	}
	switch p.method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		d := "accept"
		switch r.Decision {
		case "allow_session":
			d = "acceptForSession"
		case "deny":
			d = "decline"
		}
		return s.c.reply(p.rpcID, map[string]any{"decision": d})
	case "execCommandApproval", "applyPatchApproval":
		d := "approved"
		switch r.Decision {
		case "allow_session":
			d = "approved_for_session"
		case "deny":
			d = "denied"
		}
		return s.c.reply(p.rpcID, map[string]any{"decision": d})
	case "item/tool/requestUserInput":
		answers := map[string]any{}
		for _, q := range p.questions {
			a := r.Answers[q.ID]
			if a == nil {
				a = []string{}
			}
			answers[q.ID] = map[string]any{"answers": a}
		}
		return s.c.reply(p.rpcID, map[string]any{"answers": answers})
	case "item/permissions/requestApproval":
		if r.Decision == "deny" {
			return s.c.reply(p.rpcID, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
		}
		scope := "turn"
		if r.Decision == "allow_session" {
			scope = "session"
		}
		var params struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		_ = json.Unmarshal(p.params, &params)
		return s.c.reply(p.rpcID, map[string]any{"permissions": params.Permissions, "scope": scope})
	}
	return s.c.replyError(p.rpcID, "unsupported")
}

// request handles server→client requests: approvals and questions.
func (s *codexSession) request(id json.RawMessage, method string, params json.RawMessage) {
	key := "rq-" + strings.Trim(string(id), `"`)
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	sv := func(k string) string {
		v, _ := p[k].(string)
		return v
	}
	r := Request{ID: key, Type: "permission", Status: "pending", CanAllowSession: true, Input: params}
	pend := codexPending{rpcID: id, method: method, params: params}
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		r.ToolName = "Shell"
		cmd := sv("command")
		if acts, ok := p["commandActions"].([]any); ok && len(acts) > 0 {
			if a, ok := acts[0].(map[string]any); ok {
				if c, _ := a["command"].(string); c != "" {
					cmd = c
				}
			}
		}
		if cmd == "" {
			if arr, ok := p["command"].([]any); ok {
				parts := []string{}
				for _, x := range arr {
					parts = append(parts, fmt.Sprint(x))
				}
				cmd = strings.Join(parts, " ")
			}
		}
		r.Title = "Run " + oneLine(cmd, 160)
		if reason := sv("reason"); reason != "" {
			r.Title += " — " + oneLine(reason, 120)
		}
		r.Input = rawJSON(map[string]any{"command": cmd, "cwd": sv("cwd"), "reason": sv("reason")})
	case "item/fileChange/requestApproval", "applyPatchApproval":
		r.ToolName = "Edit files"
		r.Title = "Change files"
		if reason := sv("reason"); reason != "" {
			r.Title += " — " + oneLine(reason, 120)
		}
		if root := sv("grantRoot"); root != "" {
			r.Title = "Allow writing in " + root
		}
	case "item/tool/requestUserInput":
		r.Type = "question"
		r.Title = "Codex has a question"
		r.CanAllowSession = false
		var in struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				IsOther  bool   `json:"isOther"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(params, &in)
		for _, q := range in.Questions {
			qq := Question{ID: q.ID, Header: q.Header, Question: q.Question, Other: q.IsOther || len(q.Options) == 0}
			for _, o := range q.Options {
				qq.Options = append(qq.Options, Option{Label: o.Label, Description: o.Description})
			}
			r.Questions = append(r.Questions, qq)
		}
		pend.questions = r.Questions
	case "item/permissions/requestApproval":
		r.ToolName = "Permissions"
		r.Title = "Codex wants more permissions"
		if reason := sv("reason"); reason != "" {
			r.Title += " — " + oneLine(reason, 160)
		}
	case "mcpServer/elicitation/request":
		_ = s.c.reply(id, map[string]any{"action": "decline"})
		return
	default:
		_ = s.c.replyError(id, "Pocket AI does not handle "+method)
		return
	}
	s.mu.Lock()
	s.pending[key] = pend
	s.mu.Unlock()
	s.sink.Request(r)
}

func (s *codexSession) notify(method string, params json.RawMessage) {
	switch method {
	case "item/started", "item/completed":
		var p struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(params, &p) == nil {
			// the user's own message is already on the timeline
			if it, ok := CodexItem(p.Item); ok && it.Kind != "user" {
				s.sink.Item(it)
			}
		}
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(params, &p) == nil && p.Delta != "" {
			s.sink.Delta(p.ItemID, "text", p.Delta)
		}
	case "item/commandExecution/outputDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(params, &p) == nil && p.Delta != "" {
			s.sink.Delta(p.ItemID, "output", p.Delta)
		}
	case "turn/plan/updated":
		var p struct {
			TurnID string `json:"turnId"`
			Plan   []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if json.Unmarshal(params, &p) == nil && len(p.Plan) > 0 {
			todos := []Todo{}
			for _, st := range p.Plan {
				status := st.Status
				switch status {
				case "inProgress":
					status = "in_progress"
				case "completed", "pending":
				default:
					status = "pending"
				}
				todos = append(todos, Todo{Text: st.Step, Status: status})
			}
			s.sink.Item(Item{Key: "plan-" + p.TurnID, Kind: "todos", Todos: todos})
		}
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens       int64 `json:"inputTokens"`
					CachedInputTokens int64 `json:"cachedInputTokens"`
					OutputTokens      int64 `json:"outputTokens"`
				} `json:"total"`
				Last struct {
					InputTokens  int64 `json:"inputTokens"`
					OutputTokens int64 `json:"outputTokens"`
				} `json:"last"`
				ModelContextWindow int64 `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.mu.Lock()
			s.tokensNow.in = p.TokenUsage.Total.InputTokens
			s.tokensNow.out = p.TokenUsage.Total.OutputTokens
			s.tokensNow.cached = p.TokenUsage.Total.CachedInputTokens
			s.mu.Unlock()
			s.sink.Context(Context{Tokens: p.TokenUsage.Last.InputTokens + p.TokenUsage.Last.OutputTokens, Window: p.TokenUsage.ModelContextWindow})
		}
	case "account/rateLimits/updated":
		var p struct {
			RateLimits struct {
				LimitID   string       `json:"limitId"`
				Primary   *codexWindow `json:"primary"`
				Secondary *codexWindow `json:"secondary"`
			} `json:"rateLimits"`
		}
		if json.Unmarshal(params, &p) == nil {
			var out []Window
			for _, w := range []*codexWindow{p.RateLimits.Primary, p.RateLimits.Secondary} {
				if w != nil {
					out = append(out, Window{ID: codexWindowID(p.RateLimits.LimitID, w.WindowDurationMins), Name: windowName(w.WindowDurationMins), Used: float64(w.UsedPercent) / 100, ResetsAt: unixISO(w.ResetsAt)})
				}
			}
			if len(out) > 0 {
				s.sink.Limits("codex", out)
			}
		}
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) == nil && p.Turn.ID != "" {
			s.mu.Lock()
			s.turnID = p.Turn.ID
			s.mu.Unlock()
		}
	case "turn/completed":
		var p struct {
			Turn struct {
				ID         string `json:"id"`
				Status     string `json:"status"`
				DurationMS int64  `json:"durationMs"`
				Error      *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		if p.Turn.ID != "" && s.turnID != "" && p.Turn.ID != s.turnID {
			s.mu.Unlock()
			return
		}
		s.running = false
		dur := p.Turn.DurationMS
		if dur == 0 {
			dur = time.Since(s.turnFrom).Milliseconds()
		}
		t := TurnInfo{ID: p.Turn.ID, OK: p.Turn.Status == "completed", DurationMS: dur,
			InputTokens: s.tokensNow.in - s.tokensBase.in, OutputTokens: s.tokensNow.out - s.tokensBase.out,
			CachedTokens: s.tokensNow.cached - s.tokensBase.cached}
		s.turnID = ""
		for k := range s.pending {
			delete(s.pending, k)
		}
		s.mu.Unlock()
		if !t.OK {
			switch {
			case p.Turn.Error != nil && p.Turn.Error.Message != "":
				t.Error = p.Turn.Error.Message
			case p.Turn.Status == "interrupted":
				t.Interrupted = true
			default:
				t.Error = p.Turn.Status
			}
		}
		s.sink.TurnDone(t)
	case "error":
		var p struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		if json.Unmarshal(params, &p) == nil && p.Error.Message != "" {
			level := "error"
			if p.WillRetry {
				level = "warn"
			}
			s.sink.Item(Item{Key: fmt.Sprintf("err-%d", time.Now().UnixNano()), Kind: "notice", Level: level, Text: p.Error.Message})
		}
	case "thread/compacted":
		s.sink.Item(Item{Key: fmt.Sprintf("cmp-%d", time.Now().UnixNano()), Kind: "notice", Level: "info", Text: "Context compacted — older messages were summarised."})
	}
}

type codexWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	ResetsAt           int64 `json:"resetsAt"`
	WindowDurationMins int64 `json:"windowDurationMins"`
}

func windowName(mins int64) string {
	switch {
	case mins <= 0:
		return "limit"
	case mins%(60*24*7) == 0:
		return fmt.Sprintf("%d days", mins/(60*24))
	case mins%(60*24) == 0:
		return fmt.Sprintf("%d days", mins/(60*24))
	case mins%60 == 0:
		return fmt.Sprintf("%d hours", mins/60)
	}
	return fmt.Sprintf("%d minutes", mins)
}

// CodexItem maps a Codex ThreadItem to a timeline item. User messages map
// too (for imported history); the live runtime writes its own.
func CodexItem(raw json.RawMessage) (Item, bool) {
	var it struct {
		Type           string          `json:"type"`
		ID             string          `json:"id"`
		Text           string          `json:"text"`
		Summary        []string        `json:"summary"`
		Content        json.RawMessage `json:"content"`
		Command        string          `json:"command"`
		CommandActions []struct {
			Command string `json:"command"`
			Type    string `json:"type"`
			Path    string `json:"path"`
			Query   string `json:"query"`
		} `json:"commandActions"`
		AggregatedOutput *string `json:"aggregatedOutput"`
		ExitCode         *int    `json:"exitCode"`
		Status           string  `json:"status"`
		Changes          []struct {
			Path string          `json:"path"`
			Kind json.RawMessage `json:"kind"`
			Diff string          `json:"diff"`
		} `json:"changes"`
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Result    json.RawMessage `json:"result"`
		Error     json.RawMessage `json:"error"`
		Query     string          `json:"query"`
		Path      string          `json:"path"`
		Prompt    string          `json:"prompt"`
	}
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return Item{}, false
	}
	status := func() string {
		switch it.Status {
		case "inProgress", "":
			return "running"
		case "completed":
			if it.ExitCode != nil && *it.ExitCode != 0 {
				return "error"
			}
			return "done"
		case "declined":
			return "denied"
		default:
			return "error"
		}
	}
	switch it.Type {
	case "userMessage":
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(it.Content, &parts)
		var b []string
		for _, p := range parts {
			if p.Type == "text" {
				b = append(b, p.Text)
			}
		}
		return Item{Key: it.ID, Kind: "user", Text: strings.Join(b, "\n")}, true
	case "agentMessage":
		return Item{Key: it.ID, Kind: "assistant", Text: it.Text}, true
	case "reasoning":
		text := strings.Join(it.Summary, "\n\n")
		return Item{Key: it.ID, Kind: "thinking", Text: text}, true
	case "plan":
		return Item{Key: it.ID, Kind: "assistant", Text: it.Text}, true
	case "commandExecution":
		cmd := it.Command
		if len(it.CommandActions) == 1 && it.CommandActions[0].Command != "" {
			cmd = it.CommandActions[0].Command
		}
		t := &Tool{Name: "Shell", Kind: "bash", Title: oneLine(cmd, 160), Status: status(), ExitCode: it.ExitCode,
			Input: rawJSON(map[string]any{"command": cmd})}
		if len(it.CommandActions) == 1 {
			switch a := it.CommandActions[0]; a.Type {
			case "read":
				t.Kind, t.Title = "read", "Read "+firstNonEmpty(a.Path, cmd)
			case "search":
				t.Kind, t.Title = "search", "Search "+firstNonEmpty(a.Query, cmd)
			case "listFiles":
				t.Kind, t.Title = "search", "List "+firstNonEmpty(a.Path, cmd)
			}
		}
		if it.AggregatedOutput != nil {
			t.Output = clip(*it.AggregatedOutput, 60000)
		}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "fileChange":
		var b strings.Builder
		names := []string{}
		for _, c := range it.Changes {
			names = append(names, filepath.Base(c.Path))
			d := c.Diff
			if !strings.HasPrefix(d, "---") {
				d = "--- " + c.Path + "\n+++ " + c.Path + "\n" + d
			}
			b.WriteString(d)
			if !strings.HasSuffix(d, "\n") {
				b.WriteString("\n")
			}
		}
		t := &Tool{Name: "Edit", Kind: "edit", Title: "Edit " + strings.Join(names, ", "), Status: status(), Diff: clip(b.String(), 60000)}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "mcpToolCall":
		t := &Tool{Name: it.Server + "." + it.Tool, Kind: "mcp", Title: it.Server + " · " + it.Tool, Status: status(), Input: it.Arguments}
		if len(it.Result) > 0 && string(it.Result) != "null" {
			t.Output = clip(mcpText(it.Result), 60000)
		}
		if len(it.Error) > 0 && string(it.Error) != "null" {
			t.Output = clip(string(it.Error), 60000)
			t.Status = "error"
		}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "webSearch":
		t := &Tool{Name: "WebSearch", Kind: "search", Title: "Web search: " + oneLine(it.Query, 120), Status: "done"}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "imageView":
		t := &Tool{Name: "ViewImage", Kind: "read", Title: "View " + filepath.Base(it.Path), Status: "done"}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "collabAgentToolCall":
		t := &Tool{Name: "Agent", Kind: "task", Title: "Agent: " + oneLine(it.Prompt, 120), Status: status()}
		return Item{Key: it.ID, Kind: "tool", Tool: t}, true
	case "contextCompaction":
		return Item{Key: it.ID, Kind: "notice", Level: "info", Text: "Context compacted — older messages were summarised."}, true
	}
	return Item{}, false
}

func mcpText(raw json.RawMessage) string {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &r) == nil && len(r.Content) > 0 {
		var b []string
		for _, c := range r.Content {
			if c.Type == "text" {
				b = append(b, c.Text)
			}
		}
		return strings.Join(b, "\n")
	}
	return string(raw)
}

// Translate the private, per-session MCP proxy config without writing global
// Codex configuration or handing the Core credential to the agent process.
// The config is a file here, or the JSON itself (a conversation on another
// machine).
func codexPocketConfig(path string) (map[string]any, error) {
	raw := []byte(path)
	if !strings.HasPrefix(strings.TrimSpace(path), "{") {
		var err error
		if raw, err = ReadConfined(path); err != nil {
			return nil, err
		}
	}
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	pocket, ok := config.Servers["pocket"]
	if !ok || pocket.URL == "" {
		return nil, errors.New("missing Pocket tool connection")
	}
	return map[string]any{"mcp_servers.pocket": map[string]any{
		"url": pocket.URL, "http_headers": pocket.Headers, "enabled": true,
		"startup_timeout_sec": 15, "tool_timeout_sec": 660,
		"default_tools_approval_mode": "approve",
	}}, nil
}
