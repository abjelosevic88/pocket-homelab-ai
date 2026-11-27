package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPortableToolListDropsEmptyEnumValues(t *testing.T) {
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"data": map[string]any{"type": "object", "properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"resolved", "ignored", "open"}},
			"for":    map[string]any{"type": "string", "enum": []string{"1h", "24h", "7d", "30d", ""}, "default": ""},
			"none":   map[string]any{"type": "string", "enum": []string{""}},
			"rotate": map[string]any{"type": "integer", "enum": []int{0, 90}, "description": "Turn clockwise."},
		}},
	}})
	in := map[string]any{"tools": []askTool{{Name: "pocket_errors_update_issue", InputSchema: schema}}}
	b, _ := json.Marshal(portableToolList(in))
	s := string(b)
	if strings.Contains(s, `"30d",""`) || strings.Contains(s, `"enum":[""]`) || strings.Contains(s, `"enum":[0`) {
		t.Fatalf("empty enum value left: %s", s)
	}
	for _, want := range []string{`"enum":["1h","24h","7d","30d"]`, `"enum":["resolved","ignored","open"]`, `"default":""`, `"pocket_errors_update_issue"`, `"Turn clockwise. One of: 0, 90."`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}
