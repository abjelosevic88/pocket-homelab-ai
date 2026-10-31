-- Away from keyboard: what an agent asked while its person was away (a
-- permission, a question, a Pocket app change) is answered "not now" and
-- kept here until they decide. match identifies the exact call, so an
-- approved one runs without asking when the agent repeats it (once).
CREATE TABLE deferred_requests (
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id         TEXT NOT NULL,
  user_id    INTEGER NOT NULL,
  match      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'deferred', -- deferred | allowed | denied | answered
  used       INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  decided_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX deferred_requests_user ON deferred_requests(user_id, status);
CREATE INDEX deferred_requests_match ON deferred_requests(thread_id, match, status, used);
