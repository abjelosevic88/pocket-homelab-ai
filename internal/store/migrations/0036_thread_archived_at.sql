-- When a conversation went to the archive, for "Delete archived conversations
-- after". Ones archived before this was recorded count from now, so none goes
-- sooner than the full period after the person turns it on.
ALTER TABLE threads ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
UPDATE threads SET archived_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE archived = 1;
