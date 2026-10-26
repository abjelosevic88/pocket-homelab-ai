package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestSnippetsArePrivateAndKeepFormatting(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "alice@example.com", "user", false)
	bob, bu := person(t, s, h, "bob@example.com", "user", false)
	var snippet store.Snippet
	body := "  Review this diff\n\n  Keep indentation.\n"
	alice.want("POST", "/api/v1/snippets", map[string]any{"title": "  Zebra  ", "body": body, "user_id": bu.ID}, 201, &snippet)
	if snippet.Title != "Zebra" || snippet.Body != body {
		t.Fatalf("format changed: %#v", snippet)
	}
	path := fmt.Sprintf("/api/v1/snippets/%d", snippet.ID)
	var rows []store.Snippet
	bob.want("GET", "/api/v1/snippets", nil, 200, &rows)
	if len(rows) != 0 {
		t.Fatal("another person's snippets leaked")
	}
	bob.want("PUT", path, map[string]string{"title": "Stolen", "body": "changed"}, 404, nil)
	bob.want("DELETE", path, nil, 404, nil)
	alice.want("POST", "/api/v1/snippets", map[string]string{"title": "apple", "body": "One"}, 201, nil)
	alice.want("GET", "/api/v1/snippets", nil, 200, &rows)
	if len(rows) != 2 || rows[0].Title != "apple" || rows[1].Body != body {
		t.Fatalf("bad stored list: %#v", rows)
	}
	alice.want("PUT", path, map[string]string{"title": "Edited", "body": "New body\n"}, 200, &snippet)
	if snippet.Body != "New body\n" {
		t.Fatalf("edit not saved: %#v", snippet)
	}
	alice.want("DELETE", path, nil, 204, nil)
	alice.want("DELETE", path, nil, 404, nil)
	alice.want("PUT", path, map[string]string{"title": "Gone", "body": "gone"}, 404, nil)
	anonymous := &client{t: t, h: h}
	anonymous.want("GET", "/api/v1/snippets", nil, 401, nil)
}

func TestSnippetValidation(t *testing.T) {
	s, h, _ := testServer(t)
	c, _ := person(t, s, h, "alice@example.com", "user", false)
	for _, in := range []map[string]string{
		{"title": " ", "body": "text"}, {"title": "Title", "body": " \n"},
		{"title": strings.Repeat("é", 121), "body": "text"},
		{"title": "Title", "body": strings.Repeat("a", 65537)},
	} {
		c.want("POST", "/api/v1/snippets", in, 400, nil)
	}
	c.want("POST", "/api/v1/snippets", map[string]string{"title": strings.Repeat("é", 120), "body": strings.Repeat("a", 65536)}, 201, nil)
}
