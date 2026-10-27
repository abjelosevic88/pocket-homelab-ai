-- A prompt can wait on another owned chat without keeping a phone connected.
CREATE TABLE deferred_prompts (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    after_thread_id TEXT NOT NULL,
    after_title TEXT NOT NULL,
    after_turns INTEGER NOT NULL,
    text TEXT NOT NULL,
    upload_ids TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'waiting' CHECK(status IN ('waiting','blocked','dispatching','uncertain','sent','cancelled')),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX deferred_prompts_waiting ON deferred_prompts(status, created_at);
CREATE INDEX deferred_prompts_thread ON deferred_prompts(user_id, thread_id);
