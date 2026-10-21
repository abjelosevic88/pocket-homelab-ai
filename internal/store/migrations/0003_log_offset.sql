-- Where Pocket AI stopped reading the CLI's own session file (Claude Code
-- .jsonl), so opening a thread catches up on what happened elsewhere.
-- -1 = unknown: start at the end of the file.
ALTER TABLE threads ADD COLUMN log_offset INTEGER NOT NULL DEFAULT -1;
