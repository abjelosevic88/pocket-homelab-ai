package agent

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeSessionCostReadsLastSavedTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if _, ok := ClaudeSessionCost(path); ok {
		t.Fatal("a missing file has no cost")
	}
	big := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 3<<20) + `"}}`
	lines := []string{
		`{"type":"user","message":{"content":"{\"type\":\"cost-state\",\"totalCostUSD\":99}"}}`,
		`{"type":"cost-state","sessionId":"s","totalCostUSD":1.25}`,
		big, // the saved total is further back than the first chunk read
		`{"type":"assistant","message":{"content":"quoting {\"type\":\"cost-state\",\"totalCostUSD\":7}"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := ClaudeSessionCost(path); !ok || v != 1.25 {
		t.Fatalf("got %v %v, want 1.25 (a quoted one is not a cost-state line)", v, ok)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"cost-state","sessionId":"s","totalCostUSD":3.5}` + "\n")
	f.Close()
	if v, _ := ClaudeSessionCost(path); v != 3.5 {
		t.Fatalf("got %v, want the last one, 3.5", v)
	}
	if v, ok := ParseCostState(`{"type":"cost-state","totalCostUSD":0.4}` + "\n"); !ok || v != 0.4 {
		t.Fatalf("remote line: got %v %v", v, ok)
	}
}

// A resumed session's first result carries everything it cost before: only
// the new part is this turn's.
func TestClaudeTurnCostStartsFromRestoredTotal(t *testing.T) {
	r := newRec()
	s := &claudeSession{sink: r, cost: 10}
	for _, total := range []float64{10.4, 10.5, 0.2} {
		b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "total_cost_usd": total})
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		s.running = true
		s.handleResult(m)
	}
	want := []float64{0.4, 0.1, 0} // the last: lower than counted (a replay, or a process that started over)
	for i, ti := range r.turns {
		if math.Abs(ti.CostUSD-want[i]) > 1e-9 {
			t.Fatalf("turn %d: cost %v, want %v", i, ti.CostUSD, want[i])
		}
	}
	if r.turns[1].SessionCost != 10.5 {
		t.Fatalf("session total %v, want 10.5", r.turns[1].SessionCost)
	}
}

func TestSpendingReadsCostStatesAndCodexResponses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-p")
	_ = os.MkdirAll(dir, 0o700)
	write := func(name string, lines ...string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// two processes of one session: 1.00, then 0.50 more on the next day
	write("a.jsonl",
		`{"type":"user","timestamp":"2026-10-30T10:00:00Z","message":{"content":"hi"}}`,
		`{"type":"cost-state","totalCostUSD":1}`,
		`{"type":"user","timestamp":"2026-10-31T10:00:00Z","message":{"content":"again"}}`,
		`{"type":"cost-state","totalCostUSD":1.5}`)
	// a fork: its first total includes the 2.00 of the session it came from
	write("b.jsonl",
		`{"type":"queue-operation","timestamp":"2026-10-31T12:00:00Z"}`,
		`{"type":"user","timestamp":"2026-10-30T09:00:00Z","message":{"content":"copied"}}`,
		`{"type":"user","timestamp":"2026-10-31T12:00:01Z","message":{"content":"new"}}`,
		`{"type":"cost-state","totalCostUSD":2.25}`,
		`{"type":"user","timestamp":"2026-10-31T13:00:00Z","message":{"content":"more"}}`,
		`{"type":"cost-state","totalCostUSD":2.5}`)
	codex := t.TempDir()
	_ = os.MkdirAll(filepath.Join(codex, "sessions"), 0o700)
	usage := `{"total_token_usage":{"input_tokens":%d,"output_tokens":0},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":0,"output_tokens":0}}`
	_ = os.WriteFile(filepath.Join(codex, "sessions", "rollout-c.jsonl"), []byte(strings.Join([]string{
		`{"timestamp":"2026-10-31T08:00:00Z","type":"session_meta","payload":{"id":"c","originator":"codex_exec","cwd":"/x"}}`,
		`{"timestamp":"2026-10-31T08:00:01Z","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`,
		`{"timestamp":"2026-10-31T08:00:02Z","type":"event_msg","payload":{"type":"token_count","info":` + strings.Replace(usage, "%d", "100000", 1) + `}}`,
		`{"timestamp":"2026-10-31T08:00:03Z","type":"event_msg","payload":{"type":"token_count","info":` + strings.Replace(usage, "%d", "100000", 1) + `}}`,
	}, "\n")+"\n"), 0o600)
	// an archived one (where codex exec runs often end up) counts too
	_ = os.MkdirAll(filepath.Join(codex, "archived_sessions"), 0o700)
	_ = os.WriteFile(filepath.Join(codex, "archived_sessions", "rollout-d.jsonl"), []byte(strings.Join([]string{
		`{"timestamp":"2026-10-31T09:00:00Z","type":"session_meta","payload":{"id":"d","originator":"codex_exec","cwd":"/x"}}`,
		`{"timestamp":"2026-10-31T09:00:01Z","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`,
		`{"timestamp":"2026-10-31T09:00:02Z","type":"event_msg","payload":{"type":"token_count","info":` + strings.Replace(usage, "%d", "100000", 1) + `}}`,
	}, "\n")+"\n"), 0o600)
	h := Homes{Claude: []AccountHome{{Dir: home}}, Codex: []AccountHome{{Dir: codex}}}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got := map[string]float64{}
	for _, x := range Spending(h, "", from, time.UTC, func(p, sid string, _ int64) bool { return sid != "skip" }) {
		got[x.Provider+" "+x.Date] += x.USD
	}
	want := map[string]float64{"claude 2026-10-30": 1, "claude 2026-10-31": 0.75, "codex 2026-10-31": 0.4} // 0.5 + the fork's own 0.25; one response of 100K at $2/M per session (the repeat adds nothing)
	for k, v := range want {
		if math.Abs(got[k]-v) > 1e-9 {
			t.Fatalf("%s: got %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

func TestParseRemoteRestores(t *testing.T) {
	out := "@@ a\n" + `{"type":"cost-state","sessionId":"a","totalCostUSD":1.5` + "\n" + `"timestamp":"2026-10-30T11:00:00.5Z"` + "\n" +
		`{"type":"cost-state","sessionId":"a","totalCostUSD":2.25` + "\n@@ b\n@@missing\n@@ c\n"
	got := ParseRemoteRestores(out)
	a := got["a"]
	if a == nil || len(a.Restores) != 1 || a.Restores[0].USD != 1.5 || a.End == nil || *a.End != 2.25 {
		t.Fatalf("a: %+v", a)
	}
	if _, ok := got["b"]; ok {
		t.Fatal("a missing file is left out")
	}
	if c := got["c"]; c == nil || len(c.Restores) != 0 || c.End != nil {
		t.Fatalf("c: %+v", c)
	}
}
