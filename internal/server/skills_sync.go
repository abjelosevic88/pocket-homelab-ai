package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The other machines follow this server's skill library (skills_library.go).
// On each machine that is switched on, the skills live in ~/.agents/skills
// with a link per skill in every Claude Code login folder, as here.
//
// What goes where is decided per skill from three numbers: the library's
// sum, the sum of the copy on the machine, and the sum Pocket AI last put
// there ("pushed"):
//   - the machine's copy is the pushed one → the library moved on: send it
//   - the library is the pushed one, the machine's copy is not → it was
//     changed over there: take it into the library (the old one goes to the
//     trash), and the other machines get it from there
//   - neither → both changed (or it was never synced): the person decides
// A skill deleted from the library is removed from the machines it was put
// on; one that is only on a machine is listed, never taken by itself.

// syncState is kept as JSON in the box settings ("skills_sync", per library).
type syncState struct {
	Asked bool                `json:"asked"` // the person chose which machines follow the library
	Hosts map[int64]*hostSync `json:"hosts"`
	Off   map[string][]int64  `json:"off,omitempty"` // skill → machines it is kept off
	Log   []syncEvent         `json:"log,omitempty"` // newest first
	At    string              `json:"at,omitempty"`  // the last check of the machines
}

type hostSync struct {
	On     bool                      `json:"on"`
	Pushed map[string]string         `json:"pushed,omitempty"` // skill → the sum last put there or taken from there
	At     string                    `json:"at,omitempty"`     // when it last answered
	Error  string                    `json:"error,omitempty"`
	Skills map[string]hostSkillState `json:"skills,omitempty"` // as last seen
	Extra  []hostExtra               `json:"extra,omitempty"`  // skills only that machine has
}

type hostSkillState struct {
	State string `json:"state"`           // same | differs | off | failed
	Where string `json:"where,omitempty"` // differs: "lib" (the shared folder there) or the login folder of the other copy
	Agent string `json:"agent,omitempty"` // claude | codex, for a login folder
	Error string `json:"error,omitempty"`
}

type hostExtra struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Where       string `json:"where"`
	Agent       string `json:"agent,omitempty"`
}

type syncEvent struct {
	At    string `json:"at"`
	Host  string `json:"host"`
	Skill string `json:"skill"`
	What  string `json:"what"` // added | updated | removed | taken (the library got that machine's version)
}

const syncLogMax = 40

var syncStateMu sync.Mutex

func (s *Server) loadSyncState(ctx context.Context, sc *libScope) *syncState {
	st := &syncState{}
	_ = json.Unmarshal([]byte(s.store.BoxSetting(ctx, sc.settingKey("skills_sync"))), st)
	if st.Hosts == nil {
		st.Hosts = map[int64]*hostSync{}
	}
	if st.Off == nil {
		st.Off = map[string][]int64{}
	}
	return st
}

// updateSyncState reads, changes and stores the state in one step.
func (s *Server) updateSyncState(ctx context.Context, sc *libScope, fn func(*syncState)) *syncState {
	syncStateMu.Lock()
	defer syncStateMu.Unlock()
	st := s.loadSyncState(ctx, sc)
	fn(st)
	if len(st.Log) > syncLogMax {
		st.Log = st.Log[:syncLogMax]
	}
	if data, err := json.Marshal(st); err == nil {
		_ = s.store.SetBoxSetting(ctx, sc.settingKey("skills_sync"), string(data))
	}
	return st
}

// renameSkillState carries a skill's "kept off" list to its new name.
func (s *Server) renameSkillState(ctx context.Context, sc *libScope, from, to string) {
	s.updateSyncState(ctx, sc, func(st *syncState) {
		if off, ok := st.Off[from]; ok {
			st.Off[to] = off
			delete(st.Off, from)
		}
	})
}

// scopeHosts are the machines that can follow a library: the server's own
// for the server's, a person's own for theirs.
func (s *Server) scopeHosts(ctx context.Context, sc *libScope) []*store.Host {
	list, _ := s.store.ListHosts(ctx)
	out := []*store.Host{}
	for _, h := range list {
		owner := s.hostUser(ctx, h)
		if sc.uid != 0 {
			if owner != sc.uid {
				continue
			}
		} else if s.jailed(ctx, owner) {
			continue
		}
		s.fillHost(ctx, h)
		out = append(out, h)
	}
	return out
}

// ---------- what runs on the machine ----------

// skillRunner runs a short script on a machine (agent.Remote).
type skillRunner interface {
	RunInput(ctx context.Context, timeout time.Duration, stdin io.Reader, script string, args ...string) (string, string, error)
}

// skillSyncAuto is off in tests, which sync when they mean to.
var skillSyncAuto = true

// skillRunnerFor is replaced in tests by a shell with a home of its own.
var skillRunnerFor = func(h *store.Host) skillRunner { return remoteOf(h) }

// skillsSh starts every script: where things are over there, the sum of a
// skill folder (the same number as skillSum here), and the login folders.
const skillsSh = agent.RemoteHomesSh + `lib="$HOME/.agents/skills"; tmp="$HOME/.agents/.pocket-ai-tmp"; trash="$HOME/.agents/.pocket-ai-trash/skills"
tsum() { (cd "$1" 2>/dev/null || exit 0; find . -name .git -prune -o -type f ! -name .DS_Store ! -name '._*' -print | LC_ALL=C sort | while IFS= read -r f; do set -- $(cksum < "$f"); printf '%s %s %s\n' "$1" "$2" "$f"; done | cksum | tr ' ' '-'); }
real() { (cd "$1" 2>/dev/null && pwd -P); }
okname() { case "$1" in ''|-*|*[!a-z0-9-]*|synced) return 1;; esac; return 0; }
desc() { sed -n 's/^description:[[:space:]]*//p' "$1" 2>/dev/null | head -n 1 | cut -c1-300 | tr '\t' ' '; }
ch=$(homes claude); xh=$(homes codex)
`

// skillsViewSh: for the skills named (the ones this machine should have) it
// puts a link in every Claude Code login folder and drops copies in the
// agents' own folders that are the same as the shared one; then it prints
// what is there: "L name sum description" for the shared folder and
// "S agent folder name sum description" for real copies in a login folder.
const skillsViewSh = skillsSh + `mkdir -p "$lib" 2>/dev/null; libr=$(real "$lib")
[ -n "$libr" ] || { echo "cannot make $lib" >&2; exit 1; }
for n in "$@"; do
  okname "$n" || continue
  [ -f "$lib/$n/SKILL.md" ] || continue
  ls=$(tsum "$lib/$n"); lr=$(real "$lib/$n")
  while IFS= read -r h; do
    [ -n "$h" ] && [ -d "$h" ] || continue
    sd="$h/skills"; mkdir -p "$sd" 2>/dev/null || continue
    [ "$(real "$sd")" = "$libr" ] && continue
    e="$sd/$n"
    if [ -L "$e" ]; then
      [ -e "$e" ] && continue
      rm -f "$e"
    elif [ -d "$e" ]; then
      [ "$(tsum "$e")" = "$ls" ] || continue
      rm -rf "$e"
    elif [ -e "$e" ]; then continue
    fi
    ln -s "$lib/$n" "$e"
  done <<EOF
$ch
EOF
  while IFS= read -r h; do
    [ -n "$h" ] && [ -d "$h/skills" ] || continue
    sd="$h/skills"
    [ "$(real "$sd")" = "$libr" ] && continue
    e="$sd/$n"
    if [ -L "$e" ]; then
      if [ "$(real "$e")" = "$lr" ]; then rm -f "$e"; fi
    elif [ -d "$e" ] && [ "$(tsum "$e")" = "$ls" ]; then rm -rf "$e"
    fi
  done <<EOF
$xh
EOF
done
printf 'OK\t%s\n' "$HOME"
for e in "$lib"/*/; do
  n=${e%/}; n=${n##*/}
  okname "$n" || continue
  [ -f "$lib/$n/SKILL.md" ] || continue
  printf 'L\t%s\t%s\t%s\n' "$n" "$(tsum "$lib/$n")" "$(desc "$lib/$n/SKILL.md")"
done
seen="|$libr|"
for prov in claude codex; do
  if [ "$prov" = claude ]; then hs=$ch; else hs=$xh; fi
  while IFS= read -r h; do
    [ -n "$h" ] && [ -d "$h/skills" ] || continue
    r=$(real "$h/skills")
    case "$seen" in *"|$r|"*) continue ;; esac
    seen="$seen$r|"
    for e in "$h/skills"/*/; do
      n=${e%/}; n=${n##*/}
      okname "$n" || continue
      [ -L "$h/skills/$n" ] && continue
      [ -f "$h/skills/$n/SKILL.md" ] || continue
      printf 'S\t%s\t%s\t%s\t%s\t%s\n' "$prov" "$h" "$n" "$(tsum "$h/skills/$n")" "$(desc "$h/skills/$n/SKILL.md")"
    done
  done <<EOF
$hs
EOF
done
exit 0
`

// skillsPushSh unpacks a skill (a tar on stdin) into the shared folder.
// $1 name, $2 keep|drop: what happens to a different copy already there
// (keep: to the trash next to it). Different copies in the agents' own
// folders always go to that login's trash.
const skillsPushSh = skillsSh + `n=$1; okname "$n" || { echo "not a skill name" >&2; exit 1; }
t=$(date +%Y%m%d-%H%M%S)
mkdir -p "$lib" "$tmp" || exit 1
rm -rf "$tmp/$n" && mkdir "$tmp/$n" && tar -x -f - -C "$tmp/$n" || { echo "could not unpack the skill" >&2; exit 1; }
[ -f "$tmp/$n/SKILL.md" ] || { rm -rf "$tmp/$n"; echo "the skill did not arrive whole" >&2; exit 1; }
ns=$(tsum "$tmp/$n")
if [ -e "$lib/$n" ] || [ -L "$lib/$n" ]; then
  if [ "$2" = keep ] && [ "$(tsum "$lib/$n")" != "$ns" ]; then
    mkdir -p "$trash" && mv "$lib/$n" "$trash/$n-$t" || exit 1
  else
    rm -rf "$lib/$n" || exit 1
  fi
fi
mv "$tmp/$n" "$lib/$n" || exit 1
libr=$(real "$lib")
for hs in "$ch" "$xh"; do
  while IFS= read -r h; do
    [ -n "$h" ] && [ -d "$h/skills" ] || continue
    [ "$(real "$h/skills")" = "$libr" ] && continue
    e="$h/skills/$n"
    [ -d "$e" ] && [ ! -L "$e" ] || continue
    if [ "$(tsum "$e")" = "$ns" ]; then rm -rf "$e"
    else mkdir -p "$h/.pocket-ai-trash/skills" && mv "$e" "$h/.pocket-ai-trash/skills/$n-$t"
    fi
  done <<EOF
$hs
EOF
done
printf 'SUM\t%s\n' "$ns"
`

// skillsRemoveSh takes a skill out of the shared folder ($2 keep: to the
// trash) and removes the links to it.
const skillsRemoveSh = skillsSh + `n=$1; okname "$n" || { echo "not a skill name" >&2; exit 1; }
t=$(date +%Y%m%d-%H%M%S)
if [ -e "$lib/$n" ] || [ -L "$lib/$n" ]; then
  if [ "$2" = keep ]; then mkdir -p "$trash" && mv "$lib/$n" "$trash/$n-$t" || exit 1
  else rm -rf "$lib/$n" || exit 1
  fi
fi
for hs in "$ch" "$xh"; do
  while IFS= read -r h; do
    [ -n "$h" ] || continue
    e="$h/skills/$n"
    if [ -L "$e" ] && [ ! -e "$e" ]; then rm -f "$e"; fi
  done <<EOF
$hs
EOF
done
printf 'GONE\n'
`

// skillsPullSh prints a skill's sum and its files (a tar, base64). $2 is
// "lib" for the shared folder, else the login folder that has the copy.
const skillsPullSh = skillsSh + `n=$1; okname "$n" || { echo "not a skill name" >&2; exit 1; }
src="$lib/$n"
if [ "$2" != lib ]; then
  ok=
  for hs in "$ch" "$xh"; do
    while IFS= read -r h; do
      if [ "$h" = "$2" ]; then ok=1; fi
    done <<EOF
$hs
EOF
  done
  [ -n "$ok" ] || { echo "that is not a login folder there" >&2; exit 1; }
  src="$2/skills/$n"
fi
[ -f "$src/SKILL.md" ] || { echo "that skill is not there (any more)" >&2; exit 1; }
kb=$(du -sk "$src" 2>/dev/null | cut -f1)
[ "${kb:-0}" -le 20480 ] || { echo "that skill is over 20 MB" >&2; exit 1; }
printf 'SUM\t%s\n' "$(tsum "$src")"
COPYFILE_DISABLE=1 tar -c -f - -C "$src" . | base64 | tr -d '\n'
printf '\n'
`

// remoteCopy is one copy of a skill on a machine.
type remoteCopy struct {
	sum, desc   string
	agent, home string // for a copy in a login folder
}

// hostView is what a machine has.
type hostView struct {
	lib map[string]remoteCopy   // in ~/.agents/skills
	own map[string][]remoteCopy // real folders in the agents' own skills folders
}

func (s *Server) viewHost(ctx context.Context, h *store.Host, want []string) (*hostView, error) {
	stdout, stderr, err := skillRunnerFor(h).RunInput(agent.Repeatable(ctx), 60*time.Second, strings.NewReader(""), skillsViewSh, want...)
	if err != nil {
		return nil, errors.New(firstNonEmpty(lastLine(stderr), "not reachable"))
	}
	return parseHostView(stdout)
}

func parseHostView(out string) (*hostView, error) {
	v := &hostView{lib: map[string]remoteCopy{}, own: map[string][]remoteCopy{}}
	ok := false
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		switch {
		case f[0] == "OK":
			ok = true
		case f[0] == "L" && len(f) == 4 && validSkill(f[1]) && f[2] != "":
			v.lib[f[1]] = remoteCopy{sum: f[2], desc: plainDescription(f[3])}
		case f[0] == "S" && len(f) == 6 && validSkill(f[3]) && f[4] != "":
			v.own[f[3]] = append(v.own[f[3]], remoteCopy{agent: f[1], home: f[2], sum: f[4], desc: plainDescription(f[5])})
		}
	}
	if !ok {
		return nil, errors.New("it did not answer as expected")
	}
	return v, nil
}

// plainDescription tidies the first line of a description: read from a
// SKILL.md over there.
func plainDescription(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, ">") || strings.HasPrefix(v, "|") {
		return ""
	}
	return frontmatterValue("description: "+v, "description")
}

// hostPlan is what to do on a machine and how its skills stand.
type hostPlan struct {
	take   []string        // changed there and not here: into the library
	push   map[string]bool // name → keep a different copy there in its trash
	remove map[string]bool // name → keep it in the trash there
	forget []string        // pushed marks of skills that are gone on both sides
	states map[string]hostSkillState
	extra  []hostExtra
}

// planHost decides from the library's sums, the skills kept off this
// machine, what was put there before, and what it has now.
func planHost(lib map[string]string, off map[string]bool, pushed map[string]string, v *hostView) hostPlan {
	p := hostPlan{push: map[string]bool{}, remove: map[string]bool{}, states: map[string]hostSkillState{}, extra: []hostExtra{}}
	for name, sum := range lib {
		l, hasL := v.lib[name]
		if off[name] {
			if hasL && pushed[name] != "" {
				p.remove[name] = l.sum != pushed[name]
			}
			p.states[name] = hostSkillState{State: "off"}
			continue
		}
		var other *remoteCopy
		for i := range v.own[name] {
			if v.own[name][i].sum != sum {
				other = &v.own[name][i]
				break
			}
		}
		switch {
		case hasL && l.sum == sum && other == nil:
			p.states[name] = hostSkillState{State: "same"}
		case hasL && l.sum == sum:
			p.states[name] = hostSkillState{State: "differs", Where: other.home, Agent: other.agent}
		case hasL && pushed[name] == l.sum:
			p.push[name] = false
		case hasL && pushed[name] == sum:
			p.take = append(p.take, name)
		case hasL:
			p.states[name] = hostSkillState{State: "differs", Where: "lib"}
		case other != nil:
			p.states[name] = hostSkillState{State: "differs", Where: other.home, Agent: other.agent}
		default:
			p.push[name] = true
		}
	}
	for name, l := range v.lib {
		if _, ok := lib[name]; ok {
			continue
		}
		if pushed[name] != "" {
			p.remove[name] = l.sum != pushed[name]
		} else {
			p.extra = append(p.extra, hostExtra{Name: name, Description: l.desc, Where: "lib"})
		}
	}
	for name, copies := range v.own {
		if _, ok := lib[name]; ok {
			continue
		}
		if _, ok := v.lib[name]; !ok {
			p.extra = append(p.extra, hostExtra{Name: name, Description: copies[0].desc, Where: copies[0].home, Agent: copies[0].agent})
		}
	}
	for name := range pushed {
		_, inLib := lib[name]
		_, there := v.lib[name]
		if !inLib && !there {
			p.forget = append(p.forget, name)
		}
	}
	sort.Strings(p.take)
	sort.Slice(p.extra, func(i, j int) bool { return p.extra[i].Name < p.extra[j].Name })
	return p
}

// packSkill is a skill's files as a tar.
func packSkill(folder string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := walkSkill(folder, func(rel string, info fs.FileInfo) error {
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	return buf.Bytes(), err
}

func skillRemoteErr(stderr string, err error) error {
	return errors.New(firstNonEmpty(lastLine(stderr), err.Error()))
}

// pushSkill sends the library's copy to a machine and returns its sum there.
func (s *Server) pushSkill(ctx context.Context, sc *libScope, h *store.Host, name string, keep bool) (string, error) {
	libMu.Lock()
	sum, err := skillSum(filepath.Join(sc.dir, name))
	var pack []byte
	if err == nil {
		pack, err = packSkill(filepath.Join(sc.dir, name))
	}
	libMu.Unlock()
	if err != nil {
		return "", err
	}
	mode := "drop"
	if keep {
		mode = "keep"
	}
	stdout, stderr, err := skillRunnerFor(h).RunInput(ctx, 2*time.Minute, bytes.NewReader(pack), skillsPushSh, name, mode)
	if err != nil {
		return "", skillRemoteErr(stderr, err)
	}
	got := ""
	for _, l := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(l, "SUM\t"); ok {
			got = strings.TrimSpace(v)
		}
	}
	if got != sum {
		return "", errors.New("the copy there is not the same as the one sent")
	}
	return sum, nil
}

func (s *Server) removeSkill(ctx context.Context, h *store.Host, name string, keep bool) error {
	mode := "drop"
	if keep {
		mode = "keep"
	}
	_, stderr, err := skillRunnerFor(h).RunInput(ctx, time.Minute, nil, skillsRemoveSh, name, mode)
	if err != nil {
		return skillRemoteErr(stderr, err)
	}
	return nil
}

// takeSkill brings a machine's copy of a skill into the library (where:
// "lib" or the login folder that has it). The library's own copy, if any,
// goes to its trash. It returns the new sum.
func (s *Server) takeSkill(ctx context.Context, sc *libScope, h *store.Host, name, where string) (string, error) {
	stdout, stderr, err := skillRunnerFor(h).RunInput(agent.Repeatable(ctx), 2*time.Minute, nil, skillsPullSh, name, where)
	if err != nil {
		return "", skillRemoteErr(stderr, err)
	}
	var sum, b64 string
	for _, l := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(l, "SUM\t"); ok {
			sum = strings.TrimSpace(v)
		} else if sum != "" && len(l) > len(b64) {
			b64 = l
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || sum == "" {
		return "", errors.New("the copy did not arrive whole")
	}
	libMu.Lock()
	defer libMu.Unlock()
	if err := os.MkdirAll(sc.dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(sc.dir), ".pocket-ai-take-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, name)
	if err := os.Mkdir(in, 0o755); err != nil {
		return "", err
	}
	if err := untarSkill(data, in); err != nil {
		return "", err
	}
	if got, err := skillSum(in); err != nil || got != sum {
		return "", errors.New("the copy did not arrive whole")
	}
	dst := filepath.Join(sc.dir, name)
	if _, err := os.Lstat(dst); err == nil {
		if _, err := trashTo(sc.trash(), dst); err != nil {
			return "", err
		}
	}
	if err := moveDir(in, dst); err != nil {
		return "", err
	}
	s.arrangeLocal(sc)
	forgetSkills()
	return sum, nil
}

// librarySums: every skill of a library and its sum.
func librarySums(dir string) map[string]string {
	out := map[string]string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !validSkill(e.Name()) || !agent.Confined(filepath.Join(dir, e.Name())) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if sum, err := skillSum(filepath.Join(dir, e.Name())); err == nil {
			out[e.Name()] = sum
		}
	}
	return out
}

// ---------- the sync itself ----------

type syncRun struct {
	mu      sync.Mutex // one sync of a library at a time
	running bool
	queued  bool             // one more is waiting for its turn (never more than one)
	busy    map[int64]string // machine → what is being done there right now
}

var (
	syncRunsMu sync.Mutex
	syncRuns   = map[string]*syncRun{}
)

func syncRunOf(sc *libScope) *syncRun {
	syncRunsMu.Lock()
	defer syncRunsMu.Unlock()
	if syncRuns[sc.key] == nil {
		syncRuns[sc.key] = &syncRun{busy: map[int64]string{}}
	}
	return syncRuns[sc.key]
}

func syncRunning(sc *libScope) bool {
	run := syncRunOf(sc)
	syncRunsMu.Lock()
	defer syncRunsMu.Unlock()
	return run.running || run.queued
}

// syncBusy: what is being done on each machine right now ("checking",
// "sending pocket-app" …), for the page.
func syncBusy(sc *libScope) map[int64]string {
	run := syncRunOf(sc)
	syncRunsMu.Lock()
	defer syncRunsMu.Unlock()
	out := make(map[int64]string, len(run.busy))
	for k, v := range run.busy {
		out[k] = v
	}
	return out
}

// setBusy says what a machine is at ("" = done).
func (run *syncRun) setBusy(id int64, what string) {
	syncRunsMu.Lock()
	if what == "" {
		delete(run.busy, id)
	} else {
		run.busy[id] = what
	}
	syncRunsMu.Unlock()
}

// kickSkillSync syncs the machines in the background, soon (a moment later,
// so several saves in a row make one sync). While one runs, one more waits
// for its turn and further kicks join that one: a machine that takes long to
// not answer must not make a queue. Safe to call with libMu held.
func (s *Server) kickSkillSync(sc *libScope, _ bool) {
	if !skillSyncAuto {
		return
	}
	run := syncRunOf(sc)
	syncRunsMu.Lock()
	if run.queued {
		syncRunsMu.Unlock()
		return
	}
	run.queued = true
	syncRunsMu.Unlock()
	go func() {
		select {
		case <-s.rt.ctx.Done():
			return
		case <-time.After(800 * time.Millisecond):
		}
		s.syncSkillsQueued(s.rt.ctx, sc)
	}()
}

// syncSkills brings every machine that is switched on in line with the
// library, and takes in what was changed on one of them. Each machine is
// done on its own and its result saved as soon as it is known, so one that
// does not answer holds nobody up. It reports whether every machine is as it
// should be now (false: worth another try soon).
func (s *Server) syncSkills(ctx context.Context, sc *libScope) bool {
	return s.syncSkillsRun(ctx, sc, false)
}

// syncSkillsQueued is the sync a kick asked for: it stays "queued" until the
// one before it is through.
func (s *Server) syncSkillsQueued(ctx context.Context, sc *libScope) bool {
	return s.syncSkillsRun(ctx, sc, true)
}

func (s *Server) syncSkillsRun(ctx context.Context, sc *libScope, queued bool) bool {
	run := syncRunOf(sc)
	run.mu.Lock()
	syncRunsMu.Lock()
	run.running = true
	if queued {
		run.queued = false
	}
	syncRunsMu.Unlock()
	defer func() {
		syncRunsMu.Lock()
		run.running = false
		run.busy = map[int64]string{}
		syncRunsMu.Unlock()
		run.mu.Unlock()
		s.touch()
	}()
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	fine := true
	for pass := 0; pass < 3; pass++ {
		libMu.Lock()
		if s.arrangeLocal(sc) {
			forgetSkills()
		}
		lib := librarySums(sc.dir)
		libMu.Unlock()

		st := s.loadSyncState(ctx, sc)
		var hosts []*store.Host
		for _, h := range s.scopeHosts(ctx, sc) {
			if hs := st.Hosts[h.ID]; hs != nil && hs.On {
				hosts = append(hosts, h)
			}
		}
		var (
			mu     sync.Mutex // lib, took, fine
			takeMu sync.Mutex // one change at a time comes into the library
			took   bool
			wg     sync.WaitGroup
		)
		fine = true
		for _, h := range hosts {
			run.setBusy(h.ID, "checking")
		}
		s.touch()
		for _, h := range hosts {
			wg.Add(1)
			go func(h *store.Host) {
				defer wg.Done()
				defer run.setBusy(h.ID, "")
				ok, t := s.syncHost(ctx, sc, h, st, lib, &mu, &takeMu)
				mu.Lock()
				fine, took = fine && ok, took || t
				mu.Unlock()
			}(h)
		}
		wg.Wait()
		s.updateSyncState(ctx, sc, func(cur *syncState) { cur.At = now() })
		if !took {
			break // nothing came in from a machine: the others have nothing new to get
		}
	}
	return fine
}

// syncHost does one machine: look, take what was changed there, send and
// remove, look again, save how it stands. lib is shared between the
// machines of this pass (mu guards it). It reports whether the machine is
// as it should be, and whether the library took a change from it.
func (s *Server) syncHost(ctx context.Context, sc *libScope, h *store.Host, st *syncState, lib map[string]string, mu, takeMu *sync.Mutex) (fine, took bool) {
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	run := syncRunOf(sc)
	cur := st.Hosts[h.ID]
	hs := &hostSync{On: true, Pushed: map[string]string{}, At: cur.At, Skills: cur.Skills, Extra: cur.Extra}
	for k, v := range cur.Pushed {
		hs.Pushed[k] = v
	}
	off := map[string]bool{}
	for name, ids := range st.Off {
		for _, id := range ids {
			if id == h.ID {
				off[name] = true
			}
		}
	}
	sums := func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(lib))
		for k, v := range lib {
			out[k] = v
		}
		return out
	}
	want := func(lib map[string]string) []string {
		var out []string
		for name := range lib {
			if !off[name] {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out
	}
	var events []syncEvent
	event := func(skill, what string) {
		events = append(events, syncEvent{At: now(), Host: h.Name, Skill: skill, What: what})
		s.log.Info("skill sync", "host", h.Name, "skill", skill, "what", what)
	}
	save := func() {
		s.updateSyncState(ctx, sc, func(c *syncState) {
			was := c.Hosts[h.ID]
			if was == nil {
				return // the machine was removed meanwhile
			}
			hs.On = was.On
			c.Hosts[h.ID] = hs
			for i := len(events) - 1; i >= 0; i-- {
				c.Log = append([]syncEvent{events[i]}, c.Log...)
			}
		})
		s.touch()
	}

	v, err := s.viewHost(ctx, h, want(sums()))
	if err != nil {
		hs.Error = err.Error()
		save()
		return false, false
	}
	failed := map[string]string{}

	// a change made there comes into the library (one at a time: two
	// machines may have changed the same skill)
	for _, name := range planHost(sums(), off, hs.Pushed, v).take {
		takeMu.Lock()
		now := sums()
		if v.lib[name].sum == now[name] || hs.Pushed[name] != now[name] {
			takeMu.Unlock()
			continue // another machine's change got in first: the person decides
		}
		run.setBusy(h.ID, "taking "+name)
		sum, err := s.takeSkill(ctx, sc, h, name, "lib")
		if err == nil {
			mu.Lock()
			lib[name] = sum
			mu.Unlock()
			hs.Pushed[name] = sum
			took = true
			event(name, "taken")
		} else {
			failed[name] = err.Error()
		}
		takeMu.Unlock()
	}

	mine := sums()
	p := planHost(mine, off, hs.Pushed, v)
	ops := 0
	for name, keep := range p.push {
		_, had := v.lib[name]
		run.setBusy(h.ID, "sending "+name)
		sum, err := s.pushSkill(ctx, sc, h, name, keep)
		ops++
		if err != nil {
			failed[name] = err.Error()
			continue
		}
		hs.Pushed[name] = sum
		mine[name] = sum // what was sent, should the library have moved on since
		if had {
			event(name, "updated")
		} else {
			event(name, "added")
		}
	}
	for name, keep := range p.remove {
		ops++
		run.setBusy(h.ID, "removing "+name)
		if err := s.removeSkill(ctx, h, name, keep); err != nil {
			failed[name] = err.Error()
			continue
		}
		delete(hs.Pushed, name)
		event(name, "removed")
	}
	for _, name := range p.forget {
		delete(hs.Pushed, name)
	}
	if ops > 0 { // look again: the links are made, and what is there now is what counts
		forgetSkills() // the "/" menu of a chat on that machine asks its CLI again
		run.setBusy(h.ID, "checking")
		if v, err = s.viewHost(ctx, h, want(mine)); err != nil {
			hs.Error = err.Error()
			save()
			return false, took
		}
		p = planHost(mine, off, hs.Pushed, v)
	}
	fine = true
	for name, stt := range p.states {
		switch stt.State {
		case "same":
			hs.Pushed[name] = mine[name]
		case "differs":
			fine = false
		}
	}
	for name := range p.push {
		p.states[name] = hostSkillState{State: "failed", Error: firstNonEmpty(failed[name], "not sent yet")}
		fine = false
	}
	for _, name := range p.take {
		p.states[name] = hostSkillState{State: "failed", Error: firstNonEmpty(failed[name], "its change is not in the library yet")}
		fine = false
	}
	for name, msg := range failed {
		if _, ok := p.states[name]; !ok && mine[name] != "" {
			p.states[name] = hostSkillState{State: "failed", Error: msg}
		}
		fine = false
	}
	hs.Skills, hs.Extra, hs.Error, hs.At = p.states, p.extra, "", now()
	save()
	return fine, took
}

// skillSyncLoop keeps the library and the machines in step without anyone
// asking: this server's folders are looked at every two minutes (an agent
// may have written a skill), the machines when the library changed and
// every half hour (a skill may have been changed over there).
func (s *Server) skillSyncLoop(ctx context.Context) {
	s.shareSkills(ctx) // every login of a CLI shares one skills folder first
	var last map[string]string
	var checked, retryAt time.Time
	wait := 2 * time.Minute // until a machine that did not answer is tried again: 2, 4, 8 … 30 minutes
	first := time.After(20 * time.Second)
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		sc := s.libScopeFor(ctx, nil)
		libMu.Lock()
		if s.arrangeLocal(sc) {
			forgetSkills()
		}
		sums := librarySums(sc.dir)
		libMu.Unlock()
		changed := last != nil && !sameSums(sums, last)
		retry := !retryAt.IsZero() && time.Now().After(retryAt)
		if last != nil && (checked.IsZero() || changed || retry || time.Since(checked) > 30*time.Minute) {
			s.syncSkills(ctx, sc)
			checked = time.Now()
			switch {
			case !s.syncUnfinished(ctx, sc):
				retryAt, wait = time.Time{}, 2*time.Minute
			default:
				if changed {
					wait = 2 * time.Minute
				}
				retryAt = time.Now().Add(wait - 10*time.Second)
				if wait *= 2; wait > 30*time.Minute {
					wait = 30 * time.Minute
				}
			}
			libMu.Lock()
			sums = librarySums(sc.dir)
			libMu.Unlock()
		}
		last = sums
		select {
		case <-ctx.Done():
			return
		case <-first:
			first = nil
		case <-t.C:
		}
	}
}

// syncUnfinished: a machine did not answer or a skill could not be sent.
// Trying again may help; a skill that waits for the person's choice does not
// get better by itself.
func (s *Server) syncUnfinished(ctx context.Context, sc *libScope) bool {
	for _, hs := range s.loadSyncState(ctx, sc).Hosts {
		if !hs.On {
			continue
		}
		if hs.Error != "" {
			return true
		}
		for _, stt := range hs.Skills {
			if stt.State == "failed" {
				return true
			}
		}
	}
	return false
}

// syncOneHost brings one machine in line right now (after the person
// settled something there) and leaves the others to a sync in the
// background, so a machine that does not answer does not keep them waiting.
func (s *Server) syncOneHost(ctx context.Context, sc *libScope, h *store.Host) {
	run := syncRunOf(sc)
	run.mu.Lock()
	libMu.Lock()
	lib := librarySums(sc.dir)
	libMu.Unlock()
	var mu, takeMu sync.Mutex
	run.setBusy(h.ID, "checking")
	s.syncHost(ctx, sc, h, s.loadSyncState(ctx, sc), lib, &mu, &takeMu)
	run.setBusy(h.ID, "")
	run.mu.Unlock()
	s.kickSkillSync(sc, false)
}

func sameSums(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ---------- API ----------

func (s *Server) skillsSyncRoutes(r chi.Router) {
	r.Get("/skills/sync", s.handleSkillSync)
	r.Post("/skills/sync", s.handleSkillSyncNow)
	r.Put("/skills/sync/machines", s.handleSkillSyncMachines)
	r.Post("/skills/sync/resolve", s.handleSkillSyncResolve)
	r.Post("/skills/sync/adopt", s.handleSkillSyncAdopt)
}

type syncHostOut struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	On    bool   `json:"on"`
	At    string `json:"at,omitempty"`
	Error string `json:"error,omitempty"`
	// Busy: what is being done there right now ("checking", "sending x" …)
	Busy string `json:"busy,omitempty"`
	// Skills: how each library skill stands there (same | differs | off | failed)
	Skills map[string]hostSkillState `json:"skills"`
	Extra  []hostExtra               `json:"extra"`
}

func (s *Server) syncStatus(ctx context.Context, sc *libScope) map[string]any {
	st := s.loadSyncState(ctx, sc)
	busy := syncBusy(sc)
	hosts := []syncHostOut{}
	for _, h := range s.scopeHosts(ctx, sc) {
		o := syncHostOut{ID: h.ID, Name: h.Name, Busy: busy[h.ID], Skills: map[string]hostSkillState{}, Extra: []hostExtra{}}
		if hs := st.Hosts[h.ID]; hs != nil {
			o.On, o.At, o.Error = hs.On, hs.At, hs.Error
			if hs.On && hs.Skills != nil {
				o.Skills = hs.Skills
			}
			if hs.On && hs.Extra != nil {
				o.Extra = hs.Extra
			}
		}
		hosts = append(hosts, o)
	}
	log := st.Log
	if log == nil {
		log = []syncEvent{}
	}
	return map[string]any{"asked": st.Asked, "running": syncRunning(sc), "at": st.At, "hosts": hosts, "log": log}
}

// handleSkillSync is how the machines stand. A look that is older than five
// minutes starts a new check in the background (check=1: always).
func (s *Server) handleSkillSync(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	ctx := r.Context()
	st := s.loadSyncState(ctx, sc)
	at, _ := time.Parse(time.RFC3339, st.At)
	if r.URL.Query().Get("check") == "1" || (time.Since(at) > 5*time.Minute && !syncRunning(sc)) {
		for _, hs := range st.Hosts {
			if hs.On {
				s.kickSkillSync(sc, true)
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, s.syncStatus(ctx, sc))
}

func (s *Server) handleSkillSyncNow(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	s.kickSkillSync(sc, true)
	writeJSON(w, http.StatusAccepted, s.syncStatus(r.Context(), sc))
}

// handleSkillSyncMachines sets which machines follow the library. A machine
// switched off keeps the skills it has.
func (s *Server) handleSkillSyncMachines(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	var in struct {
		On []int64 `json:"on"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := r.Context()
	on := map[int64]bool{}
	for _, id := range in.On {
		on[id] = true
	}
	hosts := s.scopeHosts(ctx, sc)
	s.updateSyncState(ctx, sc, func(st *syncState) {
		st.Asked = true
		known := map[int64]bool{}
		for _, h := range hosts {
			known[h.ID] = true
			if st.Hosts[h.ID] == nil {
				st.Hosts[h.ID] = &hostSync{}
			}
			st.Hosts[h.ID].On = on[h.ID]
		}
		for id := range st.Hosts {
			if !known[id] {
				delete(st.Hosts, id) // a machine that was removed
			}
		}
	})
	s.kickSkillSync(sc, true)
	writeJSON(w, http.StatusOK, s.syncStatus(ctx, sc))
}

// handleLibraryMachines sets the machines a skill is kept off.
func (s *Server) handleLibraryMachines(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	name := chi.URLParam(r, "name")
	var in struct {
		Off []int64 `json:"off"`
	}
	if err := decode(r, &in); err != nil || !validSkill(name) {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if _, err := os.Stat(filepath.Join(sc.dir, name, "SKILL.md")); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ctx := r.Context()
	known := map[int64]bool{}
	for _, h := range s.scopeHosts(ctx, sc) {
		known[h.ID] = true
	}
	off := []int64{}
	for _, id := range in.Off {
		if known[id] {
			off = append(off, id)
		}
	}
	s.updateSyncState(ctx, sc, func(st *syncState) {
		if len(off) == 0 {
			delete(st.Off, name)
		} else {
			st.Off[name] = off
		}
	})
	s.kickSkillSync(sc, false)
	writeJSON(w, http.StatusOK, map[string]any{"off": off})
}

// syncHostFor is a machine of this library that is switched on.
func (s *Server) syncHostFor(ctx context.Context, sc *libScope, id int64) (*store.Host, *hostSync) {
	st := s.loadSyncState(ctx, sc)
	for _, h := range s.scopeHosts(ctx, sc) {
		if h.ID == id && st.Hosts[id] != nil && st.Hosts[id].On {
			return h, st.Hosts[id]
		}
	}
	return nil, nil
}

// handleSkillSyncResolve settles a skill that differs on a machine: use
// "library" (this server's version goes there; the other one to the trash
// over there) or "machine" (that version becomes the library's; the
// library's old one goes to the trash here).
func (s *Server) handleSkillSyncResolve(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	var in struct {
		HostID int64  `json:"host_id"`
		Name   string `json:"name"`
		Use    string `json:"use"`
	}
	if err := decode(r, &in); err != nil || !validSkill(in.Name) || (in.Use != "library" && in.Use != "machine") {
		writeErr(w, http.StatusBadRequest, "say which version to use")
		return
	}
	ctx := r.Context()
	h, hs := s.syncHostFor(ctx, sc, in.HostID)
	if h == nil {
		s.fail(w, errNoHost, "skills")
		return
	}
	stt := hs.Skills[in.Name]
	if stt.State != "differs" && stt.State != "failed" {
		writeErr(w, http.StatusConflict, "nothing to settle for that skill (any more)")
		return
	}
	run := syncRunOf(sc)
	run.mu.Lock()
	err := func() error {
		if in.Use == "machine" {
			where := firstNonEmpty(stt.Where, "lib")
			if _, err := s.takeSkill(ctx, sc, h, in.Name, where); err != nil {
				return err
			}
		}
		sum, err := s.pushSkill(ctx, sc, h, in.Name, true)
		if err != nil {
			return err
		}
		s.updateSyncState(ctx, sc, func(st *syncState) {
			if cur := st.Hosts[h.ID]; cur != nil {
				if cur.Pushed == nil {
					cur.Pushed = map[string]string{}
				}
				cur.Pushed[in.Name] = sum
			}
			what := "updated"
			if in.Use == "machine" {
				what = "taken"
			}
			st.Log = append([]syncEvent{{At: time.Now().UTC().Format(time.RFC3339), Host: h.Name, Skill: in.Name, What: what}}, st.Log...)
		})
		return nil
	}()
	run.mu.Unlock()
	if err != nil {
		s.fail(w, badRequest(h.Name+": "+err.Error()), "skills")
		return
	}
	s.syncOneHost(ctx, sc, h)
	writeJSON(w, http.StatusOK, s.syncStatus(ctx, sc))
}

// handleSkillSyncAdopt adds a skill that only a machine has to the library.
func (s *Server) handleSkillSyncAdopt(w http.ResponseWriter, r *http.Request) {
	sc := s.scopeOf(w, r)
	if sc == nil {
		return
	}
	var in struct {
		HostID int64  `json:"host_id"`
		Name   string `json:"name"`
	}
	if err := decode(r, &in); err != nil || !validSkill(in.Name) {
		writeErr(w, http.StatusBadRequest, "say which skill")
		return
	}
	ctx := r.Context()
	h, hs := s.syncHostFor(ctx, sc, in.HostID)
	if h == nil {
		s.fail(w, errNoHost, "skills")
		return
	}
	where := ""
	for _, e := range hs.Extra {
		if e.Name == in.Name {
			where = e.Where
		}
	}
	if where == "" {
		writeErr(w, http.StatusNotFound, "that skill is not on "+h.Name+" (any more)")
		return
	}
	if _, err := os.Lstat(filepath.Join(sc.dir, in.Name)); err == nil {
		writeErr(w, http.StatusConflict, "there is already a skill called "+in.Name+" in your library")
		return
	}
	run := syncRunOf(sc)
	run.mu.Lock()
	sum, err := s.takeSkill(ctx, sc, h, in.Name, where)
	if err == nil {
		// in a login folder there: put it in the shared one, as everywhere
		if where != "lib" {
			sum, err = s.pushSkill(ctx, sc, h, in.Name, true)
		}
	}
	if err == nil {
		s.updateSyncState(ctx, sc, func(st *syncState) {
			if cur := st.Hosts[h.ID]; cur != nil {
				if cur.Pushed == nil {
					cur.Pushed = map[string]string{}
				}
				cur.Pushed[in.Name] = sum
			}
			st.Log = append([]syncEvent{{At: time.Now().UTC().Format(time.RFC3339), Host: h.Name, Skill: in.Name, What: "taken"}}, st.Log...)
		})
	}
	run.mu.Unlock()
	if err != nil {
		s.fail(w, badRequest(h.Name+": "+err.Error()), "skills")
		return
	}
	s.syncOneHost(ctx, sc, h)
	writeJSON(w, http.StatusOK, s.syncStatus(ctx, sc))
}
