package agent

import (
	"context"
	"errors"
	"strings"
)

type completion struct {
	text string
	turn TurnInfo
	err  error
}

// CompleteClaude runs one text-only turn. The caller supplies a private empty
// workspace; no tools, hooks, MCP servers, session resume or file attachments.
func CompleteClaude(ctx context.Context, o Options, prompt string) (string, TurnInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	o.Ephemeral = true
	o.Restricted, o.MCPConfig, o.ResumeID = true, `{"mcpServers":{}}`, ""
	k := &completionSink{done: make(chan completion, 1), texts: map[string]string{}}
	s, err := StartClaude(ctx, o, k)
	if err != nil {
		return "", TurnInfo{}, err
	}
	defer s.Close()
	if err := s.Send(Input{Text: prompt}); err != nil {
		return "", TurnInfo{}, err
	}
	select {
	case <-ctx.Done():
		return "", TurnInfo{}, ctx.Err()
	case out := <-k.done:
		return out.text, out.turn, out.err
	}
}

type completionSink struct {
	done  chan completion
	texts map[string]string
	order []string
}

func (s *completionSink) finish(v completion) {
	select {
	case s.done <- v:
	default:
	}
}
func (s *completionSink) Item(it Item) {
	if it.Kind != "assistant" {
		return
	}
	if _, ok := s.texts[it.Key]; !ok {
		s.order = append(s.order, it.Key)
	}
	s.texts[it.Key] = it.Text
}
func (*completionSink) Delta(string, string, string) {}
func (*completionSink) SessionID(string)             {}
func (*completionSink) Context(Context)              {}
func (*completionSink) Limits(string, []Window)      {}
func (*completionSink) TurnStarted()                 {}
func (*completionSink) Subagents([]Subagent)         {}
func (*completionSink) Tasks([]Task)                 {}
func (s *completionSink) Request(Request) {
	s.finish(completion{err: errors.New("text generation requested an unavailable tool")})
}
func (s *completionSink) Exited(err error) {
	if err == nil {
		err = errors.New("agent exited before completing the answer")
	}
	s.finish(completion{err: err})
}
func (s *completionSink) TurnDone(t TurnInfo) {
	if !t.OK {
		s.finish(completion{turn: t, err: errors.New("Pocket AI generation failed: " + t.Error)})
		return
	}
	var parts []string
	for _, key := range s.order {
		parts = append(parts, s.texts[key])
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		s.finish(completion{turn: t, err: errors.New("Pocket AI returned no answer")})
		return
	}
	s.finish(completion{text: text, turn: t})
}
