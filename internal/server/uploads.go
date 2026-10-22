package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/documenttext"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	if !userFrom(r.Context()).MayRun() {
		if _, err := s.askToken(r.Context(), me(r)); err != nil {
			writeErr(w, 403, "Connect Pocket Ask before attaching files")
			return
		}
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
		data, err := io.ReadAll(io.LimitReader(f, s.cfg.UploadMax+1))
		f.Close()
		if err != nil || int64(len(data)) > s.cfg.UploadMax {
			writeErr(w, 413, "File is too large")
			return
		}
		contentType := http.DetectContentType(data)
		ext, ok := imageExt[contentType]
		extracted := ""
		if !ok {
			ext = strings.ToLower(filepath.Ext(fh.Filename))
			if ext == ".pdf" {
				if !bytes.HasPrefix(data, []byte("%PDF-")) {
					writeErr(w, 415, "This is not a PDF")
					return
				}
				extracted, err = pdfText(r.Context(), data)
				contentType = "application/pdf"
			} else {
				extracted, err = documenttext.Text(fh.Filename, data)
				contentType = "application/octet-stream"
			}
			if err != nil {
				writeErr(w, 415, err.Error())
				return
			}
			if strings.TrimSpace(extracted) == "" {
				writeErr(w, 422, "No readable text found. For scanned documents, attach page images or run OCR in Documents first.")
				return
			}
		}

		id := uuid.NewString()
		path := filepath.Join(dir, id+ext)
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			s.fail(w, err, "upload")
			return
		}
		size, err := io.Copy(dst, bytes.NewReader(data))
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
		u := &store.Upload{ID: id, UserID: uid, Name: name, Mime: contentType, Size: size, Path: path, ExtractedText: extracted}
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
	if err != nil { // someone else's picture in a shared thread
		u, err = s.store.GetUploadAny(r.Context(), chi.URLParam(r, "id"))
		if err == nil {
			if u.ThreadID == nil {
				err = store.ErrNotFound
			} else if _, level := s.threadAccess(r.Context(), userFrom(r.Context()), *u.ThreadID); level == accessNone {
				err = store.ErrNotFound
			}
		}
	}
	if err != nil {
		s.fail(w, err, "upload")
		return
	}
	w.Header().Set("Content-Type", u.Mime)
	if !strings.HasPrefix(u.Mime, "image/") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": u.Name}))
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, u.Path)
}

func (s *Server) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUpload(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil { // someone else's picture in a shared thread
		u, err = s.store.GetUploadAny(r.Context(), chi.URLParam(r, "id"))
		if err == nil {
			if u.ThreadID == nil {
				err = store.ErrNotFound
			} else if _, level := s.threadAccess(r.Context(), userFrom(r.Context()), *u.ThreadID); level == accessNone {
				err = store.ErrNotFound
			}
		}
	}
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

// Extract only text: no embedded JavaScript, external resources or macros run.
// pdftotext is a deployment dependency; output and execution time are bounded.
func pdfText(ctx context.Context, data []byte) (string, error) {
	f, err := os.CreateTemp("", "pocket-document-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdftotext", "-f", "1", "-l", "100", "-layout", "-enc", "UTF-8", f.Name(), "-")
	out := &boundedOutput{limit: 180000}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("PDF extraction is unavailable on this server; attach images or plain text")
		}
		if !out.full {
			return "", errors.New("PDF could not be read (it may be encrypted or damaged)")
		}
	}
	text := strings.ToValidUTF8(out.b.String(), "")
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	if out.full {
		text += "\n[Text truncated at 180,000 bytes]"
	}
	return text + "\n[PDF text only; first 100 pages at most. Layout, images and signatures are not verified.]", nil
}

type boundedOutput struct {
	b     strings.Builder
	limit int
	full  bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	remaining := w.limit - w.b.Len()
	if len(p) > remaining {
		w.b.Write(p[:remaining])
		w.full = true
		return len(p), nil
	}
	return w.b.Write(p)
}
