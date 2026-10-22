package server

import (
	"context"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Sharing: a thread's owner can share it with other people on this Pocket AI.
// "view" reads along live; "chat" also sends messages and answers the agent's
// requests (only while the person may run agents). The agent keeps running on
// the owner's login, folder and settings; renaming, moving, forking, sharing
// and deleting stay the owner's.

// Access to a thread, lowest first.
const (
	accessNone = iota
	accessView
	accessChat
	accessOwner
)

func (s *Server) shareRoutes(r chi.Router) {
	r.Get("/threads/{id}/shares", s.handleGetShares)
	r.Put("/threads/{id}/shares", s.handlePutShares)
}

// threadAccess loads a thread and says what the person may do with it.
func (s *Server) threadAccess(ctx context.Context, u *store.User, id string) (*store.Thread, int) {
	if u == nil {
		return nil, accessNone
	}
	if t, err := s.store.GetThread(ctx, u.ID, id); err == nil {
		return t, accessOwner
	}
	role := s.rt.shareRole(id, u.ID)
	if role == "" {
		return nil, accessNone
	}
	t, err := s.store.GetThreadAny(ctx, id)
	if err != nil {
		return nil, accessNone
	}
	if role == "chat" && u.MayRun() {
		return t, accessChat
	}
	return t, accessView
}

// threadAs loads the thread for a request that needs at least the given
// access: no access at all is a 404, too little a 403.
func (s *Server) threadAs(w http.ResponseWriter, r *http.Request, need int) *store.Thread {
	t, level := s.threadAccess(r.Context(), userFrom(r.Context()), chi.URLParam(r, "id"))
	switch {
	case level == accessNone:
		writeErr(w, http.StatusNotFound, "not found")
		return nil
	case level < need && need == accessOwner:
		writeErr(w, http.StatusForbidden, "only the person who started this chat can do that")
		return nil
	case level < need:
		writeErr(w, http.StatusForbidden, "this chat is shared with you to read only")
		return nil
	}
	return s.rt.decorate(t)
}

// ---------- the engine's copy of every share ----------

func (rt *engine) loadShares() {
	all, err := rt.s.store.AllShares(rt.ctxOrBackground())
	if err != nil {
		rt.s.log.Warn("load shares", "err", err)
		all = map[string]store.SharedThread{}
	}
	rt.sharesMu.Lock()
	rt.shares = all
	rt.sharesMu.Unlock()
}

func (rt *engine) ctxOrBackground() context.Context {
	if rt.ctx != nil {
		return rt.ctx
	}
	return context.Background()
}

func (rt *engine) sharesOf(threadID string) (store.SharedThread, bool) {
	rt.sharesMu.RLock()
	loaded := rt.shares != nil
	rt.sharesMu.RUnlock()
	if !loaded {
		rt.loadShares()
	}
	rt.sharesMu.RLock()
	defer rt.sharesMu.RUnlock()
	st, ok := rt.shares[threadID]
	return st, ok
}

// shareRole is the person's role in someone else's thread ("" = none).
func (rt *engine) shareRole(threadID string, uid int64) string {
	st, _ := rt.sharesOf(threadID)
	for _, sh := range st.Shares {
		if sh.UserID == uid {
			return sh.Role
		}
	}
	return ""
}

// sharedWith lists the ids of the threads shared with the person.
func (rt *engine) sharedWith(uid int64) []string {
	rt.sharesOf("") // load
	rt.sharesMu.RLock()
	defer rt.sharesMu.RUnlock()
	out := []string{}
	for id, st := range rt.shares {
		for _, sh := range st.Shares {
			if sh.UserID == uid {
				out = append(out, id)
			}
		}
	}
	return out
}

// sharers are the people a thread is shared with.
func (rt *engine) sharers(threadID string) []int64 {
	st, _ := rt.sharesOf(threadID)
	out := make([]int64, 0, len(st.Shares))
	for _, sh := range st.Shares {
		out = append(out, sh.UserID)
	}
	return out
}

// ---------- API ----------

type sharePerson struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	CanRun bool   `json:"can_run"`
}

// handleGetShares answers who the thread is shared with and who it could be.
func (s *Server) handleGetShares(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "people")
		return
	}
	people := []sharePerson{}
	for _, u := range users {
		if u.ID != t.UserID {
			people = append(people, sharePerson{ID: u.ID, Name: displayName(&u), Email: u.Email, CanRun: u.MayRun()})
		}
	}
	sort.Slice(people, func(i, j int) bool { return people[i].Name < people[j].Name })
	shares := t.Shares
	if shares == nil {
		shares = []store.ThreadShare{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares, "people": people})
}

// handlePutShares replaces who the thread is shared with:
// {"shares": [{"user_id": 2, "role": "view" | "chat"}]}.
func (s *Server) handlePutShares(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	var in struct {
		Shares []store.ThreadShare `json:"shares"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	seen := map[int64]bool{}
	list := []store.ThreadShare{}
	for _, sh := range in.Shares {
		if sh.UserID == t.UserID || seen[sh.UserID] {
			continue
		}
		u, err := s.store.GetUser(r.Context(), sh.UserID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "one of those people is not on this Pocket AI")
			return
		}
		switch sh.Role {
		case "view":
		case "chat":
			if t.Source == "ask" { // it reads the owner's apps as the owner
				writeErr(w, http.StatusBadRequest, "Ask chats read your apps as you, so others can only view them")
				return
			}
			if !u.MayRun() {
				writeErr(w, http.StatusBadRequest, displayName(u)+" may not run agents, so they can only view")
				return
			}
		default:
			writeErr(w, http.StatusBadRequest, "role must be view or chat")
			return
		}
		seen[sh.UserID] = true
		list = append(list, store.ThreadShare{UserID: sh.UserID, Role: sh.Role})
	}
	before := s.rt.sharers(t.ID)
	if err := s.store.SetShares(r.Context(), t.ID, list); err != nil {
		s.fail(w, err, "share")
		return
	}
	s.rt.loadShares()
	for _, uid := range before {
		if !seen[uid] {
			s.rt.toUser(uid, sseEvent{Name: "thread_removed", Data: map[string]string{"id": t.ID}})
			s.rt.dropViewer(t.ID, uid)
		}
	}
	t.Shares = nil
	s.rt.publishThread(t)
	writeJSON(w, http.StatusOK, s.rt.decorate(t))
}

// sharedThreads are the threads others shared with the person (not archived).
func (s *Server) sharedThreads(ctx context.Context, uid int64) []*store.Thread {
	out := []*store.Thread{}
	for _, id := range s.rt.sharedWith(uid) {
		if t, err := s.store.GetThreadAny(ctx, id); err == nil && !t.Archived {
			out = append(out, s.rt.decorate(t))
		}
	}
	return out
}

func displayName(u *store.User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}
