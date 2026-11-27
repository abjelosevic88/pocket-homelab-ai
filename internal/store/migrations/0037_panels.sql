-- Panels: one question put to several models at once (ask all, brainstorm,
-- council, debate, second opinion, best of N), in rounds the server runs.
-- Every participant is a thread of its own (source "panel", source_ref = the
-- panel); what each said per step is kept in panel_answers.
CREATE TABLE panels (
  id             TEXT PRIMARY KEY,
  user_id        INTEGER NOT NULL,
  kind           TEXT NOT NULL,              -- ask | brainstorm | council | debate | review | build | recipe
  title          TEXT NOT NULL DEFAULT '',
  question       TEXT NOT NULL,
  material       TEXT NOT NULL DEFAULT '',   -- what is judged or reviewed (a plan, an answer, a diff)
  spec           TEXT NOT NULL DEFAULT '{}', -- JSON: the steps as they were at the start, depth, folder, time limit
  status         TEXT NOT NULL DEFAULT 'running', -- running | done | failed | stopped
  stage          INTEGER NOT NULL DEFAULT 0, -- the step it is at
  stage_at       TEXT NOT NULL DEFAULT '',   -- when that step began
  result         TEXT NOT NULL DEFAULT '',
  error          TEXT NOT NULL DEFAULT '',
  from_thread_id TEXT NOT NULL DEFAULT '',   -- the conversation it was started from
  report         INTEGER NOT NULL DEFAULT 0, -- its result goes back there as a message
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  finished_at    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX panels_user ON panels(user_id, created_at);

CREATE TABLE panel_seats (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  panel_id   TEXT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  idx        INTEGER NOT NULL,
  chair      INTEGER NOT NULL DEFAULT 0,     -- writes the summary instead of taking part
  label      TEXT NOT NULL DEFAULT '',       -- A, B, C … (what the others see instead of its name)
  provider   TEXT NOT NULL,
  model      TEXT NOT NULL DEFAULT '',
  effort     TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0,
  host_id    INTEGER NOT NULL DEFAULT 0,
  host_home  TEXT NOT NULL DEFAULT '',
  role       TEXT NOT NULL DEFAULT '',
  thread_id  TEXT NOT NULL DEFAULT '',
  asked      INTEGER NOT NULL DEFAULT -1,    -- the last step it was sent
  state      TEXT NOT NULL DEFAULT 'waiting',-- waiting | working | done | failed | skipped
  error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX panel_seats_panel ON panel_seats(panel_id, idx);
CREATE INDEX panel_seats_thread ON panel_seats(thread_id);

CREATE TABLE panel_answers (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  panel_id   TEXT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  seat_id    INTEGER NOT NULL,
  stage      INTEGER NOT NULL,
  text       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(panel_id, seat_id, stage)
);

-- A saved set of participants ("My council").
CREATE TABLE panel_lineups (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  name       TEXT NOT NULL,
  seats      TEXT NOT NULL DEFAULT '[]',     -- JSON
  chair      TEXT NOT NULL DEFAULT '{}',     -- JSON
  is_default INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX panel_lineups_user ON panel_lineups(user_id);

-- A person's own kind of panel: named steps with their instructions.
CREATE TABLE panel_recipes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  stages      TEXT NOT NULL DEFAULT '[]',    -- JSON
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX panel_recipes_user ON panel_recipes(user_id);
