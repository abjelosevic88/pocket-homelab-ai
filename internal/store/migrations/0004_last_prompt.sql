-- The prompt a thread is (or was last) working on, for the "Running now" list.
ALTER TABLE threads ADD COLUMN last_prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN prompt_at TEXT NOT NULL DEFAULT '';
