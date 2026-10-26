package server

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The git side of worktrees. Every operation is one POSIX sh script with
// arguments, run the same way here (sh -c) and on another machine (ssh), so
// what is tested locally is what runs remotely. Scripts print key=value
// lines; errors go to stderr. Nothing here ever resets, stashes or deletes
// uncommitted work: archiving first saves it as a commit under refs/pocket-ai/.

const gitEnv = `export GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 LC_ALL=C
`

// shOn runs a sh script here (h nil) or on h; $1… are args.
func shOn(ctx context.Context, h *store.Host, timeout time.Duration, script string, args ...string) (string, string, error) {
	if h != nil {
		return remoteOf(h).Run(ctx, timeout, script, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Env = os.Environ()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), strings.TrimSpace(errb.String()), err
}

// kv parses key=value lines; repeated keys are joined with "\n".
func kv(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		if old, dup := m[k]; dup {
			m[k] = old + "\n" + v
		} else {
			m[k] = v
		}
	}
	return m
}

// scriptErr turns a failed script into a message for the person.
func scriptErr(stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 1500 {
		msg = msg[len(msg)-1500:]
	}
	return badRequest(msg)
}

// ---------- inspect ----------

const wtInspect = gitEnv + `cd -- "$1" 2>/dev/null || { echo "err=the folder $1 does not exist"; exit 0; }
top=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "repo=0"; exit 0; }
common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
case "$common" in */.git) main=${common%/.git};; *) main=$top;; esac
echo "repo=1"
echo "top=$top"
echo "main=$main"
echo "rel=$(git rev-parse --show-prefix)"
echo "branch=$(git symbolic-ref --short -q HEAD)"
echo "mainbranch=$(git -C "$main" symbolic-ref --short -q HEAD 2>/dev/null)"
echo "originhead=$(git symbolic-ref --short -q refs/remotes/origin/HEAD 2>/dev/null)"
echo "origin=$(git remote get-url origin 2>/dev/null)"
echo "home=$HOME"
`

type repoInfo struct {
	Repo       bool   `json:"repo"`
	Top        string `json:"top"`         // this checkout's top folder
	Main       string `json:"main"`        // the main checkout's top folder
	Rel        string `json:"rel"`         // the folder inside the checkout ("" or "web/")
	Branch     string `json:"branch"`      // checked out in this folder
	MainBranch string `json:"main_branch"` // checked out in the main checkout
	Default    string `json:"default"`     // origin's default branch (main)
	ProjectID  string `json:"project_id,omitempty"`
	Home       string `json:"-"`
}

func inspectRepo(ctx context.Context, h *store.Host, dir string) (repoInfo, error) {
	out, errOut, err := shOn(ctx, h, 20*time.Second, wtInspect, dir)
	if err != nil {
		return repoInfo{}, scriptErr(errOut, err)
	}
	m := kv(out)
	if m["err"] != "" {
		return repoInfo{}, badRequest(m["err"])
	}
	return repoInfo{Repo: m["repo"] == "1", Top: m["top"], Main: m["main"], Rel: m["rel"], Branch: m["branch"], MainBranch: m["mainbranch"],
		Default: strings.TrimPrefix(m["originhead"], "origin/"), ProjectID: gitProjectID(m["origin"]), Home: m["home"]}, nil
}

// Match HTTPS, ssh:// and scp-style origins without returning credentials,
// query strings or fragments. Local filesystem remotes stay machine-specific.
func gitProjectID(remote string) string {
	remote = strings.TrimSpace(remote)
	var host, repo string
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" || u.Scheme == "file" {
			return ""
		}
		host, repo = strings.ToLower(u.Hostname()), u.Path
		if port := u.Port(); port != "" && port != "22" && port != "443" && port != "80" && port != "9418" {
			host += ":" + port
		}
	} else if i := strings.IndexByte(remote, ':'); i > 0 && !strings.ContainsAny(remote[:i], "/\\") {
		host, repo = remote[:i], remote[i+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		host = strings.ToLower(host)
	} else {
		return ""
	}
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	if host == "" || repo == "" {
		return ""
	}
	if host == "github.com" {
		repo = strings.ToLower(repo)
	}
	return host + "/" + repo
}

// ---------- create ----------

// wtAdd makes the worktree: $1 main checkout, $2 folder for worktrees (may
// start with ~/), $3 name (folder and, for a new branch, branch), $4 base,
// $5 new|branch|pr, $6 the existing branch (branch) or PR number (pr). A
// taken name gets -2, -3…; a branch already checked out somewhere is not
// forked silently: "inuse=<folder>" says where it is.
const wtAdd = gitEnv + `cd -- "$1" || exit 3
dir=$2
case "$dir" in "~/"*) dir="$HOME/${dir#\~/}";; esac
mode=$5
if [ "$mode" = pr ]; then
  remote=origin
  git remote get-url "$remote" >/dev/null 2>&1 || { echo "this repository has no origin to fetch the pull request from" >&2; exit 4; }
  if command -v gh >/dev/null 2>&1; then
    pb=$(gh pr view "$6" --json baseRefName -q .baseRefName 2>/dev/null) && [ -n "$pb" ] && echo "base=$pb"
  fi
  inuse=$(git worktree list --porcelain | awk -v b="branch refs/heads/pr-$6" '/^worktree /{p=substr($0,10)} $0==b{print p; exit}')
  [ -n "$inuse" ] && { echo "inuse=$inuse"; exit 0; }
  git fetch -q "$remote" "+pull/$6/head:refs/heads/pr-$6" >&2 || { echo "could not fetch pull request #$6" >&2; exit 5; }
  set -- "$1" "$dir" "$3" "${pb:-$4}" branch "pr-$6"
  mode=branch
fi
taken() { [ -e "$dir/$1" ] || { [ "$mode" = new ] && git show-ref --verify -q "refs/heads/$1"; }; }
slug=$3; n=1
while taken "$slug"; do n=$((n+1)); slug="$3-$n"; done
p="$dir/$slug"
if [ "$mode" = branch ]; then
  b=$6
  inuse=$(git worktree list --porcelain | awk -v b="branch refs/heads/$b" '/^worktree /{p=substr($0,10)} $0==b{print p; exit}')
  [ -n "$inuse" ] && { echo "inuse=$inuse"; exit 0; }
  mkdir -p -- "$dir" || exit 7
  if git show-ref --verify -q "refs/heads/$b"; then git worktree add -q "$p" "$b" >&2 || exit 8
  elif git show-ref --verify -q "refs/remotes/origin/$b"; then git worktree add -q --track -b "$b" "$p" "origin/$b" >&2 || exit 8
  else echo "there is no branch called $b here" >&2; exit 5; fi
else
  b=$slug
  ref=$4
  git show-ref --verify -q "refs/heads/$ref" || { git show-ref --verify -q "refs/remotes/origin/$ref" && ref="origin/$ref"; }
  sha=$(git rev-parse --verify -q "$ref^{commit}") || { echo "there is no branch or commit called $4 here" >&2; exit 5; }
  mkdir -p -- "$dir" || exit 7
  git worktree add -q --no-track -b "$b" "$p" "$sha" >&2 || exit 8
fi
echo "branch=$b"
echo "sha=$(git -C "$p" merge-base HEAD "$4" 2>/dev/null || git -C "$p" rev-parse HEAD)"
echo "path=$(cd -- "$p" && pwd -P)"
`

// wtCopy copies files that are not in git (".env" and the like) from the
// main checkout into the new worktree: $1 main, $2 worktree, $3… patterns
// (git pathspec globs, relative to the repo). Only plain files up to 5 MB,
// at most 200: never folders — a pattern like **/.env would otherwise drag in
// whole ignored folders (node_modules, Claude Code's .claude/worktrees with
// other checkouts in it) that merely might contain one.
const wtCopy = gitEnv + `src=$1; dst=$2; shift 2
cd -- "$src" || exit 3
list() {
  git ls-files --others --exclude-standard "$@" -- "$pat" ':(exclude,glob)**/node_modules/**' ':(exclude,glob).claude/worktrees/**' \
    ':(exclude,glob)**/.git/**' ':(exclude,glob).pocket-ai/**' 2>/dev/null
}
for p in "$@"; do pat=":(glob)$p"; list --ignored; list; done | sort -u | head -n 200 | while IFS= read -r f; do
  case "$f" in ""|*/) continue;; esac
  [ -f "$f" ] && [ ! -L "$f" ] || continue
  [ "$(wc -c < "$f")" -le 5242880 ] || { echo "skipped=$f"; continue; }
  [ -e "$dst/$f" ] && continue
  mkdir -p -- "$dst/$(dirname -- "$f")" && cp -p -- "$f" "$dst/$f" && echo "copied=$f"
done
`

// ---------- status ----------

// wtStatus: $1 worktree, $2 base branch, $3 pull request URL (may be
// empty). "Merged" is any of: its commits are in base, each of its commits
// has an equivalent patch in base (rebased / cherry-picked), or the pull
// request was merged (squash merges on the forge). Commits on the branch follow as
// "c=<short sha>\t<unix time>\t<subject>".
const wtStatus = gitEnv + `cd -- "$1" 2>/dev/null || { echo "gone=1"; exit 0; }
echo "head=$(git rev-parse HEAD 2>/dev/null)"
echo "branch=$(git symbolic-ref --short -q HEAD)"
git rev-parse --verify -q "$2^{commit}" >/dev/null || { echo "nobase=1"; echo "dirty=$(git status --porcelain --untracked-files=normal | wc -l)"; exit 0; }
counts=$(git rev-list --left-right --count "$2...HEAD" 2>/dev/null)
echo "counts=$counts"
if git merge-base --is-ancestor HEAD "$2" 2>/dev/null; then echo "inbase=1"
elif [ "${counts##*[!0-9]}" -gt 0 ] 2>/dev/null && [ -z "$(git cherry "$2" HEAD 2>/dev/null | grep '^+')" ]; then echo "inbase=1"; echo "equivalent=1"; fi
if [ -n "$3" ] && command -v gh >/dev/null 2>&1; then echo "prstate=$(gh pr view "$3" --json state -q .state 2>/dev/null)"; fi
echo "dirty=$(git status --porcelain --untracked-files=normal | wc -l)"
up=$(git rev-parse --abbrev-ref -q '@{upstream}' 2>/dev/null) && { echo "upstream=$up"; echo "unpushed=$(git rev-list --count '@{upstream}..HEAD' 2>/dev/null)"; }
git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1 && echo "merging=1"
[ -d "$(git rev-parse --git-path rebase-merge)" ] || [ -d "$(git rev-parse --git-path rebase-apply)" ] && echo "rebasing=1"
git diff --name-only --diff-filter=U 2>/dev/null | sed 's/^/conflict=/'
git log --format='c=%h%x09%ct%x09%s' "$2..HEAD" -n 60 2>/dev/null
`

type wtCommit struct {
	SHA     string `json:"sha"`
	Time    int64  `json:"time"`
	Subject string `json:"subject"`
}

type wtStatusInfo struct {
	Gone      bool       `json:"gone"`               // the folder is not there (archived or removed by hand)
	NoBase    bool       `json:"no_base"`            // the base branch was deleted
	Head      string     `json:"head"`               // current commit
	Branch    string     `json:"branch"`             // checked out ("" = detached)
	Ahead     int        `json:"ahead"`              // commits not in base
	Behind    int        `json:"behind"`             // base commits not here
	Dirty     int        `json:"dirty"`              // changed + new files
	Merged    bool       `json:"merged"`             // its work is in base (or its pull request was merged)
	PRState   string     `json:"pr_state,omitempty"` // OPEN | MERGED | CLOSED (gh)
	Upstream  string     `json:"upstream"`           // pushed to (origin/x)
	Unpushed  int        `json:"unpushed"`           // commits not pushed yet
	Merging   bool       `json:"merging"`            // a merge is in progress (conflicts?)
	Rebasing  bool       `json:"rebasing"`           // a rebase is in progress
	Conflicts []string   `json:"conflicts"`          // unmerged files
	Commits   []wtCommit `json:"commits"`            // on the branch, newest first
	Error     string     `json:"error,omitempty"`    // could not look
	SizeKB    int64      `json:"size_kb,omitempty"`
}

func worktreeStatus(ctx context.Context, h *store.Host, w *store.Worktree) wtStatusInfo {
	if w.Status != "active" {
		return wtStatusInfo{Gone: true, Commits: []wtCommit{}, Conflicts: []string{}}
	}
	out, errOut, err := shOn(ctx, h, 25*time.Second, wtStatus, w.Path, w.Base, w.PRURL)
	st := wtStatusInfo{Commits: []wtCommit{}, Conflicts: []string{}}
	if err != nil {
		st.Error = scriptErr(errOut, err).Error()
		return st
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "gone":
			st.Gone = true
		case "nobase":
			st.NoBase = true
		case "head":
			st.Head = v
		case "branch":
			st.Branch = v
		case "counts":
			if f := strings.Fields(v); len(f) == 2 {
				st.Behind, _ = strconv.Atoi(f[0])
				st.Ahead, _ = strconv.Atoi(f[1])
			}
		case "inbase":
			st.Merged = true
		case "dirty":
			st.Dirty, _ = strconv.Atoi(strings.TrimSpace(v))
		case "upstream":
			st.Upstream = v
		case "unpushed":
			st.Unpushed, _ = strconv.Atoi(strings.TrimSpace(v))
		case "prstate":
			st.PRState = v
		case "merging":
			st.Merging = true
		case "rebasing":
			st.Rebasing = true
		case "conflict":
			st.Conflicts = append(st.Conflicts, v)
		case "c":
			if p := strings.SplitN(v, "\t", 3); len(p) == 3 {
				t, _ := strconv.ParseInt(p[1], 10, 64)
				st.Commits = append(st.Commits, wtCommit{SHA: p[0], Time: t, Subject: p[2]})
			}
		}
	}
	// "merged" only once it made commits of its own that base now has
	st.Merged = st.Merged && st.Head != "" && st.Head != w.BaseSHA || st.PRState == "MERGED"
	return st
}

// ---------- changes ----------

// wtDiff: $1 worktree, $2 base. Everything the task changed: committed on
// the branch plus uncommitted, against where it forked from base; new files
// as additions. Capped so a generated file can't flood the page.
const wtDiff = gitEnv + `cd -- "$1" || exit 3
mb=$(git merge-base "$2" HEAD 2>/dev/null) || mb=HEAD
git diff -M --no-color --no-ext-diff "$mb" -- 2>/dev/null | head -c 3000000
git ls-files --others --exclude-standard | head -n 200 | while IFS= read -r f; do
  [ -f "$f" ] || continue
  git diff --no-color --no-index -- /dev/null "$f" 2>/dev/null | head -c 200000
done
`

func worktreeDiff(ctx context.Context, h *store.Host, w *store.Worktree) (string, error) {
	out, errOut, err := shOn(ctx, h, 40*time.Second, wtDiff, w.Path, w.Base)
	if err != nil {
		return "", scriptErr(errOut, err)
	}
	return out, nil
}

// ---------- commit, update, merge, push ----------

// wtCommitAll: $1 worktree, $2 message.
const wtCommitAll = gitEnv + `cd -- "$1" || exit 3
git add -A || exit 4
git diff --cached --quiet && { echo "nothing to commit" >&2; exit 5; }
git commit -q -m "$2" >&2 || exit 6
echo "head=$(git rev-parse --short HEAD)"
`

// wtUpdate brings base into the branch: $1 worktree, $2 base, $3 merge|rebase.
// On conflicts it stops and leaves them for the agent (or the person) to
// resolve; "abort" undoes that.
const wtUpdate = gitEnv + `cd -- "$1" || exit 3
if [ "$3" = abort ]; then
  git rev-parse -q --verify MERGE_HEAD >/dev/null && { git merge --abort >&2; exit $?; }
  git rebase --abort >&2 2>/dev/null; exit 0
fi
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "commit or discard the uncommitted changes first" >&2; exit 4; }
if [ "$3" = rebase ]; then
  git rebase "$2" >&2 && exit 0
else
  git merge --no-edit "$2" >&2 && exit 0
fi
git diff --name-only --diff-filter=U | sed 's/^/conflict=/'
exit 0
`

// wtMerge lands the branch on base without touching any checkout's files
// except by fast-forwarding where base is checked out (git refuses if that
// would overwrite local changes). $1 main checkout, $2 branch, $3 base,
// $4 merge|squash, $5 message. The merge is computed with merge-tree.
const wtMerge = gitEnv + `cd -- "$1" || exit 3
head=$(git rev-parse --verify -q "refs/heads/$2") || { echo "the branch $2 is gone" >&2; exit 4; }
old=$(git rev-parse --verify -q "refs/heads/$3") || { echo "$3 is not a local branch here" >&2; exit 5; }
git merge-base --is-ancestor "$head" "$old" && { echo "already=1"; exit 0; }
if [ "$4" != squash ] && git merge-base --is-ancestor "$old" "$head"; then
  new=$head; echo "how=fast-forward"
else
  out=$(git merge-tree --write-tree --name-only --no-messages "$old" "$head") || {
    echo "$out" | tail -n +2 | sed 's/^/conflict=/'
    exit 0
  }
  tree=$(echo "$out" | head -n 1)
  if [ "$4" = squash ]; then
    new=$(git commit-tree "$tree" -p "$old" -m "$5") || exit 6; echo "how=squash"
  else
    new=$(git commit-tree "$tree" -p "$old" -p "$head" -m "$5") || exit 6; echo "how=merge"
  fi
fi
wt=$(git worktree list --porcelain | awk -v b="branch refs/heads/$3" '/^worktree /{p=substr($0,10)} $0==b{print p; exit}')
if [ -n "$wt" ]; then
  git -C "$wt" merge -q --ff-only "$new" >&2 || { echo "$3 is checked out in $wt and its local changes are in the way; commit or stash them there first" >&2; exit 7; }
else
  git update-ref "refs/heads/$3" "$new" "$old" || exit 8
fi
echo "new=$(git rev-parse --short "$new")"
`

// wtPush: $1 worktree, $2 branch, $3 base, $4 make a PR (1/0), $5 title, $6 body.
const wtPush = gitEnv + `cd -- "$1" || exit 3
remote=$(git config "branch.$3.remote" 2>/dev/null); [ -n "$remote" ] || remote=origin
git remote get-url "$remote" >/dev/null 2>&1 || { echo "this repository has no remote to push to" >&2; exit 4; }
git push -q -u "$remote" "HEAD:refs/heads/$2" >&2 || exit 5
echo "remote_url=$(git remote get-url "$remote")"
[ "$4" = 1 ] || exit 0
if command -v gh >/dev/null 2>&1; then
  url=$(gh pr view "$2" --json url -q .url 2>/dev/null)
  [ -n "$url" ] || url=$(gh pr create --base "$3" --head "$2" --title "$5" --body "$6" 2>&1 | grep -Eo 'https://[^ ]+/pull/[0-9]+' | tail -n 1)
  [ -n "$url" ] && echo "pr=$url"
fi
exit 0
`

// compareURL is the web page to open a pull request when gh is not there.
func compareURL(remote, base, branch string) string {
	u := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(u, "git@"):
		host, path, ok := strings.Cut(strings.TrimPrefix(u, "git@"), ":")
		if !ok {
			return ""
		}
		u = "https://" + host + "/" + path
	case strings.HasPrefix(u, "ssh://"):
		u = strings.TrimPrefix(u, "ssh://")
		if i := strings.Index(u, "@"); i >= 0 {
			u = u[i+1:]
		}
		if host, rest, ok := strings.Cut(u, "/"); ok {
			if h, _, ok := strings.Cut(host, ":"); ok {
				host = h // ssh port, not the web one
			}
			u = "https://" + host + "/" + rest
		}
	case strings.HasPrefix(u, "https://"):
		if i := strings.Index(u, "@"); i >= 0 && i < strings.Index(u[8:], "/")+8 {
			u = "https://" + u[i+1:] // drop credentials
		}
	default:
		return ""
	}
	if strings.Contains(u, "gitlab") {
		return u + "/-/merge_requests/new?merge_request[source_branch]=" + branch + "&merge_request[target_branch]=" + base
	}
	return u + "/compare/" + base + "..." + branch + "?expand=1"
}

// ---------- archive, restore, delete ----------

// wtArchive saves everything in the worktree (commits + uncommitted, not
// ignored files) under $3 (refs/pocket-ai/archive/<id>), then removes the
// folder. $1 worktree, $2 main checkout, $3 ref, $4 branch, $5 delete the
// branch too (1 only when it was merged).
const wtArchive = gitEnv + `if cd -- "$1" 2>/dev/null; then
  tmp=$(mktemp) || exit 3
  cp -- "$(git rev-parse --git-path index)" "$tmp" 2>/dev/null
  GIT_INDEX_FILE=$tmp git add -A >&2 && tree=$(GIT_INDEX_FILE=$tmp git write-tree) || { rm -f "$tmp"; echo "could not save the uncommitted work; nothing was removed" >&2; exit 4; }
  rm -f "$tmp"
  if [ "$tree" = "$(git rev-parse 'HEAD^{tree}')" ]; then c=$(git rev-parse HEAD)
  else c=$(git commit-tree "$tree" -p HEAD -m "Pocket AI: uncommitted work in $4 when it was archived") || exit 5; fi
  git update-ref "$3" "$c" || exit 6
  echo "saved=$c"
fi
cd -- "$2" || exit 7
if [ -e "$1" ]; then git worktree remove --force --force -- "$1" >&2 || exit 8; fi
git worktree prune
if [ "$5" = 1 ]; then git branch -D -- "$4" >&2 && echo "branch_deleted=1"; fi
exit 0
`

// wtRestore puts an archived worktree back where it was: $1 main, $2 folder,
// $3 branch, $4 archive ref. The uncommitted work comes back uncommitted.
const wtRestore = gitEnv + `cd -- "$1" || exit 3
[ -e "$2" ] && { echo "$2 already exists" >&2; exit 4; }
mkdir -p -- "$(dirname -- "$2")" || exit 5
saved=$(git rev-parse --verify -q "$4^{commit}")
if git show-ref --verify -q "refs/heads/$3"; then git worktree add -q "$2" "$3" >&2 || exit 6
elif [ -n "$saved" ]; then
  start=$saved; [ "$(git log -1 --format=%s "$saved")" = "Pocket AI: uncommitted work in $3 when it was archived" ] && start="$saved^"
  git worktree add -q -b "$3" "$2" "$start" >&2 || exit 6
else echo "the branch $3 and its archive are both gone" >&2; exit 7; fi
if [ -n "$saved" ] && [ "$(git -C "$2" rev-parse HEAD)" != "$saved" ]; then
  git -C "$2" checkout -q "$saved" -- . >&2 && git -C "$2" reset -q >&2
fi
echo "path=$(cd -- "$2" && pwd -P)"
`

// wtForget deletes an archived worktree for good: $1 main, $2 ref, $3 branch, $4 delete branch.
const wtForget = gitEnv + `cd -- "$1" || exit 3
git update-ref -d "$2" 2>/dev/null
if [ "$4" = 1 ]; then git branch -D -- "$3" >&2 2>/dev/null; fi
git worktree prune
exit 0
`

// wtSize: $1… folders → "size=<kb>\t<folder>".
const wtSize = `for d in "$@"; do [ -d "$d" ] && printf 'size=%s\t%s\n' "$(du -sk -- "$d" 2>/dev/null | cut -f1)" "$d"; done; exit 0
`
