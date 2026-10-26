package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) handleListSnippets(w http.ResponseWriter, r *http.Request) {
	snippets, err := s.store.ListSnippets(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "list snippets")
		return
	}
	writeJSON(w, http.StatusOK, snippets)
}

func (s *Server) handleSaveSnippet(w http.ResponseWriter, r *http.Request) {
	snippet := &store.Snippet{UserID: me(r)}
	if r.Method == http.MethodPut {
		current, err := s.store.GetSnippet(r.Context(), me(r), idParam(r, "id"))
		if err != nil {
			s.fail(w, err, "snippet")
			return
		}
		snippet = current
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&in) != nil {
		writeErr(w, 400, "invalid snippet JSON")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || strings.TrimSpace(in.Body) == "" {
		writeErr(w, 400, "a snippet needs a title and text")
		return
	}
	if utf8.RuneCountInString(in.Title) > 120 || len(in.Body) > 64<<10 {
		writeErr(w, 400, "use a title of up to 120 characters and text of up to 64 KB")
		return
	}
	// Keep the body byte-for-byte, including indentation and trailing newlines.
	snippet.Title, snippet.Body = in.Title, in.Body
	created := snippet.ID == 0
	if err := s.store.SaveSnippet(r.Context(), snippet); err != nil {
		s.fail(w, err, "save snippet")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, snippet)
}

func (s *Server) handleDeleteSnippet(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSnippet(r.Context(), me(r), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete snippet")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
