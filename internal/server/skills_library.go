package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// One skill library for every agent: the folders in ~/.agents/skills. Codex,
// Gemini CLI, OpenCode, Qwen Code and Antigravity read that folder
// themselves; Claude Code only reads its own (~/.claude/skills), so each
// skill gets a link there. Skills found as real folders in Claude Code's or
// Codex's own skills folder move into the library (arrangeLocal), so a skill
// an agent writes in the old place ends up in the one library too.
//
// The library of this server is the one the other machines follow
// (skills_sync.go).

const (
	libMaxFile  = 512 << 10 // a text file edited here
	libMaxSkill = 20 << 20  // a whole skill (import, a copy from a machine)
	libMaxFiles = 2000
)

// libMu guards changes to a library's folders (saving, arranging, taking a
// copy from a machine).
var libMu sync.Mutex

// libScope is whose library a request works on: the server's, or the own
// one of a person who works in their own folder.
type libScope struct {
	key    string   // "" = the server's, "u<id>" = a person's own
	uid    int64    // that person (0 = the server)
	home   string   // ~ of that library
	dir    string   // <home>/.agents/skills
	claude []string // Claude Code login folders: a link per skill
	codex  []string // Codex login folders: copies there move into the library
}

func (sc *libScope) trash() string {
	return filepath.Join(sc.home, ".agents", ".pocket-ai-trash", "skills")
}

func (sc *libScope) settingKey(name string) string {
	if sc.key == "" {
		return name
	}
	return name + "_" + sc.key
}

// libScopeFor is the library of the person asking (nil = the server's own,
// for background work).
func (s *Server) libScopeFor(ctx context.Context, u *store.User) *libScope {
	sc := &libScope{home: s.cfg.Home}
	jailed := u != nil && s.jailed(ctx, u.ID)
	if jailed {
		_ = s.ensurePerson(u.ID)
		sc.key, sc.uid, sc.home = "u"+strconv.FormatInt(u.ID, 10), u.ID, s.personHome(u.ID)
	} else {
		sc.claude, sc.codex = []string{s.cfg.ClaudeHome}, []string{s.cfg.CodexHome}
	}
	list, _ := s.store.ListAccounts(ctx)
	for _, a := range list {
		if a.Home == "" {
			continue
		}
		if jailed {
			if a.UserID == nil || *a.UserID != u.ID || !s.ownFolder(ctx, u.ID, a.Home) {
				continue
			}
		} else if agent.IsPersonPath(a.Home) {
			continue
		}
		switch a.Provider {
		case "claude":
			sc.claude = append(sc.claude, a.Home)
		case "codex":
			sc.codex = append(sc.codex, a.Home)
		}
	}
	sc.dir = filepath.Join(sc.home, ".agents", "skills")
	return sc
}

func (s *Server) skillsLibraryRoutes(r chi.Router) {
	r.Get("/skills/library", s.handleLibrary)
	r.Post("/skills/library", s.handleLibrarySave)
	r.Post("/skills/library/import", s.handleLibraryImport)
	r.Get("/skills/library/{name}", s.handleLibrarySkill)
	r.Put("/skills/library/{name}", s.handleLibrarySave)
	r.Delete("/skills/library/{name}", s.handleLibraryDelete)
	r.Post("/skills/library/{name}/duplicate", s.handleLibraryDuplicate)
	r.Get("/skills/library/{name}/download", s.handleLibraryDownload)
	r.Get("/skills/library/{name}/file", s.handleLibraryFile)
	r.Put("/skills/library/{name}/file", s.handleLibraryFileSave)
	r.Delete("/skills/library/{name}/file", s.handleLibraryFileDelete)
	r.Put("/skills/library/{name}/machines", s.handleLibraryMachines)
	s.skillsSyncRoutes(r)
}

type libFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type libSkill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Body        string    `json:"body,omitempty"`
	Files       []libFile `json:"files,omitempty"` // every file but SKILL.md (one skill only)
	FileCount   int       `json:"file_count"`      // besides SKILL.md
	Size        int64     `json:"size"`
	Sum         string    `json:"sum"`
	Modified    string    `json:"modified"`
	Path        string    `json:"path"`
	Off         []int64   `json:"off"` // machines it is kept off
}

// libAgent is an agent on this server and how it gets the library.
type libAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	How  string `json:"how"`
}

// scopeOf answers 403 unless the person may run agents here.
func (s *Server) scopeOf(w http.ResponseWriter, r *http.Request) *libScope {
	if !s.requireRun(w, r) {
		return nil
	}
	sc := s.libScopeFor(r.Context(), userFrom(r.Context()))
	// a person's library is in their own folder: a link they made there must
	// not lead the server into someone else's files
	if !agent.Confined(sc.dir) {
		writeErr(w, http.StatusForbidden, "your skills folder leads outside your own folder")
		return nil
	}
	if name := chi.URLParam(r, "name"); name != "" && !agent.Confined(filepath.Join(sc.dir, name)) {
		writeErr(w, http.StatusNotFound, "not found")
		return nil
	}
	return sc
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	ctx := r.Context()
	libMu.Lock()
	if s.arrangeLocal(sc) {
		forgetSkills()
		s.kickSkillSync(sc, false)
	}
	list := readLibrary(sc.dir)
	libMu.Unlock()
	st := s.loadSyncState(ctx, sc)
	for i := range list {
		list[i].Off = append([]int64{}, st.Off[list[i].Name]...)
	}
	agents := []libAgent{{ID: "claude", Name: "Claude Code", How: "linked into its skills folder"}, {ID: "codex", Name: "Codex", How: "reads the shared folder"}}
	if sc.uid == 0 {
		for _, p := range s.providersFor(ctx, userFrom(ctx), false) {
			if ag, ok := agent.ACPAgentByID(p.ID); ok && p.Version != "" {
				agents = append(agents, libAgent{ID: ag.ID, Name: ag.Name, How: "reads the shared folder"})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": sc.dir, "skills": list, "agents": agents})
}

// readLibrary lists the skills of a library folder.
func readLibrary(dir string) []libSkill {
	out := []libSkill{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !validSkill(e.Name()) || !agent.Confined(filepath.Join(dir, e.Name())) {
			continue
		}
		if sk, err := readLibSkill(dir, e.Name(), false); err == nil {
			out = append(out, *sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func readLibSkill(dir, name string, full bool) (*libSkill, error) {
	folder := filepath.Join(dir, name)
	data, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	front, rest := splitFrontmatter(string(data))
	sk := &libSkill{Name: name, Description: frontmatterValue(front, "description"), Path: folder, Off: []int64{}}
	if full {
		sk.Body = strings.TrimLeft(rest, "\n")
		sk.Files = []libFile{}
	}
	var newest time.Time
	err = walkSkill(folder, func(rel string, info fs.FileInfo) error {
		sk.Size += info.Size()
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if rel != "SKILL.md" {
			sk.FileCount++
			if full {
				sk.Files = append(sk.Files, libFile{Path: rel, Size: info.Size()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if sk.Sum, err = skillSum(folder); err != nil {
		return nil, err
	}
	sk.Modified = newest.UTC().Format(time.RFC3339)
	return sk, nil
}

// walkSkill visits the regular files of a skill folder (rel with forward
// slashes, in byte order). Links, .git folders and Finder's .DS_Store are
// not part of a skill.
func walkSkill(folder string, fn func(rel string, info fs.FileInfo) error) error {
	if real, err := filepath.EvalSymlinks(folder); err == nil {
		folder = real // the skill's folder may be a link itself; links inside it are left out
	}
	return filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != folder {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == ".DS_Store" || strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(folder, p)
		return fn(filepath.ToSlash(rel), info)
	})
}

// skillSum is one number for a whole skill folder; skillSumSh gives the same
// on another machine (cksum of "<crc> <size> ./<path>" lines, paths in byte
// order), so two copies can be compared without sending them.
func skillSum(folder string) (string, error) {
	var lines []string
	err := walkSkill(folder, func(rel string, _ fs.FileInfo) error {
		data, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		lines = append(lines, "./"+rel+"\x00"+cksum(data))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(lines) // by path: the NUL sorts before any path byte
	var b strings.Builder
	for _, l := range lines {
		p, sum, _ := strings.Cut(l, "\x00")
		b.WriteString(sum + " " + p + "\n")
	}
	return strings.ReplaceAll(cksum([]byte(b.String())), " ", "-"), nil
}

func (s *Server) handleLibrarySkill(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name := chi.URLParam(r, "name")
	if !validSkill(name) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	sk, err := readLibSkill(sc.dir, name, true)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	sk.Off = append([]int64{}, s.loadSyncState(r.Context(), sc).Off[name]...)
	writeJSON(w, http.StatusOK, sk)
}

func validSkill(name string) bool { return skillName.MatchString(name) && !skillsReserved[name] }

// handleLibrarySave creates a skill (POST) or changes one (PUT): its
// description and instructions, and its name when "rename" is given.
// Frontmatter keys other than name and description are kept as they are.
func (s *Server) handleLibrarySave(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Rename      string `json:"rename"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	creating := r.Method == http.MethodPost
	name := strings.TrimSpace(in.Name)
	if !creating {
		name = chi.URLParam(r, "name")
	}
	desc := strings.Join(strings.Fields(in.Description), " ")
	to := strings.TrimSpace(in.Rename)
	switch {
	case !validSkill(name) || (to != "" && !validSkill(to)):
		writeErr(w, http.StatusBadRequest, "a skill name is lowercase letters, digits and dashes (up to 64)")
		return
	case desc == "":
		writeErr(w, http.StatusBadRequest, "describe when the agent should use this skill")
		return
	case len(desc) > 1024:
		writeErr(w, http.StatusBadRequest, "keep the description under 1024 characters")
		return
	case strings.TrimSpace(in.Body) == "":
		writeErr(w, http.StatusBadRequest, "write the skill's instructions")
		return
	case len(in.Body) > libMaxFile:
		writeErr(w, http.StatusBadRequest, "the instructions are over 512 KB")
		return
	}
	libMu.Lock()
	defer libMu.Unlock()
	if err := os.MkdirAll(sc.dir, 0o755); err != nil {
		s.fail(w, err, "save skill")
		return
	}
	file := filepath.Join(sc.dir, name, "SKILL.md")
	cur, readErr := os.ReadFile(file)
	switch {
	case creating && readErr == nil:
		writeErr(w, http.StatusConflict, "there is already a skill called "+name)
		return
	case creating:
		if _, err := os.Lstat(filepath.Join(sc.dir, name)); err == nil {
			writeErr(w, http.StatusConflict, "there is already a folder called "+name+" in "+sc.dir)
			return
		}
	case readErr != nil:
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if to != "" && to != name {
		if _, err := os.Lstat(filepath.Join(sc.dir, to)); err == nil {
			writeErr(w, http.StatusConflict, "there is already a skill called "+to)
			return
		}
		if err := os.Rename(filepath.Join(sc.dir, name), filepath.Join(sc.dir, to)); err != nil {
			s.fail(w, err, "rename skill")
			return
		}
		s.renameSkillState(r.Context(), sc, name, to)
		name, file = to, filepath.Join(sc.dir, to, "SKILL.md")
	}
	if err := os.MkdirAll(filepath.Join(sc.dir, name), 0o755); err != nil {
		s.fail(w, err, "save skill")
		return
	}
	front, _ := splitFrontmatter(string(cur))
	text := "---\n" + setFrontmatter(front, name, desc) + "---\n\n" + strings.TrimLeft(strings.TrimRight(in.Body, " \t\n")+"\n", "\n")
	if err := writeFileAtomic(file, []byte(text), 0o644); err != nil {
		s.fail(w, err, "save skill")
		return
	}
	s.libraryChanged(sc)
	sk, err := readLibSkill(sc.dir, name, true)
	if err != nil {
		s.fail(w, err, "save skill")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	writeJSON(w, status, sk)
}

// libraryChanged: the agents here see the change now, the machines get it.
// Call with libMu held.
func (s *Server) libraryChanged(sc *libScope) {
	s.arrangeLocal(sc)
	forgetSkills()
	s.kickSkillSync(sc, false)
}

func writeFileAtomic(file string, data []byte, mode fs.FileMode) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// handleLibraryDelete moves the skill to the library's trash
// (~/.agents/.pocket-ai-trash/skills); the machines drop it at the next sync.
func (s *Server) handleLibraryDelete(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name := chi.URLParam(r, "name")
	if !validSkill(name) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	libMu.Lock()
	defer libMu.Unlock()
	if fi, err := os.Lstat(filepath.Join(sc.dir, name, "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	dst, err := trashTo(sc.trash(), filepath.Join(sc.dir, name))
	if err != nil {
		s.fail(w, err, "delete skill")
		return
	}
	s.libraryChanged(sc)
	writeJSON(w, http.StatusOK, map[string]string{"trash": dst})
}

// trashTo moves a folder into a trash folder under a dated name.
func trashTo(trash, folder string) (string, error) {
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", filepath.Base(folder), time.Now().Format("20060102-150405.000")))
	return dst, moveDir(folder, dst)
}

// moveDir renames, or copies and removes when the two are on different disks.
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copySkillTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func (s *Server) handleLibraryDuplicate(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	name := chi.URLParam(r, "name")
	if err := decode(r, &in); err != nil || !validSkill(in.Name) || !validSkill(name) {
		writeErr(w, http.StatusBadRequest, "a skill name is lowercase letters, digits and dashes (up to 64)")
		return
	}
	libMu.Lock()
	defer libMu.Unlock()
	src, dst := filepath.Join(sc.dir, name), filepath.Join(sc.dir, in.Name)
	data, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		writeErr(w, http.StatusConflict, "there is already a skill called "+in.Name)
		return
	}
	if err := copySkillTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		s.fail(w, err, "duplicate skill")
		return
	}
	front, rest := splitFrontmatter(string(data))
	text := "---\n" + setFrontmatter(front, in.Name, frontmatterValue(front, "description")) + "---\n" + rest
	if err := writeFileAtomic(filepath.Join(dst, "SKILL.md"), []byte(text), 0o644); err != nil {
		s.fail(w, err, "duplicate skill")
		return
	}
	s.libraryChanged(sc)
	sk, err := readLibSkill(sc.dir, in.Name, true)
	if err != nil {
		s.fail(w, err, "duplicate skill")
		return
	}
	writeJSON(w, http.StatusCreated, sk)
}

// ---------- the other files of a skill ----------

// skillFilePath checks a path inside a skill ("references/api.md") and
// returns where it is on disk. It never leaves the skill's folder.
func skillFilePath(sc *libScope, name, rel string) (string, error) {
	if !validSkill(name) {
		return "", badRequest("not found")
	}
	rel = strings.TrimSpace(rel)
	clean := path.Clean("/" + rel)[1:]
	if rel == "" || clean != rel || len(rel) > 240 || strings.ContainsAny(rel, "\\\x00\n\r\t") {
		return "", badRequest("that is not a file name inside the skill")
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" || seg == ".." || seg == "." || strings.HasSuffix(seg, ".tmp") && strings.HasPrefix(seg, ".") {
			return "", badRequest("that is not a file name inside the skill")
		}
	}
	folder := filepath.Join(sc.dir, name)
	full := filepath.Join(folder, filepath.FromSlash(rel))
	// no folder on the way may be a link out of the skill
	root, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", badRequest("not found")
	}
	for dir := filepath.Dir(full); ; dir = filepath.Dir(dir) {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
				return "", badRequest("that is not a file name inside the skill")
			}
			break
		}
		if dir == folder || len(dir) <= len(folder) {
			break
		}
	}
	return full, nil
}

func (s *Server) handleLibraryFile(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	full, err := skillFilePath(sc, chi.URLParam(r, "name"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "skill file")
		return
	}
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(full)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, full)
		return
	}
	out := map[string]any{"path": r.URL.Query().Get("path"), "size": fi.Size(), "text": false, "content": ""}
	if fi.Size() <= libMaxFile {
		if data, err := os.ReadFile(full); err == nil && utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
			out["text"], out["content"] = true, string(data)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLibraryFileSave writes a text file of a skill, or moves one
// ("rename": its new path).
func (s *Server) handleLibraryFileSave(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name, rel := chi.URLParam(r, "name"), r.URL.Query().Get("path")
	var in struct {
		Content *string `json:"content"`
		Rename  string  `json:"rename"`
		Create  bool    `json:"create"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	full, err := skillFilePath(sc, name, rel)
	if err != nil {
		s.fail(w, err, "skill file")
		return
	}
	if rel == "SKILL.md" || strings.EqualFold(in.Rename, "SKILL.md") {
		writeErr(w, http.StatusBadRequest, "SKILL.md is the skill itself: change it in the skill's own fields")
		return
	}
	libMu.Lock()
	defer libMu.Unlock()
	if _, err := os.Stat(filepath.Join(sc.dir, name, "SKILL.md")); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	fi, statErr := os.Lstat(full)
	if statErr == nil && !fi.Mode().IsRegular() {
		writeErr(w, http.StatusConflict, "there is a folder with that name")
		return
	}
	if in.Create && statErr == nil {
		writeErr(w, http.StatusConflict, "there is already a file with that name")
		return
	}
	if in.Content != nil {
		if len(*in.Content) > libMaxFile {
			writeErr(w, http.StatusBadRequest, "the file is over 512 KB")
			return
		}
		mode := fs.FileMode(0o644)
		if statErr == nil {
			mode = fi.Mode().Perm()
		} else if strings.HasSuffix(rel, ".sh") || strings.HasSuffix(rel, ".py") {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			s.fail(w, err, "skill file")
			return
		}
		if err := writeFileAtomic(full, []byte(*in.Content), mode); err != nil {
			s.fail(w, err, "skill file")
			return
		}
	} else if statErr != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if in.Rename != "" && in.Rename != rel {
		to, err := skillFilePath(sc, name, in.Rename)
		if err != nil {
			s.fail(w, err, "skill file")
			return
		}
		if _, err := os.Lstat(to); err == nil {
			writeErr(w, http.StatusConflict, "there is already a file with that name")
			return
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			s.fail(w, err, "skill file")
			return
		}
		if err := os.Rename(full, to); err != nil {
			s.fail(w, err, "skill file")
			return
		}
		pruneEmptyDirs(filepath.Dir(full), filepath.Join(sc.dir, name))
	}
	s.libraryChanged(sc)
	sk, err := readLibSkill(sc.dir, name, true)
	if err != nil {
		s.fail(w, err, "skill file")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) handleLibraryFileDelete(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name, rel := chi.URLParam(r, "name"), r.URL.Query().Get("path")
	full, err := skillFilePath(sc, name, rel)
	if err != nil {
		s.fail(w, err, "skill file")
		return
	}
	if rel == "SKILL.md" {
		writeErr(w, http.StatusBadRequest, "SKILL.md is the skill itself: delete the skill instead")
		return
	}
	libMu.Lock()
	defer libMu.Unlock()
	if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := os.Remove(full); err != nil {
		s.fail(w, err, "skill file")
		return
	}
	pruneEmptyDirs(filepath.Dir(full), filepath.Join(sc.dir, name))
	s.libraryChanged(sc)
	sk, err := readLibSkill(sc.dir, name, true)
	if err != nil {
		s.fail(w, err, "skill file")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

// pruneEmptyDirs removes folders left empty, up to (not including) stop.
func pruneEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ---------- in and out as a file ----------

// handleLibraryDownload sends a skill as a zip (name/SKILL.md …), the form
// handleLibraryImport and claude.ai take.
func (s *Server) handleLibraryDownload(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name := chi.URLParam(r, "name")
	folder := filepath.Join(sc.dir, name)
	if !validSkill(name) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := os.Stat(filepath.Join(folder, "SKILL.md")); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := walkSkill(folder, func(rel string, info fs.FileInfo) error {
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = name+"/"+rel, zip.Deflate
		f, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		s.fail(w, err, "download skill")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name+".zip"))
	_, _ = w.Write(buf.Bytes())
}

// handleLibraryImport adds skills from an uploaded zip (.zip or .skill: one
// skill at its top, or a folder per skill) or from a single SKILL.md.
// replace=1 puts a skill of the same name in the trash first.
func (s *Server) handleLibraryImport(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, libMaxSkill+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "the file is too large (20 MB at most)")
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "choose a .zip, .skill or SKILL.md file")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the file")
		return
	}
	tmp, err := os.MkdirTemp("", "pocket-skill-import-")
	if err != nil {
		s.fail(w, err, "import skill")
		return
	}
	defer os.RemoveAll(tmp)
	base := strings.ToLower(fh.Filename)
	if strings.HasSuffix(base, ".md") {
		front, _ := splitFrontmatter(string(data))
		name := frontmatterValue(front, "name")
		if !validSkill(name) {
			writeErr(w, http.StatusBadRequest, "that SKILL.md has no name: line (lowercase letters, digits and dashes)")
			return
		}
		_ = os.MkdirAll(filepath.Join(tmp, name), 0o755)
		_ = os.WriteFile(filepath.Join(tmp, name, "SKILL.md"), data, 0o644)
	} else if err := unzipSkills(data, tmp); err != nil {
		s.fail(w, err, "import skill")
		return
	}
	found := findSkillFolders(tmp, strings.TrimSuffix(strings.TrimSuffix(base, ".zip"), ".skill"))
	if len(found) == 0 {
		writeErr(w, http.StatusBadRequest, "no skill in that file: a skill is a folder with a SKILL.md")
		return
	}
	replace := r.URL.Query().Get("replace") == "1"
	libMu.Lock()
	defer libMu.Unlock()
	if err := os.MkdirAll(sc.dir, 0o755); err != nil {
		s.fail(w, err, "import skill")
		return
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	if !replace {
		var have []string
		for _, name := range names {
			if _, err := os.Lstat(filepath.Join(sc.dir, name)); err == nil {
				have = append(have, name)
			}
		}
		if len(have) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "already in your library: " + strings.Join(have, ", "), "exists": have})
			return
		}
	}
	for _, name := range names {
		dst := filepath.Join(sc.dir, name)
		if _, err := os.Lstat(dst); err == nil {
			if _, err := trashTo(sc.trash(), dst); err != nil {
				s.fail(w, err, "import skill")
				return
			}
		}
		if err := copySkillTree(found[name], dst); err != nil {
			_ = os.RemoveAll(dst)
			s.fail(w, err, "import skill")
			return
		}
	}
	s.libraryChanged(sc)
	writeJSON(w, http.StatusCreated, map[string]any{"added": names})
}

// unzipSkills unpacks a zip into dir: regular files only, nothing outside.
func unzipSkills(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return badRequest("that is not a zip file")
	}
	var total int64
	n := 0
	for _, zf := range zr.File {
		name := path.Clean("/" + strings.ReplaceAll(zf.Name, "\\", "/"))[1:]
		if zf.FileInfo().IsDir() || name == "" || !zf.Mode().IsRegular() || skipInSkill(name) {
			continue
		}
		if n++; n > libMaxFiles {
			return badRequest("too many files in that zip")
		}
		if total += int64(zf.UncompressedSize64); total > libMaxSkill {
			return badRequest("that zip unpacks to over 20 MB")
		}
		rc, err := zf.Open()
		if err != nil {
			return badRequest("could not read the zip")
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			rc.Close()
			return err
		}
		body, err := io.ReadAll(io.LimitReader(rc, libMaxSkill+1))
		rc.Close()
		if err != nil || int64(len(body)) > libMaxSkill {
			return badRequest("could not read the zip")
		}
		mode := fs.FileMode(0o644)
		if zf.Mode()&0o100 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(out, body, mode); err != nil {
			return err
		}
	}
	return nil
}

// skipInSkill: files that are never part of a skill (Finder's, git's).
func skipInSkill(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" || seg == ".DS_Store" || seg == "__MACOSX" || strings.HasPrefix(seg, "._") {
			return true
		}
	}
	return false
}

// findSkillFolders finds the skills in an unpacked upload: the top itself
// (named after its frontmatter, else the file), or folders up to two deep.
func findSkillFolders(dir, fallback string) map[string]string {
	out := map[string]string{}
	nameOf := func(folder, fb string) string {
		data, _ := os.ReadFile(filepath.Join(folder, "SKILL.md"))
		front, _ := splitFrontmatter(string(data))
		if n := frontmatterValue(front, "name"); validSkill(n) {
			return n
		}
		if validSkill(fb) {
			return fb
		}
		return ""
	}
	has := func(folder string) bool {
		fi, err := os.Lstat(filepath.Join(folder, "SKILL.md"))
		return err == nil && fi.Mode().IsRegular()
	}
	if has(dir) {
		if n := nameOf(dir, fallback); n != "" {
			out[n] = dir
		}
		return out
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if has(sub) {
			if n := nameOf(sub, e.Name()); n != "" {
				out[n] = sub
			}
			continue
		}
		inner, _ := os.ReadDir(sub)
		for _, e2 := range inner {
			if deep := filepath.Join(sub, e2.Name()); e2.IsDir() && has(deep) {
				if n := nameOf(deep, e2.Name()); n != "" {
					out[n] = deep
				}
			}
		}
	}
	return out
}

// untarSkill unpacks a copy taken from a machine into dir, with the same
// limits and the same files left out as everywhere else.
func untarSkill(data []byte, dir string) error {
	tr := tar.NewReader(bytes.NewReader(data))
	var total int64
	n := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("the copy did not arrive whole")
		}
		name := path.Clean("/" + hdr.Name)[1:]
		if hdr.Typeflag != tar.TypeReg || name == "" || skipInSkill(name) {
			continue
		}
		if n++; n > libMaxFiles {
			return errors.New("too many files in that skill")
		}
		if total += hdr.Size; total > libMaxSkill {
			return errors.New("that skill is over 20 MB")
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(tr, libMaxSkill+1))
		if err != nil {
			return errors.New("the copy did not arrive whole")
		}
		mode := fs.FileMode(0o644)
		if hdr.Mode&0o100 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(out, body, mode); err != nil {
			return err
		}
	}
}

// ---------- this server's own folders ----------

// arrangeLocal keeps this server's agents on the library: real skill folders
// in Claude Code's or Codex's own skills folder move in (the newer one wins
// when the library has a different one of that name; the other goes to the
// trash), and every library skill has a link in Claude Code's folder. Call
// with libMu held. It reports whether the library changed.
func (s *Server) arrangeLocal(sc *libScope) (changed bool) {
	if err := os.MkdirAll(sc.dir, 0o755); err != nil {
		s.log.Warn("skills library", "dir", sc.dir, "err", err)
		return false
	}
	if !agent.Confined(sc.dir) {
		return false
	}
	libReal, err := filepath.EvalSymlinks(sc.dir)
	if err != nil {
		return false
	}
	var claudeDirs []string
	seen := map[string]bool{libReal: true}
	adopt := func(home string, claude bool) {
		d := filepath.Join(home, "skills")
		if claude {
			if _, err := os.Stat(home); err != nil {
				return // not a login folder (yet)
			}
			_ = os.MkdirAll(d, 0o755)
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil || seen[real] || !agent.Confined(d) {
			return
		}
		seen[real] = true
		if claude {
			claudeDirs = append(claudeDirs, real)
		}
		ents, _ := os.ReadDir(real)
		for _, e := range ents {
			name := e.Name()
			if !validSkill(name) {
				continue
			}
			p, lib := filepath.Join(real, name), filepath.Join(sc.dir, name)
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(p)
				switch {
				case err != nil: // a link to nowhere: ours, of a skill that is gone
					if to, _ := os.Readlink(p); strings.HasPrefix(to, sc.dir+string(filepath.Separator)) {
						_ = os.Remove(p)
					}
				case !claude && target == filepath.Join(libReal, name):
					_ = os.Remove(p) // Codex reads the library itself: a link shows the skill twice
				}
				continue
			}
			if sf, err := os.Lstat(filepath.Join(p, "SKILL.md")); err != nil || !sf.Mode().IsRegular() || !fi.IsDir() {
				continue
			}
			if _, err := os.Lstat(lib); err != nil {
				if err := moveDir(p, lib); err != nil {
					s.log.Warn("skills library: move in", "skill", name, "from", real, "err", err)
					continue
				}
				changed = true
				s.log.Info("skill moved into the library", "skill", name, "from", real)
			} else {
				a, _ := skillSum(p)
				b, _ := skillSum(lib)
				switch {
				case a == b:
					_ = os.RemoveAll(p)
				case newestIn(p).After(newestIn(lib)):
					if _, err := trashTo(sc.trash(), lib); err != nil {
						continue
					}
					if err := moveDir(p, lib); err != nil {
						continue
					}
					changed = true
					s.log.Info("skill in the library replaced by a newer copy", "skill", name, "from", real)
				default:
					if _, err := trashTo(filepath.Join(home, ".pocket-ai-trash", "skills"), p); err != nil {
						continue
					}
					s.log.Info("older copy of a library skill put in the trash", "skill", name, "from", real)
				}
			}
			if claude {
				_ = os.Symlink(lib, p)
			}
		}
	}
	for _, h := range sc.claude {
		adopt(h, true)
	}
	for _, h := range sc.codex {
		adopt(h, false)
	}
	ents, _ := os.ReadDir(sc.dir)
	for _, d := range claudeDirs {
		for _, e := range ents {
			if !validSkill(e.Name()) {
				continue
			}
			if _, err := os.Stat(filepath.Join(sc.dir, e.Name(), "SKILL.md")); err != nil {
				continue
			}
			if _, err := os.Lstat(filepath.Join(d, e.Name())); errors.Is(err, fs.ErrNotExist) {
				_ = os.Symlink(filepath.Join(sc.dir, e.Name()), filepath.Join(d, e.Name()))
			}
		}
	}
	return changed
}

// newestIn is the latest change time of a skill's files.
func newestIn(folder string) time.Time {
	var t time.Time
	_ = walkSkill(folder, func(_ string, info fs.FileInfo) error {
		if info.ModTime().After(t) {
			t = info.ModTime()
		}
		return nil
	})
	return t
}
