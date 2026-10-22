-- A thread its owner shared: each person may view it (read along live) or
-- chat in it (also send messages and answer the agent's requests).
CREATE TABLE thread_shares (
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK (role IN ('view', 'chat')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (thread_id, user_id)
);
CREATE INDEX thread_shares_user ON thread_shares(user_id);
