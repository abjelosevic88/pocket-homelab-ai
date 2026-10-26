-- Groups inside groups on the Worktrees page: NULL = at the top.
ALTER TABLE worktree_groups ADD COLUMN parent_id INTEGER REFERENCES worktree_groups(id) ON DELETE SET NULL;
