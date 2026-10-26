-- Personal shortcuts, not filesystem objects or shared workspaces. Host 0 is local.
CREATE TABLE remembered_folders (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  host_id INTEGER NOT NULL DEFAULT 0,
  path TEXT NOT NULL,
  used_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, host_id, path)
);
CREATE TABLE folder_choices (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  host_id INTEGER NOT NULL DEFAULT 0,
  path TEXT NOT NULL,
  PRIMARY KEY (user_id, host_id)
);
CREATE TRIGGER forget_deleted_host_folders AFTER DELETE ON hosts BEGIN
  DELETE FROM remembered_folders WHERE host_id = OLD.id;
  DELETE FROM folder_choices WHERE host_id = OLD.id;
END;
