package worktreefiles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	put(t, dir, ".gitignore", "*\n", 0600)
	return dir
}
func put(t *testing.T, root, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

const ref = "refs/pocket-ai/archive/7"

func TestIgnoredFilesSnapshotAndRestore(t *testing.T) {
	dir := fixture(t)
	ctx := context.Background()
	put(t, dir, ".env", "PRIVATE=test\n", 0600)
	put(t, dir, "local settings/data.txt", "irreplaceable", 0640)
	put(t, dir, "dist/custom-output", "keep unknown build output", 0600)
	put(t, dir, "node_modules/package/index.js", "generated", 0644)
	put(t, dir, ".next/cache/large", "generated", 0644)
	outside := filepath.Join(t.TempDir(), "outside")
	put(t, filepath.Dir(outside), "outside", "never read or overwrite", 0600)
	if err := os.Symlink(outside, filepath.Join(dir, "external-link")); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Generated) != 2 || len(plan.Files) != 5 {
		t.Fatalf("plan: %+v", plan)
	}
	saved, err := Snapshot(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range saved.Files {
		if strings.Contains(e.Path, "node_modules") {
			t.Fatal("dependencies backed up")
		}
		if err := os.Remove(filepath.Join(dir, e.Path)); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(saved.Backup); err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("private backup directory: %v %v", fi, err)
	}
	fi, err := os.Stat(filepath.Join(saved.Backup, "manifest.json"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("private manifest: %v %v", fi, err)
	}
	restored, err := Restore(ctx, dir, ref)
	if err != nil || len(restored.Warnings) > 0 {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(data) != "PRIVATE=test\n" {
		t.Fatal("configuration lost")
	}
	link, err := os.Readlink(filepath.Join(dir, "external-link"))
	if err != nil || link != outside {
		t.Fatalf("symlink: %q %v", link, err)
	}
	fi, _ = os.Stat(filepath.Join(dir, "local settings/data.txt"))
	if fi.Mode().Perm() != 0640 {
		t.Fatal("permissions lost")
	}
}

func TestRestoreDoesNotOverwriteOrFollowSymlinks(t *testing.T) {
	dir := fixture(t)
	ctx := context.Background()
	put(t, dir, "config/secret", "original", 0600)
	put(t, dir, ".env", "original", 0600)
	_, err := Snapshot(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err = os.Symlink(target, filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	put(t, dir, ".env", "new configuration", 0600)
	restored, err := Restore(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Warnings) != 3 {
		t.Fatalf("expected .gitignore, .env and parent symlink conflicts: %+v", restored)
	}
	if _, err = os.Stat(filepath.Join(target, "secret")); !os.IsNotExist(err) {
		t.Fatal("wrote through parent symlink")
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(data) != "new configuration" {
		t.Fatal("overwrote new file")
	}
}

func TestChangedFileStopsSnapshot(t *testing.T) {
	dir := fixture(t)
	put(t, dir, ".env", "original", 0600)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	put(t, bin, "git", `#!/bin/sh
for arg in "$@"; do
 if [ "$arg" = ls-files ]; then
  if [ -f "$BACKUP_MARKER" ]; then printf 'mutated!' > "$BACKUP_ENV"; else touch "$BACKUP_MARKER"; fi
 fi
done
exec "$REAL_GIT" "$@"
`, 0700)
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("BACKUP_MARKER", filepath.Join(bin, "once"))
	t.Setenv("BACKUP_ENV", filepath.Join(dir, ".env"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = Snapshot(context.Background(), dir, ref)
	if err == nil || !strings.Contains(err.Error(), "changed during backup") {
		t.Fatalf("changed file accepted: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(data) != "mutated!" {
		t.Fatal("source changed by backup")
	}
}

func TestLimitsAndSymlinkNamedLikeCache(t *testing.T) {
	dir := fixture(t)
	if err := os.Symlink("/outside", filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Generated) != 0 || len(plan.Files) != 2 {
		t.Fatalf("symlink discarded: %+v", plan)
	}
	f, err := os.Create(filepath.Join(dir, "huge-data"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxBackupBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "huge-data") {
		t.Fatalf("oversize file not identified: %v", err)
	}
}

func TestEmptySnapshotDoesNotResurrectDeletedFiles(t *testing.T) {
	dir := fixture(t)
	ctx := context.Background()
	put(t, dir, ".env", "old", 0600)
	_, err := Snapshot(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, ".env"))
	_ = os.Remove(filepath.Join(dir, ".gitignore"))
	_, err = Snapshot(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(ctx, dir, ref)
	if err != nil || len(restored.Files) > 0 {
		t.Fatalf("old files resurrected: %+v %v", restored, err)
	}
}

func TestCorruptBackupIsKeptAndForgetRemovesGenerations(t *testing.T) {
	dir := fixture(t)
	ctx := context.Background()
	put(t, dir, ".env", "original", 0600)
	saved, err := Snapshot(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, e := range saved.Files {
		if e.Path == ".env" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing config")
	}
	put(t, saved.Backup, strconv.Itoa(index), "corrupted", 0600)
	_ = os.Remove(filepath.Join(dir, ".env"))
	restored, err := Restore(ctx, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(restored.Warnings, " "), "checksum") {
		t.Fatal("corruption not reported", restored.Warnings)
	}
	if _, err = os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatal("corrupt file restored")
	}
	if _, err = Forget(ctx, dir, ref); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(saved.Backup); !os.IsNotExist(err) {
		t.Fatal("deleted archive backup remains")
	}
}
