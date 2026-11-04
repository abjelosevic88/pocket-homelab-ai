-- Heartbeats (like Paseo's): a prompt an agent set to come back to its own
-- conversation on a schedule (spec as in schedules), until it is removed, its
-- runs are used up or it ends.
CREATE TABLE heartbeats (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL,
  prompt     TEXT NOT NULL,
  spec       TEXT NOT NULL,
  next_at    TEXT NOT NULL,
  last_at    TEXT NOT NULL DEFAULT '',
  runs       INTEGER NOT NULL DEFAULT 0,
  max_runs   INTEGER NOT NULL DEFAULT 0, -- 0 = no limit
  ends_at    TEXT NOT NULL DEFAULT '',   -- '' = never
  created_at TEXT NOT NULL
);
CREATE INDEX heartbeats_next ON heartbeats(next_at);
CREATE INDEX heartbeats_thread ON heartbeats(thread_id);
