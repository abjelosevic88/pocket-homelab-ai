-- Reservations are retained even if the server stops mid-send: an uncertain send
-- must never be automatically started a second time after a restart.
CREATE TABLE message_receipts (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status INTEGER NOT NULL DEFAULT 0,
    body BLOB,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, thread_id, request_key)
);
CREATE TABLE mobile_devices (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id TEXT NOT NULL,
    token_id INTEGER NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    token TEXT NOT NULL,
    environment TEXT NOT NULL CHECK (environment IN ('sandbox', 'production')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, device_id),
    UNIQUE (token, environment)
);
