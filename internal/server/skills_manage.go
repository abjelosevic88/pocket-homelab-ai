package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// Your own skills: folders with a SKILL.md in the login's skills folder
// (~/.claude/skills, ~/.codex/skills). Plugin, project, synced and system
// skills are listed by the CLI (skills_api.go) and are not edited here.

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// skillsReserved: folders the CLIs manage themselves.
var skillsReserved = map[string]bool{"synced": true}

type ownSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Files       int    `json:"files"` // besides SKILL.md (references, scripts)
	Sum         string `json:"sum"`   // cksum of SKILL.md (skills_hosts.go)
	Path        string `json:"path"`
}

func (s *Server) skillsManageRoutes(r chi.Router) {
	r.Get("/skills/own", s.handleListOwnSkills)
	r.Post("/skills/own", s.handleSaveOwnSkill)
	r.Get("/skills/own/{name}", s.handleGetOwnSkill)
	r.Put("/skills/own/{name}", s.handleSaveOwnSkill)
	r.Delete("/skills/own/{name}", s.handleDeleteOwnSkill)
	r.Post("/skills/own/{name}/copy", s.handleCopyOwnSkill)
	r.Post("/skills/own/{name}/to-host", s.handleCopySkillToHost)
	r.Get("/skills/hosts", s.handleSkillsOnHosts)
}

// ownSkillsDir is the skills folder of the login the request names
// (provider + account), if the person may change it.
func (s *Server) ownSkillsDir(r *http.Request) (string, error) {
	return s.skillsDirFor(r, r.URL.Query().Get("provider"), queryInt64(r, "account"))
}

func (s *Server) skillsDirFor(r *http.Request, provider string, acct int64) (string, error) {
	ctx := r.Context()
	u := userFrom(ctx)
	if !u.MayRun() {
		return "", errForbidden("an admin has not allowed you to run agents on this server")
	}
	if provider != "claude" && provider != "codex" {
		return "", badRequest("provider must be claude or codex")
	}
	if err := s.checkAccount(ctx, u, provider, acct); err != nil {
		return "", err
	}
	home, _ := s.accountHome(ctx, provider, acct)
	dir := filepath.Join(home, "skills")
	if s.jailed(ctx, u.ID) && !agent.Confined(dir) {
		return "", errForbidden("these skills are shared from the server; ask its owner to change them")
	}
	return dir, nil
}

func (s *Server) handleListOwnSkills(w http.ResponseWriter, r *http.Request) {
	dir, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "skills")
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.fail(w, err, "skills")
		return
	}
	out := []ownSkill{}
	for _, e := range ents {
		if !skillName.MatchString(e.Name()) || skillsReserved[e.Name()] {
			continue
		}
		if sk, err := readOwnSkill(dir, e.Name(), false); err == nil {
			out = append(out, *sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "skills": out})
}

func (s *Server) handleGetOwnSkill(w http.ResponseWriter, r *http.Request) {
	dir, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "skill")
		return
	}
	name := chi.URLParam(r, "name")
	if !skillName.MatchString(name) || skillsReserved[name] {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	sk, err := readOwnSkill(dir, name, true)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

// handleSaveOwnSkill creates a skill (POST) or changes one (PUT). Frontmatter
// keys other than name and description are kept as they are.
func (s *Server) handleSaveOwnSkill(w http.ResponseWriter, r *http.Request) {
	dir, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "save skill")
		return
	}
	var in struct {
		Name        string `json:"name"`
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
	switch {
	case !skillName.MatchString(name) || skillsReserved[name]:
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
	case len(in.Body) > 256<<10:
		writeErr(w, http.StatusBadRequest, "the instructions are over 256 KB")
		return
	}
	file := filepath.Join(dir, name, "SKILL.md")
	cur, readErr := os.ReadFile(file)
	switch {
	case creating && readErr == nil:
		writeErr(w, http.StatusConflict, "there is already a skill called "+name)
		return
	case creating:
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			writeErr(w, http.StatusConflict, "there is already a folder called "+name+" in "+dir)
			return
		}
	case readErr != nil:
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		s.fail(w, err, "save skill")
		return
	}
	front, _ := splitFrontmatter(string(cur))
	text := "---\n" + setFrontmatter(front, name, desc) + "---\n\n" + strings.TrimLeft(strings.TrimRight(in.Body, " \t\n")+"\n", "\n")
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		s.fail(w, err, "save skill")
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		s.fail(w, err, "save skill")
		return
	}
	forgetSkills()
	sk, err := readOwnSkill(dir, name, true)
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

// handleDeleteOwnSkill moves the skill's folder to the login's trash
// (<home>/.pocket-ai-trash/skills) rather than deleting it.
func (s *Server) handleDeleteOwnSkill(w http.ResponseWriter, r *http.Request) {
	dir, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "delete skill")
		return
	}
	name := chi.URLParam(r, "name")
	src := filepath.Join(dir, name)
	if !skillName.MatchString(name) || skillsReserved[name] {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if fi, err := os.Lstat(filepath.Join(src, "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	dst, err := trashSkill(dir, name)
	if err != nil {
		s.fail(w, err, "delete skill")
		return
	}
	forgetSkills()
	writeJSON(w, http.StatusOK, map[string]string{"trash": dst})
}

// trashSkill moves a skill folder to <home>/.pocket-ai-trash/skills.
func trashSkill(dir, name string) (string, error) {
	trash := filepath.Join(filepath.Dir(dir), ".pocket-ai-trash", "skills")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", name, time.Now().Format("20060102-150405.000")))
	return dst, os.Rename(filepath.Join(dir, name), dst)
}

// handleCopyOwnSkill copies a skill folder (SKILL.md and its other files) to
// another login, e.g. from Claude Code to Codex. Move removes the original
// afterwards; replace puts a skill already there in the trash first.
func (s *Server) handleCopyOwnSkill(w http.ResponseWriter, r *http.Request) {
	from, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	var in struct {
		To        string `json:"to"`
		ToAccount int64  `json:"to_account"`
		Move      bool   `json:"move"`
		Replace   bool   `json:"replace"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	to, err := s.skillsDirFor(r, in.To, in.ToAccount)
	if err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	name := chi.URLParam(r, "name")
	if !skillName.MatchString(name) || skillsReserved[name] {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if fi, err := os.Lstat(filepath.Join(from, name, "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	same := func(a, b string) bool {
		ra, err1 := filepath.EvalSymlinks(a)
		rb, err2 := filepath.EvalSymlinks(b)
		return err1 == nil && err2 == nil && ra == rb
	}
	if same(from, to) {
		writeErr(w, http.StatusBadRequest, "that is the same skills folder")
		return
	}
	if _, err := os.Lstat(filepath.Join(to, name)); err == nil {
		if !in.Replace {
			writeErr(w, http.StatusConflict, "there is already a skill called "+name+" there")
			return
		}
		if _, err := trashSkill(to, name); err != nil {
			s.fail(w, err, "copy skill")
			return
		}
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	tmp := filepath.Join(to, "."+name+".copying")
	_ = os.RemoveAll(tmp)
	if err := copySkillTree(filepath.Join(from, name), tmp); err != nil {
		_ = os.RemoveAll(tmp)
		s.fail(w, err, "copy skill")
		return
	}
	if err := os.Rename(tmp, filepath.Join(to, name)); err != nil {
		_ = os.RemoveAll(tmp)
		s.fail(w, err, "copy skill")
		return
	}
	if in.Move {
		if _, err := trashSkill(from, name); err != nil {
			s.fail(w, err, "move skill")
			return
		}
	}
	forgetSkills()
	sk, err := readOwnSkill(to, name, false)
	if err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

// copySkillTree copies folders and regular files; links are left out, so a
// copy never reaches outside the skill.
func copySkillTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, info.Mode().Perm())
	})
}

// forgetSkills drops the cached CLI lists so the composer sees changes now.
func forgetSkills() {
	skillsMu.Lock()
	skillsCache = map[string]skillsEntry{}
	skillsMu.Unlock()
}

func readOwnSkill(dir, name string, body bool) (*ownSkill, error) {
	folder := filepath.Join(dir, name)
	data, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	front, rest := splitFrontmatter(string(data))
	sk := &ownSkill{Name: name, Description: frontmatterValue(front, "description"), Path: folder, Sum: cksum(data)}
	if body {
		sk.Body = strings.TrimLeft(rest, "\n")
	}
	_ = filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != filepath.Join(folder, "SKILL.md") {
			sk.Files++
		}
		return nil
	})
	return sk, nil
}

// splitFrontmatter returns the YAML between the leading --- lines (with its
// trailing newline) and the text after it.
func splitFrontmatter(s string) (front, rest string) {
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", s
	}
	lines := strings.SplitAfter(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") == "---" {
			return strings.Join(lines[1:i], ""), strings.Join(lines[i+1:], "")
		}
	}
	return "", s
}

// frontmatterValue reads a top-level key: plain, quoted or a folded/literal
// block (> or |) on the indented lines that follow.
func frontmatterValue(front, key string) string {
	lines := strings.Split(front, "\n")
	for i, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok || k != key {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, ">") || strings.HasPrefix(v, "|") {
			var parts []string
			for _, c := range lines[i+1:] {
				if c != "" && c[0] != ' ' && c[0] != '\t' {
					break
				}
				if t := strings.TrimSpace(c); t != "" {
					parts = append(parts, t)
				}
			}
			return strings.Join(parts, " ")
		}
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			var out string
			if json.Unmarshal([]byte(v), &out) == nil {
				return out
			}
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		return v
	}
	return ""
}

// setFrontmatter writes name and description first and keeps every other key.
func setFrontmatter(front, name, desc string) string {
	q, _ := json.Marshal(desc) // a JSON string is a valid YAML double-quoted one
	out := []string{"name: " + name, "description: " + string(q)}
	skipping := false
	for _, l := range strings.Split(strings.TrimRight(front, "\n"), "\n") {
		if skipping && l != "" && (l[0] == ' ' || l[0] == '\t') {
			continue // the old value's continuation lines
		}
		skipping = false
		k, _, _ := strings.Cut(l, ":")
		if k == "name" || k == "description" {
			skipping = true
			continue
		}
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n") + "\n"
}
