-- Whose a CLI session is. Every session id one of a person's threads uses is
-- claimed here (the first claim wins, and it outlives the thread), so the
-- server-wide lists (On this server, Today, Running now) never show one
-- person's chats to another. Unclaimed sessions are the server user's own.
CREATE TABLE session_owners (
  provider   TEXT NOT NULL,
  session_id TEXT NOT NULL,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (provider, session_id)
);
INSERT OR IGNORE INTO session_owners (provider, session_id, user_id)
  SELECT provider, provider_session_id, user_id FROM threads WHERE provider_session_id <> '' ORDER BY created_at;
