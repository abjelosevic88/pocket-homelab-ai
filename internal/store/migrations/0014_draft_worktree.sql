-- A prepared conversation can ask to work in a git worktree of its own
-- (Pocket Projects: "a worktree for this ticket"): JSON, '' = none.
ALTER TABLE link_drafts ADD COLUMN worktree TEXT NOT NULL DEFAULT '';
