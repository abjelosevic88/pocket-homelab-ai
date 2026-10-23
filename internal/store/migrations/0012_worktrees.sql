-- Git worktrees Pocket AI made for threads (one isolated checkout + branch
-- per task). Several threads can work in one worktree. host_id 0 = this
-- server. status: active | archived (removed from disk; archive_ref keeps
-- every commit and the uncommitted work, so it can be restored).
CREATE TABLE worktrees (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  host_id      INTEGER NOT NULL DEFAULT 0,
  repo         TEXT NOT NULL,             -- the main checkout's top folder
  path         TEXT NOT NULL,             -- the worktree's folder
  branch       TEXT NOT NULL,
  base         TEXT NOT NULL,             -- branch it started from (merge target)
  base_sha     TEXT NOT NULL DEFAULT '',  -- commit it started from
  title        TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'active',
  setup        TEXT NOT NULL DEFAULT '',  -- '' none | running | ok | failed
  archive_ref  TEXT NOT NULL DEFAULT '',
  merged_at    TEXT NOT NULL DEFAULT '',
  pr_url       TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX worktrees_user ON worktrees(user_id, status);
ALTER TABLE threads ADD COLUMN worktree_id INTEGER NOT NULL DEFAULT 0;
-- Worktree setup per repository, kept in Pocket AI (no file in the repo
-- needed): setup / teardown commands, files to copy, default base. A
-- paseo.json or .pocket-ai.json in the repo is read too; these win.
CREATE TABLE repo_settings (
  host_id    INTEGER NOT NULL DEFAULT 0,
  repo       TEXT NOT NULL,
  data       TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host_id, repo)
);
