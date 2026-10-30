-- Claude Remote Control: a row = the person turned it on for that thread, so
-- its session is reachable from claude.ai/code and the Claude app. The
-- bridge session id lets a restarted process reattach to the same session.
CREATE TABLE thread_remote (
  thread_id         TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
  bridge_session_id TEXT NOT NULL DEFAULT '',
  session_url       TEXT NOT NULL DEFAULT '',
  updated_at        TEXT NOT NULL DEFAULT ''
);
