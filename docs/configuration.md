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
