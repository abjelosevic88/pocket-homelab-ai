package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// A person's own groups on the Worktrees page: named, ordered, folded or
// not; worktrees are dragged into them (worktree_groups.go in store).

func (s *Server) handleWorktreeGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	list, err := s.store.WorktreeGroups(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "worktree groups")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func groupName(v string) (string, bool) {
	v = strings.TrimSpace(v)
	return oneLine(v, 80), v != ""
}

func (s *Server) handleCreateWorktreeGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = decode(r, &in)
	name, ok := groupName(in.Name)
	if !ok {
		writeErr(w, 400, "name the group")
		return
	}
	g, err := s.store.CreateWorktreeGroup(r.Context(), me(r), name)
	if err != nil {
		s.fail(w, err, "worktree group")
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateWorktreeGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		Name      *string `json:"name"`
		Collapsed *bool   `json:"collapsed"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if in.Name != nil {
		name, ok := groupName(*in.Name)
		if !ok {
			writeErr(w, 400, "name the group")
			return
		}
		in.Name = &name
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.UpdateWorktreeGroup(r.Context(), me(r), id, in.Name, in.Collapsed); err != nil {
		s.fail(w, err, "worktree group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteWorktreeGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteWorktreeGroup(r.Context(), me(r), id); err != nil {
		s.fail(w, err, "worktree group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOrderWorktreeGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil || len(in.IDs) > 500 {
		writeErr(w, 400, "give the groups' ids in order")
		return
	}
	if err := s.store.OrderWorktreeGroups(r.Context(), me(r), in.IDs); err != nil {
		s.fail(w, err, "worktree groups")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /worktrees/{id}/group {group_id (0: out of any group), order: the group's worktree ids after the move}
func (s *Server) handlePlaceWorktree(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		GroupID int64   `json:"group_id"`
		Order   []int64 `json:"order"`
	}
	if err := decode(r, &in); err != nil || len(in.Order) > 1000 {
		writeErr(w, 400, "invalid json")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.PlaceWorktree(r.Context(), me(r), id, in.GroupID, in.Order); err != nil {
		s.fail(w, err, "move worktree")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
