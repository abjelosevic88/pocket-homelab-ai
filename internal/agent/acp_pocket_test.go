package agent

import "testing"

func TestIsPocketToolCall(t *testing.T) {
	for title, want := range map[string]bool{
		"Run mcp_pocket_pocket_find_tools":  true,
		"mcp_pocket_pocket_notes_create":    true,
		"mcp__pocket__pocket_money_summary": true,
		"Run uname -sm && whoami":           false,
		"mcp_github_create_issue":           false,
		"mcp_pocket_pocket_x && rm -rf /":   false,
		"":                                  false,
	} {
		if got := isPocketToolCall(Tool{Title: title, Name: "Tool"}); got != want {
			t.Errorf("%q: got %v, want %v", title, got, want)
		}
	}
}

func TestAntigravityFullAccess(t *testing.T) {
	ag, _ := ACPAgentByID("antigravity")
	if acp, auto := ag.mode("full-access"); acp != "accept-edits" || !auto {
		t.Fatalf("full-access = %q, %v", acp, auto)
	}
	for _, id := range []string{"default", "accept-edits", "auto", "plan"} {
		if acp, auto := ag.mode(id); acp != id || auto {
			t.Fatalf("%s = %q, %v", id, acp, auto)
		}
	}
}
