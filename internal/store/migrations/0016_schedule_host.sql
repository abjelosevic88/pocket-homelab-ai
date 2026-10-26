-- A schedule can run on another machine (Settings → Servers): 0 = this server.
ALTER TABLE schedules ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0;
