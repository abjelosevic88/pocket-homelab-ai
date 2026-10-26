-- A prepared conversation can run on another machine (Settings → Servers):
-- 0 = this server.
ALTER TABLE link_drafts ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0;
