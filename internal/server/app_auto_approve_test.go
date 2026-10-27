package server

import (
	"encoding/json"
	"testing"
)

func TestAppAutoRules(t *testing.T) {
	tool := func(name, app, kind string) askTool {
		var x askTool
		_ = json.Unmarshal([]byte(`{"name":"`+name+`","_meta":{"pocket/app":"`+app+`","pocket/kind":"`+kind+`"}}`), &x)
		return x
	}
	rules := appAutoRules{Apps: map[string]string{"projects": "add", "notes": "all", "bad app": "all", "money": "bogus"}, Tools: []string{"pocket_tasks_complete", "rm -rf"}}.clean()
	if len(rules.Apps) != 2 || len(rules.Tools) != 1 {
		t.Fatalf("clean kept invalid rules: %+v", rules)
	}
	cases := []struct {
		tool askTool
		auto bool
	}{
		{tool("pocket_projects_link_pr", "projects", "add"), true},
		{tool("pocket_projects_update_issue", "projects", "change"), false},
		{tool("pocket_notes_update", "notes", "change"), true},
		{tool("pocket_tasks_complete", "tasks", "change"), true},
		{tool("pocket_tasks_create", "tasks", "add"), false},
		{tool("pocket_money_create_transaction", "money", "add"), false},
		{tool("pocket_projects_mystery", "", ""), false}, // no Core metadata: always ask
		{tool("pocket_projects_issues", "projects", "read"), false},
	}
	for _, c := range cases {
		if got := rules.allows(c.tool) != ""; got != c.auto {
			t.Errorf("%s: auto=%v, want %v", c.tool.Name, got, c.auto)
		}
	}
}
