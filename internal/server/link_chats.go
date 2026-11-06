package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// ChatsScope is the Core link scope for starting Ask chats about one of the
// person's items (a book in Pocket Bookshelf, …). Unlike threads:write these
// are Ask conversations: Claude with the person's Pocket tools, no folder,
// no coding agent, and they start right away.
const ChatsScope = "chats:write"

// chatCaller authenticates an app acting for a person with chats:write and
// returns the person and the app's Core slug.
func (s *Server) chatCaller(w http.ResponseWriter, r *http.Request) (*store.User, string) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer pcx_") || !s.core.Configured() {
		writeErr(w, http.StatusUnauthorized, "a Pocket Core app-link token is required")
		return nil, ""
	}
	in, err := s.core.Introspect(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil || in == nil || !in.Active || !in.Delegated() || in.Audience != "ai" || !in.HasScope(ChatsScope) ||
		in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		writeErr(w, http.StatusForbidden, "Pocket AI chat link rejected")
		return nil, ""
	}
	u, err := s.coreIdentityUser(r.Context(), in)
	if err != nil {
		s.fail(w, err, "resolve AI identity")
		return nil, ""
	}
	return u, strings.ToLower(in.App.Slug)
}

// linkChat is one Ask chat about an item, as the app sees it.
type linkChat struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"` // idle | running | waiting | error
	Preview   string `json:"preview"`
	Model     string `json:"model"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Server) linkChatOf(t *store.Thread) linkChat {
	t = s.rt.decorate(t)
	return linkChat{ID: t.ID, Title: t.Title, Status: linkStatus(t), Preview: t.Preview, Model: t.Model, URL: s.threadURL(t.ID),
		CreatedAt: t.CreatedAt, UpdatedAt: firstNonEmpty(t.ActivityAt, t.UpdatedAt)}
}

// handleLinkCreateChat starts an Ask chat about one item: the first message
// is sent at once, context goes to the agent as the item's (untrusted) details.
//
//	POST /api/v1/link/chats {text, title, url, context?, name?, model?: sonnet|opus|haiku, no_memory?}
func (s *Server) handleLinkCreateChat(w http.ResponseWriter, r *http.Request) {
	u, app := s.chatCaller(w, r)
	if u == nil {
		return
	}
	var in struct {
		Text     string `json:"text"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		Context  string `json:"context"`
		Name     string `json:"name"` // what the chat is called after the item (default: the question)
		Model    string `json:"model"`
		NoMemory bool   `json:"no_memory"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10)).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "invalid chat")
		return
	}
	if strings.TrimSpace(in.URL) == "" {
		writeErr(w, http.StatusBadRequest, "say which item the chat is about (url)")
		return
	}
	item := oneLine(in.Title, 120)
	t, err := s.createAsk(r.Context(), u, askCreate{Text: in.Text, Model: in.Model, NoMemory: in.NoMemory,
		Source: askSource{App: app, URL: strings.TrimSpace(in.URL), Title: item, Selection: in.Context},
		Title:  strings.TrimPrefix(item+" · "+oneLine(firstNonEmpty(strings.TrimSpace(in.Name), in.Text), 100), " · ")})
	if err != nil {
		if t != nil { // created, but the first message failed: say so with the chat
			writeJSON(w, http.StatusCreated, s.linkChatOf(t))
			return
		}
		s.fail(w, err, "create linked chat")
		return
	}
	writeJSON(w, http.StatusCreated, s.linkChatOf(t))
}

// handleLinkChats lists the person's chats about one item, newest first.
//
//	GET /api/v1/link/chats?url=<item url>
func (s *Server) handleLinkChats(w http.ResponseWriter, r *http.Request) {
	u, app := s.chatCaller(w, r)
	if u == nil {
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("url"))
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "url is required")
		return
	}
	ctx := r.Context()
	rows, err := s.store.DB.QueryContext(ctx, `SELECT t.id FROM threads t JOIN ask_contexts c ON c.thread_id = t.id
		WHERE t.user_id = ? AND t.source = 'ask' AND t.source_ref = ? AND t.archived = 0 AND json_extract(c.context, '$.app') = ?
		ORDER BY t.activity_at DESC LIMIT 50`, u.ID, ref, app)
	if err != nil {
		s.fail(w, err, "linked chats")
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []linkChat{}
	for _, id := range ids {
		if t, err := s.store.GetThread(ctx, u.ID, id); err == nil {
			out = append(out, s.linkChatOf(t))
		}
	}
	writeJSON(w, http.StatusOK, out)
}
