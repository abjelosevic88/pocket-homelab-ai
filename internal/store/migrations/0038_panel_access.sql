-- What a line-up's participants may do without asking: '' (only look),
-- 'auto' (each agent's own automatic mode) or 'full' (never asks).
ALTER TABLE panel_lineups ADD COLUMN access TEXT NOT NULL DEFAULT '';
