CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  username      TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'admin',
  -- Agents run as the Unix user the server runs as, with that user's files
  -- and logins. Admins may always run them; a member only when an admin
  -- switched this on for them.
  can_run       INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);
CREATE UNIQUE INDEX users_username ON users(username) WHERE username <> '';

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup_complete). Everything a person chooses is
-- in user_settings under their id.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

-- A workspace is a folder on the server agents start in (Paseo's projects).
CREATE TABLE workspaces (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  path       TEXT NOT NULL,
  color      TEXT NOT NULL DEFAULT '',
  sort_order REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX workspaces_user ON workspaces(user_id);

-- A thread is one conversation with one CLI agent. provider_session_id is
-- the Claude Code session id / Codex thread id it resumes.
CREATE TABLE threads (
  id                  TEXT PRIMARY KEY,
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title               TEXT NOT NULL DEFAULT '',
  provider            TEXT NOT NULL,
  model               TEXT NOT NULL DEFAULT '',
  effort              TEXT NOT NULL DEFAULT '',
  mode                TEXT NOT NULL DEFAULT '',
  cwd                 TEXT NOT NULL DEFAULT '',
  workspace_id        INTEGER REFERENCES workspaces(id) ON DELETE SET NULL,
  provider_session_id TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'idle',
  error               TEXT NOT NULL DEFAULT '',
  pinned              INTEGER NOT NULL DEFAULT 0,
  archived            INTEGER NOT NULL DEFAULT 0,
  unread              INTEGER NOT NULL DEFAULT 0,
  source              TEXT NOT NULL DEFAULT 'web',
  source_ref          TEXT NOT NULL DEFAULT '',
  preview             TEXT NOT NULL DEFAULT '',
  usage               TEXT NOT NULL DEFAULT '{}',
  created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  activity_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX threads_user ON threads(user_id, archived, activity_at);
CREATE INDEX threads_provider_session ON threads(user_id, provider, provider_session_id);

-- The timeline of a thread: one row per message, tool call, request or
-- turn summary. key is stable per thread so streaming updates replace rows.
CREATE TABLE items (
  id         INTEGER PRIMARY KEY,
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  key        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  data       TEXT NOT NULL DEFAULT '{}',
  body       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (thread_id, key)
);
CREATE INDEX items_thread ON items(thread_id, id);

-- Full-text search over what was said (user + assistant text).
CREATE VIRTUAL TABLE items_fts USING fts5(body, content='items', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER items_ai AFTER INSERT ON items BEGIN
  INSERT INTO items_fts(rowid, body) VALUES (new.id, new.body);
END;
CREATE TRIGGER items_ad AFTER DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, body) VALUES ('delete', old.id, old.body);
END;
CREATE TRIGGER items_au AFTER UPDATE OF body ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, body) VALUES ('delete', old.id, old.body);
  INSERT INTO items_fts(rowid, body) VALUES (new.id, new.body);
END;

-- Images (and other files) attached to a message. Stored under DATA_DIR/uploads.
CREATE TABLE uploads (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  thread_id  TEXT REFERENCES threads(id) ON DELETE SET NULL,
  name       TEXT NOT NULL DEFAULT '',
  mime       TEXT NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  path       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX uploads_user ON uploads(user_id);

-- A recipe is a saved prompt with its agent settings: one tap to run.
CREATE TABLE recipes (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  prompt     TEXT NOT NULL,
  icon       TEXT NOT NULL DEFAULT '',
  provider   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  effort     TEXT NOT NULL DEFAULT '',
  mode       TEXT NOT NULL DEFAULT '',
  cwd        TEXT NOT NULL DEFAULT '',
  pinned     INTEGER NOT NULL DEFAULT 1,
  sort_order REAL NOT NULL DEFAULT 0,
  runs       INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX recipes_user ON recipes(user_id);

-- A schedule runs a prompt on its own and tells the person how it went.
CREATE TABLE schedules (
  id             INTEGER PRIMARY KEY,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  prompt         TEXT NOT NULL,
  provider       TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL DEFAULT '',
  effort         TEXT NOT NULL DEFAULT '',
  mode           TEXT NOT NULL DEFAULT '',
  cwd            TEXT NOT NULL DEFAULT '',
  spec           TEXT NOT NULL,                     -- "every 6h" | "daily 08:00" | "weekly mon 08:00" | "0 8 * * *"
  notify         TEXT NOT NULL DEFAULT 'attention', -- always | attention | never
  enabled        INTEGER NOT NULL DEFAULT 1,
  next_run_at    TEXT NOT NULL DEFAULT '',
  last_run_at    TEXT NOT NULL DEFAULT '',
  last_thread_id TEXT NOT NULL DEFAULT '',
  last_status    TEXT NOT NULL DEFAULT '',          -- running | ok | attention | failed
  last_summary   TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX schedules_user ON schedules(user_id);
CREATE INDEX schedules_due ON schedules(enabled, next_run_at);

-- One row per finished turn, for the usage page.
CREATE TABLE turns (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  thread_id     TEXT NOT NULL,
  provider      TEXT NOT NULL,
  model         TEXT NOT NULL DEFAULT '',
  ok            INTEGER NOT NULL DEFAULT 1,
  duration_ms   INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd      REAL NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX turns_user ON turns(user_id, created_at);
