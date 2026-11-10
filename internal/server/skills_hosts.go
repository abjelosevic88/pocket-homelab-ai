package server

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// Your own skills on the other machines: which of them each machine has (in
// any login folder of Claude Code / Codex there) and copying one over. A chat
// on a machine only sees the skills of that machine.

type hostSkillCopy struct {
	Home string `json:"home"` // the login folder over there (~/.claude, ~/.claude-max …)
	Sum  string `json:"sum"`  // cksum of its SKILL.md, compared with ownSkill.Sum
}

type hostSkills struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
	// per CLI: its login folders there, and each skill's copies
	Homes  map[string][]string                   `json:"homes"`
	Skills map[string]map[string][]hostSkillCopy `json:"skills"`
}

// hostSkillsSh prints the login folders ("H <cli> <folder>") and their skills
// ("S <cli> <folder> <name> <cksum> <size>") of both CLIs.
const hostSkillsSh = agent.RemoteHomesSh + `for p in claude codex; do
  homes "$p" | while IFS= read -r h; do
    [ -d "$h" ] || continue
    printf 'H\t%s\t%s\n' "$p" "$h"
    for f in "$h"/skills/*/SKILL.md; do
      [ -f "$f" ] || continue
      n=$(basename "$(dirname "$f")")
      set -- $(cksum < "$f")
      printf 'S\t%s\t%s\t%s\t%s %s\n' "$p" "$h" "$n" "$1" "$2"
    done
  done
done
`

func (s *Server) handleSkillsOnHosts(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	hosts, err := s.hostsFor(ctx, userFrom(ctx))
	if err != nil {
		s.fail(w, err, "skills")
		return
	}
	out := make([]hostSkills, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		out[i] = hostSkills{ID: h.ID, Name: h.Name, Homes: map[string][]string{}, Skills: map[string]map[string][]hostSkillCopy{"claude": {}, "codex": {}}}
		wg.Add(1)
		go func(hs *hostSkills, rem *agent.Remote) {
			defer wg.Done()
			stdout, stderr, err := rem.Run(ctx, 15*time.Second, hostSkillsSh)
			if err != nil {
				hs.Error = firstNonEmpty(lastLine(stderr), "not reachable")
				return
			}
			for _, l := range strings.Split(stdout, "\n") {
				f := strings.Split(l, "\t")
				switch {
				case len(f) == 3 && f[0] == "H" && hs.Skills[f[1]] != nil:
					hs.Homes[f[1]] = append(hs.Homes[f[1]], f[2])
				case len(f) == 5 && f[0] == "S" && hs.Skills[f[1]] != nil && skillName.MatchString(f[3]) && !skillsReserved[f[3]]:
					hs.Skills[f[1]][f[3]] = append(hs.Skills[f[1]][f[3]], hostSkillCopy{Home: f[2], Sum: f[4]})
				}
			}
		}(&out[i], remoteOf(h))
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// handleCopySkillToHost copies one of your skills (provider + account name
// the copy on this server) into every login folder of that CLI on a machine.
// A different copy already there goes to .pocket-ai-trash next to it.
func (s *Server) handleCopySkillToHost(w http.ResponseWriter, r *http.Request) {
	from, err := s.ownSkillsDir(r)
	if err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	provider := r.URL.Query().Get("provider")
	var in struct {
		HostID int64 `json:"host_id"`
	}
	if err := decode(r, &in); err != nil || in.HostID == 0 {
		writeErr(w, http.StatusBadRequest, "say which machine")
		return
	}
	ctx := r.Context()
	h, err := s.checkHostUse(ctx, userFrom(ctx), in.HostID)
	if err != nil || h == nil {
		s.fail(w, firstErr(err, errNoHost), "copy skill")
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
	pack, err := tarSkill(filepath.Join(from, name))
	if err != nil {
		s.fail(w, err, "copy skill")
		return
	}
	// unpack once into the first folder, then copy to the others
	script := agent.RemoteHomesSh + `p=$1; n=$2; t=$(date +%Y%m%d-%H%M%S); first=
homes "$p" > "${TMPDIR:-/tmp}/pocket-skill-homes.$$" || exit 1
while IFS= read -r h; do
  [ -d "$h" ] || continue
  mkdir -p "$h/skills" || exit 1
  if [ -e "$h/skills/$n" ]; then mkdir -p "$h/.pocket-ai-trash/skills" && mv "$h/skills/$n" "$h/.pocket-ai-trash/skills/$n-$t" || exit 1; fi
  if [ -z "$first" ]; then
    rm -rf "$h/skills/.$n.copying" && mkdir "$h/skills/.$n.copying" && tar -x -f - -C "$h/skills/.$n.copying" && mv "$h/skills/.$n.copying" "$h/skills/$n" || exit 1
    first="$h/skills/$n"
  else
    cp -R "$first" "$h/skills/$n" || exit 1
  fi
  printf '%s\n' "$h"
done < "${TMPDIR:-/tmp}/pocket-skill-homes.$$"
rm -f "${TMPDIR:-/tmp}/pocket-skill-homes.$$"
[ -n "$first" ] || { echo "no $p login there" >&2; exit 1; }
`
	stdout, stderr, err := remoteOf(h).RunInput(ctx, 30*time.Second, bytes.NewReader(pack), script, provider, name)
	if err != nil {
		s.fail(w, badRequest("could not copy to "+h.Name+": "+firstNonEmpty(lastLine(stderr), err.Error())), "copy skill")
		return
	}
	forgetSkills()
	s.log.Info("skill copied to a machine", "skill", name, "provider", provider, "host", h.Name)
	writeJSON(w, http.StatusOK, map[string]any{"homes": strings.Fields(stdout)})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// tarSkill packs a skill folder: folders and regular files only, so a copy
// never reaches outside the skill.
func tarSkill(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
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

// cksum is POSIX cksum(1) ("<crc> <size>"), so a copy here and one over there
// (where only the cksum tool is sure to be) can be compared.
func cksum(data []byte) string {
	var crc uint32
	step := func(b byte) {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	for _, b := range data {
		step(b)
	}
	for n := len(data); n > 0; n >>= 8 {
		step(byte(n))
	}
	return strconv.FormatUint(uint64(^crc), 10) + " " + strconv.Itoa(len(data))
}
