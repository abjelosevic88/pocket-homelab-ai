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
	"syscall"
	"time"
)

// ClaudeModes are Claude Code's permission modes as offered in the UI.
var ClaudeModes = []Mode{
	{ID: "default", Name: "Ask first", Description: "Asks before editing files or running commands it is not sure about."},
	{ID: "acceptEdits", Name: "Accept edits", Description: "Edits files without asking; still asks before other commands."},
	{ID: "plan", Name: "Plan", Description: "Reads and plans only; shows you the plan before changing anything."},
	{ID: "auto", Name: "Auto", Description: "Claude's classifier decides; asks only for risky actions."},
	{ID: "bypassPermissions", Name: "Full access", Description: "Never asks. Runs anything as the server user."},
}

// claudeSession is one `claude -p` process speaking stream-json.
type claudeSession struct {
	opts  Options
	sink  Sink
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex
	done  chan struct{}
	errb  *tail

	mu       sync.Mutex
	pending  map[string]claudePending // control request id → what was asked
	denied   map[string]bool          // tool_use ids the person denied
	tools    map[string]*Tool         // tool_use id → tool (to fill in results)
	seq      int
	reqSeq   int
	closed   bool
	running  bool
	turnFrom time.Time

	// streaming: message id → block index → item key
	curMsg    string
	streamKey map[int]string
	streamTyp map[int]string
	consumed  map[string]int // message id → blocks of the final message matched so far
	msgKeys   map[string][]string
	lastUsage struct{ in, out, cached int64 }
	cost      float64 // cumulative cost reported by this process
	subs      []Subagent
	tasks     []Task // background shell commands and monitors still running

	// Remote Control: control requests waiting for their answer, whether it
	// is on, and what we sent while it was (the CLI may echo those back).
	waiters map[string]chan controlReply
	remote  bool
	sent    []string
}

type controlReply struct {
	ok   bool
	err  string
	body json.RawMessage
}

type claudePending struct {
	toolName    string
	input       json.RawMessage
	suggestions json.RawMessage
	kind        string // permission | question | plan
	questions   []Question
	toolUseID   string
}

// StartClaude starts Claude Code for a thread.
func StartClaude(ctx context.Context, o Options, sink Sink) (Session, error) {
	bin := o.Bin
	if bin == "" {
		bin = "claude"
	}
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio"}
	if o.Ephemeral {
		args = append(args, "--no-session-persistence")
	}
	if o.Model != "" && o.Model != "default" {
		args = append(args, "--model", o.Model)
	}
	if o.Effort != "" {
		args = append(args, "--effort", o.Effort)
	}
	if o.Speed == "fast" {
		// fast mode is opt-in for SDK-style clients, through flag settings
		args = append(args, "--settings", `{"fastMode":true}`)
	}
	mode := o.Mode
	if mode == "" {
		mode = "default"
	}
	if o.Restricted {
		args = append(args, "--restricted", "--permission-mode", "manual", "--tools", "", "--strict-mcp-config", "--mcp-config", o.MCPConfig, "--setting-sources", "", "--disable-slash-commands", "--settings", `{"disableAllHooks":true}`, "--allowedTools", "mcp__pocket__*")
	} else if mode == "bypassPermissions" {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		// allow switching to full access later without a restart
		args = append(args, "--permission-mode", mode, "--allow-dangerously-skip-permissions")
	}
	if !o.Restricted && o.MCPConfig != "" {
		args = append(args, "--mcp-config", o.MCPConfig, "--allowedTools", "mcp__pocket__*")
	}
	if o.ResumeID != "" {
		args = append(args, "--resume", o.ResumeID)
		if o.Fork {
			args = append(args, "--fork-session")
		}
	}
	if strings.TrimSpace(o.SystemPrompt) != "" {
		flag := "--append-system-prompt"
		if o.Restricted {
			flag = "--system-prompt"
		}
		args = append(args, flag, o.SystemPrompt)
	}
	cmd, err := Command(o.Remote, o.Cwd, o.Env, bin, args...)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s := &claudeSession{opts: o, sink: sink, cmd: cmd, done: make(chan struct{}), errb: &tail{n: 8 << 10},
		pending: map[string]claudePending{}, denied: map[string]bool{}, tools: map[string]*Tool{}, waiters: map[string]chan controlReply{},
		streamKey: map[int]string{}, streamTyp: map[int]string{}, consumed: map[string]int{}, msgKeys: map[string][]string{}}
	cmd.Stderr = s.errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s.stdin = stdin
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go s.read(stdout)
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	_ = s.write(map[string]any{"type": "control_request", "request_id": "init", "request": map[string]any{"subtype": "initialize"}})
	return s, nil
}

func (s *claudeSession) Done() <-chan struct{} { return s.done }

func (s *claudeSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *claudeSession) Send(in Input) error {
	var content any = in.Text
	if len(in.Images) > 0 {
		blocks := []map[string]any{}
		for _, im := range in.Images {
			data, err := os.ReadFile(im.Path)
			if err != nil {
				return fmt.Errorf("read image: %w", err)
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": im.Mime, "data": base64.StdEncoding.EncodeToString(data)}})
		}
		text := in.Text
		if strings.TrimSpace(text) == "" {
			text = "(see the attached image)"
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
		content = blocks
	}
	s.mu.Lock()
	if s.remote {
		s.sent = append(s.sent, strings.TrimSpace(in.Text))
		if len(s.sent) > 20 {
			s.sent = s.sent[len(s.sent)-20:]
		}
	}
	if !s.running {
		s.running = true
		s.turnFrom = time.Now()
		s.mu.Unlock()
		s.sink.TurnStarted()
	} else {
		s.mu.Unlock()
	}
	return s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content},
		"parent_tool_use_id": nil, "session_id": ""})
}

func (s *claudeSession) Interrupt() error {
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("int-%d", s.reqSeq)
	s.mu.Unlock()
	return s.write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}})
}

func (s *claudeSession) SetMode(mode string) error {
	if s.opts.Restricted {
		return errors.New("Pocket Ask permissions cannot be changed")
	}
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("mode-%d", s.reqSeq)
	s.opts.Mode = mode
	s.mu.Unlock()
	return s.write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "set_permission_mode", "mode": mode}})
}

// RemoteControl turns Claude Code's Remote Control on or off for this
// process: on, the session shows up in claude.ai/code and the Claude app,
// and messages and approvals from there run here.
func (s *claudeSession) RemoteControl(ctx context.Context, on bool, name, reattach string) (RemoteInfo, error) {
	if s.opts.Restricted {
		return RemoteInfo{}, errors.New("Pocket Ask conversations cannot use Remote Control")
	}
	req := map[string]any{"subtype": "remote_control", "enabled": on}
	if on && name != "" {
		req["name"] = name
	}
	if on && reattach != "" {
		req["reattach_session_id"] = reattach
	}
	s.mu.Lock()
	s.reqSeq++
	id := fmt.Sprintf("rc-%d", s.reqSeq)
	ch := make(chan controlReply, 1)
	s.waiters[id] = ch
	if on {
		s.remote = true
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, id)
		s.mu.Unlock()
	}()
	if err := s.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return RemoteInfo{}, err
	}
	var rep controlReply
	select {
	case rep = <-ch:
	case <-s.done:
		return RemoteInfo{}, ErrClosed
	case <-ctx.Done():
		return RemoteInfo{}, errors.New("Claude Code did not answer the Remote Control request in time")
	}
	if !rep.ok {
		if on {
			s.mu.Lock()
			s.remote = false
			s.mu.Unlock()
		}
		return RemoteInfo{}, errors.New(firstNonEmpty(rep.err, "Claude Code refused Remote Control"))
	}
	if !on {
		s.mu.Lock()
		s.remote, s.sent = false, nil
		s.mu.Unlock()
		return RemoteInfo{}, nil
	}
	var r struct {
		SessionURL      string `json:"session_url"`
		BridgeSessionID string `json:"bridge_session_id"`
	}
	_ = json.Unmarshal(rep.body, &r)
	return RemoteInfo{SessionURL: r.SessionURL, BridgeSessionID: r.BridgeSessionID}, nil
}

func (s *claudeSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		go func() {
			select {
			case <-s.done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-s.done:
				case <-time.After(5 * time.Second):
					_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		}()
	}
	return nil
}

func (s *claudeSession) Respond(id string, r Response) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("that request is no longer waiting")
	}
	var resp map[string]any
	switch {
	case r.Decision == "deny":
		msg := strings.TrimSpace(r.Message)
		if msg == "" {
			msg = "The user denied this. Do not retry it; ask what to do instead if you are stuck."
			if p.kind == "plan" {
				msg = "The user wants to keep planning."
			}
		} else if p.kind == "plan" {
			msg = "The user wants changes to the plan: " + msg
		} else {
			msg = "The user denied this and said: " + msg
		}
		resp = map[string]any{"behavior": "deny", "message": msg}
		if p.toolUseID != "" {
			s.mu.Lock()
			s.denied[p.toolUseID] = true
			s.mu.Unlock()
		}
	case p.kind == "question":
		var input map[string]any
		_ = json.Unmarshal(p.input, &input)
		if input == nil {
			input = map[string]any{}
		}
		answers := map[string]string{}
		for _, q := range p.questions {
			if a := r.Answers[q.ID]; len(a) > 0 {
				answers[q.Question] = strings.Join(a, ", ")
			}
		}
		input["answers"] = answers
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
	default:
		var input any = map[string]any{}
		if len(p.input) > 0 {
			input = json.RawMessage(p.input)
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
		if r.Decision == "allow_session" && len(p.suggestions) > 0 && string(p.suggestions) != "null" {
			resp["updatedPermissions"] = json.RawMessage(p.suggestions)
		}
	}
	return s.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": resp}})
}

// ---------- reading the stream ----------

func (s *claudeSession) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		s.handle(m)
	}
	err := s.cmd.Wait()
	s.mu.Lock()
	running := s.running
	s.running = false
	s.closed = true
	for id := range s.pending {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	close(s.done)
	if running {
		s.sink.TurnDone(TurnInfo{OK: false, Error: s.exitError(err)})
	}
	if err != nil && !isKilled(err) {
		s.sink.Exited(errors.New(s.exitError(err)))
	} else {
		s.sink.Exited(nil)
	}
}

func isKilled(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return true
		}
	}
	return false
}

func (s *claudeSession) exitError(err error) string {
	msg := s.errb.String()
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "Claude Code exited"
	}
	return oneLine(lastLines(msg, 3), 400)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
}

func str(m map[string]json.RawMessage, k string) string {
	var v string
	_ = json.Unmarshal(m[k], &v)
	return v
}

func (s *claudeSession) handle(m map[string]json.RawMessage) {
	switch str(m, "type") {
	case "system":
		switch str(m, "subtype") {
		case "init":
			if id := str(m, "session_id"); id != "" {
				s.sink.SessionID(id)
			}
		case "compact_boundary":
			s.notice("info", "Context compacted — older messages were summarised.")
		case "task_started", "task_progress", "task_updated", "task_notification":
			s.handleTask(m)
		case "background_tasks_changed":
			s.handleBackground(m)
		case "bridge_state":
			if b, ok := s.sink.(BridgeSink); ok {
				b.Bridge(str(m, "state"))
			}
		}
	case "stream_event":
		if p := m["parent_tool_use_id"]; len(p) > 0 && string(p) != "null" {
			return // a sub-agent's stream; its final messages are folded into the Task tool
		}
		s.selfStarted()
		s.handleStream(m["event"])
	case "assistant":
		if p := m["parent_tool_use_id"]; len(p) > 0 && string(p) != "null" {
			return
		}
		s.selfStarted()
		s.handleAssistant(m["message"])
	case "user":
		if p := m["parent_tool_use_id"]; len(p) > 0 && string(p) != "null" {
			return
		}
		s.handleRemoteUser(m)
		s.handleUser(m["message"], m["tool_use_result"])
	case "result":
		s.handleResult(m)
	case "rate_limit_event":
		s.handleRateLimit(m["rate_limit_info"])
	case "control_request":
		s.handleControl(str(m, "request_id"), m["request"])
	case "control_response":
		s.handleControlResponse(m["response"])
	case "control_cancel_request":
		id := str(m, "request_id")
		s.mu.Lock()
		_, ok := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if c, can := s.sink.(CancelSink); ok && can {
			c.RequestCancelled(id)
		}
	}
}

// handleControlResponse hands answers to the requests someone waits on
// (Remote Control); the ones to initialize / interrupt are not needed.
func (s *claudeSession) handleControlResponse(raw json.RawMessage) {
	var r struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	s.mu.Lock()
	ch := s.waiters[r.RequestID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- controlReply{ok: r.Subtype == "success", err: r.Error, body: r.Response}:
		default:
		}
	}
}

// handleRemoteUser puts a message typed in claude.ai or the Claude app on the
// timeline: with Remote Control on, it reaches the process without us.
func (s *claudeSession) handleRemoteUser(m map[string]json.RawMessage) {
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if !remote {
		return
	}
	for _, k := range []string{"isReplay", "isSynthetic", "isMeta"} {
		if string(m[k]) == "true" {
			return
		}
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m["message"], &msg) != nil {
		return
	}
	var text string
	if json.Unmarshal(msg.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(msg.Content, &blocks) != nil {
			return
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
			default:
				return // tool results and the like
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	// tags are the CLI talking to the model (reminders, task notifications)
	if text == "" || strings.HasPrefix(text, "<") {
		return
	}
	s.mu.Lock()
	for i, t := range s.sent {
		if t == text {
			s.sent = append(s.sent[:i], s.sent[i+1:]...)
			s.mu.Unlock()
			return // one of ours, echoed
		}
	}
	key := "rc-" + str(m, "uuid")
	if key == "rc-" {
		key = s.nextKey("rc")
	}
	s.mu.Unlock()
	s.selfStarted()
	s.sink.Item(Item{Key: key, Kind: "user", Text: text, By: "Claude app"})
}

// selfStarted opens a turn the agent began without a message from us: when
// background sub-agents finish, Claude Code carries on by itself. Without it
// that turn's result is dropped (no footer, no "working" state).
func (s *claudeSession) selfStarted() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.turnFrom = time.Now()
	s.mu.Unlock()
	s.sink.TurnStarted()
}

func (s *claudeSession) nextKey(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixMilli(), s.seq)
}

func (s *claudeSession) notice(level, text string) {
	s.mu.Lock()
	k := s.nextKey("n")
	s.mu.Unlock()
	s.sink.Item(Item{Key: k, Kind: "notice", Level: level, Text: text})
}

func (s *claudeSession) handleStream(raw json.RawMessage) {
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Type {
	case "message_start":
		s.curMsg = ev.Message.ID
		s.streamKey = map[int]string{}
		s.streamTyp = map[int]string{}
	case "content_block_start":
		kind := ""
		switch ev.ContentBlock.Type {
		case "text":
			kind = "assistant"
		case "thinking":
			kind = "thinking"
		default:
			return
		}
		k := fmt.Sprintf("c-%s-%d", s.curMsg, ev.Index)
		s.streamKey[ev.Index] = k
		s.streamTyp[ev.Index] = kind
		s.msgKeys[s.curMsg] = append(s.msgKeys[s.curMsg], kind+"|"+k)
		s.mu.Unlock()
		s.sink.Item(Item{Key: k, Kind: kind})
		s.mu.Lock()
	case "content_block_delta":
		k := s.streamKey[ev.Index]
		if k == "" {
			return
		}
		text := ev.Delta.Text
		if ev.Delta.Type == "thinking_delta" {
			text = ev.Delta.Thinking
		}
		if text == "" {
			return
		}
		s.mu.Unlock()
		s.sink.Delta(k, "text", text)
		s.mu.Lock()
	}
}

// keyFor matches a block of a final assistant message to the key its stream used.
func (s *claudeSession) keyFor(msgID, kind string) string {
	keys := s.msgKeys[msgID]
	n := s.consumed[msgID]
	for i := n; i < len(keys); i++ {
		parts := strings.SplitN(keys[i], "|", 2)
		if parts[0] == kind {
			s.consumed[msgID] = i + 1
			return parts[1]
		}
	}
	s.seq++
	return fmt.Sprintf("c-%s-x%d", msgID, s.seq)
}

func (s *claudeSession) handleAssistant(raw json.RawMessage) {
	var msg struct {
		ID      string `json:"id"`
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens          int64 `json:"input_tokens"`
			OutputTokens         int64 `json:"output_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
			CacheCreation        int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	if ctxTokens := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreation; ctxTokens > 0 {
		s.sink.Context(Context{Tokens: ctxTokens + msg.Usage.OutputTokens})
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			s.mu.Lock()
			k := s.keyFor(msg.ID, "assistant")
			s.mu.Unlock()
			s.sink.Item(Item{Key: k, Kind: "assistant", Text: b.Text})
		case "thinking":
			s.mu.Lock()
			k := s.keyFor(msg.ID, "thinking")
			s.mu.Unlock()
			if strings.TrimSpace(b.Thinking) != "" {
				s.sink.Item(Item{Key: k, Kind: "thinking", Text: b.Thinking})
			}
		case "tool_use":
			if b.Name == "TodoWrite" {
				s.sink.Item(Item{Key: b.ID, Kind: "todos", Todos: claudeTodos(b.Input)})
				continue
			}
			t := claudeTool(b.Name, b.Input)
			s.mu.Lock()
			s.tools[b.ID] = t
			s.mu.Unlock()
			s.sink.Item(Item{Key: b.ID, Kind: "tool", Tool: t})
		}
	}
}

func claudeTodos(input json.RawMessage) []Todo {
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			ActiveForm string `json:"activeForm"`
			Status     string `json:"status"`
		} `json:"todos"`
	}
	_ = json.Unmarshal(input, &in)
	out := []Todo{}
	for _, t := range in.Todos {
		out = append(out, Todo{Text: t.Content, Status: t.Status})
	}
	return out
}

// claudeTool turns a tool_use block into a timeline tool row.
func claudeTool(name string, input json.RawMessage) *Tool {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	sv := func(k string) string {
		v, _ := in[k].(string)
		return v
	}
	t := &Tool{Name: name, Input: input, Status: "running", Kind: "other", Title: name}
	switch name {
	case "Bash":
		t.Kind = "bash"
		t.Title = oneLine(sv("command"), 160)
		if d := sv("description"); d != "" && len(sv("command")) > 80 {
			t.Title = d
		}
	case "BashOutput", "KillShell", "KillBash":
		t.Kind = "bash"
		t.Title = name
	case "Edit":
		t.Kind = "edit"
		t.Title = "Edit " + filepath.Base(sv("file_path"))
		t.Diff = simpleDiff(sv("file_path"), sv("old_string"), sv("new_string"))
	case "MultiEdit":
		t.Kind = "edit"
		t.Title = "Edit " + filepath.Base(sv("file_path"))
		if edits, ok := in["edits"].([]any); ok {
			var b strings.Builder
			for _, e := range edits {
				if em, ok := e.(map[string]any); ok {
					o, _ := em["old_string"].(string)
					n, _ := em["new_string"].(string)
					b.WriteString(simpleDiff("", o, n))
				}
			}
			t.Diff = "--- " + sv("file_path") + "\n+++ " + sv("file_path") + "\n" + b.String()
		}
	case "Write":
		t.Kind = "write"
		t.Title = "Write " + filepath.Base(sv("file_path"))
		t.Diff = simpleDiff(sv("file_path"), "", clip(sv("content"), 20000))
	case "NotebookEdit":
		t.Kind = "edit"
		t.Title = "Edit " + filepath.Base(sv("notebook_path"))
	case "Read":
		t.Kind = "read"
		t.Title = "Read " + sv("file_path")
	case "Grep":
		t.Kind = "search"
		t.Title = "Search " + oneLine(sv("pattern"), 100)
		if p := sv("path"); p != "" {
			t.Title += " in " + p
		}
	case "Glob":
		t.Kind = "search"
		t.Title = "Find " + sv("pattern")
	case "WebSearch":
		t.Kind = "search"
		t.Title = "Web search: " + oneLine(sv("query"), 120)
	case "WebFetch":
		t.Kind = "web"
		t.Title = "Fetch " + sv("url")
	case "Skill":
		t.Kind = "skill"
		t.Title = "Skill: " + sv("skill")
	case "Task", "Agent":
		t.Kind = "task"
		t.Title = "Agent: " + oneLine(firstNonEmpty(sv("description"), sv("prompt")), 120)
	default:
		if strings.HasPrefix(name, "mcp__") {
			t.Kind = "mcp"
			parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
			if len(parts) == 2 {
				t.Title = parts[0] + " · " + parts[1]
			}
		}
	}
	if t.Title == "" {
		t.Title = name
	}
	return t
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (s *claudeSession) handleUser(raw, result json.RawMessage) {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		s.mu.Lock()
		t := s.tools[b.ToolUseID]
		denied := s.denied[b.ToolUseID]
		delete(s.tools, b.ToolUseID)
		s.mu.Unlock()
		if t == nil {
			continue
		}
		t.Output = clip(toolResultText(b.Content), 60000)
		t.Images = countImages(b.Content)
		switch {
		case denied:
			t.Status = "denied"
		case b.IsError:
			t.Status = "error"
		default:
			t.Status = "done"
		}
		if t.Kind == "edit" || t.Kind == "write" {
			if d := structuredDiff(result); d != "" {
				t.Diff = d
			}
		}
		s.sink.Item(Item{Key: b.ToolUseID, Kind: "tool", Tool: t})
	}
}

// structuredDiff renders Claude Code's structuredPatch for Edit/Write.
func structuredDiff(raw json.RawMessage) string {
	var r struct {
		FilePath        string `json:"filePath"`
		StructuredPatch []struct {
			OldStart int      `json:"oldStart"`
			OldLines int      `json:"oldLines"`
			NewStart int      `json:"newStart"`
			NewLines int      `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil || len(r.StructuredPatch) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- " + r.FilePath + "\n+++ " + r.FilePath + "\n")
	for _, h := range r.StructuredPatch {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			b.WriteString(l + "\n")
		}
	}
	return clip(b.String(), 60000)
}

// countImages counts the image blocks in message content.
func countImages(raw json.RawMessage) int {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return 0
	}
	n := 0
	for _, p := range parts {
		if p.Type == "image" {
			n++
		}
	}
	return n
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b []string
		for _, p := range parts {
			switch p.Type {
			case "text":
				b = append(b, p.Text)
			case "image":
				b = append(b, "[image]")
			}
		}
		return strings.Join(b, "\n")
	}
	return string(raw)
}

func (s *claudeSession) handleResult(m map[string]json.RawMessage) {
	var r struct {
		Subtype      string   `json:"subtype"`
		IsError      bool     `json:"is_error"`
		DurationMS   int64    `json:"duration_ms"`
		TotalCostUSD float64  `json:"total_cost_usd"`
		Result       string   `json:"result"`
		StopReason   string   `json:"stop_reason"`
		Errors       []string `json:"errors"`
		Usage        struct {
			InputTokens          int64 `json:"input_tokens"`
			OutputTokens         int64 `json:"output_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
			CacheCreation        int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			ContextWindow int64 `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	raw, _ := json.Marshal(m)
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	s.mu.Lock()
	// total_cost_usd adds up over the life of the process; report this turn's share.
	cost := r.TotalCostUSD - s.cost
	if cost < 0 {
		cost = r.TotalCostUSD
	}
	s.cost = r.TotalCostUSD
	wasRunning := s.running
	s.running = false
	dur := r.DurationMS
	if dur == 0 && !s.turnFrom.IsZero() {
		dur = time.Since(s.turnFrom).Milliseconds()
	}
	s.mu.Unlock()
	var window int64
	for _, mu := range r.ModelUsage {
		if mu.ContextWindow > window {
			window = mu.ContextWindow
		}
	}
	if window > 0 {
		s.sink.Context(Context{Window: window})
	}
	t := TurnInfo{OK: r.Subtype == "success" && !r.IsError, DurationMS: dur, CostUSD: cost,
		InputTokens:  r.Usage.InputTokens + r.Usage.CacheCreation + r.Usage.CacheReadInputTokens,
		OutputTokens: r.Usage.OutputTokens, CachedTokens: r.Usage.CacheReadInputTokens}
	if !t.OK {
		switch {
		case r.Subtype == "error_during_execution":
			t.Interrupted = true
		case r.Subtype == "error_max_turns":
			t.Error = "Stopped: too many turns."
		case len(r.Errors) > 0:
			t.Error = strings.Join(r.Errors, "; ")
		case r.IsError && r.Result != "":
			t.Error = oneLine(r.Result, 400)
		default:
			t.Error = r.Subtype
		}
	}
	if wasRunning {
		s.sink.TurnDone(t)
	}
}

func (s *claudeSession) handleRateLimit(raw json.RawMessage) {
	var r struct {
		Windows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
		Type        string  `json:"rateLimitType"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	names := map[string]string{"five_hour": "5 hours", "seven_day": "7 days", "seven_day_opus": "7 days · Opus", "seven_day_sonnet": "7 days · Sonnet"}
	var out []Window
	for _, k := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet"} {
		if w, ok := r.Windows[k]; ok {
			out = append(out, Window{ID: k, Name: names[k], Used: w.Utilization, ResetsAt: unixISO(w.ResetsAt)})
		}
	}
	if len(out) == 0 && r.Type != "" {
		n := names[r.Type]
		if n == "" {
			n = r.Type
		}
		out = append(out, Window{ID: r.Type, Name: n, Used: r.Utilization, ResetsAt: unixISO(r.ResetsAt)})
	}
	if len(out) > 0 {
		s.sink.Limits("claude", out)
	}
}

func (s *claudeSession) handleControl(id string, raw json.RawMessage) {
	var req struct {
		Subtype     string          `json:"subtype"`
		ToolName    string          `json:"tool_name"`
		DisplayName string          `json:"display_name"`
		Input       json.RawMessage `json:"input"`
		Description string          `json:"description"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
		ToolUseID   string          `json:"tool_use_id"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	if req.Subtype != "can_use_tool" {
		// hooks / MCP messages we do not serve: answer so the CLI does not hang.
		_ = s.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": id, "error": "not supported by Pocket AI"}})
		return
	}
	if s.opts.Restricted {
		behavior := "deny"
		if strings.HasPrefix(req.ToolName, "mcp__pocket__") {
			behavior = "allow"
		}
		_ = s.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{"behavior": behavior, "updatedInput": req.Input, "message": "Pocket Ask only permits Pocket tools"}}})
		return
	}
	p := claudePending{toolName: req.ToolName, input: req.Input, suggestions: req.Suggestions, kind: "permission", toolUseID: req.ToolUseID}
	r := Request{ID: id, Type: "permission", ToolName: req.ToolName, Input: req.Input, Status: "pending",
		CanAllowSession: len(req.Suggestions) > 0 && string(req.Suggestions) != "null" && string(req.Suggestions) != "[]"}
	switch req.ToolName {
	case "AskUserQuestion":
		p.kind, r.Type = "question", "question"
		var in struct {
			Questions []struct {
				Question    string `json:"question"`
				Header      string `json:"header"`
				MultiSelect bool   `json:"multiSelect"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(req.Input, &in)
		for i, q := range in.Questions {
			qq := Question{ID: fmt.Sprintf("q%d", i), Header: q.Header, Question: q.Question, Multi: q.MultiSelect, Other: true}
			for _, o := range q.Options {
				qq.Options = append(qq.Options, Option{Label: o.Label, Description: o.Description})
			}
			r.Questions = append(r.Questions, qq)
		}
		p.questions = r.Questions
		r.Title = "Claude has a question"
		r.CanAllowSession = false
	case "ExitPlanMode":
		p.kind, r.Type = "plan", "plan"
		var in struct {
			Plan string `json:"plan"`
		}
		_ = json.Unmarshal(req.Input, &in)
		r.Plan = in.Plan
		r.Title = "Claude has a plan"
		r.CanAllowSession = false
	default:
		t := claudeTool(req.ToolName, req.Input)
		r.Title = t.Title
	}
	s.mu.Lock()
	s.pending[id] = p
	s.mu.Unlock()
	s.sink.Request(r)
}

// handleTask keeps the list of background sub-agents (and the count of
// background shell commands) up to date.
func (s *claudeSession) handleTask(m map[string]json.RawMessage) {
	var ev struct {
		Subtype     string `json:"subtype"`
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
		Type        string `json:"subagent_type"`
		TaskType    string `json:"task_type"` // local_agent, remote_agent, local_workflow, local_bash
		Status      string `json:"status"`
		Patch       struct {
			Status string `json:"status"`
		} `json:"patch"`
		Usage struct {
			ToolUses int `json:"tool_uses"`
		} `json:"usage"`
	}
	raw, _ := json.Marshal(m)
	if json.Unmarshal(raw, &ev) != nil || ev.TaskID == "" {
		return
	}
	done := ev.Subtype == "task_notification"
	switch ev.Patch.Status {
	case "completed", "failed", "killed", "cancelled", "stopped":
		done = true
	}
	kind := taskKind(ev.TaskType)
	if s.trackTask(ev.TaskID, kind, ev.Description, ev.Subtype == "task_started" && kind != "", done) || kind != "" {
		return // shell commands and monitors report as tasks too; they are not sub-agents
	}
	s.mu.Lock()
	idx := -1
	for i, sa := range s.subs {
		if sa.ID == ev.TaskID {
			idx = i
		}
	}
	switch {
	case done:
		if idx < 0 {
			s.mu.Unlock()
			return
		}
		s.subs = append(s.subs[:idx], s.subs[idx+1:]...)
	case ev.Subtype == "task_started":
		if idx < 0 {
			s.subs = append(s.subs, Subagent{ID: ev.TaskID, Description: ev.Description, Type: ev.Type, StartedAt: time.Now().UTC().Format(time.RFC3339)})
		}
	case ev.Subtype == "task_progress" && idx >= 0:
		s.subs[idx].Activity = ev.Description
		s.subs[idx].ToolUses = ev.Usage.ToolUses
	default:
		s.mu.Unlock()
		return
	}
	list := append([]Subagent(nil), s.subs...)
	s.mu.Unlock()
	s.sink.Subagents(list)
}

// taskKind tells background work that is not a sub-agent ("" = a sub-agent).
func taskKind(taskType string) string {
	switch {
	case taskType == "local_bash":
		return "shell"
	case strings.HasPrefix(taskType, "monitor"):
		return "monitor"
	}
	return ""
}

// trackTask follows shell commands and monitors Claude Code runs as tasks
// (the ones put in the background outlive the turn). It reports whether the
// event was about one. Notifications carry no task type, so they match by id.
func (s *claudeSession) trackTask(id, kind, desc string, started, done bool) bool {
	s.mu.Lock()
	idx := -1
	for i, t := range s.tasks {
		if t.ID == id {
			idx = i
		}
	}
	switch {
	case started && !done && idx < 0:
		s.tasks = append(s.tasks, Task{ID: id, Kind: kind, Description: desc, StartedAt: time.Now().UTC().Format(time.RFC3339)})
	case done && idx >= 0:
		s.tasks = append(s.tasks[:idx], s.tasks[idx+1:]...)
	case idx >= 0 || started:
		s.mu.Unlock()
		return true // nothing changed
	default:
		s.mu.Unlock()
		return false
	}
	list := append([]Task(nil), s.tasks...)
	s.mu.Unlock()
	s.sink.Tasks(list)
	return true
}

// handleBackground takes the CLI's own list of background work as the truth
// for shells and monitors: one it no longer lists has ended, even if its
// notification never came.
func (s *claudeSession) handleBackground(m map[string]json.RawMessage) {
	var ev struct {
		Tasks []struct {
			TaskID      string `json:"task_id"`
			TaskType    string `json:"task_type"`
			Description string `json:"description"`
		} `json:"tasks"`
	}
	raw, _ := json.Marshal(m)
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	s.mu.Lock()
	next := []Task{}
	for _, bt := range ev.Tasks {
		kind := taskKind(bt.TaskType)
		if kind == "" || bt.TaskID == "" {
			continue
		}
		t := Task{ID: bt.TaskID, Kind: kind, Description: bt.Description, StartedAt: time.Now().UTC().Format(time.RFC3339)}
		for _, old := range s.tasks {
			if old.ID == t.ID {
				t.StartedAt = old.StartedAt
				if t.Description == "" {
					t.Description = old.Description
				}
			}
		}
		next = append(next, t)
	}
	same := len(next) == len(s.tasks)
	for i := 0; same && i < len(next); i++ {
		same = next[i] == s.tasks[i]
	}
	s.tasks = next
	s.mu.Unlock()
	if !same {
		s.sink.Tasks(append([]Task(nil), next...))
	}
}
