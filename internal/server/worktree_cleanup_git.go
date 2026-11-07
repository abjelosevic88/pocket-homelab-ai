package server

// Both phases verify the checkout identity. Never operate on the main
// checkout, a detached HEAD, an unregistered path, a locked worktree, or a
// checkout with an unfinished operation. Arguments: path, main, branch. An
// empty branch (inspection) accepts whatever branch an agent switched to;
// removal passes the branch inspection saw. Ignored files (.env copies,
// node_modules, build output) go with the folder, as with manual Archive.
const wtCleanupGuard = `cd -- "$1" 2>/dev/null || fail "Worktree folder is missing"
[ -f .git ] || fail "This is not a linked worktree"
top=$(git rev-parse --show-toplevel) || fail "Could not inspect the repository"
[ "$(pwd -P)" = "$(cd -- "$top" && pwd -P)" ] || fail "Worktree path is not the checkout root"
common=$(git rev-parse --git-common-dir) || fail "Could not find the repository"
common=$(cd -- "$common" && pwd -P) || fail "Could not find the repository"
main_common=$(git -C "$2" rev-parse --git-common-dir) || fail "Main repository is missing"
case "$main_common" in /*) ;; *) main_common="$2/$main_common" ;; esac
[ "$common" = "$(cd -- "$main_common" && pwd -P)" ] || fail "Worktree belongs to a different repository"
branch=$(git symbolic-ref --short -q HEAD) || fail "No branch is checked out"
[ -z "$3" ] || [ "$branch" = "$3" ] || fail "Checked-out branch changed"
gitdir=$(git rev-parse --absolute-git-dir) || fail "Could not inspect worktree registration"
[ -f "$gitdir/gitdir" ] || fail "Worktree registration is missing"
[ "$(cat "$gitdir/gitdir")" = "$top/.git" ] || fail "Worktree registration changed"
[ ! -f "$gitdir/locked" ] || fail "Worktree is locked"
for marker in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD rebase-merge rebase-apply sequencer BISECT_LOG; do
 [ ! -e "$(git rev-parse --git-path "$marker")" ] || fail "A Git operation is in progress"
done
flags=$(git ls-files -v) || fail "Could not inspect index flags"
if printf '%s\n' "$flags" | grep -q '^[a-zS]'; then fail "Index flags can hide local changes; review this worktree manually"; fi
status=$(git status --porcelain --untracked-files=all) || fail "Could not inspect local files"
[ -z "$status" ] || fail "Uncommitted or untracked files"
head=$(git rev-parse --verify HEAD) || fail "Could not inspect the current commit"
`

// Always fetch the actual remote target, without moving the main checkout
// or trusting stale origin/* refs. PR lookup discovers branches pushed by
// agents too, whether or not Pocket AI previously saved their PR URL.
const wtCleanupInspect = gitEnv + `fail() { printf 'reason=%s\n' "$1"; exit 0; }
` + wtCleanupGuard + `echo "head=$head"
echo "branch=$branch"
base=$4
case "$base" in refs/heads/*) base=${base#refs/heads/} ;; refs/remotes/origin/*) base=${base#refs/remotes/origin/} ;; origin/*) base=${base#origin/} ;; esac
git check-ref-format "refs/heads/$base" >/dev/null 2>&1 || fail "Base is not a branch"
[ "$branch" != "$base" ] || fail "This worktree is on the base branch"
default=$(git symbolic-ref -q refs/remotes/origin/HEAD 2>/dev/null)
[ "$default" != "refs/remotes/origin/$branch" ] || fail "This worktree is on the repository default branch"
echo "base=$base"
origin=$(git remote get-url origin 2>/dev/null)
if [ -n "$origin" ]; then
 ref="refs/pocket-ai/cleanup-base/$6"
 git fetch --no-tags --no-write-fetch-head origin "+refs/heads/$base:$ref" >&2 || fail "Could not refresh the remote base branch"
 target=$(git rev-parse --verify "$ref^{commit}") || fail "Could not inspect the remote base"
 git update-ref -d "$ref" "$target" || fail "Could not finish checking the remote base"
else
 target=$(git rev-parse --verify "refs/heads/$base^{commit}") || fail "Base branch is missing"
fi
if [ "$head" != "$5" ] && git merge-base --is-ancestor "$head" "$target"; then echo "merged=1"; exit 0; fi
# Patch-equivalence can discard intentionally empty or merge-only commits.
# For squash/rebase merges require the exact branch head reported by the PR.
if [ -n "$origin" ] && command -v gh >/dev/null 2>&1; then
 prs=$(gh pr list --repo "$origin" --head "$branch" --base "$base" --state merged --limit 100 --json state,headRefOid,headRefName,baseRefName,url,isCrossRepository 2>/dev/null) && printf 'prs=%s\n' "$prs" || echo "pr_error=1"
else echo "pr_error=1"; fi
exit 0
`

// Expected HEAD is supplied by the inspection, and compared again before
// removal. The recovery ref is durable even if a later step fails. No force
// removal; compare-and-delete the branch so a concurrent commit survives.
const wtCleanupRemove = gitEnv + `fail() { printf '%s\n' "$1" >&2; exit 1; }
` + wtCleanupGuard + `[ "$head" = "$4" ] || fail "New commits appeared after the merge check"
git update-ref "$5" "$head" || fail "Could not save the recovery reference"
cd -- "$2" || fail "Main repository is missing"
git worktree remove -- "$1" >&2 || fail "Git refused to remove the worktree"
if [ "$6" = 1 ]; then
 # git branch -D has no expected-old argument. update-ref does, but needs
 # an explicit check that another checkout has not taken the branch.
 if ! git worktree list --porcelain | grep -Fqx "branch refs/heads/$3"; then
  git update-ref -d "refs/heads/$3" "$4" && echo "branch_deleted=1"
 fi
fi
exit 0
`
