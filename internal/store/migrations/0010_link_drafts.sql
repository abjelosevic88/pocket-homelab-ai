-- A conversation another Pocket app prepared for a person (Pocket Projects:
-- "Investigate AS-2 in the code"). The person reads it on the start page and
-- presses send; thread_id is set then. system is hidden context that every
-- start and resume of that thread appends to the CLI's system prompt.
CREATE TABLE link_drafts (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider   TEXT NOT NULL DEFAULT '',
  account_id INTEGER,
  model      TEXT NOT NULL DEFAULT '',
  effort     TEXT NOT NULL DEFAULT '',
  mode       TEXT NOT NULL DEFAULT '',
  cwd        TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  text       TEXT NOT NULL,
  system     TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '',
  ref        TEXT NOT NULL DEFAULT '',
  ref_url    TEXT NOT NULL DEFAULT '',
  ref_label  TEXT NOT NULL DEFAULT '',
  thread_id  TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  used_at    TEXT
);
CREATE INDEX link_drafts_ref ON link_drafts(user_id, source, ref);

CREATE TABLE thread_context (
  thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
  system    TEXT NOT NULL DEFAULT '',
  source    TEXT NOT NULL DEFAULT '',
  ref       TEXT NOT NULL DEFAULT '',
  ref_url   TEXT NOT NULL DEFAULT '',
  ref_label TEXT NOT NULL DEFAULT ''
);
