# Pocket AI API

All endpoints live under `/api/v1`, take and return JSON, and need a session cookie
(`pai_session`), a bearer token (`Authorization: Bearer pai_…`) or a Pocket Core token.
Everything is strictly per person: another person's thread, recipe, schedule, workspace or
upload answers `404`. Starting agents needs `me.can_run` (admins always; members only when an
admin allowed it); otherwise run endpoints answer `403`.

Errors: `{"error": "message"}` with a 4xx/5xx status.

## Auth (same as every Pocket app)

| Method | Path | Notes |
|---|---|---|
| GET | `/auth/status` | `{setup_needed, oidc, oidc_name, auth_mode, title}` |
| POST | `/auth/setup` | first admin: `{email, name, password}` |
| POST | `/auth/login` | `{email, password}` |
| POST | `/auth/logout` | |
| GET | `/auth/me` | `User` + `can_run` |
| PUT | `/auth/me` | `{name, email, password, current_password}` |
| GET | `/auth/oidc/start?next=` | Sign in with Pocket Core |

`User`: `{id, email, name, username, role: "admin"|"member", can_run, created_at}`

## Providers

`GET /providers` →

```json
[{
  "id": "claude", "name": "Claude Code", "available": true, "version": "2.1.306",
  "error": "",
  "models": [{"id": "default", "name": "Default (recommended)", "description": "Opus 5.5 · …", "efforts": ["low","medium","high","xhigh","max"]}],
  "efforts": ["low","medium","high","xhigh","max"],
  "modes": [{"id": "default", "name": "Ask first", "description": "…"}],
  "default_model": "default", "default_effort": "high", "default_mode": "default",
  "account": "Claude Max · aleksandar@…"
}]
```

`speeds`: `[{id: "", name: "Standard", available: true}, {id: "fast" | "<codex tier>", name, description, available, note}]` —
models with `fast: true` can use the non-standard one. Claude's `fast` is Claude Code fast mode (opt-in via
`--settings {"fastMode":true}`; `available: false` with a `note` when the account cannot use it, e.g. extra usage
off). Codex speeds are the model's service tiers (`priority` = "Fast"). Threads, recipes, schedules and the
per-provider defaults carry `speed` (`""` = standard); `PATCH /threads/{id}` with `{speed}` resumes the session
at that speed.

Claude modes: `default` (asks before edits/commands), `acceptEdits`, `plan`, `auto`, `bypassPermissions`.
Codex modes: `untrusted` (asks for everything but reads), `on-request` (asks when it wants to leave the sandbox),
`full-access` (no sandbox, never asks), `read-only`.

## Accounts (more than one CLI login)

Each Claude Code / Codex login is a config folder of its own (`CLAUDE_CONFIG_DIR` / `CODEX_HOME`). Account `0`
is the server's own login (`~/.claude`, `~/.codex`) and has no row; added accounts live next to it
(`~/.claude-<name>`, `~/.codex-<name>`) and share its settings, skills, plugins (and Claude's MCP servers /
Codex's `config.toml`) through symlinks. Login, history and plan limits are per account.

`GET /accounts` → the logins the person may use (`?manage=1`: admins also see everyone's; `?refresh=1` asks
the CLIs again instead of the 10-minute cache):

```json
[{"id": 0, "provider": "claude", "label": "Server login", "home": "/home/me/.claude", "user_id": null, "default": true,
  "signed_in": true, "email": "me@…", "plan": "max", "checked_at": "…"},
 {"id": 3, "provider": "codex", "label": "Second Pro", "home": "/home/me/.codex-second-pro", "user_id": 2, "default": false,
  "signed_in": false}]
```

Admin only: `POST /accounts {provider, label, user_id?}` (creates the folder; `user_id` null = everyone who may
run agents), `PATCH /accounts/{id} {label?, user_id?}`, `DELETE /accounts/{id}` (threads continue on account 0;
the folder stays on disk), `POST /accounts/{id}/logout`.

Signing in from the browser: `POST /accounts/{id}/login` starts `claude auth login` / `codex login --device-auth`
and answers `{state, url, code, need_code}`. Claude: open `url`, then `POST /accounts/{id}/login/code {code}`
with the code the page shows. Codex: open `url` and type `code`; `GET /accounts/{id}/login?wait=1` long-polls
until `state` is `done` or `failed`. `DELETE /accounts/{id}/login` cancels.

Threads carry `account_id`; `POST /threads` takes it (default: the person's `account_id` in the provider
defaults). `PATCH /threads/{id} {account_id}` switches: the next turn runs on that login, the session file is
copied into its folder first (the newest copy wins), and a notice is added to the timeline. Plan limits are
keyed `claude` / `codex` for account 0 and `claude:3` for account 3 (with `label`, `account_id`). "On this
server", Today and Running now read every folder the person may use (`account_id` on each session).
The SSE stream sends `accounts` when the list or a sign-in changes.

## Threads

`Thread`:

```json
{
  "id": "uuid", "title": "…", "provider": "claude"|"codex", "model": "…", "effort": "high", "mode": "default",
  "cwd": "/home/me/Conversations", "workspace_id": 1|null, "provider_session_id": "…",
  "status": "idle"|"running"|"waiting"|"error", "error": "",
  "pinned": false, "archived": false, "unread": false,
  "source": "web"|"recipe"|"schedule"|"import"|"api", "source_ref": "",
  "preview": "last assistant line…",
  "usage": {"input_tokens":0,"output_tokens":0,"cached_tokens":0,"cost_usd":0,"context_tokens":0,"context_window":0,"turns":0},
  "created_at": "…", "updated_at": "…", "activity_at": "…",
  "live": true, "pending": 0, "queued": 0,
  "owner_id": 1, "owner_name": "Alice",            // owner_name only when shared
  "shares": [{"user_id": 2, "name": "Bob", "role": "view"|"chat"}]   // omitted when private
}
```

**Sharing.** A thread is private to the person who started it until they share it. `view` reads
along live (timeline, events, export); `chat` also sends messages, manages the queue, stops turns and
answers requests, and only works while the person may run agents. The agent keeps running on the
owner's login, folder and settings; PATCH, DELETE, fork and sharing stay the owner's (403 for
others, 404 for anyone it isn't shared with, admins included). `GET /threads` lists threads shared
with you after your own (`owner_id` ≠ you). Messages sent in a shared thread carry `by` / `by_id`.

Pocket app tools run on the apps of whoever sent the message a turn answers (the owner's saved memory
is only added to the owner's own messages). In a shared thread a change waits for that person
(`request.for_id` / `for`) and only they may answer it. Ask chats can be shared with `chat` to anyone
(no run permission needed; the sender needs Ask connected), and `/app-calls?thread=` shows each person
only their own calls.

`waiting` = the agent asked something (permission / question / plan) and waits for an answer.

| Method | Path | Body / query | Returns |
|---|---|---|---|
| GET | `/threads` | `?archived=1`, `?workspace=ID`, `?q=` (title filter) | `Thread[]` newest activity first, pinned first |
| POST | `/threads` | `{provider, model?, effort?, mode?, cwd?, workspace_id?, title?, text, upload_ids?: string[]}` | `Thread` (201); the first turn starts right away |
| GET | `/threads/{id}` | | `{thread: Thread, items: Item[]}` |
| PATCH | `/threads/{id}` | any of `{title, pinned, archived, model, effort, mode}` | `Thread` |
| DELETE | `/threads/{id}` | | 204 (stops the agent, deletes the timeline; the CLI's own session file stays) |
| POST | `/threads/{id}/messages` | `{text, upload_ids?: string[], send_now?: bool}` | `{queued: bool, thread: Thread}` — normally waits for the current turn; `send_now` prioritizes this message and interrupts the current turn, preserving older queued messages |
| DELETE | `/threads/{id}/queue` | | 204 — drop queued messages |
| POST | `/threads/{id}/queue/{key}/edit` | | `{text, uploads: Upload[]}` — takes your own queued message out for editing; 409 if it has already started sending |
| POST | `/threads/{id}/queue/{key}/now` | | 204 — sends this queued message next, interrupting the current turn |
| POST | `/threads/{id}/interrupt` | | 204 — stop the current turn |
| POST | `/threads/{id}/requests/{requestId}` | `{decision: "allow"\|"allow_session"\|"deny", message?: string, answers?: {[questionId]: string[]}}` | 204 |
| POST | `/threads/{id}/read` | | 204 — clears `unread` |
| POST | `/threads/{id}/fork` | | `Thread` — a new thread continuing from the same agent session (Claude: `--fork-session`; Codex: `thread/fork`) |
| GET | `/threads/{id}/export.md` | | Markdown transcript (download) |
| GET | `/threads/{id}/shares` | | owner only: `{shares: ThreadShare[], people: [{id, name, email, can_run}]}` |
| PUT | `/threads/{id}/shares` | `{shares: [{user_id, role: "view"\|"chat"}]}` (the full list; `[]` = private again) | `Thread` — people removed lose it at once, open views included |

### Item (timeline entry)

Every item has `{id, key, kind, created_at, updated_at}` plus fields by kind:

| kind | fields |
|---|---|
| `user` | `text`, `images?: [{id, name, url}]`, `queued?: true` |
| `assistant` | `text` (Markdown) |
| `thinking` | `text` |
| `tool` | `tool: {name, title, kind, input, output, status, exit_code?, diff?}` |
| `todos` | `todos: [{text, status: "pending"\|"in_progress"\|"completed"}]` |
| `request` | `request: Request` |
| `notice` | `text`, `level: "info"\|"warn"\|"error"` |
| `turn` | `turn: {ok, duration_ms, cost_usd, input_tokens, output_tokens, error?}` |

`tool.kind`: `bash` (input.command, output = stdout/stderr), `edit` / `write` (input.file_path, `diff` = unified diff
when known), `read`, `search` (grep/glob/web search), `web` (fetch), `task` (sub-agent), `mcp`, `other`.
`tool.status`: `running` | `done` | `error` | `denied`. `tool.title` is a one-line summary for the collapsed row
(e.g. `ls -la ~/caddy`, `Edit Caddyfile`).

`Request`:

```json
{
  "id": "…", "type": "permission"|"question"|"plan",
  "tool_name": "Bash", "title": "Run ls -la", "input": {…},
  "questions": [{"id": "q1", "header": "Auth", "question": "Which?", "multi": false,
                  "options": [{"label": "A", "description": "…"}], "other": true}],
  "plan": "markdown (type=plan)",
  "can_allow_session": true,
  "status": "pending"|"allowed"|"denied"|"answered"|"expired",
  "answer": {…}
}
```

### Live updates (Server-Sent Events)

`GET /threads/{id}/events` — one thread. Events:

- `item` — `Item` (insert or replace by `key`)
- `delta` — `{key, field: "text"|"output", text}`: append `text` to that field of the item with this key
- `thread` — `Thread` (status, usage, title, pending, queued changed)
- `ping` — every 20 s

While a thread's stream is open, Pocket AI also follows the CLI's own session log (Claude Code's `.jsonl`,
Codex through `codex app-server`), so a session driven from a terminal, another app or another device shows up live
on every open device, and opening a thread catches up on what was said elsewhere while it was closed. A thread
that is being driven elsewhere has `external: true` and `status: "running"`; `POST /threads/{id}/messages` answers
`409` until it is idle.

`GET /events` — everything of mine: `thread` events for all my threads (list badges, notifications),
`limits` events (`Limits`, below).

## Uploads (images for the prompt)

| Method | Path | Notes |
|---|---|---|
| POST | `/uploads` | `multipart/form-data`, field `file` (repeatable). Images up to 20 MB (png, jpeg, gif, webp, heic → converted is NOT done; heic is refused with a hint). Returns `Upload[]` |
| GET | `/uploads/{id}` | the file (auth; `Cache-Control: private`) |
| DELETE | `/uploads/{id}` | 204 (only when not yet sent) |

`Upload`: `{id, name, mime, size, url, created_at}`. Send them with `upload_ids` on `POST /threads` or
`POST /threads/{id}/messages`. Claude gets them as image blocks, Codex as local image paths.

## Workspaces

`Workspace`: `{id, name, path, color, sort_order, threads, created_at}`

| Method | Path | Notes |
|---|---|---|
| GET | `/workspaces` | |
| POST | `/workspaces` | `{name, path, color?}` — path must be an existing directory inside the allowed roots |
| PUT | `/workspaces/{id}` | |
| DELETE | `/workspaces/{id}` | threads keep their cwd |
| GET | `/fs/dirs?path=` | folder picker: `{path, parent, dirs: [{name, path, git: bool}], roots: string[]}` |

## Recipes (saved prompts)

`Recipe`: `{id, name, prompt, icon, provider, model, effort, mode, cwd, pinned, sort_order, runs, created_at, updated_at}`

`prompt` may contain `{{name}}` placeholders; `POST /recipes/{id}/run` with `{vars: {name: "value"}}` fills them.

| Method | Path |
|---|---|
| GET | `/recipes` |
| POST | `/recipes` |
| PUT | `/recipes/{id}` |
| DELETE | `/recipes/{id}` |
| POST | `/recipes/{id}/run` → `Thread` |

`GET /recipes/suggestions` → starter recipes (`Recipe` without id) the UI can offer to add.

## Schedules

`Schedule`: `{id, name, prompt, provider, model, effort, mode, cwd, spec, notify, enabled, next_run_at, last_run_at,
last_thread_id, last_status, last_summary, created_at, updated_at}`

- `spec`: `every 30m` / `every 6h` / `daily 08:00` / `weekdays 08:00` / `weekly mon 08:00` / a 5-field cron line.
- `notify`: `always` | `attention` (only when the agent's reply starts with `ATTENTION`, or the run failed) | `never`.
- `last_status`: `running` | `ok` | `attention` | `failed`.

Scheduled prompts get an instruction appended: finish with a first line `OK: …` or `ATTENTION: …`.

| Method | Path |
|---|---|
| GET | `/schedules` |
| POST | `/schedules` (400 with a readable message when `spec` does not parse) |
| PUT | `/schedules/{id}` |
| DELETE | `/schedules/{id}` |
| POST | `/schedules/{id}/run` → `Thread` (run now) |
| GET | `/schedules/preview?spec=` → `{ok, next: [iso, iso, iso], error}` |

## History (sessions the CLIs already have on the server)

| Method | Path | Notes |
|---|---|---|
| GET | `/history?provider=claude\|codex&q=&limit=` | `[{provider, session_id, cwd, title, updated_at, messages, thread_id}]` — `thread_id` set when already imported |
| POST | `/history/import` | `{provider, session_id}` → `Thread` with the transcript loaded; sending a message resumes that CLI session |

Admins and members with `can_run` only (these are the server user's files).

## Search

`GET /search?q=` → `[{thread_id, title, provider, snippet, item_key, activity_at}]` — full text over what was said
(snippet has `<mark>` around hits, everything else HTML-escaped).

## Usage

`GET /usage?days=30` →

```json
{
  "days": [{"date": "2026-10-21", "cost_usd": 1.2, "input_tokens": 0, "output_tokens": 0, "turns": 3}],
  "providers": {"claude": {"cost_usd": 0, "turns": 0}, "codex": {"cost_usd": 0, "turns": 0}},
  "limits": Limits
}
```

`Limits`: `{"claude": {"windows": [{"name": "5 hours", "used": 0.61, "resets_at": "…"}], "updated_at": "…"},
"codex": {…}}` — taken from what the CLIs report while they run (missing until one ran).

## Settings

`GET /settings` / `PUT /settings`:

```json
{
  "title": "Pocket AI",
  "default_provider": "claude", "default_cwd": "/home/me",
  "claude": {"model": "default", "effort": "high", "mode": "default"},
  "codex": {"model": "", "effort": "high", "mode": "on-request"},
  "notify_kind": "apprise", "notify_url": "", "notify_finished": true, "notify_input": true, "notify_failed": true,
  "notify_only_away": true,
  "enter_sends": true, "enter_send_now": false, "show_thinking": false, "system_prompt": ""
}
```

`enter_send_now`: personal composer default. False: Enter/send button queues while busy, Cmd/Ctrl+Enter sends immediately. True: swaps those actions. `enter_sends: false` keeps plain Enter as a new line. The client sends `send_now` explicitly; API callers retain queue-by-default behavior.

`notify_only_away`: skip the push when the person has the thread open in a browser.
`system_prompt` is appended to every agent's system prompt (both CLIs).

`GET /settings/system` (admin: binaries, versions, roots, live processes), `POST /notify/test`.

Users (`GET/POST/PUT/DELETE /users`, admin; body adds `can_run`), tokens (`GET/POST/DELETE /tokens`),
`GET /backup` (admin, single-user boxes) work as in the other Pocket apps.
