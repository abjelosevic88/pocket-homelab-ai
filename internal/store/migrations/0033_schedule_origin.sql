-- A schedule set from a conversation (pocket_agent_schedule_create, or the
-- thread's "Schedule…" menu) remembers it, and can send its answer back there.
ALTER TABLE schedules ADD COLUMN from_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN report INTEGER NOT NULL DEFAULT 0;
