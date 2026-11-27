package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Helpers for skill folders (a folder with a SKILL.md): names, frontmatter,
// copying. The library itself is in skills_library.go.

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// skillsReserved: folders the CLIs manage themselves.
var skillsReserved = map[string]bool{"synced": true}

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
