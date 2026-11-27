-- Shared AI: the owner lends one of their Claude logins to someone else on
-- the box, with an allowance. That person gets a login of their own (an
-- accounts row in their folder; shared_from = the lent login, 0 = the
-- server's own) whose agent reaches Anthropic through Pocket AI
-- (server/ai_share.go): it never sees the owner's sign-in, and every
-- request is counted against the allowance.
ALTER TABLE accounts ADD COLUMN shared_from INTEGER;

CREATE TABLE ai_shares (
  account_id   INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  token        TEXT NOT NULL UNIQUE,
  minutes_day  INTEGER NOT NULL DEFAULT 0, -- agent time per day, 0 = no limit
  percent_week REAL NOT NULL DEFAULT 0,    -- share of the plan's weekly limit, 0 = no limit
  paused       INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- What went through the proxy, per hour (UTC hour start, "2026-11-27T18").
CREATE TABLE ai_share_usage (
  account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  hour          TEXT NOT NULL,
  active_ms     INTEGER NOT NULL DEFAULT 0,
  requests      INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  cache_write   INTEGER NOT NULL DEFAULT 0,
  cost_usd      REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, hour)
);
