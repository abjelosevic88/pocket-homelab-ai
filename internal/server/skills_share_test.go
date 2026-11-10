package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkSkills(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, ".claude", "skills")
	mk := func(dir, name, body string) {
		_ = os.MkdirAll(filepath.Join(dir, name), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0o644)
	}
	mk(shared, "both", "shared copy")

	// no folder yet: just the link (the shared folder made if needed)
	a := filepath.Join(root, ".claude-a", "skills")
	_ = os.MkdirAll(filepath.Dir(a), 0o755)
	if _, _, err := linkSkills(shared, a); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(a); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("a is not a link")
	}

	// a folder of its own: its new skill moves over, the clash goes to its trash
	b := filepath.Join(root, ".claude-b", "skills")
	mk(b, "only-b", "b")
	mk(b, "both", "b's copy")
	moved, trashed, err := linkSkills(shared, b)
	if err != nil || moved != 1 || trashed != 1 {
		t.Fatalf("moved %d trashed %d err %v", moved, trashed, err)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "only-b", "SKILL.md")); string(got) != "b" {
		t.Fatalf("only-b through the link: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(shared, "both", "SKILL.md")); string(got) != "shared copy" {
		t.Fatalf("shared copy changed: %q", got)
	}
	if m, _ := filepath.Glob(filepath.Join(root, ".claude-b", ".pocket-ai-trash", "skills", "both-*")); len(m) != 1 {
		t.Fatalf("trash: %v", m)
	}

	// already linked: nothing to do
	if moved, trashed, err := linkSkills(shared, b); err != nil || moved+trashed != 0 {
		t.Fatalf("second run: %d %d %v", moved, trashed, err)
	}
}
