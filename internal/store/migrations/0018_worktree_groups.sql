-- A person's own groups for worktrees (the Worktrees page), arranged by
-- dragging. A worktree in no group shows under its repository.
CREATE TABLE worktree_groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  collapsed INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE worktree_group_items (
  worktree_id INTEGER PRIMARY KEY REFERENCES worktrees(id) ON DELETE CASCADE,
  group_id INTEGER NOT NULL REFERENCES worktree_groups(id) ON DELETE CASCADE,
  position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX worktree_group_items_group ON worktree_group_items(group_id, position);
