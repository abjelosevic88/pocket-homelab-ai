package server

import (
	"os"
	"path/filepath"
	"testing"
)

// The guards of the file changes, on throwaway folders: never the root, the
// home folder or a person's own top folders; names never carry a path.
func TestFileOpGuards(t *testing.T) {
	home := t.TempDir()
	p := &filePlace{home: home}
	for _, bad := range []string{"/", home, home + "/"} {
		if _, err := p.localOp("delete", bad, ""); err == nil {
			t.Fatalf("delete %q was allowed", bad)
		}
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal("home is gone")
	}
	for _, name := range []string{"", ".", "..", "a/b", "../x", "x\x00"} {
		if _, err := checkEntryName(name); err == nil {
			t.Fatalf("name %q was allowed", name)
		}
	}
	// a normal round: folder, file, rename, no clobber, delete
	if _, err := p.localOp("mkdir", home, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.localOp("newfile", home, "n.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.localOp("rename", filepath.Join(home, "n.txt"), "a"); err == nil {
		t.Fatal("rename over an existing entry was allowed")
	}
	if _, err := p.localOp("rename", filepath.Join(home, "n.txt"), "m.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.localOp("delete", filepath.Join(home, "a"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "m.txt")); err != nil {
		t.Fatal("m.txt missing")
	}

	// a person's folder: only inside it, never it or its top folders
	root := t.TempDir()
	jhome := filepath.Join(root, "home")
	_ = os.MkdirAll(filepath.Join(jhome, "x"), 0o755)
	j := &filePlace{home: jhome, jail: root}
	for _, bad := range []string{root, jhome, "/tmp", filepath.Join(root, "..")} {
		if _, err := j.localOp("delete", bad, ""); err == nil {
			t.Fatalf("jailed delete %q was allowed", bad)
		}
	}
	if _, err := j.localOp("delete", filepath.Join(jhome, "x"), ""); err != nil {
		t.Fatal(err)
	}
}
