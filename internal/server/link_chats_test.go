package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// chatCore fakes Core's introspection for chats:write links: pcx_chat acts
// for email as Pocket Bookshelf; the others are each wrong in one way.
func chatCore(t *testing.T, s *Server, email string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		scope, app := ChatsScope, "bookshelf"
		switch input.Token {
		case "pcx_threads_only":
			scope = ThreadsScope
		case "pcx_notes":
			app = "notes"
		}
		json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "scope": scope, "audience": "ai",
			"app":  map[string]any{"id": 3, "slug": app, "name": app},
			"user": map[string]any{"id": 7, "sub": "sub-" + email, "email": email, "name": email}})
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
}

func TestLinkChats(t *testing.T) {
	s, h, root := testServer(t)
	// may not run agents: Ask chats don't need it
	_, u := person(t, s, h, "reader@example.test", "member", false)
	chatCore(t, s, u.Email)
	book := "https://bookshelf.example.test/books/17"
	ask := func(app, ref string) *store.Thread {
		th := &store.Thread{ID: uuid.NewString(), UserID: u.ID, Provider: "claude", Mode: "default", Cwd: root, Title: "Dune · Who is Stilgar?", Source: "ask", SourceRef: ref}
		if err := s.store.CreateThread(context.Background(), th); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(askSource{App: app, URL: ref, Title: "Dune"})
		if _, err := s.store.DB.Exec(`INSERT INTO ask_contexts(thread_id,context) VALUES(?,?)`, th.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
		return th
	}
	mine := ask("bookshelf", book)
	ask("bookshelf", "https://bookshelf.example.test/books/18") // another book
	ask("notes", book)                                          // another app, same address
	plain := thread(t, s, u.ID, root)                           // not an Ask chat
	_, _ = s.store.DB.Exec(`UPDATE threads SET source_ref=? WHERE id=?`, book, plain.ID)

	app := &client{t: t, h: h, token: "pcx_chat"}
	var list []linkChat
	app.want("GET", "/api/v1/link/chats?url="+url.QueryEscape(book), nil, 200, &list)
	if len(list) != 1 || list[0].ID != mine.ID || list[0].Title != "Dune · Who is Stilgar?" || list[0].Status != "idle" {
		t.Fatalf("chats about the book: %+v", list)
	}
	app.want("GET", "/api/v1/link/chats", nil, 400, nil)

	// the other app sees only its own chats about the address
	(&client{t: t, h: h, token: "pcx_notes"}).want("GET", "/api/v1/link/chats?url="+url.QueryEscape(book), nil, 200, &list)
	if len(list) != 1 || list[0].ID == mine.ID {
		t.Fatalf("notes' chats: %+v", list)
	}

	// threads:write alone is not enough, nor a person's own token
	(&client{t: t, h: h, token: "pcx_threads_only"}).want("GET", "/api/v1/link/chats?url="+url.QueryEscape(book), nil, 403, nil)
	(&client{t: t, h: h, token: "pai_whatever"}).want("POST", "/api/v1/link/chats", map[string]any{"text": "hi", "url": book}, 401, nil)

	// starting one needs the item and the person's Ask connection
	app.want("POST", "/api/v1/link/chats", map[string]any{"text": "Who is Stilgar?", "title": "Dune"}, 400, nil)
	code, body := app.call("POST", "/api/v1/link/chats", map[string]any{"text": "Who is Stilgar?", "title": "Dune", "url": book})
	if code != 400 || !json.Valid(body) {
		t.Fatalf("without an Ask connection: %d %s", code, body)
	}
}
