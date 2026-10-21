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

## Schedules

A schedule creates a thread with the prompt plus an instruction to begin the final answer with `OK: …` or
`ATTENTION: …`; the first line decides `last_status` (anything else counts as attention) and whether to notify.
Specs: `every 30m`, `hourly`, `daily 08:00`, `weekdays 08:00`, `weekly mon,thu 08:00`, or five-field cron.
