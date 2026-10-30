package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Files on this server or one of your machines: browse a folder, open a
// file in a tab (text, images, PDF, audio, video), upload into a folder.
// Who may see what is the terminal's rule: this server's files are the
// server account's, so the owner or an admin; people with a folder of their
// own see only that folder; other machines through their ssh account.

const (
	filesListMax   = 3000
	filesRemoteMax = 64 << 20 // a file read over ssh is held in memory
)

func (s *Server) fileRoutes(r chi.Router) {
	r.Get("/api/v1/files", s.handleListFiles)
	r.Get("/api/v1/files/raw", s.handleRawFile)
	r.Post("/api/v1/files/upload", s.handleUploadFiles)
	r.Post("/api/v1/files/mkdir", s.handleFileOp("mkdir"))
	r.Post("/api/v1/files/newfile", s.handleFileOp("newfile"))
	r.Post("/api/v1/files/rename", s.handleFileOp("rename"))
	r.Post("/api/v1/files/delete", s.handleFileOp("delete"))
}

type fileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Link  bool   `json:"link,omitempty"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix seconds
}

type fileListing struct {
	HostID  int64       `json:"host_id"`
	Host    string      `json:"host"`
	Path    string      `json:"path"`
	Home    string      `json:"home"`
	Parent  string      `json:"parent,omitempty"`
	Entries []fileEntry `json:"entries"`
	More    bool        `json:"more,omitempty"` // cut at filesListMax
}

// filePlace is where a person's file request goes: a machine (h), or this
// server, where jail (when set) is the only folder they may touch.
type filePlace struct {
	h    *store.Host
	name string
	jail string
	home string
}

func (s *Server) filePlace(r *http.Request) (*filePlace, error) {
	ctx := r.Context()
	u := userFrom(ctx)
	if !u.MayRun() {
		return nil, errForbidden("an admin has to allow you to run agents first")
	}
	hostID, _ := strconv.ParseInt(r.URL.Query().Get("host"), 10, 64)
	if hostID != 0 {
		h, err := s.checkHostUse(ctx, u, hostID)
		if err != nil {
			return nil, err
		}
		return &filePlace{h: h, name: h.Name}, nil
	}
	name, _ := os.Hostname()
	if s.jailed(ctx, u.ID) {
		if err := s.ensurePerson(u.ID); err != nil {
			return nil, err
		}
		return &filePlace{name: name, jail: s.personRoot(u.ID), home: s.personHome(u.ID)}, nil
	}
	if !s.isOwner(ctx, u.ID) && u.Role != "admin" {
		return nil, errForbidden("files on this server are the server's own account: only an admin sees them")
	}
	home, _ := os.UserHomeDir()
	return &filePlace{name: name, home: home}, nil
}

// local resolves a path on this server: absolute (empty = home), and for a
// person with a folder of their own inside it with no symlink out.
func (p *filePlace) local(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	switch {
	case q == "" || q == "~":
		q = p.home
	case strings.HasPrefix(q, "~/"):
		q = filepath.Join(p.home, q[2:])
	}
	if !filepath.IsAbs(q) {
		return "", badRequest("use an absolute path (or ~/…)")
	}
	q = filepath.Clean(q)
	if p.jail != "" {
		rel, err := filepath.Rel(p.jail, q)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || !agent.Confined(q) {
			return "", errForbidden("only your own folder is there for you")
		}
	}
	return q, nil
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.filePlace(r)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	want := r.URL.Query().Get("path")
	var out *fileListing
	if p.h != nil {
		out, err = s.listRemoteFiles(r.Context(), p.h, want)
	} else {
		out, err = p.listLocal(want)
	}
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	out.Host = p.name
	if p.h != nil {
		out.HostID = p.h.ID
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	writeJSON(w, http.StatusOK, out)
}

func (p *filePlace) listLocal(want string) (*fileListing, error) {
	dir, err := p.local(want)
	if err != nil {
		return nil, err
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil, badRequest("that folder is not there (or not yours to open)")
	}
	out := &fileListing{Path: dir, Home: p.home, Entries: []fileEntry{}}
	if dir != "/" && (p.jail == "" || dir != p.jail) {
		out.Parent = filepath.Dir(dir)
	}
	for _, e := range list {
		if len(out.Entries) >= filesListMax {
			out.More = true
			break
		}
		fe := fileEntry{Name: e.Name(), Link: e.Type()&os.ModeSymlink != 0}
		fi, err := os.Stat(filepath.Join(dir, e.Name())) // through a link: what it points at
		if err != nil {
			fi, err = e.Info()
		}
		if err == nil {
			fe.Dir, fe.Size, fe.MTime = fi.IsDir(), fi.Size(), fi.ModTime().Unix()
		}
		if fe.Dir {
			fe.Size = 0
		}
		out.Entries = append(out.Entries, fe)
	}
	return out, nil
}

// remoteFiles lists a folder ($1, empty = home): path=, home=, then one
// line per entry: type letters (d/f/…, a second one for a link's target),
// size, mtime, name. GNU find when there is one (one process), else a loop.
const remoteFiles = `p=$1
case "$p" in ""|"~") p=$HOME;; "~/"*) p="$HOME/${p#\~/}";; esac
cd -- "$p" 2>/dev/null || { echo "err=that folder is not there (or not yours to open)"; exit 0; }
echo "path=$(pwd -P)"
echo "home=$HOME"
if find . -maxdepth 0 -printf '' >/dev/null 2>&1; then
  find . -mindepth 1 -maxdepth 1 -printf '%y%Y\t%s\t%T@\t%f\n' 2>/dev/null | head -n $2
else
  n=0
  for f in * .[!.]* ..?*; do
    [ -e "$f" ] || [ -L "$f" ] || continue
    n=$((n+1)); [ $n -le $2 ] || break
    t=f; [ -d "$f" ] && t=d
    l=$t; [ -L "$f" ] && l=l
    s=0; [ -f "$f" ] && s=$(wc -c < "$f" 2>/dev/null | tr -d ' ')
    m=$(stat -f %m -- "$f" 2>/dev/null || stat -c %Y -- "$f" 2>/dev/null)
    printf '%s%s\t%s\t%s\t%s\n' "$l" "$t" "${s:-0}" "${m:-0}" "$f"
  done
fi
`

func (s *Server) listRemoteFiles(ctx context.Context, h *store.Host, want string) (*fileListing, error) {
	stdout, stderr, err := remoteOf(h).Run(ctx, 40*time.Second, remoteFiles, want, strconv.Itoa(filesListMax+1))
	if err != nil {
		return nil, badRequest(h.Name + ": " + remoteErr(stderr, err))
	}
	out := &fileListing{Entries: []fileEntry{}}
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "err="):
			return nil, badRequest(strings.TrimPrefix(line, "err="))
		case strings.HasPrefix(line, "path="):
			out.Path = strings.TrimPrefix(line, "path=")
		case strings.HasPrefix(line, "home="):
			out.Home = strings.TrimPrefix(line, "home=")
		default:
			f := strings.SplitN(line, "\t", 4)
			if len(f) != 4 || len(f[0]) < 2 || f[3] == "" {
				continue
			}
			if len(out.Entries) >= filesListMax {
				out.More = true
				continue
			}
			size, _ := strconv.ParseInt(f[1], 10, 64)
			mt, _ := strconv.ParseFloat(f[2], 64)
			e := fileEntry{Name: f[3], Link: f[0][0] == 'l', Dir: f[0][1] == 'd', Size: size, MTime: int64(mt)}
			if e.Dir {
				e.Size = 0
			}
			out.Entries = append(out.Entries, e)
		}
	}
	if out.Path != "/" && out.Path != "" {
		out.Parent = path.Dir(out.Path)
	}
	return out, nil
}

// contentType by the name, else by the first bytes.
func contentType(name string, head []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return http.DetectContentType(head)
}

// rawHeaders: the file is shown inside Pocket AI's own origin, so nothing in
// it may run as the page (an HTML or SVG file opened directly is sandboxed;
// PDF goes to the browser's own viewer).
func rawHeaders(w http.ResponseWriter, name, ctype string, download bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	if !strings.HasPrefix(ctype, "application/pdf") {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	}
	if strings.HasPrefix(ctype, "text/html") || strings.Contains(ctype, "xhtml") || strings.HasPrefix(ctype, "text/javascript") || strings.HasPrefix(ctype, "application/javascript") {
		ctype = "text/plain; charset=utf-8" // shown as its source
	}
	w.Header().Set("Content-Type", ctype)
	disp := "inline"
	if download {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
}

func (s *Server) handleRawFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.filePlace(r)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	want := r.URL.Query().Get("path")
	download := r.URL.Query().Get("download") == "1"
	if p.h != nil {
		data, err := s.readRemoteFile(r.Context(), p.h, want)
		if err != nil {
			s.fail(w, err, "files")
			return
		}
		name := path.Base(want)
		rawHeaders(w, name, contentType(name, data[:min(512, len(data))]), download)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(string(data)))
		return
	}
	fp, err := p.local(want)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	f, err := os.Open(fp)
	if err != nil {
		s.fail(w, badRequest("that file is not there (or not yours to open)"), "files")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() || !fi.Mode().IsRegular() {
		s.fail(w, badRequest("that is not a file"), "files")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	_, _ = f.Seek(0, io.SeekStart)
	rawHeaders(w, fi.Name(), contentType(fi.Name(), head[:n]), download)
	http.ServeContent(w, r, "", fi.ModTime(), f) // ranges: video can seek
}

const remoteRead = `[ -f "$1" ] || { echo "that is not a file (or not yours to open)" >&2; exit 3; }
s=$(wc -c < "$1" | tr -d ' ')
[ "$s" -le "$2" ] || { echo "too big to open here ($s bytes): download it with scp" >&2; exit 4; }
cat -- "$1"`

func (s *Server) readRemoteFile(ctx context.Context, h *store.Host, p string) ([]byte, error) {
	if !strings.HasPrefix(p, "/") {
		return nil, badRequest("use an absolute path")
	}
	stdout, stderr, err := remoteOf(h).Run(ctx, 2*time.Minute, remoteRead, p, strconv.Itoa(filesRemoteMax))
	if err != nil {
		return nil, badRequest(remoteErr(stderr, err))
	}
	return []byte(stdout), nil
}

// uploadName: a file name from the browser, without any path in it.
func uploadName(raw string) (string, error) {
	n := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	if n == "" || n == "." || n == ".." || n == "/" || strings.ContainsAny(n, "\x00\n") {
		return "", badRequest("a file has no usable name")
	}
	return n, nil
}

// freeName: name, else "name (1).ext", "name (2).ext"… — an upload never
// replaces a file that is there.
func freeName(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" { // ".env"
		base, ext = name, ""
	}
	n := name
	for i := 1; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, n)); errors.Is(err, os.ErrNotExist) {
			return n
		}
		n = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

const remoteUpload = `d=$1; f=$2; n=$f
case "$f" in .*.*|[!.]*.*) b=${f%.*}; e=.${f##*.};; *) b=$f; e=;; esac
i=1
while [ -e "$d/$n" ] || [ -L "$d/$n" ]; do n="$b ($i)$e"; i=$((i+1)); done
[ -d "$d" ] && [ -w "$d" ] || { echo "that folder is not there (or not yours to write to)" >&2; exit 3; }
umask 022
t="$d/.$n.part.$$"
cat > "$t" && mv -f -- "$t" "$d/$n" && printf '%s' "$d/$n" || { rm -f -- "$t"; exit 1; }`

// handleUploadFiles takes multipart files into ?path= (a folder) and
// answers where each one went.
func (s *Server) handleUploadFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.filePlace(r)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	want := r.URL.Query().Get("path")
	var dir string
	if p.h != nil {
		if !strings.HasPrefix(want, "/") {
			s.fail(w, badRequest("use an absolute folder path"), "files")
			return
		}
		dir = strings.TrimSuffix(want, "/")
		if dir == "" {
			dir = "/"
		}
	} else {
		if dir, err = p.local(want); err != nil {
			s.fail(w, err, "files")
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			s.fail(w, badRequest("that folder is not there"), "files")
			return
		}
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "send the files as multipart/form-data")
		return
	}
	saved := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.fail(w, badRequest("the upload broke off: "+err.Error()), "files")
			return
		}
		if part.FileName() == "" {
			continue
		}
		name, err := uploadName(part.FileName())
		if err != nil {
			s.fail(w, err, "files")
			return
		}
		var where string
		if p.h != nil {
			stdout, stderr, err := remoteOf(p.h).RunInput(r.Context(), 30*time.Minute, part, remoteUpload, dir, name)
			if err != nil {
				s.fail(w, badRequest(name+": "+remoteErr(stderr, err)), "files")
				return
			}
			where = strings.TrimSpace(stdout)
		} else {
			where, err = saveLocal(dir, name, part)
			if err != nil {
				s.fail(w, badRequest(name+": "+err.Error()), "files")
				return
			}
		}
		saved = append(saved, where)
	}
	s.log.Info("files uploaded", "user", me(r), "host", r.URL.Query().Get("host"), "dir", dir, "n", len(saved))
	writeJSON(w, http.StatusOK, map[string]any{"paths": saved})
}

func saveLocal(dir, name string, src io.Reader) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+name+".part.*")
	if err != nil {
		return "", errors.New("cannot write in that folder")
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	_ = tmp.Chmod(0o644)
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	final := filepath.Join(dir, freeName(dir, name))
	if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return final, nil
}

// ---------- new folder / new file / rename / delete ----------

// remoteFileOp does one change on a machine: $1 op, $2 path (the folder for
// mkdir/newfile, the entry for rename/delete), $3 a new name.
const remoteFileOp = `op=$1; p=$2; n=$3
case "$op" in
mkdir|newfile)
  [ -d "$p" ] || { echo "that folder is not there" >&2; exit 3; }
  [ -e "$p/$n" ] || [ -L "$p/$n" ] && { echo "something called $n is there already" >&2; exit 3; }
  if [ "$op" = mkdir ]; then mkdir -- "$p/$n"; else : > "$p/$n"; fi || exit 1
  printf '%s' "$p/$n";;
rename)
  [ -e "$p" ] || [ -L "$p" ] || { echo "it is not there any more" >&2; exit 3; }
  d=$(dirname -- "$p")
  [ -e "$d/$n" ] || [ -L "$d/$n" ] && { echo "something called $n is there already" >&2; exit 3; }
  mv -- "$p" "$d/$n" && printf '%s' "$d/$n";;
delete)
  [ -e "$p" ] || [ -L "$p" ] || exit 0
  [ "$(cd -- "$p" 2>/dev/null && pwd -P)" = "$(cd && pwd -P)" ] && { echo "that is the home folder" >&2; exit 3; }
  rm -rf -- "$p";;
esac`

// handleFileOp: {host, path, name}. Delete takes folders with everything in
// them (the page asks first); never "/" or a home folder.
func (s *Server) handleFileOp(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Host int64  `json:"host"`
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		q := r.URL.Query()
		q.Set("host", strconv.FormatInt(in.Host, 10))
		r.URL.RawQuery = q.Encode()
		p, err := s.filePlace(r)
		if err != nil {
			s.fail(w, err, "files")
			return
		}
		name := ""
		if op != "delete" {
			if name, err = checkEntryName(in.Name); err != nil {
				s.fail(w, err, "files")
				return
			}
		}
		target := strings.TrimSuffix(strings.TrimSpace(in.Path), "/")
		if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "~") {
			s.fail(w, badRequest("use an absolute path"), "files")
			return
		}
		var where string
		if p.h != nil {
			if target == "" || target == "/" {
				s.fail(w, badRequest("not the root folder"), "files")
				return
			}
			stdout, stderr, err := remoteOf(p.h).Run(r.Context(), 2*time.Minute, remoteFileOp, op, target, name)
			if err != nil {
				s.fail(w, badRequest(remoteErr(stderr, err)), "files")
				return
			}
			where = strings.TrimSpace(stdout)
		} else if where, err = p.localOp(op, target, name); err != nil {
			s.fail(w, err, "files")
			return
		}
		s.log.Info("files changed", "user", me(r), "op", op, "host", in.Host, "path", target, "name", name)
		writeJSON(w, http.StatusOK, map[string]string{"path": where})
	}
}

// checkEntryName: one plain name, no path in it.
func checkEntryName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00\n") || len(n) > 255 {
		return "", badRequest("use a plain name, without slashes")
	}
	return n, nil
}

func (p *filePlace) localOp(op, target, name string) (string, error) {
	fp, err := p.local(target)
	if err != nil {
		return "", err
	}
	switch op {
	case "mkdir", "newfile":
		if fi, err := os.Stat(fp); err != nil || !fi.IsDir() {
			return "", badRequest("that folder is not there")
		}
		dst := filepath.Join(fp, name)
		if _, err := os.Lstat(dst); err == nil {
			return "", badRequest("something called " + name + " is there already")
		}
		if op == "mkdir" {
			err = os.Mkdir(dst, 0o755)
		} else {
			var f *os.File
			if f, err = os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
				err = f.Close()
			}
		}
		if err != nil {
			return "", badRequest("could not make it: " + err.Error())
		}
		return dst, nil
	case "rename":
		if _, err := os.Lstat(fp); err != nil {
			return "", badRequest("it is not there any more")
		}
		dst := filepath.Join(filepath.Dir(fp), name)
		if _, err := os.Lstat(dst); err == nil {
			return "", badRequest("something called " + name + " is there already")
		}
		if err := os.Rename(fp, dst); err != nil {
			return "", badRequest("could not rename it: " + err.Error())
		}
		return dst, nil
	case "delete":
		if fp == "/" || fp == filepath.Clean(p.home) || (p.jail != "" && (fp == p.jail || filepath.Dir(fp) == p.jail)) {
			return "", badRequest("that folder cannot be deleted from here")
		}
		if err := os.RemoveAll(fp); err != nil {
			return "", badRequest("could not delete it: " + err.Error())
		}
		return fp, nil
	}
	return "", badRequest("unknown change")
}
