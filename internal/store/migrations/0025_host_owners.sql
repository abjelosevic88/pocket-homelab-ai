-- Servers belong to the person who added them: everyone but the box owner
-- reaches theirs with an ssh key of their own. NULL = the owner's (the
-- server's own keys), like every server added before.
ALTER TABLE hosts ADD COLUMN user_id INTEGER;
CREATE TRIGGER forget_deleted_user_hosts AFTER DELETE ON users BEGIN
  DELETE FROM hosts WHERE user_id = OLD.id;
END;
