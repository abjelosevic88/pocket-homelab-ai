-- A panel keeps every version of the thing it works on (a plan, a text): the
-- original, each rewrite, and what the person changed in the panel's chat or
-- by hand. The newest is the current one. panel_chat is the person's
-- conversation with the panel's editor about it.
CREATE TABLE panel_versions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  panel_id   TEXT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  n          INTEGER NOT NULL,
  text       TEXT NOT NULL,
  source     TEXT NOT NULL,              -- original | rewrite | chat | edit | restore
  note       TEXT NOT NULL DEFAULT '',   -- what changed, in the editor's or the person's words
  stage      INTEGER NOT NULL DEFAULT -1,-- the step that wrote it (-1 = none)
  created_at TEXT NOT NULL,
  UNIQUE(panel_id, n)
);

CREATE TABLE panel_chat (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  panel_id   TEXT NOT NULL REFERENCES panels(id) ON DELETE CASCADE,
  role       TEXT NOT NULL,              -- person | editor
  text       TEXT NOT NULL,
  proposal   TEXT NOT NULL DEFAULT '',   -- a new version the editor proposes
  version_n  INTEGER NOT NULL DEFAULT 0, -- the version it became once taken
  state      TEXT NOT NULL DEFAULT '',   -- editor: working | done | failed
  created_at TEXT NOT NULL
);
CREATE INDEX panel_chat_panel ON panel_chat(panel_id, id);
