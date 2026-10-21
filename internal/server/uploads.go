package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func (s *Server) uploadRoutes(r chi.Router) {
	r.Post("/uploads", s.handleUpload)
	r.Get("/uploads/{id}", s.handleGetUpload)
	r.Delete("/uploads/{id}", s.handleDeleteUpload)
}

type uploadOut struct {
	*store.Upload
	URL string `json:"url"`
}

// handleUpload takes images for the next message (multipart field "file").
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*s.cfg.UploadMax)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "upload too large or not multipart")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "no file")
		return
	}
	uid := me(r)
	dir := filepath.Join(s.cfg.DataDir, "uploads", fmt.Sprint(uid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.fail(w, err, "upload dir")
		return
	}
	out := []uploadOut{}
	for _, fh := range files {
		if fh.Size > s.cfg.UploadMax {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("%s is larger than %d MB", fh.Filename, s.cfg.UploadMax>>20))
			return
		}
		f, err := fh.Open()
		if err != nil {
			s.fail(w, err, "upload")
			return
		}
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		mime := http.DetectContentType(head[:n])
		ext, ok := imageExt[mime]
		if !ok {
			f.Close()
			lower := strings.ToLower(fh.Filename)
			if strings.HasSuffix(lower, ".heic") || strings.HasSuffix(lower, ".heif") {
				writeErr(w, http.StatusUnsupportedMediaType, "HEIC photos are not supported — on iPhone set Camera → Formats → Most Compatible, or share the photo as JPEG")
				return
			}
			writeErr(w, http.StatusUnsupportedMediaType, fh.Filename+" is not a PNG, JPEG, GIF or WebP image")
			return
		}
		id := uuid.NewString()
		path := filepath.Join(dir, id+ext)
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			f.Close()
			s.fail(w, err, "upload")
			return
		}
		size, err := io.Copy(dst, io.MultiReader(strings.NewReader(string(head[:n])), f))
		f.Close()
		dst.Close()
		if err != nil {
			os.Remove(path)
			s.fail(w, err, "upload")
			return
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == "blob" || name == "image.png" {
			name = "image" + ext
		}
		u := &store.Upload{ID: id, UserID: uid, Name: name, Mime: mime, Size: size, Path: path}
		if err := s.store.CreateUpload(r.Context(), u); err != nil {
			os.Remove(path)
			s.fail(w, err, "upload")
			return
		}
		out = append(out, uploadOut{Upload: u, URL: "/api/v1/uploads/" + id})
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUpload(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "upload")
		return
	}
	w.Header().Set("Content-Type", u.Mime)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, u.Path)
}

func (s *Server) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUpload(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "upload")
		return
	}
	if u.ThreadID != nil {
		writeErr(w, http.StatusConflict, "already sent")
		return
	}
	_ = s.store.DeleteUpload(r.Context(), u.UserID, u.ID)
	s.removeUploadFile(u.Path)
	w.WriteHeader(http.StatusNoContent)
}

// removeUploadFile deletes an upload, but only inside DATA_DIR/uploads.
func (s *Server) removeUploadFile(path string) {
	base, _ := filepath.Abs(filepath.Join(s.cfg.DataDir, "uploads"))
	p, _ := filepath.Abs(path)
	if strings.HasPrefix(p, base+string(os.PathSeparator)) {
		_ = os.Remove(p)
	}
}
