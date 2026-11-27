-- One link per machine (docs/machine-link-plan.md).
--
-- controller_lease: the one server process of this installation that may
-- drive links. It renews every 10 s and the lease runs out after 30 s;
-- expires_at is Unix milliseconds. Only the holder allocates generations.
CREATE TABLE controller_lease (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  holder     TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

-- The installation id is the settings key "installation_id", made once.

-- A machine's link: the mode its person asked for (off | status | agents),
-- the last generation allocated for it (only ever goes up), and when and why
-- it fell back to ssh per agent (JSON {at, why}; '' = it did not).
ALTER TABLE hosts ADD COLUMN link_mode TEXT NOT NULL DEFAULT 'off';
ALTER TABLE hosts ADD COLUMN link_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN link_fallback TEXT NOT NULL DEFAULT '';

-- How each agent on a machine is reached: ssh | to-link | link | to-ssh.
-- A move state is written before the old route is closed, the final one only
-- after the new route works (phase 2).
CREATE TABLE agent_routes (
  thread_id  TEXT PRIMARY KEY,
  host_id    INTEGER NOT NULL,
  port       INTEGER NOT NULL DEFAULT 0,
  route      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX agent_routes_host ON agent_routes(host_id);
