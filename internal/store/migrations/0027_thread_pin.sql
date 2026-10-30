-- A conversation a Pocket Projects issue started is pinned to that issue: its
-- Projects changes may only touch the issue (and new sub-issues of it).
-- unpinned = the person lifted that for this conversation.
ALTER TABLE thread_context ADD COLUMN unpinned INTEGER NOT NULL DEFAULT 0;
