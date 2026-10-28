# Configuration

Everything is an environment variable (the systemd unit reads `~/.config/pocket-ai/env`).

| Variable | Default | |
|---|---|---|
| `HOST` | `127.0.0.1` | Address(es) to listen on, comma-separated. An address that does not exist yet (a docker bridge before docker starts) is retried. |
| `PORT` | `8195` | |
| `BASE_URL` | | Public URL (`https://ai.example.com`). Needed for Sign in with Pocket Core and links in notifications. |
| `DATA_DIR` | `./data` | SQLite database, `secret.key`, uploaded images (`uploads/`), backups. |
| `TZ` | `UTC` | Default time zone for schedules (each person can pick their own). |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS. |
| `AUTH_MODE` | `local` | `local` (accounts), `proxy` (trust `AUTH_PROXY_HEADER`), `none` (loopback only — refused otherwise). |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `ADMIN_NAME` | | First admin, created on an empty database. |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME` | | Sign in with any OpenID provider. Not needed with Pocket Core. |
| `CORE_URL`, `CORE_REGISTRATION_KEY`, `CORE_APP_URL` | | Pocket Core: register, publish events, accept paired-phone tokens, Sign in with Pocket Core. |
| `NOTIFY_URL`, `NOTIFY_KIND` | | Box notification route (`apprise` / `ntfy` / `webhook`), used for the owner unless they set their own. |
| `ROOTS` | your home | Folders agents may start in and the folder picker shows, comma-separated, `~` allowed. Symlinks are resolved: a link out of a root is outside. |
| `PEOPLE_DIR` | `DATA_DIR/people` | A folder per person other than the owner (their home with their CLI logins, and their projects); their agents run jailed there. See README → People and the sandbox. |
| `SANDBOX` | `on` | `on`: people's agents run in bubblewrap (only the owner can run agents while it does not work). `off`: their own folders and logins, but no bubblewrap. |
| `BWRAP_BIN` | `bwrap` on `PATH` | The bubblewrap binary. |
| `AGENT_HOME` | your home | What `~` means, and the default folder. |
| `CLAUDE_BIN`, `CODEX_BIN` | `claude`, `codex` | The CLIs. Use absolute paths when the service PATH differs from your shell's. |
| `CLAUDE_HOME`, `CODEX_HOME` | `~/.claude`, `~/.codex` | The server's own login (account 0): where its sessions are read from. More logins are added in Settings → Agents → Accounts, each in its own folder next to these. |
| `IDLE_TIMEOUT` | `20m` | A thread's CLI process stops after this long idle; the next message resumes the session. |
| `MAX_LIVE` | `12` | Most CLI processes at once; the longest-idle ones are stopped first. |
| `UPLOAD_MAX_MB` | `20` | Per image. PNG, JPEG, GIF, WebP (the web app shrinks big photos and converts HEIC where the browser can decode it). |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `text` | |

## Security model

- Agents run as the Unix user that runs Pocket AI. Anyone who can use it can do what that user can do on the box,
  within the permission mode they pick. Keep it on a private network and behind TLS.
- Admins may always run agents. Members see the app but can only start or answer agents when an admin turns on
  **May run agents** for them (Settings → Users).
- Threads, recipes, schedules, workspaces and images are per person; other people's ids answer 404.
- Folders are limited to `ROOTS`. That limits where a thread *starts*, not what an agent in full-access mode can
  reach — that is what the permission modes are for.

## Files

- `DATA_DIR/pocket-ai.db` — WAL-mode SQLite. Back it up with `pocket-ai backup` (consistent copy) rather than
  copying the live file.
- `DATA_DIR/uploads/<user>/` — images sent in prompts; unsent ones are deleted after a day, the rest with their thread.
- The CLIs keep their own session files (`~/.claude/projects`, `~/.codex/sessions`); deleting a thread here does
  not delete those.

## Native iOS notifications

APNs delivery runs alongside each person's existing notification route and follows the same
finished/input/failed and only-away preferences. Configure these server environment variables:

| Variable | Value |
| --- | --- |
| `APNS_TEAM_ID` | Apple developer team ID |
| `APNS_KEY_ID` | APNs token signing key ID |
| `APNS_KEY_FILE` | Private mount path to the EC P-256 `.p8` signing key |
| `APNS_TOPIC` | App bundle ID; defaults to `org.bjelke.pocketai` |
| `BASE_URL` | Server URL used in notification links; match the URL signed into on iOS |

The app requires Apple Push Notifications provisioning. Enable it under iOS Settings →
Notifications after enabling native delivery in Pocket AI's notification settings. Native
registrations are bound to the device's API token and cascade away when it is revoked.
Invalid/unregistered APNs tokens are removed after Apple's response. Signing out unregisters
on a best-effort basis. No signing key or API token is placed in a push payload.

The iOS client discovers support through authenticated `GET /api/v1/mobile`. Native endpoints
include `PUT /mobile/devices`, `DELETE /mobile/devices/{device}`, `POST /mobile/push/test`, and
`GET /mobile/attention`. Migration `0020_mobile.sql` also adds durable message receipts:
`POST /threads/{id}/messages` with an `Idempotency-Key` dispatches at most once and replays its
saved response. A reservation interrupted before its response was saved returns 409 and
requires checking the timeline; it is never automatically dispatched again. Keys must not be
reused for different content. `GET /threads/{id}/review` is owner-only and requires run access;
it returns current Git status and a tracked-file diff capped at 2 MB.


### Personal snippets

Migration `0021_snippets.sql` stores each person's reusable text on this server. Authenticated
`GET /api/v1/snippets`, `POST /api/v1/snippets`, `PUT /api/v1/snippets/{id}` and
`DELETE /api/v1/snippets/{id}` list and manage only the caller's library. A title is required
(up to 120 Unicode code points); text is required (up to 64 KiB). Body formatting is preserved.
Saving and inserting snippets does not start an agent and does not require run permission.
The web Snippets link sits below Usage; iOS offers Snippets below Usage in composer tools.
