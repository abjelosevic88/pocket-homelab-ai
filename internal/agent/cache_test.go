package agent

import "testing"

func TestClaudeReportsPromptCache(t *testing.T) {
	r := newRec()
	s := &claudeSession{sink: r}
	for _, c := range []struct {
		usage string
		ttl   int64
	}{
		{`{"input_tokens":3,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":900}}`, 3600},
		{`{"input_tokens":3,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_5m_input_tokens":900,"ephemeral_1h_input_tokens":0}}`, 300},
		{`{"input_tokens":3,"cache_read_input_tokens":900}`, 0},
	} {
		r.ctx = nil
		s.handleAssistant([]byte(`{"id":"m","content":[],"usage":` + c.usage + `}`))
		if len(r.ctx) != 1 || !r.ctx[0].Cached || r.ctx[0].CacheTTL != c.ttl {
			t.Fatalf("%s: got %+v, want a cached call with ttl %d", c.usage, r.ctx, c.ttl)
		}
	}
}
