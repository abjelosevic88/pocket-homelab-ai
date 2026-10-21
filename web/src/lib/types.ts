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

export interface AgentDefaults { model: string; effort: string; mode: string; speed?: string }

export interface Settings {
  title: string
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
  show_thinking: boolean
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
  id: string
  title: string
  provider: ProviderId
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
  source: 'web' | 'recipe' | 'schedule' | 'import' | 'api'
  source_ref: string
  preview: string
  usage: Usage
  created_at: string
  updated_at: string
  activity_at: string
  live: boolean
  pending: number
  queued: number
}

export interface ItemImage { id: string; name: string; url: string }

export type ToolKind = 'bash' | 'edit' | 'write' | 'read' | 'search' | 'web' | 'task' | 'mcp' | 'other'

export interface ToolInfo {
  name: string
  title: string
  kind: ToolKind
  input: unknown
  output: string
  status: 'running' | 'done' | 'error' | 'denied'
  exit_code?: number
  diff?: string
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
}

export interface TurnInfo { ok: boolean; duration_ms: number; cost_usd: number; input_tokens: number; output_tokens: number; error?: string }

export interface Item {
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
  turn?: TurnInfo
}

export interface ThreadDetail { thread: Thread; items: Item[] }

export interface Upload { id: string; name: string; mime: string; size: number; url: string; created_at: string }

// ---------- workspaces, recipes, schedules ----------

export interface Workspace { id: number; name: string; path: string; color: string; sort_order: number; threads: number; created_at: string }

export interface DirListing { path: string; parent: string; dirs: { name: string; path: string; git: boolean }[]; roots: string[] }

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

export interface HistorySession {
  provider: ProviderId
  session_id: string
  cwd: string
  title: string
  updated_at: string
  messages: number
  thread_id: string
}

export interface SearchHit { thread_id: string; title: string; provider: ProviderId; snippet: string; item_key: string; activity_at: string }

export interface LimitWindow { id?: string; name: string; used: number; resets_at: string }
export interface ProviderLimits { windows: LimitWindow[]; updated_at: string; note?: string; error?: string }
export type Limits = Partial<Record<ProviderId, ProviderLimits>>

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
