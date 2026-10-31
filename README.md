<p align="center"><img src="web/public/favicon.svg" width="72" alt=""></p>
<h1 align="center">Pocket AI</h1>
<p align="center">Claude Code and Codex on your own server, from any browser or phone: ask it to check your apps and your box, watch what it does, approve what it wants to run, paste a screenshot into the prompt — and let saved checks run on a schedule and tell you when something needs attention.<br>One Go binary, one SQLite file. It drives the CLIs already installed and logged in on the server.</p>
<p align="center">
  <a href="https://github.com/abjelosevic88/pocket-homelab-ai/actions"><img src="https://github.com/abjelosevic88/pocket-homelab-ai/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/abjelosevic88/pocket-homelab-ai/releases"><img src="https://img.shields.io/badge/binaries-linux%20%7C%20macOS-blue" alt="binaries"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT"></a>
</p>

---

Personal app assistance: [Briefing, workflows, memory, sources and document uploads](docs/assistant.md).

<p align="center"><img src="docs/screenshots/thread.png" width="900" alt="Claude Code reading a compose file and a log, then fixing the port, with the diff expanded"></p>

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/permission.png" alt="A permission request: Allow, Allow for session, Deny"></td>
    <td width="50%"><img src="docs/screenshots/home.png" alt="New thread: Claude Code or Codex, model, effort, permission mode, folder, recipes"></td>
  </tr>
  <tr>
    <td align="center"><sub>The agent asks before it runs something; answer from wherever you are.</sub></td>
    <td align="center"><sub>Start a thread with either CLI, or tap a recipe.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/schedules.png" alt="Schedules with their last verdict"></td>
    <td width="50%" align="center">
      <img src="docs/screenshots/phone-image.png" width="190" alt="A screenshot sent to Codex from a phone">&nbsp;
      <img src="docs/screenshots/phone-permission-dark.png" width="190" alt="A permission request on a phone, dark">
    </td>
  </tr>
  <tr>
    <td align="center"><sub>Scheduled checks answer OK or ATTENTION; you hear only about the second.</sub></td>
    <td align="center"><sub>On the phone: send a screenshot, approve a command.</sub></td>
  </tr>
</table>

## What it does

**Your agents, not a new one**
- Talks to the `claude` and `codex` CLIs that are already installed and signed in on the server: Claude Code
  through its stream-json protocol, Codex through `codex app-server`. Your subscriptions, your `CLAUDE.md` /
  `AGENTS.md`, MCP servers, skills and settings all apply, exactly as in a terminal.
- Model, effort and permission mode per thread (Claude: ask first, accept edits, plan, auto, full access; Codex:
  ask first, sandboxed, read only, full access), changeable mid-thread — the session is resumed with the new flags.
- **More than one account**: add more Claude Code / Codex logins in Settings (signed in from the browser, each
  in its own folder, sharing your settings and skills), pick the account per thread next to the model, and
  switch mid-thread when one hits its limit; the conversation is moved over and carries on. Limits show per
  account.
- **Worktrees**: switch on *Worktree* next to the folder and the agent works in its own git worktree — its own
  folder and branch (named from the prompt), so several agents can change one repo at once. Start from any
  branch, check out an existing branch or a pull request, or continue in an existing worktree. Setup per repo
  (commands, `.env` files to copy, teardown) lives in Pocket AI; a `paseo.json` / `.pocket-ai.json` in the repo
  works too. The thread's worktree panel shows commits and changes and can commit, update from base, merge
  (fast-forward, merge commit or squash — computed with `git merge-tree`, never touching your main checkout's
  uncommitted work), push and open a pull request, or ask the agent to commit or resolve conflicts. Archiving
  saves uncommitted work first and Restore brings it back; the Worktrees page cleans up merged ones and adopts
  or removes worktrees made by hand or by Paseo. Works on other machines too.
- **Other machines**, like Paseo's hosts: add a server in Settings → Agents → Servers (any machine this server
  can `ssh` into without a password, a tailnet name works) and pick it next to the folder when you start a
  thread. The CLI runs there with that machine's own login; folders and git branches are browsed over ssh, and
  the conversation lives here as usual. Nothing to install there but Claude Code and/or Codex.
- Every thread is a real CLI session: it resumes after a restart, and **On this server** lists the sessions you
  started in a terminal (or in another tool) so you can open and continue them here.
- **Agents outlive their connection.** Each CLI runs under a small holder, `pocket-hold`, which Pocket AI
  copies to every machine itself (`~/.pocket-ai/bin`). The CLI keeps working when the ssh link drops or Pocket
  AI is redeployed. Pocket AI then attaches again where it stopped: a dropped link within 15 minutes, a
  redeploy at its next start. A turn in progress, its sub-agents, background shells and pending approvals all
  come back. `make build` embeds the holders (`go generate ./internal/hold`); without them, or with
  `POCKET_HOLD=off`, agents run as plain children of their connection again.

**Pocket apps, directly**
- Enable Pocket Ask once from an app. Ordinary Claude and Codex threads then use that person's app tools directly, including when resuming a saved conversation.
- Spending questions use Money's aggregate report in one call; Notes tools identify folders by their paths. Agents can combine app reads and prepare changes without searching server files for API credentials.
- Every Pocket write requires approval of that operation, even in a full-access coding thread. Your coding model, effort and permissions stay as selected.
- See [Pocket Ask architecture and access rules](https://github.com/abjelosevic88/pocket-homelab-core/blob/main/docs/ask.md).

**Made for the phone**
- Streams text, thinking, tool calls with their output, diffs of edited files, and the agent's to-do list as it
  works. Consecutive tool calls share a compact summary; expand it to inspect each input and result. Errors and image previews remain visible.
- Permission prompts, questions (`AskUserQuestion`, Codex `request_user_input`) and plans to approve appear as
  cards above the composer. Allow once, allow for the session, or deny with a note.
- **Images in the prompt**: paste a screenshot, drag one in, or use the camera/photo library on an iPhone. Claude
  gets image blocks, Codex local image inputs. Large photos are shrunk in the browser first.
- Type while it works: messages queue and go out when the turn ends. Stop any time.
- A push through Apprise / ntfy / a webhook when a turn finishes, fails or waits for you — only when you are not
  already looking at that thread. Browser notifications and an app badge when installed as a PWA.

**Checks that run themselves**
- **Recipes** are saved prompts with their agent settings and `{{placeholders}}` ("Check an app: {{app}}"); one
  tap to run. Starter recipes for server health, a misbehaving app, disk space, container updates, recent errors.
- **Schedules** run a prompt every N minutes/hours, daily, on weekdays, weekly or on a cron line. The agent ends
  with `OK: …` or `ATTENTION: …`; you are notified always, only on attention, or never.

**And**
- Full-text search over everything said, usage per day and per provider (cost for Claude, tokens for both) and the
  live 5-hour / weekly limit windows both CLIs report.
- Fork into a fresh agent session with earlier chat attached as reference, export as Markdown, pin, archive, workspaces for the folders
  you use most, a folder picker limited to the folders you allow.
- Per person, like every Pocket app: nobody sees anyone else's threads, admins included. Members may only run
  agents when an admin allows it. Sign in with Pocket Core, API tokens, Pocket Core events
  (`ai.turn_finished`, `ai.needs_input`, `ai.schedule_attention`).
- Everyone on their own Claude Code and Codex: the server's login is the owner's (the first person). Everyone
  else signs in with their own subscription in Settings → Agents → Accounts, and works in a folder of their own
  (`PEOPLE_DIR/<id>`: `home/` with their logins, `projects/` for their code). Their agents, sign-ins and git run
  in a [bubblewrap](https://github.com/containers/bubblewrap) jail that shows them that folder and nothing else
  of the server — not its files, not its environment, not other people's agents. See
  [People and the sandbox](#people-and-the-sandbox).

## Compared with Paseo

Pocket AI started as "Paseo, for this homelab". Paseo is a daemon with its own app and a hosted web client
reached through a relay, many agent providers, git worktrees and terminals — a coding cockpit. Pocket AI is
narrower and lives with the other Pocket apps:

- a plain web app/PWA on your own domain and network — no relay, no separate account;
- Pocket accounts and Sign in with Pocket Core, per-person data, an admin switch for who may run agents;
  chats are private until you share one (per person: can view, or can chat in it too), and
  the server-wide lists (On this server, Today, Running now) show each person only their own chats, and
  terminal / Paseo sessions only to admins. Everyone but the owner runs sandboxed in their own folder, on
  their own Claude / Codex login.
- built around looking after a server: recipes with placeholders on the start screen, schedules that answer
  OK / ATTENTION and only notify on attention, pushes through the Apprise / ntfy route the box already uses;
- every Claude Code and Codex session on the server listed and resumable, full-text search, usage per day;
- only Claude Code and Codex.

## People and the sandbox

The first person on the server is its **owner**: their agents run as the server's user, in the folders `ROOTS`
allows, on the server's own CLI logins (`~/.claude`, `~/.codex`) and on any remote server under Settings →
Servers. Everybody else:

- gets `PEOPLE_DIR/<id>` (default `DATA_DIR/people`): `home/` is their agents' `$HOME` with their Claude Code
  and Codex logins, worktrees and Pocket Ask's folder; `projects/` is where they work (their folder picker's
  only root);
- adds their own logins (Settings → Agents → Accounts → Add) and signs in with their own subscription — the
  server's login, the owner's accounts and the owner's ssh key are not theirs to use;
- adds servers of their own (Settings → Agents → Servers): Pocket AI makes them an ssh key in
  `home/.ssh/id_ed25519` to put in `authorized_keys` there, and ssh runs in their sandbox with only that key,
  their own `known_hosts` and (if they write one) `home/.ssh/config`. Their servers are invisible to everyone
  else, and the owner's to them;
- runs every agent, sign-in, git command and worktree script inside bubblewrap: their folder read-write at its
  own path, the system read-only, a fresh `/tmp`, `/dev` and `/proc`, their own process space, and none of the
  server's environment. A folder another app shares for a whole team (Pocket Projects) maps to their own clone
  of the same name in `projects/`.

Install bubblewrap on the server (`apt install bubblewrap`; Ubuntu's AppArmor profile for `/usr/bin/bwrap` is
what lets it make namespaces). Until it works, only the owner can run agents, and Settings says why. In a
systemd service, `ProtectKernelTunables=` must be off (the jail mounts its own `/proc`). `SANDBOX=off` keeps the
folders and logins apart but runs people's agents without bubblewrap. Existing people are moved over at
start: their saved folders point at their projects, their Pocket Ask chats move into their folder, and
accounts nobody owned become the owner's.

## Run it

Pocket AI must run **on the host, as the user whose CLIs it drives** — not in a container. Agents get that user's
files, PATH and logins, so treat access to it like SSH access.

```sh
make all                       # web UI + binary (Go 1.26, Node 22)
make install                   # → ~/.local/bin/pocket-ai
make service                   # systemd user unit; edit ~/.config/pocket-ai/env first (see .env.example)
loginctl enable-linger $USER   # keep it running without a login session
```

Put it behind your reverse proxy with TLS (it streams Server-Sent Events; Caddy and nginx with
`proxy_buffering off` work) and keep it on a private network. It listens on `127.0.0.1:8195` by default; `HOST`
takes several addresses (`127.0.0.1,172.17.0.1` lets a proxy in Docker reach it). `AUTH_MODE=none` is refused on
anything but loopback. All options: [docs/configuration.md](docs/configuration.md). API: [docs/api.md](docs/api.md).
How it talks to the CLIs: [docs/design.md](docs/design.md).

## Built with

Go (chi, modernc SQLite with FTS5), React 19 + Vite, marked + DOMPurify. The Pocket Core SDK snapshot is vendored
in `third_party/pocket-core` and checked by hash in CI. MIT licensed.

## Text generation for linked Pocket apps

Travel, Money, Day and Bookshelf can reuse Pocket AI instead of storing another
OpenAI API key. Register the updated apps with Core and approve their **Generate
text** link (`inference:write`) in Core's Links page. Existing app updates do not
auto-approve new permissions; each person can also disable the link.

`POST /api/v1/inference` accepts `{ "system": "...", "prompt": "...", "json": true }`
and returns text, model and token usage. It requires a Core delegated token for
that person, audience `ai`, scope `inference:write`. Such tokens cannot access
normal account, conversation or agent-control endpoints. Inference uses the
person's selected Claude account and model, with a private temporary workspace,
no tools/MCP/hooks, and no saved CLI session. It does not transcribe audio or
perform web searches. At most four requests run at once, each bounded to 140
seconds. Busy responses and model failures are surfaced to the calling app.

Pocket AI model selection: AI settings offer the models discovered from your connected Claude account. “Use Pocket AI default” follows your current model in Pocket AI (Sonnet when no model is set). An explicit choice is saved per person in this app and never changes the Pocket AI default or your OpenAI model. Loading models does not create a chat or send a generation prompt. If a saved model is no longer offered, choose another model or return to the default; generation does not silently substitute a different model.

### Personal model defaults

Set your default for connected apps in **Pocket AI → Settings → App defaults**. This is personal to your account and applies on the next text-generation request in Travel, Money, Day, and Bookshelf when the app uses “Use Pocket AI default”. A model chosen in an individual app takes precedence. “Follow Claude default” preserves existing behavior: use your Claude model under Agents, or Sonnet when no specific model is selected. Claude and Codex chat defaults remain under Agents. Core manages app connections and permissions; model preferences live in Pocket AI alongside your connected accounts.

App defaults also include a personal **Effort level for apps**, with levels discovered for the selected model. Automatic preserves Medium where supported and otherwise uses the model’s own default. The shared effort applies to app-specific models too; if a model does not support the selected level, it uses Automatic. Choosing a new central model resets an incompatible effort to Automatic. Chat effort preferences remain separate.

### Edit and rewind messages

Each sent user message has **Edit message** and **Rewind here** actions. Rewind
removes that message and everything after it **from the same conversation** and
restores its text and attachments in the composer. Rewind on a reply retains
that reply and removes all later messages. Web and iPhone show a confirmation
before deleting history; there is no new chat or Undo action. Files and Pocket
app changes already made are not undone.

Only the owner can rewind, after the agent, subagents, pending approvals and queue
have stopped. The chat keeps its ID, title, folder, sharing, links, account, model,
effort, memory preference and Pocket Ask restrictions. Claude and Codex prepare a
private provider history at the selected boundary and the existing chat switches
to it atomically with timeline truncation. No model turn starts during rewind;
the next message resumes only the retained context. Connected machines are
supported over SSH using their configured binary and login. A history mismatch
or unsupported provider boundary returns an error without deleting the timeline.

Queued messages have **Edit in composer**: the message leaves the queue and its
text and attachments return to the composer. Any existing draft is kept below it.
Queueing a message does not show a toast.

On a computer, **Enter** queues while the agent is working; **Cmd/Ctrl+Enter**
sends immediately by interrupting the current turn and putting the new message
ahead of older queued messages. **Settings → General → Composer → Send behavior**
swaps those actions and sets the send button's behavior. **Shift+Enter** adds a
new line; turning off **Enter sends** makes plain Enter add a new line too.
Phones keep Enter for new lines and use the send button.


Native iPhone/iPad notification event choices are per registered device, separate from the web/external-service policy.
`GET/PUT /api/v1/mobile/devices/{device}/notifications` uses that device's API-token authentication and complete
`finished`, `input`, `failed`, `only_away` booleans. Registration accepts initial `notifications`; refreshing a
registration preserves existing choices. Migration 0023 snapshots the former shared choices once for existing
devices. Disabled event types still get badge-only pushes. Web tabs do not suppress native pushes; the native
foreground notification delegate applies away-only presentation on its own phone.
