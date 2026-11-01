package agent

import "testing"

func TestConnectorReadTool(t *testing.T) {
	allowed := []string{"Notion", "Google Drive"}
	cases := []struct {
		tool, name string
		ok         bool
	}{
		{"mcp__claude_ai_Notion__notion-search", "Notion", true},
		{"mcp__claude_ai_Notion__notion-fetch", "Notion", true},
		{"mcp__claude_ai_Notion__notion-query-data-sources", "Notion", true},
		{"mcp__claude_ai_Notion__notion-memory-search", "Notion", true},
		{"mcp__claude_ai_Notion__notion-calendar-list-events", "Notion", true},
		{"mcp__claude_ai_Notion__notion-create-view", "Notion", false},
		{"mcp__claude_ai_Notion__notion-update-page", "Notion", false},
		{"mcp__claude_ai_Notion__notion-calendar-cancel-events", "Notion", false},
		{"mcp__claude_ai_Notion__notion-wait-session", "Notion", false},
		{"mcp__claude_ai_Google_Drive__search_files", "Google Drive", true},
		{"mcp__claude_ai_Gmail__search_threads", "", false},
		{"mcp__pocket__pocket_search", "", false},
		{"Bash", "", false},
	}
	for _, c := range cases {
		name, ok := ConnectorReadTool(c.tool, allowed)
		if name != c.name || ok != c.ok {
			t.Errorf("%s: got %q %v, want %q %v", c.tool, name, ok, c.name, c.ok)
		}
	}
	if _, ok := ConnectorReadTool("mcp__claude_ai_Notion__notion-search", nil); ok {
		t.Error("no connectors allowed: Notion must be refused")
	}
}
