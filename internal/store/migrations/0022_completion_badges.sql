ALTER TABLE threads ADD COLUMN completed_unread INTEGER NOT NULL DEFAULT 0;
-- Preserve successful, unseen completions from before mobile badges existed.
UPDATE threads SET completed_unread = 1 WHERE unread = 1 AND status = 'idle'
 AND (SELECT ok FROM turns WHERE thread_id = threads.id ORDER BY id DESC LIMIT 1) = 1;
CREATE INDEX threads_completion_badge ON threads(user_id, completed_unread, unread, archived, status);
