package server

import (
	"net/http"
	"path"
	"strconv"
	"strings"
)

// These endpoints only store personal shortcuts. They never touch a directory
// or connect over SSH; missing/offline folders can still be forgotten.
func (s *Server) handleRememberedFolders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	host := int64(0)
	p := r.URL.Query().Get("path")
	if raw := r.URL.Query().Get("host"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, 400, "invalid machine")
			return
		}
		host = n
	}
	if r.Method == http.MethodPost {
		var in struct {
			Host int64  `json:"host_id"`
			Path string `json:"path"`
		}
		if err := decode(r, &in); err != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		host, p = in.Host, strings.TrimSpace(in.Path)
	}
	if host < 0 {
		writeErr(w, 400, "invalid machine")
		return
	}
	if _, err := s.checkHostUse(ctx, u, host); err != nil {
		s.fail(w, err, "remembered folders")
		return
	}
	if r.Method != http.MethodGet {
		if len(p) > 4096 || strings.ContainsRune(p, 0) || (p != "" && !path.IsAbs(p)) {
			writeErr(w, 400, "choose an absolute folder path")
			return
		}
		if p != "" {
			p = path.Clean(p)
		}
		var err error
		if r.Method == http.MethodDelete {
			err = s.store.ForgetFolder(ctx, u.ID, host, p)
		} else {
			err = s.store.RememberFolder(ctx, u.ID, host, p)
		}
		if err != nil {
			s.fail(w, err, "remembered folders")
			return
		}
	}
	out, err := s.store.FolderHistory(ctx, u.ID, host)
	if err != nil {
		s.fail(w, err, "remembered folders")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
