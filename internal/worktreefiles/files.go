// Package worktreefiles preserves ignored local files outside Git objects.
// The same helper runs locally and on remote machines through pocket-hold.
package worktreefiles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const maxBackupBytes = 512 << 20
const maxFiles = 10000

type Entry struct {
	Path string      `json:"path"`
	Mode fs.FileMode `json:"mode"`
	Size int64       `json:"size"`
	Link string      `json:"link,omitempty"`
	Hash string      `json:"hash,omitempty"`
}
type Summary struct {
	Files     []Entry  `json:"files"`
	Generated []string `json:"generated"`
	Bytes     int64    `json:"bytes"`
	Backup    string   `json:"backup,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Only well-known dependency and tool-cache directories are disposable.
// Generic build/, dist/, data/, .cache/ and all symlinks are backed up.
var generatedDirs = map[string]bool{
	"node_modules": true, ".next": true, ".nuxt": true, ".turbo": true,
	".parcel-cache": true, "__pycache__": true, ".pytest_cache": true,
	".mypy_cache": true, ".ruff_cache": true,
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("could not inspect local files: %w", err)
	}
	return out, nil
}
func validPath(p string) bool {
	if !filepath.IsLocal(p) || filepath.Clean(p) != p || p == "." {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".git" {
			return false
		}
	}
	return true
}

func Plan(ctx context.Context, dir string) (Summary, error) {
	result := Summary{Files: []Entry{}, Generated: []string{}}
	data, err := git(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return result, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	seen := map[string]bool{}
	for _, name := range strings.Split(string(data), "\x00") {
		if name == "" {
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if !validPath(name) {
			return result, fmt.Errorf("cannot safely preserve ignored path %q", name)
		}
		visit := func(p string, d fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return fmt.Errorf("cannot read ignored file %q: %w", p, walkErr)
			}
			if d.IsDir() && generatedDirs[d.Name()] {
				result.Generated = append(result.Generated, p+"/")
				return fs.SkipDir
			}
			if !validPath(p) {
				return fmt.Errorf("ignored path %q needs manual review", p)
			}
			if d.IsDir() {
				return nil
			}
			if seen[p] {
				return nil
			}
			seen[p] = true
			info, err := root.Lstat(p)
			if err != nil {
				return err
			}
			entry := Entry{Path: p, Mode: info.Mode(), Size: info.Size()}
			if info.Mode()&os.ModeSymlink != 0 {
				entry.Link, err = root.Readlink(p)
				entry.Size = 0
			} else if !info.Mode().IsRegular() {
				return fmt.Errorf("ignored file %q is not a regular file or symlink", p)
			}
			if err != nil {
				return fmt.Errorf("cannot read ignored file %q: %w", p, err)
			}
			result.Bytes += entry.Size
			if result.Bytes > maxBackupBytes || len(result.Files) >= maxFiles {
				return fmt.Errorf("ignored files exceed the private backup limit (512 MB / 10,000 files) at %q; move large data out first", p)
			}
			result.Files = append(result.Files, entry)
			return nil
		}
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return result, fmt.Errorf("cannot inspect ignored file %q: %w", name, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			err = visit(name, fs.FileInfoToDirEntry(info), nil)
		} else {
			err = fs.WalkDir(root.FS(), name, visit)
		}
		if err != nil {
			return result, err
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sort.Strings(result.Generated)
	return result, nil
}

func backupRoot(ctx context.Context, dir, ref string) (*os.Root, string, error) {
	if !strings.HasPrefix(ref, "refs/pocket-ai/archive/") {
		return nil, "", errors.New("invalid recovery reference")
	}
	data, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, "", err
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	root, err := os.OpenRoot(common)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256([]byte(ref))
	return root, "pocket-ai/local-files/" + hex.EncodeToString(hash[:]), nil
}

func digest(root *os.Root, e Entry) (string, error) {
	f, err := root.Open(e.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBackupBytes+1))
	if err != nil {
		return "", err
	}
	if n != e.Size {
		return "", fmt.Errorf("ignored file %q changed during backup", e.Path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Snapshot(ctx context.Context, dir, ref string) (Summary, error) {
	plan, err := Plan(ctx, dir)
	if err != nil {
		return plan, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return plan, err
	}
	defer root.Close()
	common, base, err := backupRoot(ctx, dir, ref)
	if err != nil {
		return plan, err
	}
	defer common.Close()
	if err := common.MkdirAll(base, 0700); err != nil {
		return plan, err
	}
	// A generation is never overwritten, including a backup from a failed removal.
	folder, err := common.OpenRoot(base)
	if err != nil {
		return plan, err
	}
	defer folder.Close()
	name := "snapshot-" + rand.Text()
	if err = folder.Mkdir(name, 0700); err != nil {
		return plan, err
	}
	snapshot, err := folder.OpenRoot(name)
	if err != nil {
		return plan, err
	}
	defer snapshot.Close()
	tmp := filepath.Join(folder.Name(), name)
	complete := false
	defer func() {
		if !complete {
			_ = folder.RemoveAll(name)
		}
	}()
	for i := range plan.Files {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		e := &plan.Files[i]
		if e.Mode&os.ModeSymlink != 0 {
			continue
		} // target text lives in the manifest; never follow it
		src, err := root.Open(e.Path)
		if err != nil {
			return plan, fmt.Errorf("cannot back up %q: %w", e.Path, err)
		}
		dst, err := snapshot.OpenFile(strconv.Itoa(i), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			src.Close()
			return plan, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, maxBackupBytes+1))
		src.Close()
		syncErr := dst.Sync()
		closeErr := dst.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return plan, fmt.Errorf("cannot back up %q: %w", e.Path, err)
		}
		if n != e.Size {
			return plan, fmt.Errorf("ignored file %q changed during backup", e.Path)
		}
		e.Hash = hex.EncodeToString(h.Sum(nil))
	}
	// Compare file names, modes, links and bytes after copying. A changed file
	// prevents removal and leaves the source untouched.
	again, err := Plan(ctx, dir)
	if err != nil {
		return plan, err
	}
	expected := append([]Entry(nil), plan.Files...)
	for i := range expected {
		expected[i].Hash = ""
	}
	if !reflect.DeepEqual(expected, again.Files) && !(len(expected) == 0 && len(again.Files) == 0) {
		return plan, errors.New("ignored files changed during backup; retry cleanup")
	}
	for _, e := range plan.Files {
		if e.Mode&os.ModeSymlink != 0 {
			continue
		}
		hash, err := digest(root, e)
		if err != nil {
			return plan, err
		}
		if hash != e.Hash {
			return plan, fmt.Errorf("ignored file %q changed during backup", e.Path)
		}
	}
	plan.Backup = tmp
	data, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	if err = writeSync(snapshot, "manifest.json", data); err != nil {
		return plan, err
	}
	if err = syncRoot(snapshot); err != nil {
		return plan, err
	}
	pointerName := ".current-" + rand.Text()
	pointer, err := folder.OpenFile(pointerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return plan, err
	}
	defer folder.Remove(pointerName)
	_, err = pointer.WriteString(name)
	err = errors.Join(err, pointer.Sync(), pointer.Close())
	if err != nil {
		return plan, err
	}
	if err = folder.Rename(pointerName, "current"); err != nil {
		return plan, err
	}
	complete = true
	if err = syncRoot(folder); err != nil {
		return plan, err
	}
	return plan, nil
}
func writeSync(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Sync(), f.Close())
}

func syncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Restore never overwrites existing files or follows a parent symlink.
// Conflicts remain in the private backup, with an actionable warning.
func Restore(ctx context.Context, dir, ref string) (Summary, error) {
	result := Summary{Files: []Entry{}, Generated: []string{}}
	common, base, err := backupRoot(ctx, dir, ref)
	if err != nil {
		return result, err
	}
	defer common.Close()
	pointer, err := common.ReadFile(base + "/current")
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	name := string(pointer)
	if filepath.Base(name) != name || !strings.HasPrefix(name, "snapshot-") {
		return result, errors.New("invalid local-file backup pointer")
	}
	backup, err := common.OpenRoot(base + "/" + name)
	if err != nil {
		return result, err
	}
	defer backup.Close()
	data, err := backup.ReadFile("manifest.json")
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	for i, e := range result.Files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := restoreEntry(root, backup, i, e); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s (saved in %s)", e.Path, err, result.Backup))
		}
	}
	return result, nil
}
func restoreEntry(root, backup *os.Root, index int, e Entry) error {
	if !validPath(e.Path) {
		return errors.New("invalid saved path")
	}
	parent := filepath.Dir(e.Path)
	parts := strings.Split(filepath.ToSlash(parent), "/")
	at := ""
	for _, part := range parts {
		if part == "." {
			continue
		}
		at = filepath.Join(at, part)
		info, err := root.Lstat(at)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(at, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("parent is a file or symlink; left untouched")
		}
	}
	if _, err := root.Lstat(e.Path); err == nil {
		return errors.New("already exists; left untouched")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if e.Mode&os.ModeSymlink != 0 {
		return root.Symlink(e.Link, e.Path)
	}
	src, err := backup.Open(strconv.Itoa(index))
	if err != nil {
		return err
	}
	defer src.Close()
	// Validate the private copy before writing any restored file.
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(src, maxBackupBytes+1))
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.Hash {
		return errors.New("backup checksum failed")
	}
	if _, err = src.Seek(0, 0); err != nil {
		return err
	}
	dst, err := root.OpenFile(e.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, e.Mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return errors.Join(err, dst.Sync(), dst.Close())
}

// Forget removes private generations only when the user deletes the archive.
func Forget(ctx context.Context, dir, ref string) (Summary, error) {
	result := Summary{Files: []Entry{}, Generated: []string{}}
	common, base, err := backupRoot(ctx, dir, ref)
	if err != nil {
		return result, err
	}
	defer common.Close()
	return result, common.RemoveAll(base)
}

// Main is the small command-line surface used by local and SSH scripts.
func Main(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: worktree-files plan|snapshot|restore DIR [REF]")
		return 2
	}
	ctx := context.Background()
	var result Summary
	var err error
	switch args[0] {
	case "plan":
		result, err = Plan(ctx, args[1])
	case "snapshot", "restore", "forget":
		if len(args) != 3 {
			return 2
		}
		if args[0] == "snapshot" {
			result, err = Snapshot(ctx, args[1], args[2])
		} else if args[0] == "restore" {
			result, err = Restore(ctx, args[1], args[2])
		} else {
			result, err = Forget(ctx, args[1], args[2])
		}
	default:
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return 1
	}
	return 0
}
