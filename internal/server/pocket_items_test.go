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
	got := pocketItems(sections, mentionApps, pocketItemsPerApp)
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

func TestPocketItemsPhotosCarryDateAndThumb(t *testing.T) {
	sections := []appSection{{Tool: "pocket_photos_search", Data: map[string]any{"result": map[string]any{"items": []any{
		map[string]any{"id": "03d3d01d-f4e7-4bc5-b148-9c2687ddc8db", "title": "IMG_1257.JPG", "taken_at": "2026-10-25T13:44:13.000Z", "place": "Mostar, Bosnia and Herzegovina",
			"item_url": "https://photos.test/?photo=03d3d01d-f4e7-4bc5-b148-9c2687ddc8db", "thumb_url": "https://photos.test/api/v1/immich/assets/03d3d01d-f4e7-4bc5-b148-9c2687ddc8db/thumbnail?size=thumbnail"},
		map[string]any{"id": "x", "title": "bad thumb", "thumb_url": "javascript:alert(1)"},
	}}}}}
	got := pocketItems(sections, []string{"photos"}, pocketItemsOneApp)
	if len(got) != 2 || got[0].Subtitle != "2026-10-25 · Mostar, Bosnia and Herzegovina" || got[0].Thumb == "" || got[0].URL == "" || got[1].Thumb != "" {
		t.Fatalf("photos = %+v", got)
	}
}

func TestWithNewToolsKeepsSessionTools(t *testing.T) {
	session := map[string]askTool{"pocket_analytics_report": {Name: "pocket_analytics_report", Description: "link"}}
	fresh := map[string]askTool{"pocket_analytics_report": {Name: "pocket_analytics_report", Description: "core"}, "pocket_photos_view": {Name: "pocket_photos_view"}}
	got := withNewTools(session, fresh)
	if len(got) != 2 || got["pocket_analytics_report"].Description != "link" || got["pocket_photos_view"].Name == "" {
		t.Fatalf("merged = %+v", got)
	}
	if len(withNewTools(session, nil)) != 1 {
		t.Fatal("no fresh list should keep the session's")
	}
}
