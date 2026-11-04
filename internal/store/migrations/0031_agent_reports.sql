-- An agent another agent started (a thread with source "agent", source_ref =
-- the parent) reports its answer back to the parent when its turn ends, once
-- per message the parent sent it. A row = such a report is still owed.
CREATE TABLE agent_reports (
  child_id   TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
  parent_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL
);
