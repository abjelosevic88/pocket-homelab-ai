ALTER TABLE threads ADD COLUMN no_memory INTEGER NOT NULL DEFAULT 0;
ALTER TABLE uploads ADD COLUMN extracted_text TEXT NOT NULL DEFAULT '';
CREATE TABLE memories (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 label TEXT NOT NULL, value TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX memories_user ON memories(user_id);
CREATE TABLE app_calls (
 id TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 thread_id TEXT REFERENCES threads(id) ON DELETE CASCADE, tool TEXT NOT NULL,
 input TEXT NOT NULL DEFAULT '{}', result TEXT NOT NULL DEFAULT '{}', before_state TEXT NOT NULL DEFAULT '{}',
 is_write INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'running', duration_ms INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')), completed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX app_calls_thread ON app_calls(user_id,thread_id,created_at);
CREATE TABLE workflows (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, prompt TEXT NOT NULL, model TEXT NOT NULL DEFAULT 'sonnet',
 trigger_kind TEXT NOT NULL DEFAULT 'manual', spec TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
 next_run_at TEXT NOT NULL DEFAULT '', last_thread_id TEXT NOT NULL DEFAULT '', last_run_at TEXT NOT NULL DEFAULT '',
 last_status TEXT NOT NULL DEFAULT '', seen TEXT NOT NULL DEFAULT '', notify INTEGER NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX workflows_user ON workflows(user_id);
CREATE TABLE workflow_runs (
 id INTEGER PRIMARY KEY, workflow_id INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
 trigger_key TEXT NOT NULL, thread_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(workflow_id,trigger_key)
);
