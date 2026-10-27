# How Pocket AI talks to the CLIs

One live process per active thread, started on the first message and stopped after `IDLE_TIMEOUT`; the next
message starts a new process that resumes the same CLI session. Everything a process says is turned into
**items** (user, assistant, thinking, tool, todos, request, notice, turn) with a stable key per thread, stored in
SQLite and fanned out to the browser over SSE (`item` = insert/replace, `delta` = append text while streaming).

## Claude Code

`claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages
--permission-prompt-tool stdio --permission-mode <mode> [--model] [--effort] [--resume <id> [--fork-session]]
[--append-system-prompt]` — the same control protocol the Claude Agent SDK uses:

- `control_request {subtype: initialize}` on start; `{subtype: interrupt}` for Stop.
- User messages are `{"type":"user","message":{"role":"user","content":[…image blocks…, text]}}`.
- `stream_event` lines stream text and thinking (`content_block_delta`); the final `assistant` message replaces
  the streamed block under the same key.
- `control_request {subtype: can_use_tool}` becomes a request card. The answer is a `control_response` with
  `behavior: allow` (+ `updatedPermissions` from the CLI's own suggestions for "allow for session") or
  `behavior: deny` with a message. `AskUserQuestion` is answered with `updatedInput.answers`; `ExitPlanMode` is the
  plan card.
- `result` ends the turn (`total_cost_usd` adds up per process, so each turn records the difference);
  `rate_limit_event` feeds the limits on the usage page.

## Codex

`codex app-server` (JSON-RPC over stdio): `initialize` → `thread/start` / `thread/resume` / `thread/fork` with the
cwd, approval policy and sandbox for the mode → `turn/start` with text + `localImage` inputs → `turn/interrupt`.
`item/started` / `item/completed` (agent messages, reasoning, command executions, file changes, MCP calls, web
searches) map to items; `item/*/delta` stream; `turn/plan/updated` is the to-do list; `thread/tokenUsage/updated`
and `account/rateLimits/updated` feed usage. Server requests `item/commandExecution/requestApproval`,
`item/fileChange/requestApproval`, `item/permissions/requestApproval` and `item/tool/requestUserInput` become
request cards. Notifications are dispatched off the reader goroutine so a handler can make calls (starting the next
queued turn from `turn/completed`).

History: Claude Code sessions are read from `~/.claude/projects/*/*.jsonl`; Codex threads come from
`thread/list` and `thread/read`.

## Other machines (hosts)

A thread can run on another machine (`threads.host_id`, table `hosts`; 0 = this server). Pocket AI starts the
same command there over ssh — `ssh -T -o BatchMode=yes -o StrictHostKeyChecking=accept-new … <target> --
<command>` with this server's keys and `~/.ssh/config` — and both stdio protocols go through unchanged. The
remote command is a POSIX `sh` script sent as base64 (`exec /bin/sh -c 'eval "$(echo … | base64 -d)"'`), so the
remote login shell (bash, zsh, fish) never parses the prompt or flags; the script sources `/etc/profile` and
`~/.profile` quietly, puts the usual install folders on `PATH`, `cd`s into the thread's folder and `exec`s the CLI.
When the ssh link closes the CLI sees EOF and exits.

- The remote's own login is used (no account switching); its rate-limit windows are ignored here.
- Pictures: Claude gets them inline as always; Codex gets `{"type":"image","url":"data:…"}` instead of
  `localImage` (the file is here).
- Folders (`/fs/dirs?host=`) and git (`/fs/git?host=`, `/fs/git/switch {host}`) run small scripts over ssh;
  git info is one round trip. Agents may start in any folder the ssh account can open (its permissions are the
  boundary); the listing also offers "places" — top-level folders the account owns or may write, such as
  `/work`, `/srv/<x>`, `/opt/<x>`, `/mnt/<x>` — as one-tap chips next to Home.
- Not available for remote threads: Pocket app tools (their MCP endpoint is on this server's loopback),
  following the session from other tools, "On this server", log images, rewind/edit and fork at an earlier
  turn (all read the CLI's session file, which is over there). Forking the latest turn works (`--fork-session`).
- `POST /hosts/{id}/check` runs a probe: home, OS, hostname, `claude --version` + `claude auth status`,
  `codex --version` + `codex login status`. `GET /hosts/key` shows this server's public key for
  `authorized_keys`. Hosts are admin-managed; anyone who may run agents can use them.

## Worktrees

`worktrees` rows (owner, host, main checkout `repo`, `path`, `branch`, `base`, `base_sha`, status
active|archived, `setup` ''|running|ok|failed, `archive_ref`, `pr_url`) and `threads.worktree_id`; several threads
can share one. Git work is POSIX sh scripts in `internal/server/worktree_git.go`, run with `sh -c` here and over
ssh on hosts — the same code both ways, covered by `TestWorktreeLifecycle` against a real repository.

- **Where / names:** `~/.pocket-ai/worktrees/<repo>-<sha1(repo)[:6]>/<name>` (`WORKTREES_DIR` overrides locally).
  The name is the folder *and* the branch, from the prompt (`slugify`, ≤5 words) or typed; taken → `-2`, `-3`.
  Modes: new branch from a base (local branch, else `origin/<base>`), existing branch (remote-only → tracking
  branch), PR (`git fetch origin +pull/N/head:pr-N`, base from `gh pr view` when gh exists), existing worktree.
  A branch already checked out somewhere is refused with its folder (no silent `-1` fork like Paseo).
- **Setup:** effective config = repo file (`.pocket-ai.json`, else `paseo.json`: `worktree.setup|teardown|copy|base`,
  string or list) overridden per field by `repo_settings` (per host + repo, edited in the UI). Copy = untracked /
  ignored files matching `.env`, `.env.*`, `**/.env*`, `paseo.json`, `.pocket-ai.json` + configured patterns.
  Commands run `set -e` in the worktree with `POCKET_*` and Paseo's `PASEO_*` env (`ROOT_PATH`,
  `WORKTREE_PATH`, `BRANCH_NAME`, `WORKTREE_PORT`), streamed into the thread as a tool row. The thread's
  messages are held (`wtHolds`) until copy + setup finish; on failure they wait for "Run again" or "Continue".
  A restart marks running setups failed.
- **Status:** ahead/behind base, dirty count, upstream/unpushed, merge/rebase in progress, conflicts, commits.
  Merged = HEAD is in base, or every commit has a patch-equivalent in base (`git cherry`), or the PR is MERGED.
- **Merge into base:** refuses with uncommitted files; fast-forward when possible, else `git merge-tree
  --write-tree` + `commit-tree` (merge or squash). Where base is checked out it lands with `merge --ff-only`
  (git refuses rather than overwrite local changes), else `update-ref` with the old value. Conflicts → 409 with
  the files; the panel offers "Ask the agent to resolve". Options: push base, archive + delete branch.
- **Update from base:** `git merge`/`rebase` in the worktree, conflicts left for the agent, `abort` undoes.
- **Push / PR:** `git push -u` to the base's remote (origin); `gh pr create` when gh exists, else the forge's
  compare URL (GitHub, Gitea/Forgejo, GitLab).
- **Archive:** busy agents block it; idle ones stop; teardown runs (failure logged, not blocking); everything
  not ignored is committed with a temporary index into `refs/pocket-ai/archive/<id>`; `worktree remove --force`.
  Restore re-adds the worktree (branch, or recreated from the archive) and puts the uncommitted work back
  uncommitted, then setup runs again. Delete for good drops the ref (+ branch).
- **Other worktrees** (`git worktree list` of the person's repos that Pocket AI did not make, e.g. Paseo's):
  adopt (row with base from settings) or remove with the same backup under `refs/pocket-ai/removed/…`.

## Schedules

A schedule creates a thread with the prompt plus an instruction to begin the final answer with `OK: …` or
`ATTENTION: …`; the first line decides `last_status` (anything else counts as attention) and whether to notify.
Specs: `every 30m`, `hourly`, `daily 08:00`, `weekdays 08:00`, `weekly mon,thu 08:00`, or five-field cron.

## Conversations other apps prepare (Core link `threads:write`)

Another Pocket app (Pocket Projects: "Investigate AS-2 in the code") acts for a person through a Pocket Core link with the `threads:write` scope (offered in the manifest, switched on by the admin in Core). It can only leave a **draft**: the person opens it, reads (and may edit) the prompt and presses send. The person must be allowed to run agents (admin or `can_run`).

App side (Bearer `pcx_…`, audience `ai`, delegated, scope `threads:write`; `internal/server/link_threads.go`):

- `GET /api/v1/link/agent-options` → `{providers:[{id,name,available,error,models:[{id,name,efforts}],efforts,modes:[{id,name,description}],readonly_mode,default_model,default_effort,default_mode,default_account_id}], accounts:[{id,provider,label,signed_in}], workspaces:[{name,path}], default_provider, home, roots}`. Account 0 is the server's own login; `readonly_mode` is `plan` (Claude) / `read-only` (Codex).
- `POST /api/v1/link/drafts` `{provider?, account_id?, model?, effort?, mode?, cwd, title, text, system?, source, ref, ref_url?, ref_label?}` → 201 `{id, url}` (`url` = `BASE_URL/?draft=<id>`). `cwd` (absolute or `~/…`) must exist inside ROOTS; `system` ≤ 64 KB is hidden context.
- `files: [{name, data}]` on `POST /api/v1/link/drafts` (base64, ≤8, ≤`UPLOAD_MAX_MB` each): stored as the person's uploads (pictures as pictures, PDFs/documents as extracted text); the response lists `files: [{name, ok, error}]`. They go with the first message; the draft page shows them, each removable (`draft_uploads` on start = the ones kept, absent = all).
- `POST /api/v1/link/threads/{draft_id}/unlink` takes a conversation off the app's item (the thread stays); `DELETE /api/v1/link/threads/{draft_id}` deletes the thread (stopping its agent) and the draft. Only for drafts the calling app made (`source` = the acting app's Core slug), else 404; an unsent draft's files go with it.
- `GET /api/v1/link/threads?source=&ref=` → `[{draft_id, thread_id, title, status, url, created_at, updated_at}]`, the person's own only, newest first; `status` is `draft` until sent, then the thread's (`idle|running|waiting|error`), `deleted` when the thread is gone.
- Deploys (`internal/server/link_deploy.go`): a project (`gitProjectID` of the caller's `host_id` + `cwd`) keeps one deploy setup, `{host_id, dir, run[], url, current?}` (`repo_settings` host -1, repo `deploy:<project>`), and ONE slot: the newest deploy is what runs on the target. `GET /api/v1/link/deploy?host_id=&cwd=&source=&ref=&branch=` → `{project, repo, configured, config, host_name, branches:[{branch, sha, ahead (-1 = not on origin), dirty, path, subject, committed_at}], latest, current, live:{checked, now, live}}` — branches = the item's conversations' worktrees and folders' branches, `branch`, and any branch named after `ref` (the repo's default/main left out); `current` = last good deploy, `live` asks the target's `current` command (first word = commit or branch, cached 15 s). `PUT /api/v1/link/deploy/config {host_id, cwd, config|null}`. `POST /api/v1/link/deploys {host_id, cwd, source, ref, branch}` pushes the branch when origin lacks commits of it (refuses diverged), then starts `run` on the target detached (`~/.pocket-ai/deploys/<project>/`: run.sh, wrap.sh, pid, log, exit; `systemd-run --user` when there is a user manager, else setsid/nohup — it outlives ssh and a Pocket AI restart) with `POCKET_BRANCH/SHA/REF/SOURCE/PROJECT/DEPLOY_ID/DEPLOYED_BY`; 409 while one runs. A watcher polls every 3 s (started again lazily after a restart when anyone reads it); a last log line that is just a URL becomes the deploy's url. `GET /api/v1/link/deploys/{id}`: others' deploys come without the log.

Person side (session): `GET /api/v1/drafts/{id}` (owner only), `POST /api/v1/drafts/{id}/start` (what the composer has; empty fields fall back to the draft; 409 `{thread_id}` once started) → thread with `source` `link:<app>`, `source_ref` = ref. `GET /api/v1/threads/{id}/context` → `{source, ref, ref_url, ref_label}` for the header chip.

The draft's `system` is stored in `thread_context` and appended to the system prompt on every start and resume (Claude `--append-system-prompt`, Codex `developerInstructions`). The first start happens inside `createThread`, before the thread id is known to the handler, so the draft is handed over through an in-process map keyed by person/source/ref under a mutex.
