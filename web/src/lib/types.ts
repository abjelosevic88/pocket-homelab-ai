// Shapes of the Pocket AI API (see docs/api.md).

export interface User {
  id: number
  email: string
  name: string
  username: string
  role: 'admin' | 'member'
  can_run: boolean
  created_at: string
}

export interface AuthStatus {
  needs_setup: boolean
  setup_needed?: boolean
  core_configured?: boolean
  auth_mode: string
  authenticated: boolean
  user: User | null
  version: string
  title: string
  oidc?: boolean
  oidc_name?: string
}

export type ProviderId = 'claude' | 'codex'
export type NotifyKind = 'apprise' | 'ntfy' | 'webhook'

export interface AgentDefaults { model: string; effort: string; mode: string; speed?: string; account_id?: number }

/** A Claude Code / Codex login on the server. id 0 = the server's own login. */
/** Another machine agents run on, reached over ssh from the server. */
export interface Host {
  id: number
  name: string
  /** ssh destination: a Host from ~/.ssh/config or user@host */
  target: string
  port: number
  claude_bin: string
  codex_bin: string
  info: {
    ok: boolean
    error?: string
    home?: string
    os?: string
    hostname?: string
    versions?: Partial<Record<ProviderId, string>>
    missing?: Partial<Record<ProviderId, string>>
    checked_at?: string
  }
  created_at: string
}

export interface Account {
  id: number
  provider: ProviderId
  label: string
  home: string
  user_id: number | null
  default: boolean
  signed_in: boolean
  email?: string
  plan?: string
  error?: string
  checked_at?: string
  org?: string
  /** Another login on the server is signed in to this same account. */
  same_as?: string
}

/** A sign-in started from Settings (Claude: paste the code; Codex: type the code on the page). */
export interface LoginState { state: 'idle' | 'starting' | 'waiting' | 'checking' | 'done' | 'failed' | 'cancelled'; url?: string; code?: string; need_code?: boolean; error?: string; code_sent?: boolean }

export interface Settings {
  title: string
  app_effort: string
  app_model: string
  /** Claude login for connected apps; -1 follows the Claude chat default. */
  app_account_id: number
  default_provider: ProviderId
  default_cwd: string
  claude: AgentDefaults
  codex: AgentDefaults
  notify_kind: NotifyKind
  notify_url: string
  notify_finished: boolean
  notify_input: boolean
  notify_failed: boolean
  notify_only_away: boolean
  enter_sends: boolean
  enter_send_now: boolean
  show_thinking: boolean
  usage_widget: boolean
  thread_tabs: boolean
  /** Keyboard shortcuts changed from their defaults (lib/shortcuts.ts); "" = off. */
  shortcuts?: Record<string, string>
  usage_pinned: string[]
  system_prompt: string
}

export interface CoreStatus { configured: boolean; url: string; registered: boolean; error: string }

/** What the app keeps from GET /settings — the bare object, or the
 *  template-style wrapper `{settings, notify_has_token, core}`. */
export interface SettingsResponse {
  settings: Settings
  notify_has_token?: boolean
  notify_box_route?: boolean
  core?: CoreStatus
}

export interface APIToken {
  id: number
  user_id: number
  name: string
  prefix: string
  last_used_at: string | null
  created_at: string
}

// ---------- providers ----------

export interface ProviderModel { id: string; name: string; description?: string; efforts?: string[]; fast?: boolean }
export interface ProviderSpeed { id: string; name: string; description?: string; available: boolean; note?: string }
export interface ProviderMode { id: string; name: string; description?: string }

export interface Provider {
  id: ProviderId
  name: string
  available: boolean
  version: string
  error: string
  models: ProviderModel[]
  efforts: string[]
  modes: ProviderMode[]
  speeds?: ProviderSpeed[]
  default_model: string
  default_effort: string
  default_mode: string
  account?: string
}

// ---------- threads ----------

export interface Usage {
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  cost_usd: number
  context_tokens: number
  context_window: number
  turns: number
}

export type ThreadStatus = 'idle' | 'running' | 'waiting' | 'error'

export interface Thread {
  no_memory: boolean
  id: string
  title: string
  provider: ProviderId
  /** The login it runs on (0 = the server's own). */
  account_id: number
  /** The machine it runs on (0 = this server, else a Host). */
  host_id: number
  /** Its own git worktree (0 = none). */
  worktree_id: number
  model: string
  effort: string
  mode: string
  speed?: string
  cwd: string
  workspace_id: number | null
  provider_session_id: string
  status: ThreadStatus
  error: string
  pinned: boolean
  archived: boolean
  unread: boolean
  source: 'web' | 'recipe' | 'schedule' | 'import' | 'api' | 'ask' | 'fork' | 'rewind'
  source_ref: string
  preview: string
  usage: Usage
  created_at: string
  updated_at: string
  activity_at: string
  live: boolean
  pending: number
  queued: number
  external?: boolean
  subagents?: Subagent[]
  last_prompt?: string
  prompt_at?: string
  /** Whose thread it is, and who else may open it (shared threads). */
  owner_id: number
  owner_name?: string
  shares?: ThreadShare[]
}

/** Someone a thread is shared with: view reads along, chat also sends. */
export interface ThreadShare { user_id: number; name: string; role: 'view' | 'chat' }
/** Someone a thread could be shared with (GET /threads/{id}/shares). */
export interface SharePerson { id: number; name: string; email: string; can_run: boolean }

export interface ItemImage { id: string; name: string; url: string }

export type ToolKind = 'bash' | 'edit' | 'write' | 'read' | 'search' | 'web' | 'task' | 'mcp' | 'skill' | 'other'

/** A skill or custom command the CLI loads in a folder (GET /skills). */
export interface Skill { name: string; description: string; argument_hint?: string; scope?: string; invoke: string }

export interface ToolInfo {
  name: string
  title: string
  kind: ToolKind
  input: unknown
  output: string
  status: 'running' | 'done' | 'error' | 'denied'
  exit_code?: number
  diff?: string
  images?: number
}

export interface Todo { text: string; status: 'pending' | 'in_progress' | 'completed' }

export interface Question {
  id: string
  header: string
  question: string
  multi: boolean
  options: { label: string; description?: string }[]
  other?: boolean
}

export interface AgentRequest {
  id: string
  type: 'permission' | 'question' | 'plan'
  tool_name: string
  title: string
  input: unknown
  questions?: Question[]
  plan?: string
  can_allow_session?: boolean
  status: 'pending' | 'allowed' | 'denied' | 'answered' | 'expired'
  answer?: unknown
  /** A Pocket app change in a shared chat: whose apps (only they answer it). */
  for_id?: number
  for?: string
  /** A Pocket app change: its app and kind (add | change), and the rule that allowed it automatically. */
  app?: string
  kind?: 'add' | 'change'
  auto?: string
}

export interface TurnInfo { ok: boolean; duration_ms: number; cost_usd: number; input_tokens: number; output_tokens: number; error?: string
  /** you pressed Stop: not an error, nothing to say about it */
  interrupted?: boolean }

export interface Item {
  files?: ItemImage[]
  id: number
  key: string
  kind: 'user' | 'assistant' | 'thinking' | 'tool' | 'todos' | 'request' | 'notice' | 'turn' | string
  created_at: string
  updated_at: string
  text?: string
  images?: ItemImage[]
  queued?: boolean
  tool?: ToolInfo
  todos?: Todo[]
  request?: AgentRequest
  level?: 'info' | 'warn' | 'error'
  /** notice: what the page offers to put it right ('folder' = move the conversation) */
  fix?: string
  turn?: TurnInfo
  log_images?: number
  unsent?: boolean
  /** why it was not sent, when it is not a restart */
  unsent_why?: string
  /** Who sent a message in a shared thread. */
  by?: string
  by_id?: number
}

export interface ThreadDetail { thread: Thread; items: Item[] }

export interface Upload { id: string; name: string; mime: string; size: number; url: string; created_at: string }

// ---------- workspaces, recipes, schedules ----------

export interface Workspace { id: number; name: string; path: string; color: string; sort_order: number; threads: number; created_at: string }

export interface GitBranch { name: string; remote?: boolean; current?: boolean; upstream?: string; time?: number; subject?: string }
export interface GitInfo { repo: boolean; root?: string; branch?: string; detached?: boolean; head?: string; dirty: number; ahead: number; behind: number; branches?: GitBranch[] }

export interface DirListing {
  path: string; parent: string; dirs: { name: string; path: string; git: boolean }[]; roots: string[]
  home?: string
  /** another machine: its working folders outside home (/work, /srv/app) */
  places?: string[]
  /** the folder asked for does not exist; path is the nearest one that does */
  missing?: string
}

export interface AgentChoice { provider: ProviderId | ''; model: string; effort: string; mode: string; speed?: string; cwd: string }

export interface Recipe extends AgentChoice {
  id: number
  name: string
  prompt: string
  icon: string
  pinned: boolean
  sort_order: number
  runs: number
  created_at: string
  updated_at: string
}

export interface Schedule extends AgentChoice {
  id: number
  name: string
  /** The machine it runs on (0 = this server, else a Host). */
  host_id: number
  prompt: string
  spec: string
  notify: 'always' | 'attention' | 'never'
  enabled: boolean
  next_run_at: string
  last_run_at: string
  last_thread_id: string
  last_status: '' | 'running' | 'ok' | 'attention' | 'failed'
  last_summary: string
  created_at: string
  updated_at: string
}

export interface SchedulePreview { ok: boolean; next: string[]; error: string }

// ---------- history, search, usage ----------

export type Origin = 'pocket-ai' | 'schedule' | 'paseo' | 'terminal' | 'app'

/** A session either CLI keeps on the server ("On this server"). */
export interface ServerSession {
  provider: ProviderId
  session_id: string
  title: string
  last_prompt: string
  cwd: string
  origin: Origin
  model?: string
  started_at: string
  updated_at: string
  runs: number
  work_ms: number
  running: boolean
  running_at?: string
  thread_id: string
  /** Its Pocket AI thread is shared with these people (absent = private). */
  shared_with?: string[]
}

/** One prompt and the agent's work on it (the Today page). */
export interface Run {
  provider: ProviderId
  session_id: string
  key?: string
  title: string
  prompt: string
  images?: number
  cwd: string
  origin: Origin
  model?: string
  started_at: string
  ended_at?: string
  duration_ms: number
  status: 'running' | 'done' | 'interrupted' | 'unfinished'
  tools: number
  follow_ups?: number
  output_tokens?: number
  thread_id?: string
  /** Its Pocket AI thread is shared with these people (absent = private). */
  shared_with?: string[]
}

export interface SearchHit { thread_id: string; title: string; provider: ProviderId; snippet: string; item_key: string; activity_at: string }

export interface LimitWindow { id?: string; name: string; used: number; resets_at: string }
export interface ResetCredit { title: string; granted_at?: string; expires_at?: string }
/** Codex's free "reset my limits" credits; each one expires. */
export interface Resets { available: number; credits?: ResetCredit[] }
export interface ProviderLimits {
  provider?: ProviderId; account_id?: number; label?: string; windows: LimitWindow[]; updated_at: string; note?: string; error?: string; resets?: Resets
  /** Who the login is signed in as (email only for your own logins, or admins). */
  email?: string; org?: string; plan?: string
  /** The server's own login is (or is signed in to the same account as) this one. */
  server_login?: boolean
  /** Limits keys of other logins on the same account, folded into this one. */
  aliases?: string[]
}
/** Keyed "claude" / "codex" for the server's own login, "claude:3" for account 3;
 * logins signed in to the same account arrive once, under the named one. */
export type Limits = Record<string, ProviderLimits | undefined>

export interface UsageReport {
  days: { date: string; cost_usd: number; input_tokens: number; output_tokens: number; turns: number }[]
  providers: Partial<Record<ProviderId, { cost_usd: number; turns: number }>>
  limits: Limits
}

export interface SystemInfo {
  version: string
  uptime?: string
  go?: string
  auth_mode?: string
  timezone?: string
  base_url?: string
  db_size?: number
  users?: number
  core?: CoreStatus
  counts?: Record<string, number>
  [k: string]: unknown
}

// ---------- running now ----------

export interface RunningItem {
  provider: ProviderId
  session_id: string
  cwd: string
  title: string
  prompt: string
  prompt_at: string
  updated_at: string
  origin: 'pocket-ai' | 'paseo' | 'terminal' | 'app'
  subagents?: number
  thread_id: string
  status: 'running' | 'waiting'
  /** Its Pocket AI thread is shared with these people (absent = private). */
  shared_with?: string[]
}

export interface Subagent { id: string; description: string; activity?: string; type?: string; started_at: string; tool_uses?: number }
/** A sub-agent of a Claude Code session, working or finished (GET /threads/{id}/subagents). */
export interface SubagentInfo {
  id: string; description: string; type?: string; model?: string
  status: 'running' | 'done' | 'stopped'
  started_at?: string; ended_at?: string; tool_uses?: number
  /** how full its context was at its last reply */
  tokens?: number
  activity?: string; result?: string
}

/** A git worktree Pocket AI made for a task: its own folder and branch. */
export interface Worktree {
  id: number
  owner_id: number
  host_id: number
  host_name?: string
  repo: string
  path: string
  branch: string
  base: string
  base_sha: string
  title: string
  status: 'active' | 'archived'
  setup: '' | 'running' | 'ok' | 'failed'
  archive_ref: string
  merged_at: string
  pr_url: string
  created_at: string
  archived_at: string
  threads: { id: string; title: string; status: string; provider: ProviderId; updated_at: string; live: boolean }[]
  state?: WorktreeState
}

export interface WorktreeState {
  gone: boolean
  no_base: boolean
  head: string
  branch: string
  ahead: number
  behind: number
  dirty: number
  merged: boolean
  pr_state?: string
  upstream: string
  unpushed: number
  merging: boolean
  rebasing: boolean
  conflicts: string[]
  commits: { sha: string; time: number; subject: string }[]
  error?: string
  size_kb?: number
}

export interface WorktreeSetupConfig { setup: string[]; teardown: string[]; copy: string[]; base?: string }

export interface WorktreeRepoInfo {
  repo: string
  top: string
  branch: string
  main_branch: string
  default: string
  base: string
  branches: string[]
  dir: string
  is_worktree: boolean
  /** who the repo is on any machine (github.com/owner/repo); empty = no git remote */
  project_id?: string
  config: {
    file: string; file_config: WorktreeSetupConfig; saved: WorktreeSetupConfig | null; effective: WorktreeSetupConfig; default_copy: string[]
    project?: string
    /** where saved comes from: every machine (project), this checkout only (machine), or nothing saved */
    scope?: 'project' | 'machine' | ''
  }
}

/** How a new thread gets its worktree. */
export interface WorktreeSpec { mode: 'new' | 'branch' | 'pr' | 'existing'; base?: string; branch?: string; pr?: number; id?: number; name?: string }
