package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/documenttext"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Notebooks: a set of sources (uploaded files, web pages, Pocket notes,
// documents and files, pasted text) to ask questions about. A notebook is a
// folder: sources/ holds each source as Markdown text, originals/ the files
// as they came, INDEX.md the list. Its chats are ordinary conversations whose
// agent works in that folder, told to answer from the sources and cite them.

const (
	notebookSourceMax = 50 << 20 // bytes per uploaded or fetched source
	notebookTextMax   = 2 << 20  // bytes of text kept per source
	notebookMaxCount  = 300      // sources per notebook
	notebookPageMax   = 15 << 20 // bytes read from a web page
	notebookNameMax   = 120      // runes
	notebookInstrMax  = 8 << 10  // bytes of notebook instructions
	notebookPasteMax  = 1 << 20  // bytes of pasted text
	notebookFetchWait = 25 * time.Second
)

var sourceKindLabel = map[string]string{
	"file": "Uploaded file", "link": "Web page", "note": "Pocket Notes note", "document": "Pocket Documents document",
	"pocket-file": "Pocket Files file", "text": "Pasted text",
}

func (s *Server) notebookRoutes(r chi.Router) {
	r.Get("/notebooks", s.handleListNotebooks)
	r.Post("/notebooks", s.handleSaveNotebook)
	r.Get("/notebooks/{id}", s.handleGetNotebook)
	r.Put("/notebooks/{id}", s.handleSaveNotebook)
	r.Delete("/notebooks/{id}", s.handleDeleteNotebook)
	r.Post("/notebooks/{id}/chats", s.handleNotebookChat)
	r.Post("/notebooks/{id}/sources/upload", s.handleNotebookUpload)
	r.Post("/notebooks/{id}/sources/link", s.handleNotebookLink)
	r.Post("/notebooks/{id}/sources/pocket", s.handleNotebookPocket)
	r.Post("/notebooks/{id}/sources/text", s.handleNotebookText)
	r.Get("/notebooks/{id}/sources/{sid}/text", s.handleSourceText)
	r.Get("/notebooks/{id}/sources/{sid}/raw", s.handleSourceRaw)
	r.Post("/notebooks/{id}/sources/{sid}/refresh", s.handleSourceRefresh)
	r.Put("/notebooks/{id}/sources/{sid}", s.handleRenameSource)
	r.Delete("/notebooks/{id}/sources/{sid}", s.handleDeleteSource)
}

// notebookBase holds a person's notebooks: inside their own folder when they
// have one (their agent runs jailed there), else the server's data folder.
func (s *Server) notebookBase(ctx context.Context, uid int64) string {
	return filepath.Join(s.askBase(ctx, uid), "notebooks")
}

func (s *Server) notebookDir(ctx context.Context, uid, id int64) string {
	return filepath.Join(s.notebookBase(ctx, uid), strconv.FormatInt(id, 10))
}

// notebookOfCwd is the notebook a conversation's folder belongs to (0 = none).
// Callers still check the notebook is the person's (it is looked up by owner).
func notebookOfCwd(cwd string) int64 {
	if filepath.Base(filepath.Dir(cwd)) != "notebooks" {
		return 0
	}
	id, err := strconv.ParseInt(filepath.Base(cwd), 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

// notebookPrompt tells the agent of a notebook chat what the folder is.
func (s *Server) notebookPrompt(ctx context.Context, t *store.Thread) string {
	id := notebookOfCwd(t.Cwd)
	if id == 0 || t.HostID != 0 || filepath.Clean(t.Cwd) != s.notebookDir(ctx, t.UserID, id) {
		return ""
	}
	nb, err := s.store.GetNotebook(ctx, t.UserID, id)
	if err != nil {
		return ""
	}
	p := "\n\nThis conversation belongs to the person's Pocket AI notebook “" + nb.Name + "”, a set of sources they collected to ask questions about. " +
		"Your working folder is that notebook: INDEX.md lists every source (title, kind, where it came from, its text file); sources/ holds each one as Markdown text; " +
		"originals/ keeps uploaded files as they came (PDF pages, pictures, spreadsheets) for when layout, images or exact figures matter. " +
		"The person can add or remove sources at any time, so read INDEX.md again before answering a new question. " +
		"Answer from these sources: read the relevant files with your file tools (Read, Grep, Glob; they need no approval here, shell commands do), searching across sources/ for words when there are many; name the source title for each point, e.g. [Title], " +
		"and say plainly when the sources do not cover something; general knowledge is fine when you mark it as such. " +
		"When the person points at one source (by title, by its sources/ file, or “this one”), focus on that one. " +
		"Do not change, move or delete files in this folder; when the person asks for a write-up, put it in a new file under answers/ and say so."
	if strings.TrimSpace(nb.Instructions) != "" {
		p += "\n\nThe person's instructions for this notebook:\n" + nb.Instructions
	}
	return p
}

// ---------- notebooks ----------

type notebookOut struct {
	*store.Notebook
	Folder      string                  `json:"folder"`
	SourcesList []*store.NotebookSource `json:"source_list"`
	ChatsList   []*store.Thread         `json:"chat_list"`
}

func (s *Server) handleListNotebooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListNotebooks(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "list notebooks")
		return
	}
	for _, nb := range list {
		chats, _ := s.store.ThreadsInFolder(r.Context(), me(r), s.notebookDir(r.Context(), me(r), nb.ID))
		for _, t := range chats {
			if !t.Archived {
				nb.Chats++
			}
		}
	}
	writeJSON(w, 200, list)
}

func (s *Server) notebookOf(w http.ResponseWriter, r *http.Request) (*store.Notebook, bool) {
	nb, err := s.store.GetNotebook(r.Context(), me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "notebook")
		return nil, false
	}
	return nb, true
}

func (s *Server) handleGetNotebook(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.notebookFull(r.Context(), nb))
}

func (s *Server) notebookFull(ctx context.Context, nb *store.Notebook) notebookOut {
	dir := s.notebookDir(ctx, nb.UserID, nb.ID)
	out := notebookOut{Notebook: nb, Folder: dir, SourcesList: []*store.NotebookSource{}, ChatsList: []*store.Thread{}}
	if list, err := s.store.ListNotebookSources(ctx, nb.ID); err == nil {
		out.SourcesList = list
	}
	nb.Sources = len(out.SourcesList)
	chats, _ := s.store.ThreadsInFolder(ctx, nb.UserID, dir)
	for _, t := range chats {
		if !t.Archived {
			out.ChatsList = append(out.ChatsList, s.rt.decorate(t))
		}
	}
	nb.Chats = len(out.ChatsList)
	return out
}

func (s *Server) handleSaveNotebook(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb := &store.Notebook{UserID: me(r)}
	if r.Method == http.MethodPut {
		var ok bool
		if nb, ok = s.notebookOf(w, r); !ok {
			return
		}
	}
	var in struct {
		Name         *string `json:"name"`
		Instructions *string `json:"instructions"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if in.Name != nil {
		nb.Name = strings.TrimSpace(*in.Name)
	}
	if in.Instructions != nil {
		nb.Instructions = strings.TrimSpace(*in.Instructions)
	}
	if nb.Name == "" {
		nb.Name = "Untitled notebook"
	}
	if utf8.RuneCountInString(nb.Name) > notebookNameMax || len(nb.Instructions) > notebookInstrMax {
		writeErr(w, 400, "Use a name of up to 120 characters and instructions of up to 8 KB")
		return
	}
	created := nb.ID == 0
	if err := s.store.SaveNotebook(r.Context(), nb); err != nil {
		s.fail(w, err, "save notebook")
		return
	}
	dir := s.notebookDir(r.Context(), nb.UserID, nb.ID)
	for _, sub := range []string{"sources", "originals"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			s.fail(w, err, "notebook folder")
			return
		}
	}
	s.writeNotebookIndex(r.Context(), nb)
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, s.notebookFull(r.Context(), nb))
}

func (s *Server) handleDeleteNotebook(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	dir := s.notebookDir(r.Context(), nb.UserID, nb.ID)
	chats, _ := s.store.ThreadsInFolder(r.Context(), nb.UserID, dir)
	for _, t := range chats {
		if t.Status == "running" || t.Status == "waiting" {
			writeErr(w, 409, "A chat in this notebook is still working. Stop it first.")
			return
		}
	}
	for _, t := range chats {
		// its folder is going: the chat stays readable in the archive
		_, _ = s.store.DB.ExecContext(r.Context(), `UPDATE threads SET archived=1 WHERE id=? AND user_id=?`, t.ID, nb.UserID)
		s.rt.stop(t.ID)
	}
	if err := s.store.DeleteNotebook(r.Context(), nb.UserID, nb.ID); err != nil {
		s.fail(w, err, "delete notebook")
		return
	}
	if notebookOfCwd(dir) == nb.ID {
		_ = os.RemoveAll(dir)
	}
	w.WriteHeader(204)
}

// writeNotebookIndex rewrites INDEX.md, the list the agent reads first.
func (s *Server) writeNotebookIndex(ctx context.Context, nb *store.Notebook) {
	dir := s.notebookDir(ctx, nb.UserID, nb.ID)
	list, _ := s.store.ListNotebookSources(ctx, nb.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "# Notebook: %s\n\n", nb.Name)
	b.WriteString("This folder is a Pocket AI notebook. Answer from its sources and name the source for each point.\n")
	b.WriteString("Each source's text is in sources/; uploaded files as they came are in originals/.\n\n")
	if strings.TrimSpace(nb.Instructions) != "" {
		fmt.Fprintf(&b, "## Instructions\n\n%s\n\n", nb.Instructions)
	}
	fmt.Fprintf(&b, "## Sources (%d)\n\n", len(list))
	if len(list) == 0 {
		b.WriteString("None yet.\n")
	}
	for i, x := range list {
		fmt.Fprintf(&b, "%d. **%s** (%s", i+1, oneLine(x.Title, 200), sourceKindLabel[x.Kind])
		if x.Origin != "" && x.Kind != "file" {
			fmt.Fprintf(&b, ", from %s", oneLine(x.Origin, 300))
		}
		fmt.Fprintf(&b, ", added %s)\n   - text: %s", dayOf(x.CreatedAt), x.File)
		if x.Original != "" {
			fmt.Fprintf(&b, "\n   - original: %s", x.Original)
		}
		b.WriteString("\n")
	}
	_ = os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte(b.String()), 0o600)
	s.store.TouchNotebook(ctx, nb.ID)
}

func dayOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// ---------- chats ----------

type notebookCwd struct{}

func (s *Server) handleNotebookChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	var in newThread
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	dir := s.notebookDir(r.Context(), nb.UserID, nb.ID)
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o700); err != nil {
		s.fail(w, err, "notebook folder")
		return
	}
	s.writeNotebookIndex(r.Context(), nb)
	// here, in the notebook's folder: no other machine, worktree or folder
	in.HostID, in.HostHome, in.Worktree, in.WorkspaceID, in.Cwd = 0, "", nil, nil, dir
	ctx := context.WithValue(context.WithValue(r.Context(), startInBackground{}, true), notebookCwd{}, dir)
	t, err := s.createThread(ctx, userFrom(r.Context()), in, "web", "")
	if err != nil && t == nil {
		s.fail(w, err, "notebook chat")
		return
	}
	if err != nil {
		s.rt.saveItem(t.ID, agent.Item{Key: "err-" + uuid.NewString()[:8], Kind: "notice", Level: "error", Text: err.Error()})
		t = s.rt.setStatus(t.ID, "error", err.Error())
	}
	writeJSON(w, 201, t)
}

// ---------- adding sources ----------

// addSource stores a source's text (and original) and lists it.
func (s *Server) addSource(ctx context.Context, nb *store.Notebook, x *store.NotebookSource, text string, original []byte, originalName string) (*store.NotebookSource, error) {
	list, _ := s.store.ListNotebookSources(ctx, nb.ID)
	if len(list) >= notebookMaxCount {
		return nil, badRequest(fmt.Sprintf("A notebook holds up to %d sources", notebookMaxCount))
	}
	x.NotebookID = nb.ID
	x.Title = oneLine(firstNonEmpty(x.Title, "Untitled"), 200)
	x.File = "sources/pending.md"
	if err := s.store.SaveNotebookSource(ctx, x); err != nil {
		return nil, err
	}
	if err := s.writeSource(ctx, nb, x, text, original, originalName); err != nil {
		_ = s.store.DeleteNotebookSource(ctx, nb.ID, x.ID)
		return nil, err
	}
	s.writeNotebookIndex(ctx, nb)
	return x, nil
}

// writeSource writes (or rewrites) a source's files and saves its row.
func (s *Server) writeSource(ctx context.Context, nb *store.Notebook, x *store.NotebookSource, text string, original []byte, originalName string) error {
	dir := s.notebookDir(ctx, nb.UserID, nb.ID)
	for _, sub := range []string{"sources", "originals"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	base := fmt.Sprintf("%03d-%s", x.ID, slugify(x.Title))
	if original != nil {
		ext := strings.ToLower(filepath.Ext(originalName))
		if len(ext) > 10 {
			ext = ""
		}
		if x.Original != "" {
			_ = os.Remove(filepath.Join(dir, x.Original))
		}
		x.Original = "originals/" + base + ext
		if err := os.WriteFile(filepath.Join(dir, x.Original), original, 0o600); err != nil {
			return err
		}
		x.Size = int64(len(original))
	}
	if len(text) > notebookTextMax {
		text = strings.ToValidUTF8(text[:notebookTextMax], "") + "\n\n[Shortened: only the first 2 MB of text are kept"
		if x.Original != "" {
			text += "; the full file is " + x.Original
		}
		text += "]"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- Kind: %s\n", x.Title, sourceKindLabel[x.Kind])
	if x.Origin != "" {
		fmt.Fprintf(&b, "- From: %s\n", x.Origin)
	}
	fmt.Fprintf(&b, "- Added: %s\n", dayOf(firstNonEmpty(x.CreatedAt, store.Now())))
	if x.Original != "" {
		fmt.Fprintf(&b, "- Original: %s\n", x.Original)
	}
	b.WriteString("\n---\n\n")
	b.WriteString(text)
	b.WriteString("\n")
	file := "sources/" + base + ".md"
	if x.File != "" && x.File != file && x.File != "sources/pending.md" {
		_ = os.Remove(filepath.Join(dir, x.File))
	}
	x.File = file
	if err := os.WriteFile(filepath.Join(dir, file), []byte(b.String()), 0o600); err != nil {
		return err
	}
	x.Chars = int64(utf8.RuneCountInString(text))
	return s.store.SaveNotebookSource(ctx, x)
}

func (s *Server) sourceResult(w http.ResponseWriter, r *http.Request, nb *store.Notebook, x *store.NotebookSource, err error) {
	if err != nil {
		var ue uploadErr
		if errors.As(err, &ue) {
			writeErr(w, ue.status, ue.msg)
			return
		}
		s.fail(w, err, "add source")
		return
	}
	writeJSON(w, 201, x)
}

// fileText reads an uploaded file the way the composer does; pictures and
// scanned PDFs have no text, the agent looks at the original instead.
func (s *Server) fileText(ctx context.Context, name string, data []byte) (string, string, error) {
	ctype := http.DetectContentType(data)
	if _, ok := imageExt[ctype]; ok {
		return "(A picture: look at the original file to see it.)", ctype, nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return "", "", uploadErr{415, name + " is not a PDF"}
		}
		text, err := pdfText(ctx, data)
		if err != nil {
			return "", "", uploadErr{415, err.Error()}
		}
		if strings.TrimSpace(text) == "" {
			text = "(No text could be taken out of this PDF; it is probably scanned. Read the original file to see its pages.)"
		}
		return text, "application/pdf", nil
	case legacyOffice[ext] != "":
		text, err := s.legacyOfficeText(ctx, name, legacyOffice[ext], data)
		if err != nil {
			return "", "", uploadErr{415, err.Error()}
		}
		return text, "application/octet-stream", nil
	}
	text, err := documenttext.Text(name, data)
	if err != nil {
		return "", "", uploadErr{415, name + ": " + err.Error()}
	}
	if strings.TrimSpace(text) == "" {
		return "", "", uploadErr{422, "No readable text found in " + name}
	}
	if ext == ".md" || ext == ".txt" || ext == ".csv" {
		ctype = "text/plain; charset=utf-8"
	}
	return text, ctype, nil
}

func (s *Server) handleNotebookUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4*notebookSourceMax)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "Upload too large or not multipart (up to 50 MB per file)")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, "no file")
		return
	}
	out := []*store.NotebookSource{}
	var failed []string
	for _, fh := range files {
		if fh.Size > notebookSourceMax {
			failed = append(failed, fh.Filename+" is larger than 50 MB")
			continue
		}
		f, err := fh.Open()
		if err != nil {
			failed = append(failed, fh.Filename+": could not be read")
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, notebookSourceMax+1))
		f.Close()
		if err != nil {
			failed = append(failed, fh.Filename+": could not be read")
			continue
		}
		name := filepath.Base(fh.Filename)
		text, ctype, err := s.fileText(r.Context(), name, data)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		x, err := s.addSource(r.Context(), nb, &store.NotebookSource{Kind: "file", Title: name, Origin: name, Mime: ctype}, text, data, name)
		if err != nil {
			failed = append(failed, name+": "+err.Error())
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		writeErr(w, 415, strings.Join(failed, "; "))
		return
	}
	writeJSON(w, 201, map[string]any{"sources": out, "failed": failed})
}

func (s *Server) handleNotebookLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	var in struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	x := &store.NotebookSource{Kind: "link", Origin: strings.TrimSpace(in.URL), Title: strings.TrimSpace(in.Title)}
	text, data, name, err := s.fetchSource(r.Context(), x)
	if err != nil {
		s.sourceResult(w, r, nb, nil, err)
		return
	}
	x, err = s.addSource(r.Context(), nb, x, text, data, name)
	s.sourceResult(w, r, nb, x, err)
}

// fetchSource reads a link's page into x (title, mime): its text, and the
// file itself when it is one (a PDF) rather than a page.
func (s *Server) fetchSource(ctx context.Context, x *store.NotebookSource) (string, []byte, string, error) {
	u, err := url.Parse(x.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", nil, "", badRequest("Paste a full web address starting with https://")
	}
	ctx, cancel := context.WithTimeout(ctx, notebookFetchWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, "", badRequest("That web address does not work")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PocketAI-Notebook/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,text/plain;q=0.9,*/*;q=0.5")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, "", uploadErr{502, "Could not open " + u.Host + ": " + shortNetErr(err)}
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return "", nil, "", uploadErr{502, fmt.Sprintf("%s answered %s", u.Host, res.Status)}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, notebookPageMax+1))
	if err != nil {
		return "", nil, "", uploadErr{502, "The page stopped loading"}
	}
	if len(data) > notebookPageMax {
		return "", nil, "", uploadErr{413, "The page is larger than 15 MB"}
	}
	ctype, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	x.Mime = ctype
	name := filepath.Base(u.Path)
	switch {
	case ctype == "application/pdf" || bytes.HasPrefix(data, []byte("%PDF-")):
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
		x.Title = firstNonEmpty(x.Title, strings.TrimSuffix(name, ".pdf"), u.Host)
		text, _, err := s.fileText(ctx, name, data)
		return text, data, name, err
	case ctype == "text/html" || ctype == "application/xhtml+xml":
		title, text := htmlText(string(data))
		x.Title = firstNonEmpty(x.Title, title, u.Host+u.Path)
		if strings.TrimSpace(text) == "" {
			return "", nil, "", uploadErr{422, "No readable text on that page (it may need JavaScript or a login)"}
		}
		return text, nil, "", nil
	case strings.HasPrefix(ctype, "text/") || ctype == "application/json" || strings.HasSuffix(ctype, "+json") || strings.HasSuffix(ctype, "xml"):
		if !utf8.Valid(data) {
			data = []byte(strings.ToValidUTF8(string(data), ""))
		}
		x.Title = firstNonEmpty(x.Title, name, u.Host)
		return string(data), nil, "", nil
	}
	// an office file or picture behind a link
	x.Title = firstNonEmpty(x.Title, name, u.Host)
	text, mt, err := s.fileText(ctx, name, data)
	if err != nil {
		return "", nil, "", err
	}
	x.Mime = mt
	return text, data, name, nil
}

func shortNetErr(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}

func (s *Server) handleNotebookPocket(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Items []struct {
			App   string `json:"app"`
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"items"`
	}
	if err := decode(r, &in); err != nil || len(in.Items) == 0 || len(in.Items) > 50 {
		writeErr(w, 400, "Choose up to 50 items")
		return
	}
	out := []*store.NotebookSource{}
	var failed []string
	for _, it := range in.Items {
		kind := map[string]string{"notes": "note", "documents": "document", "files": "pocket-file"}[it.App]
		if kind == "" {
			failed = append(failed, it.Title+": only notes, documents and text files can be added")
			continue
		}
		text, _, err := s.pocketText(r.Context(), me(r), it.App, it.ID, notebookTextMax)
		if err != nil {
			failed = append(failed, firstNonEmpty(it.Title, it.ID)+": "+err.Error())
			continue
		}
		x := &store.NotebookSource{Kind: kind, Title: firstNonEmpty(strings.TrimSpace(it.Title), it.ID), Origin: it.App + ":" + it.ID, Mime: "text/markdown"}
		if link := strings.TrimSpace(it.URL); strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
			x.Origin += " " + link
		}
		if x, err = s.addSource(r.Context(), nb, x, text, nil, ""); err != nil {
			failed = append(failed, it.Title+": "+err.Error())
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		writeErr(w, 422, strings.Join(failed, "; "))
		return
	}
	writeJSON(w, 201, map[string]any{"sources": out, "failed": failed})
}

func (s *Server) handleNotebookText(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > notebookPasteMax {
		writeErr(w, 400, "Paste some text (up to 1 MB)")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = oneLine(in.Text, 60)
	}
	x, err := s.addSource(r.Context(), nb, &store.NotebookSource{Kind: "text", Title: title, Mime: "text/markdown"}, in.Text, nil, "")
	s.sourceResult(w, r, nb, x, err)
}

// ---------- reading, renaming, refreshing, removing ----------

func (s *Server) sourceOf(w http.ResponseWriter, r *http.Request) (*store.Notebook, *store.NotebookSource, bool) {
	nb, ok := s.notebookOf(w, r)
	if !ok {
		return nil, nil, false
	}
	x, err := s.store.GetNotebookSource(r.Context(), nb.ID, idParam(r, "sid"))
	if err != nil {
		s.fail(w, err, "source")
		return nil, nil, false
	}
	return nb, x, true
}

func (s *Server) handleSourceText(w http.ResponseWriter, r *http.Request) {
	nb, x, ok := s.sourceOf(w, r)
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(s.notebookDir(r.Context(), nb.UserID, nb.ID), x.File))
	if err != nil {
		writeErr(w, 404, "The text of this source is missing; add it again")
		return
	}
	text := string(data)
	if i := strings.Index(text, "\n---\n\n"); i >= 0 {
		text = text[i+6:]
	}
	writeJSON(w, 200, map[string]any{"text": text})
}

func (s *Server) handleSourceRaw(w http.ResponseWriter, r *http.Request) {
	nb, x, ok := s.sourceOf(w, r)
	if !ok {
		return
	}
	if x.Original == "" {
		writeErr(w, 404, "This source has no original file")
		return
	}
	p := filepath.Join(s.notebookDir(r.Context(), nb.UserID, nb.ID), x.Original)
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "The original file is missing")
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	_, _ = f.Seek(0, io.SeekStart)
	ctype := http.DetectContentType(head[:n])
	inline := ctype == "application/pdf" || imageExt[ctype] != ""
	name := firstNonEmpty(x.Title, filepath.Base(p))
	if filepath.Ext(name) == "" {
		name += filepath.Ext(p)
	}
	if !inline {
		ctype = "application/octet-stream"
	}
	rawHeaders(w, name, ctype, !inline)
	fi, _ := f.Stat()
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleRenameSource(w http.ResponseWriter, r *http.Request) {
	nb, x, ok := s.sourceOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Title) == "" {
		writeErr(w, 400, "Give it a name")
		return
	}
	x.Title = oneLine(in.Title, 200)
	if err := s.store.SaveNotebookSource(r.Context(), x); err != nil {
		s.fail(w, err, "rename source")
		return
	}
	s.writeNotebookIndex(r.Context(), nb)
	writeJSON(w, 200, x)
}

func (s *Server) handleSourceRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	nb, x, ok := s.sourceOf(w, r)
	if !ok {
		return
	}
	var text, name string
	var data []byte
	var err error
	switch x.Kind {
	case "link":
		title := x.Title
		text, data, name, err = s.fetchSource(r.Context(), x)
		x.Title = title
	case "note", "document", "pocket-file":
		ref, _, _ := strings.Cut(x.Origin, " ")
		app, id, _ := strings.Cut(ref, ":")
		var status int
		if text, status, err = s.pocketText(r.Context(), me(r), app, id, notebookTextMax); err != nil {
			err = uploadErr{status, err.Error()}
		}
	default:
		writeErr(w, 400, "Only web pages and Pocket items can be read again; upload a file again to replace it")
		return
	}
	if err == nil {
		err = s.writeSource(r.Context(), nb, x, text, data, name)
	}
	if err == nil {
		s.writeNotebookIndex(r.Context(), nb)
	}
	if err != nil {
		s.sourceResult(w, r, nb, nil, err)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	nb, x, ok := s.sourceOf(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNotebookSource(r.Context(), nb.ID, x.ID); err != nil {
		s.fail(w, err, "remove source")
		return
	}
	dir := s.notebookDir(r.Context(), nb.UserID, nb.ID)
	for _, f := range []string{x.File, x.Original} {
		if f != "" && !strings.Contains(f, "..") {
			_ = os.Remove(filepath.Join(dir, f))
		}
	}
	s.writeNotebookIndex(r.Context(), nb)
	w.WriteHeader(204)
}
