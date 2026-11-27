package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func mkSkill(t *testing.T, dir, name, body string) string {
	t.Helper()
	folder := filepath.Join(dir, name)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: About " + name + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return folder
}

func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestLibraryAddEditFilesRemove(t *testing.T) {
	skillSyncAuto = false
	t.Cleanup(func() { skillSyncAuto = true })
	s, h, root := testServer(t)
	owner, _ := person(t, s, h, "owner@example.com", "admin", true)
	_ = os.MkdirAll(s.cfg.ClaudeHome, 0o755)
	lib := filepath.Join(root, ".agents", "skills")
	const api = "/api/v1/skills/library"

	var list struct {
		Dir    string     `json:"dir"`
		Skills []libSkill `json:"skills"`
	}
	owner.want("GET", api, nil, 200, &list)
	if len(list.Skills) != 0 || list.Dir != lib {
		t.Fatalf("empty library expected, got %#v", list)
	}
	for _, bad := range []map[string]string{
		{"name": "Bad Name", "description": "d", "body": "b"},
		{"name": "synced", "description": "d", "body": "b"},
		{"name": "ok", "description": " ", "body": "b"},
		{"name": "ok", "description": "d", "body": "\n"},
	} {
		owner.want("POST", api, bad, 400, nil)
	}
	var sk libSkill
	owner.want("POST", api, map[string]string{"name": "deploy-app", "description": "Deploy: the app\nnow", "body": "# Deploy\n\nRun it.\n"}, 201, &sk)
	if sk.Description != "Deploy: the app now" || sk.Body != "# Deploy\n\nRun it.\n" || sk.Sum == "" {
		t.Fatalf("bad skill: %#v", sk)
	}
	owner.want("POST", api, map[string]string{"name": "deploy-app", "description": "d", "body": "b"}, 409, nil)
	if !isLink(filepath.Join(s.cfg.ClaudeHome, "skills", "deploy-app")) {
		t.Fatal("no link in Claude Code's skills folder")
	}

	// extra frontmatter keys survive an edit
	file := filepath.Join(lib, "deploy-app", "SKILL.md")
	_ = os.WriteFile(file, []byte("---\nname: deploy-app\ndescription: >\n  Old folded\n  text\nallowed-tools: Bash\n---\n\nOld\n"), 0o644)
	owner.want("PUT", api+"/deploy-app", map[string]string{"description": "New", "body": "New body"}, 200, &sk)
	if got := read(file); got != "---\nname: deploy-app\ndescription: \"New\"\nallowed-tools: Bash\n---\n\nNew body\n" {
		t.Fatalf("bad file:\n%s", got)
	}
	owner.want("PUT", api+"/missing", map[string]string{"description": "d", "body": "b"}, 404, nil)

	// other files: add, read, change, move, delete; never outside the skill
	q := api + "/deploy-app/file?path="
	owner.want("PUT", q+"references/steps.md", map[string]any{"content": "one", "create": true}, 200, &sk)
	if sk.FileCount != 1 || len(sk.Files) != 1 || sk.Files[0].Path != "references/steps.md" {
		t.Fatalf("bad files: %#v", sk.Files)
	}
	owner.want("PUT", q+"references/steps.md", map[string]any{"content": "x", "create": true}, 409, nil)
	var f struct {
		Text    bool   `json:"text"`
		Content string `json:"content"`
	}
	owner.want("GET", q+"references/steps.md", nil, 200, &f)
	if !f.Text || f.Content != "one" {
		t.Fatalf("bad file read: %#v", f)
	}
	owner.want("PUT", q+"references/steps.md", map[string]any{"content": "two", "rename": "docs/steps.md"}, 200, &sk)
	if read(filepath.Join(lib, "deploy-app", "docs", "steps.md")) != "two" {
		t.Fatal("file was not moved")
	}
	if _, err := os.Stat(filepath.Join(lib, "deploy-app", "references")); err == nil {
		t.Fatal("the empty folder stayed")
	}
	for _, bad := range []string{"../x.md", "/etc/passwd", "a/../../x", "SKILL.md", ".git/config", ""} {
		if code, _ := owner.call("PUT", q+bad, map[string]any{"content": "x"}); code != 400 {
			t.Fatalf("path %q: got %d", bad, code)
		}
	}
	owner.want("DELETE", q+"docs/steps.md", nil, 200, &sk)
	if sk.FileCount != 0 {
		t.Fatalf("file not deleted: %#v", sk.Files)
	}

	// rename, duplicate, download
	owner.want("PUT", api+"/deploy-app", map[string]string{"rename": "ship-app", "description": "New", "body": "New body"}, 200, &sk)
	if sk.Name != "ship-app" || !strings.Contains(read(filepath.Join(lib, "ship-app", "SKILL.md")), "name: ship-app\n") {
		t.Fatalf("not renamed: %#v", sk)
	}
	if isLink(filepath.Join(s.cfg.ClaudeHome, "skills", "deploy-app")) || !isLink(filepath.Join(s.cfg.ClaudeHome, "skills", "ship-app")) {
		t.Fatal("Claude Code's links did not follow the rename")
	}
	owner.want("POST", api+"/ship-app/duplicate", map[string]string{"name": "ship-app-2"}, 201, &sk)
	if !strings.Contains(read(filepath.Join(lib, "ship-app-2", "SKILL.md")), "name: ship-app-2\n") {
		t.Fatal("the copy kept the old name")
	}
	if code, body := owner.call("GET", api+"/ship-app/download", nil); code != 200 || !bytes.HasPrefix(body, []byte("PK")) {
		t.Fatalf("download: %d", code)
	}

	// folders the CLIs manage are not skills
	_ = os.MkdirAll(filepath.Join(lib, "synced", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(lib, "synced", "SKILL.md"), []byte("x"), 0o644)
	owner.want("GET", api, nil, 200, &list)
	if len(list.Skills) != 2 {
		t.Fatalf("bad list: %#v", list.Skills)
	}
	owner.want("DELETE", api+"/synced", nil, 404, nil)

	owner.want("DELETE", api+"/ship-app", nil, 200, nil)
	if _, err := os.Stat(filepath.Join(lib, "ship-app")); err == nil {
		t.Fatal("still there")
	}
	if isLink(filepath.Join(s.cfg.ClaudeHome, "skills", "ship-app")) {
		t.Fatal("Claude Code's link stayed")
	}
	if ents, _ := os.ReadDir(filepath.Join(root, ".agents", ".pocket-ai-trash", "skills")); len(ents) != 1 {
		t.Fatalf("not in the trash: %v", ents)
	}

	// someone who may not run agents cannot touch the library
	guest, _ := person(t, s, h, "guest@example.com", "member", false)
	guest.want("GET", api, nil, 403, nil)
	guest.want("POST", api, map[string]string{"name": "x", "description": "d", "body": "b"}, 403, nil)
}

// Skills in Claude Code's or Codex's own folder move into the library.
func TestArrangeLocal(t *testing.T) {
	skillSyncAuto = false
	t.Cleanup(func() { skillSyncAuto = true })
	s, _, root := testServer(t)
	claude, codex := filepath.Join(s.cfg.ClaudeHome, "skills"), filepath.Join(s.cfg.CodexHome, "skills")
	lib := filepath.Join(root, ".agents", "skills")
	mkSkill(t, claude, "only-claude", "c")
	mkSkill(t, codex, "only-codex", "x")
	mkSkill(t, claude, "same", "s")
	mkSkill(t, codex, "same", "s")
	old := mkSkill(t, claude, "newer-in-codex", "old")
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(old, "SKILL.md"), past, past)
	mkSkill(t, codex, "newer-in-codex", "new")
	mkSkill(t, codex, ".system", "codex's own")
	mkSkill(t, claude, "synced", "claude.ai's")

	sc := s.libScopeFor(context.Background(), nil)
	libMu.Lock()
	changed := s.arrangeLocal(sc)
	libMu.Unlock()
	if !changed {
		t.Fatal("nothing moved")
	}
	for _, n := range []string{"only-claude", "only-codex", "same", "newer-in-codex"} {
		if fi, err := os.Lstat(filepath.Join(lib, n)); err != nil || !fi.IsDir() {
			t.Fatalf("%s is not in the library", n)
		}
		if !isLink(filepath.Join(claude, n)) {
			t.Fatalf("%s has no link in Claude Code's folder", n)
		}
		if _, err := os.Lstat(filepath.Join(codex, n)); err == nil {
			t.Fatalf("%s is still in Codex's folder (it reads the library itself)", n)
		}
	}
	if !strings.Contains(read(filepath.Join(lib, "newer-in-codex", "SKILL.md")), "new") {
		t.Fatal("the older copy won")
	}
	if ents, _ := os.ReadDir(filepath.Join(root, ".agents", ".pocket-ai-trash", "skills")); len(ents) != 1 {
		t.Fatalf("the older copy is not in the trash: %v", ents)
	}
	for _, p := range []string{filepath.Join(codex, ".system"), filepath.Join(claude, "synced")} {
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			t.Fatalf("%s was touched", p)
		}
	}
	libMu.Lock()
	again := s.arrangeLocal(sc)
	libMu.Unlock()
	if again {
		t.Fatal("a second pass changed something")
	}
}

func TestPlanHost(t *testing.T) {
	lib := map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7"}
	v := &hostView{
		lib: map[string]remoteCopy{
			"a":     {sum: "1"},      // same
			"b":     {sum: "old"},    // we put "old" there: send
			"c":     {sum: "theirs"}, // changed there, not here: take
			"d":     {sum: "x"},      // both changed
			"g":     {sum: "7"},      // kept off, was put there
			"gone":  {sum: "9"},      // deleted from the library
			"extra": {sum: "8", desc: "E"},
		},
		own: map[string][]remoteCopy{
			"f":    {{sum: "other", agent: "claude", home: "/h/.claude"}}, // a different copy in Claude Code's folder
			"mine": {{sum: "m", agent: "codex", home: "/h/.codex"}},
		},
	}
	pushed := map[string]string{"a": "1", "b": "old", "c": "3", "d": "y", "g": "7", "gone": "9", "dead": "0"}
	p := planHost(lib, map[string]bool{"g": true}, pushed, v)
	if p.states["a"].State != "same" || p.states["d"].State != "differs" || p.states["d"].Where != "lib" || p.states["g"].State != "off" {
		t.Fatalf("bad states: %#v", p.states)
	}
	if st := p.states["f"]; st.State != "differs" || st.Where != "/h/.claude" || st.Agent != "claude" {
		t.Fatalf("bad state for f: %#v", st)
	}
	if keep, ok := p.push["b"]; !ok || keep {
		t.Fatalf("b: %#v", p.push)
	}
	if keep, ok := p.push["e"]; !ok || !keep || len(p.push) != 2 {
		t.Fatalf("e: %#v", p.push)
	}
	if len(p.take) != 1 || p.take[0] != "c" {
		t.Fatalf("take: %v", p.take)
	}
	if keep, ok := p.remove["gone"]; !ok || keep || len(p.remove) != 2 {
		t.Fatalf("remove: %#v", p.remove)
	}
	if len(p.extra) != 2 || p.extra[0].Name != "extra" || p.extra[1].Name != "mine" || p.extra[1].Where != "/h/.codex" {
		t.Fatalf("extra: %#v", p.extra)
	}
	if len(p.forget) != 1 || p.forget[0] != "dead" {
		t.Fatalf("forget: %v", p.forget)
	}
}

// shRunner stands in for a machine: the same scripts, in a home of its own.
type shRunner struct{ home string }

func (r shRunner) RunInput(ctx context.Context, timeout time.Duration, stdin io.Reader, script string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Env = []string{"HOME=" + r.home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func TestSkillSyncWithAMachine(t *testing.T) {
	skillSyncAuto = false
	t.Cleanup(func() { skillSyncAuto = true })
	s, h, root := testServer(t)
	owner, _ := person(t, s, h, "owner@example.com", "admin", true)
	ctx := context.Background()
	_ = os.MkdirAll(s.cfg.ClaudeHome, 0o755)
	lib := filepath.Join(root, ".agents", "skills")

	// the machine: two Claude Code logins, Codex, a skill of its own
	rh := t.TempDir()
	rlib := filepath.Join(rh, ".agents", "skills")
	for _, d := range []string{".claude", ".claude-max", ".codex/skills/.system"} {
		_ = os.MkdirAll(filepath.Join(rh, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(rh, ".claude-max", ".claude.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(rh, ".codex", "auth.json"), []byte("{}"), 0o644)
	mkSkill(t, filepath.Join(rh, ".claude", "skills"), "old-thing", "theirs")
	host := &store.Host{Name: "mac", Target: "mac"}
	if err := s.store.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	prev := skillRunnerFor
	skillRunnerFor = func(*store.Host) skillRunner { return shRunner{home: rh} }
	t.Cleanup(func() { skillRunnerFor = prev })

	const api = "/api/v1/skills"
	owner.want("POST", api+"/library", map[string]string{"name": "deploy-app", "description": "Deploy", "body": "v1"}, 201, nil)
	owner.want("PUT", api+"/library/deploy-app/file?path=references/a.md", map[string]any{"content": "ref"}, 200, nil)
	sc := s.libScopeFor(ctx, nil)

	type status struct {
		Asked bool `json:"asked"`
		Hosts []struct {
			ID     int64                     `json:"id"`
			On     bool                      `json:"on"`
			Error  string                    `json:"error"`
			Skills map[string]hostSkillState `json:"skills"`
			Extra  []hostExtra               `json:"extra"`
		} `json:"hosts"`
		Log []syncEvent `json:"log"`
	}
	var st status
	sync := func() {
		t.Helper()
		s.syncSkills(ctx, sc)
		st = status{}
		owner.want("GET", api+"/sync", nil, 200, &st)
		if len(st.Hosts) != 1 || st.Hosts[0].Error != "" {
			t.Fatalf("bad status: %#v", st)
		}
	}
	state := func(name string) string { return st.Hosts[0].Skills[name].State }

	// nothing goes anywhere until the machine is switched on
	sync()
	if st.Asked || st.Hosts[0].On {
		t.Fatalf("on before asked: %#v", st)
	}
	if _, err := os.Stat(rlib); err == nil {
		t.Fatal("a machine that is off was touched")
	}
	owner.want("PUT", api+"/sync/machines", map[string]any{"on": []int64{host.ID}}, 200, nil)
	sync()
	if !st.Asked || state("deploy-app") != "same" {
		t.Fatalf("not synced: %#v", st)
	}
	if read(filepath.Join(rlib, "deploy-app", "references", "a.md")) != "ref" {
		t.Fatal("the skill's files are not there")
	}
	for _, home := range []string{".claude", ".claude-max"} {
		if !isLink(filepath.Join(rh, home, "skills", "deploy-app")) {
			t.Fatalf("no link in %s", home)
		}
	}
	if len(st.Hosts[0].Extra) != 1 || st.Hosts[0].Extra[0].Name != "old-thing" || st.Hosts[0].Extra[0].Description != "About old-thing" {
		t.Fatalf("extra: %#v", st.Hosts[0].Extra)
	}
	if len(st.Log) != 1 || st.Log[0].What != "added" {
		t.Fatalf("log: %#v", st.Log)
	}

	// a change here goes there
	owner.want("PUT", api+"/library/deploy-app", map[string]string{"description": "Deploy", "body": "v2"}, 200, nil)
	sync()
	if !strings.Contains(read(filepath.Join(rlib, "deploy-app", "SKILL.md")), "v2") || state("deploy-app") != "same" || st.Log[0].What != "updated" {
		t.Fatalf("change not sent: %#v", st)
	}
	if ents, _ := os.ReadDir(filepath.Join(rh, ".agents", ".pocket-ai-trash", "skills")); len(ents) != 0 {
		t.Fatal("a copy Pocket AI put there itself went to the trash")
	}

	// a change there comes here (the old one is kept in the trash)
	rfile := filepath.Join(rlib, "deploy-app", "SKILL.md")
	_ = os.WriteFile(rfile, []byte(strings.Replace(read(rfile), "v2", "v3 from the mac", 1)), 0o644)
	sync()
	if !strings.Contains(read(filepath.Join(lib, "deploy-app", "SKILL.md")), "v3 from the mac") || state("deploy-app") != "same" || st.Log[0].What != "taken" {
		t.Fatalf("change not taken: %#v", st)
	}
	if read(filepath.Join(lib, "deploy-app", "references", "a.md")) != "ref" || !isLink(filepath.Join(s.cfg.ClaudeHome, "skills", "deploy-app")) {
		t.Fatal("the library's copy is not whole")
	}
	if ents, _ := os.ReadDir(filepath.Join(root, ".agents", ".pocket-ai-trash", "skills")); len(ents) != 1 {
		t.Fatalf("the library's old version is not in the trash: %v", ents)
	}

	// changed on both sides: nothing moves until the person says which
	lfile := filepath.Join(lib, "deploy-app", "SKILL.md")
	_ = os.WriteFile(lfile, []byte(read(lfile)+"here\n"), 0o644)
	_ = os.WriteFile(rfile, []byte(read(rfile)+"there\n"), 0o644)
	sync()
	if st.Hosts[0].Skills["deploy-app"] != (hostSkillState{State: "differs", Where: "lib"}) {
		t.Fatalf("no conflict: %#v", st.Hosts[0].Skills)
	}
	if !strings.HasSuffix(read(lfile), "here\n") || !strings.HasSuffix(read(rfile), "there\n") {
		t.Fatal("a side was overwritten")
	}
	st = status{}
	owner.want("POST", api+"/sync/resolve", map[string]any{"host_id": host.ID, "name": "deploy-app", "use": "machine"}, 200, &st)
	if !strings.HasSuffix(read(lfile), "there\n") || state("deploy-app") != "same" {
		t.Fatalf("the machine's version was not taken: %#v", st.Hosts[0].Skills)
	}
	_ = os.WriteFile(lfile, []byte(read(lfile)+"here 2\n"), 0o644)
	_ = os.WriteFile(rfile, []byte(read(rfile)+"there 2\n"), 0o644)
	sync()
	st = status{}
	owner.want("POST", api+"/sync/resolve", map[string]any{"host_id": host.ID, "name": "deploy-app", "use": "library"}, 200, &st)
	if !strings.HasSuffix(read(rfile), "here 2\n") || state("deploy-app") != "same" {
		t.Fatalf("the library's version was not sent: %#v", st.Hosts[0].Skills)
	}
	if ents, _ := os.ReadDir(filepath.Join(rh, ".agents", ".pocket-ai-trash", "skills")); len(ents) != 1 {
		t.Fatalf("the machine's other version is not in its trash: %v", ents)
	}

	// a skill only the machine has joins the library when asked
	st = status{}
	owner.want("POST", api+"/sync/adopt", map[string]any{"host_id": host.ID, "name": "old-thing"}, 200, &st)
	if !strings.Contains(read(filepath.Join(lib, "old-thing", "SKILL.md")), "theirs") || state("old-thing") != "same" || len(st.Hosts[0].Extra) != 0 {
		t.Fatalf("not adopted: %#v", st)
	}
	if !isLink(filepath.Join(rh, ".claude", "skills", "old-thing")) || !strings.Contains(read(filepath.Join(rlib, "old-thing", "SKILL.md")), "theirs") {
		t.Fatal("over there it did not move to the shared folder")
	}

	// kept off this machine, and back on
	owner.want("PUT", api+"/library/deploy-app/machines", map[string]any{"off": []int64{host.ID}}, 200, nil)
	sync()
	if _, err := os.Lstat(filepath.Join(rlib, "deploy-app")); err == nil || isLink(filepath.Join(rh, ".claude", "skills", "deploy-app")) || state("deploy-app") != "off" {
		t.Fatalf("still there: %#v", st.Hosts[0].Skills)
	}
	owner.want("PUT", api+"/library/deploy-app/machines", map[string]any{"off": []int64{}}, 200, nil)
	sync()
	if state("deploy-app") != "same" || !isLink(filepath.Join(rh, ".claude-max", "skills", "deploy-app")) {
		t.Fatalf("not back: %#v", st.Hosts[0].Skills)
	}

	// copies in the agents' own folders over there: the same ones give way,
	// a different one waits for the person
	mkSkill(t, lib, "fmt", "format")
	mkSkill(t, filepath.Join(rh, ".codex", "skills"), "fmt", "format")
	mkSkill(t, filepath.Join(rh, ".claude", "skills"), "fmt", "format")
	mkSkill(t, lib, "lint", "ours")
	mkSkill(t, filepath.Join(rh, ".claude", "skills"), "lint", "theirs")
	sync()
	if state("fmt") != "same" || !isLink(filepath.Join(rh, ".claude", "skills", "fmt")) {
		t.Fatalf("fmt: %#v", st.Hosts[0].Skills)
	}
	if _, err := os.Lstat(filepath.Join(rh, ".codex", "skills", "fmt")); err == nil {
		t.Fatal("Codex's same copy stayed")
	}
	if got := st.Hosts[0].Skills["lint"]; got.State != "differs" || got.Agent != "claude" || got.Where != filepath.Join(rh, ".claude") {
		t.Fatalf("lint: %#v", got)
	}
	st = status{}
	owner.want("POST", api+"/sync/resolve", map[string]any{"host_id": host.ID, "name": "lint", "use": "machine"}, 200, &st)
	if !strings.Contains(read(filepath.Join(lib, "lint", "SKILL.md")), "theirs") || state("lint") != "same" || !isLink(filepath.Join(rh, ".claude", "skills", "lint")) {
		t.Fatalf("lint not settled: %#v", st.Hosts[0].Skills)
	}

	// deleted here: gone there
	owner.want("DELETE", api+"/library/fmt", nil, 200, nil)
	sync()
	if _, err := os.Lstat(filepath.Join(rlib, "fmt")); err == nil || isLink(filepath.Join(rh, ".claude", "skills", "fmt")) {
		t.Fatal("fmt is still on the machine")
	}
	if _, ok := st.Hosts[0].Skills["fmt"]; ok || st.Log[0].What != "removed" {
		t.Fatalf("fmt still listed: %#v", st)
	}
	if fi, err := os.Lstat(filepath.Join(rh, ".codex", "skills", ".system")); err != nil || !fi.IsDir() {
		t.Fatal("Codex's own skills were touched")
	}

	// switched off: left as it is
	owner.want("PUT", api+"/sync/machines", map[string]any{"on": []int64{}}, 200, nil)
	mkSkill(t, lib, "later", "x")
	sync()
	if _, err := os.Lstat(filepath.Join(rlib, "later")); err == nil || st.Hosts[0].On {
		t.Fatal("a machine that is off got a skill")
	}
}

// sshRunner runs the scripts on a real machine, in a throwaway home there.
type sshRunner struct{ target, home string }

func (r sshRunner) RunInput(ctx context.Context, timeout time.Duration, stdin io.Reader, script string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := "set --"
	for _, a := range args {
		full += " '" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	b := base64.StdEncoding.EncodeToString([]byte(full + "\n" + script))
	remote := "export HOME='" + r.home + "'; unset CLAUDE_CONFIG_DIR CODEX_HOME; exec /bin/sh -c 'eval \"$(echo " + b + " | base64 -d 2>/dev/null || echo " + b + " | base64 -D)\"'"
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", r.target, remote)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// The scripts on another kind of machine (macOS: BSD tools):
// SKILL_SYNC_SSH=mac go test ./internal/server -run TestSkillScriptsOverSSH
func TestSkillScriptsOverSSH(t *testing.T) {
	target := os.Getenv("SKILL_SYNC_SSH")
	if target == "" {
		t.Skip("set SKILL_SYNC_SSH to an ssh host to run the scripts there")
	}
	skillSyncAuto = false
	t.Cleanup(func() { skillSyncAuto = true })
	s, _, root := testServer(t)
	ctx := context.Background()
	rh := "/tmp/pocket-ai-skill-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := sshRunner{target: target, home: rh}
	sh := func(script string) string {
		t.Helper()
		out, errb, err := run.RunInput(ctx, time.Minute, nil, script)
		if err != nil {
			t.Fatalf("%s: %v %s", script, err, errb)
		}
		return out
	}
	sh(`mkdir -p "$HOME/.claude" "$HOME/.claude-max" "$HOME/.codex/skills" && : > "$HOME/.claude-max/.claude.json"`)
	t.Cleanup(func() {
		_, _, _ = run.RunInput(ctx, time.Minute, nil, `case "$HOME" in /tmp/pocket-ai-skill-test-*) rm -rf "$HOME";; esac`)
	})
	prev := skillRunnerFor
	skillRunnerFor = func(*store.Host) skillRunner { return run }
	t.Cleanup(func() { skillRunnerFor = prev })

	lib := filepath.Join(root, ".agents", "skills")
	folder := mkSkill(t, lib, "deploy-app", "v1 – ünïcode")
	_ = os.MkdirAll(filepath.Join(folder, "references", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(folder, "references", "a file.md"), []byte("with a space"), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "references", "deep", "Z.md"), []byte("upper"), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "references", "deep", "a.md"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(folder, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	_ = os.WriteFile(filepath.Join(folder, ".DS_Store"), []byte("finder"), 0o644)
	host := &store.Host{Name: target, Target: target}
	if err := s.store.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	sc := s.libScopeFor(ctx, nil)
	s.updateSyncState(ctx, sc, func(st *syncState) { st.Hosts[host.ID] = &hostSync{On: true} })
	s.syncSkills(ctx, sc)
	st := s.loadSyncState(ctx, sc).Hosts[host.ID]
	if st.Error != "" || st.Skills["deploy-app"].State != "same" {
		t.Fatalf("not synced: %#v", st)
	}
	out := sh(`ls -l "$HOME/.claude/skills" "$HOME/.claude-max/skills"; test -x "$HOME/.agents/skills/deploy-app/run.sh" && echo EXEC; ls -a "$HOME/.agents/skills/deploy-app"`)
	if strings.Count(out, "deploy-app -> ") != 2 || !strings.Contains(out, "EXEC") || strings.Contains(out, ".DS_Store") {
		t.Fatalf("not as expected over there:\n%s", out)
	}
	// a change there comes back whole
	sh(`printf 'more\n' >> "$HOME/.agents/skills/deploy-app/SKILL.md"; printf 'new' > "$HOME/.agents/skills/deploy-app/references/new one.md"`)
	s.syncSkills(ctx, sc)
	st = s.loadSyncState(ctx, sc).Hosts[host.ID]
	if st.Skills["deploy-app"].State != "same" || read(filepath.Join(folder, "references", "new one.md")) != "new" || !strings.HasSuffix(read(filepath.Join(folder, "SKILL.md")), "more\n") {
		t.Fatalf("change not taken: %#v", st)
	}
	if fi, err := os.Stat(filepath.Join(folder, "run.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatal("the script lost its run bit")
	}
	// and a removal
	_ = os.RemoveAll(folder)
	s.syncSkills(ctx, sc)
	if out := sh(`ls "$HOME/.agents/skills" "$HOME/.claude/skills"`); strings.Contains(out, "deploy-app") {
		t.Fatalf("still there:\n%s", out)
	}
}

// A person with their own folder has their own library there, and a link
// they make in it does not lead the server to someone else's skills.
func TestLibraryOfAPersonStaysInTheirFolder(t *testing.T) {
	skillSyncAuto = false
	t.Cleanup(func() { skillSyncAuto = true })
	s, h, root := testServer(t)
	owner, _ := person(t, s, h, "owner@example.com", "admin", true)
	guest, gu := person(t, s, h, "guest@example.com", "member", true)
	const api = "/api/v1/skills/library"
	owner.want("POST", api, map[string]string{"name": "secret", "description": "Owner's", "body": "private"}, 201, nil)
	guest.want("POST", api, map[string]string{"name": "mine", "description": "Guest's", "body": "b"}, 201, nil)
	glib := filepath.Join(s.personHome(gu.ID), ".agents", "skills")
	if _, err := os.Stat(filepath.Join(glib, "mine", "SKILL.md")); err != nil {
		t.Fatalf("the guest's skill is not in their own folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "mine")); err == nil {
		t.Fatal("the guest's skill is in the server's library")
	}
	var list struct {
		Skills []libSkill `json:"skills"`
	}
	if err := os.Symlink(filepath.Join(root, ".agents", "skills", "secret"), filepath.Join(glib, "borrowed")); err != nil {
		t.Fatal(err)
	}
	guest.want("GET", api, nil, 200, &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "mine" {
		t.Fatalf("the guest sees %#v", list.Skills)
	}
	guest.want("GET", api+"/borrowed", nil, 404, nil)
	guest.want("GET", api+"/borrowed/download", nil, 404, nil)
	guest.want("GET", api+"/borrowed/file?path=SKILL.md", nil, 404, nil)
	guest.want("GET", api+"/secret", nil, 404, nil)
	if sums := librarySums(glib); len(sums) != 1 {
		t.Fatalf("a linked skill would be sent to the guest's machines: %v", sums)
	}
}
