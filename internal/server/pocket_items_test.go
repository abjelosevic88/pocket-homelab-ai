package server

import "testing"

func TestPocketItemsFlattenEachAppsResultShape(t *testing.T) {
	sections := []appSection{
		{Tool: "pocket_files_search", Data: map[string]any{"result": map[string]any{"entries": []any{
			map[string]any{"name": "notes.md", "path": "/notes.md", "mime": "text/markdown", "size": 120.0},
			map[string]any{"name": "Order.pdf", "path": "/Order.pdf", "mime": "application/pdf", "size": 82467.0},
			map[string]any{"name": "Photos", "path": "/Photos", "is_dir": true},
		}}}},
		{Tool: "pocket_notes_list", Data: map[string]any{"result": []any{
			map[string]any{"id": 401595.0, "title": "AS-7 plan", "category": "Projects/Athena", "item_url": "https://notes/note/401595"},
			map[string]any{"omitted": 3.0},
		}}},
		{Tool: "pocket_tasks_list", Data: map[string]any{"result": map[string]any{"tasks": []any{
			map[string]any{"id": 82.0, "title": "Provjeri ugovore", "due": "2026-10-28", "list": map[string]any{"name": "Athena Studio"}},
		}}}},
		{Tool: "pocket_contacts_list", Error: "This source could not be read."},
	}
	got := pocketItems(sections)
	if len(got) != 4 {
		t.Fatalf("want 4 items, got %d: %+v", len(got), got)
	}
	want := []pocketItem{
		{App: "notes", ID: "401595", Title: "AS-7 plan", Subtitle: "Projects/Athena", URL: "https://notes/note/401595", Attachable: true},
		{App: "tasks", ID: "82", Title: "Provjeri ugovore", Subtitle: "Athena Studio · due 2026-10-28"},
		{App: "files", ID: "/notes.md", Title: "notes.md", Subtitle: "/notes.md", Attachable: true},
		{App: "files", ID: "/Order.pdf", Title: "Order.pdf", Subtitle: "/Order.pdf"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestFindTextReadsCommonBodyFields(t *testing.T) {
	cases := map[string]any{
		"plain":   "plain",
		"content": map[string]any{"title": "x", "content": "content"},
		"nested":  map[string]any{"document": map[string]any{"text": "nested"}},
	}
	for want, in := range cases {
		if got := findText(in); got != want {
			t.Errorf("findText(%v) = %q, want %q", in, got, want)
		}
	}
	if findText(map[string]any{"id": 1.0}) != "" {
		t.Error("no body should give no text")
	}
}
