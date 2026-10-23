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

## Schedules

A schedule creates a thread with the prompt plus an instruction to begin the final answer with `OK: …` or
`ATTENTION: …`; the first line decides `last_status` (anything else counts as attention) and whether to notify.
Specs: `every 30m`, `hourly`, `daily 08:00`, `weekdays 08:00`, `weekly mon,thu 08:00`, or five-field cron.

## Conversations other apps prepare (Core link `threads:write`)

Another Pocket app (Pocket Projects: "Investigate AS-2 in the code") acts for a person through a Pocket Core link with the `threads:write` scope (offered in the manifest, switched on by the admin in Core). It can only leave a **draft**: the person opens it, reads (and may edit) the prompt and presses send. The person must be allowed to run agents (admin or `can_run`).

App side (Bearer `pcx_…`, audience `ai`, delegated, scope `threads:write`; `internal/server/link_threads.go`):

- `GET /api/v1/link/agent-options` → `{providers:[{id,name,available,error,models:[{id,name,efforts}],efforts,modes:[{id,name,description}],readonly_mode,default_model,default_effort,default_mode,default_account_id}], accounts:[{id,provider,label,signed_in}], workspaces:[{name,path}], default_provider, home, roots}`. Account 0 is the server's own login; `readonly_mode` is `plan` (Claude) / `read-only` (Codex).
- `POST /api/v1/link/drafts` `{provider?, account_id?, model?, effort?, mode?, cwd, title, text, system?, source, ref, ref_url?, ref_label?}` → 201 `{id, url}` (`url` = `BASE_URL/?draft=<id>`). `cwd` (absolute or `~/…`) must exist inside ROOTS; `system` ≤ 64 KB is hidden context.
- `GET /api/v1/link/threads?source=&ref=` → `[{draft_id, thread_id, title, status, url, created_at, updated_at}]`, the person's own only, newest first; `status` is `draft` until sent, then the thread's (`idle|running|waiting|error`), `deleted` when the thread is gone.

Person side (session): `GET /api/v1/drafts/{id}` (owner only), `POST /api/v1/drafts/{id}/start` (what the composer has; empty fields fall back to the draft; 409 `{thread_id}` once started) → thread with `source` `link:<app>`, `source_ref` = ref. `GET /api/v1/threads/{id}/context` → `{source, ref, ref_url, ref_label}` for the header chip.

The draft's `system` is stored in `thread_context` and appended to the system prompt on every start and resume (Claude `--append-system-prompt`, Codex `developerInstructions`). The first start happens inside `createThread`, before the thread id is known to the handler, so the draft is handed over through an in-process map keyed by person/source/ref under a mutex.
