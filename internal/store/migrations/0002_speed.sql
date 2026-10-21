-- Speed: "" = standard; "fast" (Claude Code fast mode) or a Codex service tier id ("priority").
ALTER TABLE threads ADD COLUMN speed TEXT NOT NULL DEFAULT '';
ALTER TABLE recipes ADD COLUMN speed TEXT NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN speed TEXT NOT NULL DEFAULT '';
