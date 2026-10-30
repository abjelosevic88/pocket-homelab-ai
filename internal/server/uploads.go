package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/documenttext"
	"io"
	"mime"
	"mime/multipart"
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
		if err != nil {
			s.fail(w, err, "upload")
			return
		}
		u, err := s.saveUpload(r.Context(), uid, fh.Filename, data)
		if err != nil {
			var ue uploadErr
			if errors.As(err, &ue) {
				writeErr(w, ue.status, ue.msg)
				return
			}
			s.fail(w, err, "upload")
			return
		}
		out = append(out, uploadOut{Upload: u, URL: "/api/v1/uploads/" + u.ID})
	}
	writeJSON(w, http.StatusCreated, out)
}

// uploadErr is a file the person (or app) has to change.
type uploadErr struct {
	status int
	msg    string
}

func (e uploadErr) Error() string { return e.msg }

// saveUpload stores a file for a person: pictures as they are, PDFs and
// documents with their text extracted (that text is what the agent reads).
func (s *Server) saveUpload(ctx context.Context, uid int64, filename string, data []byte) (*store.Upload, error) {
	if int64(len(data)) > s.cfg.UploadMax {
		return nil, uploadErr{413, fmt.Sprintf("%s is larger than %d MB", filename, s.cfg.UploadMax>>20)}
	}
	dir := filepath.Join(s.cfg.DataDir, "uploads", fmt.Sprint(uid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	contentType := http.DetectContentType(data)
	ext, ok := imageExt[contentType]
	extracted := ""
	if !ok {
		var err error
		ext = strings.ToLower(filepath.Ext(filename))
		if ext == ".pdf" {
			if !bytes.HasPrefix(data, []byte("%PDF-")) {
				return nil, uploadErr{415, filename + " is not a PDF"}
			}
			extracted, err = pdfText(ctx, data)
			contentType = "application/pdf"
		} else if modern, legacy := legacyOffice[ext]; legacy {
			extracted, err = s.legacyOfficeText(ctx, filename, modern, data)
			contentType = "application/octet-stream"
		} else {
			extracted, err = documenttext.Text(filename, data)
			contentType = "application/octet-stream"
		}
		if err != nil {
			return nil, uploadErr{415, err.Error()}
		}
		if strings.TrimSpace(extracted) == "" {
			return nil, uploadErr{422, "No readable text found in " + filename + ". For scanned documents, attach page images or run OCR in Documents first."}
		}
	}
	id := uuid.NewString()
	path := filepath.Join(dir, id+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	name := filepath.Base(filename)
	if name == "" || name == "." || name == "blob" || name == "image.png" {
		name = "image" + ext
	}
	u := &store.Upload{ID: id, UserID: uid, Name: name, Mime: contentType, Size: int64(len(data)), Path: path, ExtractedText: extracted}
	if err := s.store.CreateUpload(ctx, u); err != nil {
		os.Remove(path)
		return nil, err
	}
	return u, nil
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

// legacyOffice maps the old binary Office formats to what Collabora
// converts them into before the usual (bounded, macro-free) extraction.
var legacyOffice = map[string]string{".doc": "docx", ".xls": "xlsx", ".ppt": "pptx"}

// oleMagic starts every .doc/.xls/.ppt (OLE2 compound file).
var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// legacyOfficeText converts an old Office file with Collabora's convert-to
// API (OFFICE_CONVERT_URL) and extracts the text of the result.
func (s *Server) legacyOfficeText(ctx context.Context, filename, modern string, data []byte) (string, error) {
	if !bytes.HasPrefix(data, oleMagic) {
		return "", fmt.Errorf("%s is not an old-style Office file", filepath.Base(filename))
	}
	if s.cfg.OfficeConvertURL == "" {
		return "", errors.New("old .doc/.xls/.ppt files need a converter on this server; save as DOCX, XLSX or PPTX first")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("data", "upload"+filepath.Ext(filename))
	if err != nil {
		return "", err
	}
	part.Write(data) //nolint:errcheck
	mw.Close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.OfficeConvertURL+"/cool/convert-to/"+modern, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errors.New("the document converter is not reachable; try again or save as " + strings.ToUpper(modern))
	}
	defer res.Body.Close()
	converted, err := io.ReadAll(io.LimitReader(res.Body, 20<<20+1))
	if err != nil || res.StatusCode != http.StatusOK || len(converted) == 0 {
		return "", fmt.Errorf("%s could not be converted (it may be encrypted or damaged)", filepath.Base(filename))
	}
	return documenttext.Text("converted."+modern, converted)
}
