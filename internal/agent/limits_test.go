package agent

import "testing"

func TestParseClaudeUsage(t *testing.T) {
	const pre = `{"type":"control_response","response":{"subtype":"success","request_id":"usage-1","response":{"rate_limits_available":true,"rate_limits":`

	// Not the get_usage answer.
	if _, _, ok, _ := parseClaudeUsage([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}`)); ok {
		t.Fatal("init answer taken for usage")
	}

	// The full list.
	w, _, ok, err := parseClaudeUsage([]byte(pre + `{"limits":[
		{"kind":"session","percent":2,"resets_at":"a","scope":null},
		{"kind":"weekly_all","percent":23,"resets_at":"b","scope":null},
		{"kind":"weekly_scoped","percent":4,"resets_at":"c","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}]}}}}`))
	if !ok || err != nil || len(w) != 3 || w[0].ID != "five_hour" || w[1].Used != 0.23 || w[2].ID != "weekly_scoped:fable" || w[2].Name != "7 days · Fable" {
		t.Fatalf("limits: ok=%v err=%v %+v", ok, err, w)
	}

	// No list: the fixed windows.
	w, _, ok, err = parseClaudeUsage([]byte(pre + `{"limits":[],"five_hour":{"utilization":1,"resets_at":"a"},"seven_day":{"utilization":50,"resets_at":"b"},
		"seven_day_opus":null,"model_scoped":[{"display_name":"Fable","utilization":4,"resets_at":"c"}]}}}}`))
	if !ok || err != nil || len(w) != 3 || w[0].ID != "five_hour" || w[1].Used != 0.5 || w[2].ID != "weekly_scoped:fable" {
		t.Fatalf("fixed windows: ok=%v err=%v %+v", ok, err, w)
	}

	// Nothing at all: an answer, but no windows (ClaudeLimits asks again).
	w, _, ok, err = parseClaudeUsage([]byte(pre + `{"limits":[],"five_hour":null,"seven_day":null}}}}`))
	if !ok || err != nil || len(w) != 0 {
		t.Fatalf("empty: ok=%v err=%v %+v", ok, err, w)
	}
}
