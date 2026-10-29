-- A thread on another machine can run on any Claude Code / Codex login there
-- (a CLAUDE_CONFIG_DIR / CODEX_HOME folder like ~/.claude-max), not only the
-- default one. '' = the default (~/.claude, ~/.codex).
ALTER TABLE threads ADD COLUMN host_home TEXT NOT NULL DEFAULT '';
