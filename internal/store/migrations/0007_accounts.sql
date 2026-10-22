-- More than one Claude Code / Codex login on the server. Each account is a
-- CLI config folder (CLAUDE_CONFIG_DIR / CODEX_HOME); account 0 is the
-- server's own login and has no row. user_id NULL = anyone who may run agents.
CREATE TABLE accounts (
  id         INTEGER PRIMARY KEY,
  provider   TEXT NOT NULL,
  label      TEXT NOT NULL,
  home       TEXT NOT NULL UNIQUE,
  user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
ALTER TABLE threads ADD COLUMN account_id INTEGER NOT NULL DEFAULT 0;
