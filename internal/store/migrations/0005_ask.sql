CREATE TABLE ask_connections (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 credential TEXT NOT NULL
);
CREATE TABLE ask_contexts (
 thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
 context TEXT NOT NULL
);
