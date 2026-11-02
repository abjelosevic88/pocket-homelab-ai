package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnSkillsAddEditRemove(t *testing.T) {
	s, h, root := testServer(t)
	owner, _ := person(t, s, h, "owner@example.com", "admin", true)
	const q = "?provider=claude"
	dir := filepath.Join(s.cfg.ClaudeHome, "skills")

	var list struct {
		Skills []ownSkill `json:"skills"`
	}
	owner.want("GET", "/api/v1/skills/own"+q, nil, 200, &list)
	if len(list.Skills) != 0 {
		t.Fatalf("no skills folder yet, got %#v", list.Skills)
	}
	for _, bad := range []map[string]string{
		{"name": "Bad Name", "description": "d", "body": "b"},
		{"name": "synced", "description": "d", "body": "b"},
		{"name": "ok", "description": " ", "body": "b"},
		{"name": "ok", "description": "d", "body": "\n"},
	} {
		owner.want("POST", "/api/v1/skills/own"+q, bad, 400, nil)
	}
	var sk ownSkill
	owner.want("POST", "/api/v1/skills/own"+q, map[string]string{"name": "deploy-app", "description": "Deploy: the app\nnow", "body": "# Deploy\n\nRun it.\n"}, 201, &sk)
	if sk.Description != "Deploy: the app now" || sk.Body != "# Deploy\n\nRun it.\n" {
		t.Fatalf("bad skill: %#v", sk)
	}
	owner.want("POST", "/api/v1/skills/own"+q, map[string]string{"name": "deploy-app", "description": "d", "body": "b"}, 409, nil)

	// extra frontmatter keys and other files survive an edit
	file := filepath.Join(dir, "deploy-app", "SKILL.md")
	_ = os.WriteFile(file, []byte("---\nname: deploy-app\ndescription: >\n  Old folded\n  text\nallowed-tools: Bash\n---\n\nOld\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "deploy-app", "notes.md"), []byte("x"), 0o644)
	owner.want("GET", "/api/v1/skills/own/deploy-app"+q, nil, 200, &sk)
	if sk.Description != "Old folded text" || sk.Files != 1 {
		t.Fatalf("bad read: %#v", sk)
	}
	owner.want("PUT", "/api/v1/skills/own/deploy-app"+q, map[string]string{"description": "New", "body": "New body"}, 200, &sk)
	data, _ := os.ReadFile(file)
	if got := string(data); got != "---\nname: deploy-app\ndescription: \"New\"\nallowed-tools: Bash\n---\n\nNew body\n" {
		t.Fatalf("bad file:\n%s", got)
	}
	owner.want("PUT", "/api/v1/skills/own/missing"+q, map[string]string{"description": "d", "body": "b"}, 404, nil)

	// synced and dot folders are the CLI's own
	_ = os.MkdirAll(filepath.Join(dir, "synced", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "synced", "SKILL.md"), []byte("x"), 0o644)
	owner.want("GET", "/api/v1/skills/own"+q, nil, 200, &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "deploy-app" {
		t.Fatalf("bad list: %#v", list.Skills)
	}
	owner.want("DELETE", "/api/v1/skills/own/synced"+q, nil, 404, nil)

	var del struct{ Trash string }
	owner.want("DELETE", "/api/v1/skills/own/deploy-app"+q, nil, 200, &del)
	if _, err := os.Stat(filepath.Join(del.Trash, "SKILL.md")); err != nil || !strings.HasPrefix(del.Trash, root) {
		t.Fatalf("not in the trash: %q %v", del.Trash, err)
	}
	owner.want("DELETE", "/api/v1/skills/own/deploy-app"+q, nil, 404, nil)

	// someone who may not run agents, or a person on the server's login
	viewer, _ := person(t, s, h, "viewer@example.com", "user", false)
	viewer.want("GET", "/api/v1/skills/own"+q, nil, 403, nil)
	other, _ := person(t, s, h, "other@example.com", "user", true)
	other.want("POST", "/api/v1/skills/own"+q, map[string]string{"name": "x", "description": "d", "body": "b"}, 400, nil)
}

func TestOwnSkillsCopyBetweenAgents(t *testing.T) {
	s, h, _ := testServer(t)
	owner, _ := person(t, s, h, "owner@example.com", "admin", true)
	owner.want("POST", "/api/v1/skills/own?provider=claude", map[string]string{"name": "review", "description": "Review code", "body": "Look closely."}, 201, nil)
	src := filepath.Join(s.cfg.ClaudeHome, "skills", "review")
	_ = os.MkdirAll(filepath.Join(src, "refs"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "refs", "style.md"), []byte("style"), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(src, "leak"))

	copyTo := func(from, name string, body map[string]any, code int) {
		owner.want("POST", "/api/v1/skills/own/"+name+"/copy?provider="+from, body, code, nil)
	}
	copyTo("claude", "review", map[string]any{"to": "claude"}, 400) // same folder
	copyTo("claude", "missing", map[string]any{"to": "codex"}, 404)
	copyTo("claude", "review", map[string]any{"to": "codex"}, 200)
	dst := filepath.Join(s.cfg.CodexHome, "skills", "review")
	if b, err := os.ReadFile(filepath.Join(dst, "refs", "style.md")); err != nil || string(b) != "style" {
		t.Fatalf("other files not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "leak")); err == nil {
		t.Fatal("a link was copied")
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		t.Fatal("copy removed the original")
	}
	copyTo("claude", "review", map[string]any{"to": "codex"}, 409)

	owner.want("PUT", "/api/v1/skills/own/review?provider=claude", map[string]string{"description": "Newer", "body": "b"}, 200, nil)
	copyTo("claude", "review", map[string]any{"to": "codex", "replace": true, "move": true}, 200)
	var sk ownSkill
	owner.want("GET", "/api/v1/skills/own/review?provider=codex", nil, 200, &sk)
	if sk.Description != "Newer" {
		t.Fatalf("not replaced: %#v", sk)
	}
	owner.want("GET", "/api/v1/skills/own/review?provider=claude", nil, 404, nil)
}
