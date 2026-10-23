-- Other machines agents can run on, reached over ssh with the server's own
-- keys (like Paseo's hosts). Host 0 is this server and has no row. info is
-- the last check (CLI versions, home folder, errors) as JSON.
CREATE TABLE hosts (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  target     TEXT NOT NULL,
  port       INTEGER NOT NULL DEFAULT 0,
  claude_bin TEXT NOT NULL DEFAULT '',
  codex_bin  TEXT NOT NULL DEFAULT '',
  info       TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
ALTER TABLE threads ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0;
