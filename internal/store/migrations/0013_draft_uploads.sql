-- Files the app attached to a prepared conversation (Pocket Projects: the
-- issue's screenshots and documents), as upload ids of the draft's person.
-- They go with the first message unless the person removes them.
ALTER TABLE link_drafts ADD COLUMN upload_ids TEXT NOT NULL DEFAULT '[]';
