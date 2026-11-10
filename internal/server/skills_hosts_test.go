package server

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCksumMatchesPOSIX(t *testing.T) {
	// what cksum(1) prints for these inputs
	for in, want := range map[string]string{"": "4294967295 0", "hello\n": "3015617425 6"} {
		if got := cksum([]byte(in)); got != want {
			t.Errorf("cksum(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTarSkillLeavesLinksOut(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "refs"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "refs", "a.md"), []byte("a"), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(dir, "link"))
	data, err := tarSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = true
	}
	for _, n := range []string{"SKILL.md", "refs", "refs/a.md"} {
		if !got[n] {
			t.Errorf("missing %s in %v", n, got)
		}
	}
	if got["link"] {
		t.Error("the link was packed")
	}
}
