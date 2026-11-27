// Panels: one question put to several models at once (GET/POST /panels).
import type { ProviderId } from './types'

export interface PanelStage { name: string; who: 'all' | 'chair'; sees: '' | 'others' | 'all' | 'everything'; prompt: string; depth?: number }
export interface PanelKind { id: string; name: string; description: string; ask: string; material: string; roles: string[] | null; quorum: number; stages: PanelStage[]; command: string }
export interface OwnKind { id: number; name: string; description: string; stages: PanelStage[] }
export interface SeatSpec { provider: ProviderId; model: string; effort: string; account_id: number; host_id: number; host_home: string; role: string }
export interface PanelLogin { provider: ProviderId; account_id: number; host_id: number; host_home: string; label: string; where: string; signed_in: boolean; email?: string; plan?: string; used: number; resets_at?: string; error?: string }
export interface Lineup { id: number; name: string; seats: SeatSpec[]; chair: SeatSpec | null; default: boolean }
export interface PanelOptions {
  kinds: PanelKind[]
  depths: { id: Depth; name: string; description: string }[]
  roles: string[]
  lineups: Lineup[]
  own_kinds: OwnKind[]
  logins: PanelLogin[]
  suggested: { seats: SeatSpec[]; chair: SeatSpec | null }
  max_seats: number
}
export type Depth = 'quick' | 'standard' | 'deep'

export interface PanelSeat extends SeatSpec {
  id: number; idx: number; chair: boolean; label: string; thread_id: string; asked: number
  state: 'waiting' | 'working' | 'done' | 'failed' | 'skipped'; error: string
  name: string; who: string; machine: string
  activity: string; live?: string; cost_usd: number; tokens: number
}
export interface PanelAnswer { id: number; seat_id: number; stage: number; text: string; created_at: string }
export interface Panel {
  id: string; kind: string; title: string; question: string; material: string
  spec: { depth: number; stages: PanelStage[]; kind_name: string; cwd: string; host_id: number; limit_min: number; quorum: number }
  status: 'running' | 'done' | 'failed' | 'stopped'
  stage: number; stage_at: string; result: string; error: string
  from_thread_id: string; report: boolean; from_title?: string
  created_at: string; finished_at: string
  depth: Depth; seats: PanelSeat[]; answers?: PanelAnswer[]; cost_usd: number
}

/** What a new panel starts with (the start sheet fills in the rest). */
export interface PanelDraft { kind?: string; question?: string; material?: string; fromThreadId?: string; cwd?: string; hostId?: number }

/** Ask the shell to open the start sheet. */
export const OPEN_PANEL = 'pai:panel-new'
export const openPanelSheet = (d: PanelDraft = {}) => window.dispatchEvent(new CustomEvent<PanelDraft>(OPEN_PANEL, { detail: d }))
/** A panel moved on (SSE "panel"): { id, status, stage, from_thread_id }. */
export const PANEL_EVENT = 'pai:panel'

/** The "/" commands that start a panel → its kind. */
export const PANEL_COMMANDS: Record<string, string> = { 'ask-all': 'ask', brainstorm: 'brainstorm', council: 'council', debate: 'debate', 'second-opinion': 'review', 'best-of': 'build', committee: 'ask', advisor: 'review', panel: '' }

export const KIND_ICON: Record<string, string> = { ask: 'message', brainstorm: 'sparkles', council: 'users', debate: 'repeat', review: 'eye', build: 'fork', recipe: 'list-checks' }

export const sameLogin = (a: Pick<SeatSpec, 'provider' | 'account_id' | 'host_id' | 'host_home'>, b: Pick<SeatSpec, 'provider' | 'account_id' | 'host_id' | 'host_home'>) =>
  a.provider === b.provider && (a.account_id || 0) === (b.account_id || 0) && (a.host_id || 0) === (b.host_id || 0) && (a.host_home || '') === (b.host_home || '')

/** How many model runs a panel takes: every participant per round, plus the summaries. */
export function panelRuns(stages: PanelStage[], depth: number, seats: number): number {
  return stages.filter(s => (s.depth || 0) <= depth).reduce((n, s) => n + (s.who === 'chair' ? 1 : seats), 0)
}
export const DEPTHS: Depth[] = ['quick', 'standard', 'deep']
