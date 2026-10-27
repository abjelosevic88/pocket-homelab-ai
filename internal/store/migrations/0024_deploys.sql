-- Deploys: a branch put on a project's deploy target (one slot per project:
-- the newest deploy is what runs there, unless something else replaced it).
CREATE TABLE deploys (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project TEXT NOT NULL,
    host_id INTEGER NOT NULL,
    branch TEXT NOT NULL,
    sha TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX deploys_project ON deploys(project, id);
