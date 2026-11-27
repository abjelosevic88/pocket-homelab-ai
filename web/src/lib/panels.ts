// Panels: one question put to several models at once (GET/POST /panels).
import { useEffect, useState } from 'react'
import { api, V1 } from './api'
import type { LimitWindow, ProviderId } from './types'

export interface PanelStage { name: string; who: 'all' | 'chair'; sees: '' | 'others' | 'all' | 'everything'; prompt: string; depth?: number }
export interface PanelKind { id: string; name: string; description: string; ask: string; material: string; roles: string[] | null; quorum: number; stages: PanelStage[]; command: string }
export interface OwnKind { id: number; name: string; description: string; stages: PanelStage[] }
export interface SeatSpec { provider: ProviderId; model: string; effort: string; account_id: number; host_id: number; host_home: string; role: string }
export interface PanelLogin { provider: ProviderId; account_id: number; host_id: number; host_home: string; label: string; where: string; signed_in: boolean; email?: string; plan?: string; used: number; resets_at?: string; windows?: LimitWindow[]; error?: string }
export interface Lineup { id: number; name: string; seats: SeatSpec[]; chair: SeatSpec | null; default: boolean; access: Access }
/** What participants may do without asking: only look, each agent's own automatic mode, or anything. */
export type Access = '' | 'auto' | 'full'
export const ACCESS: { value: Access; label: string; help: string }[] = [
  { value: '', label: 'Only look', help: 'They read and answer. One that wants to do more has to ask you, and waits for your answer.' },
  { value: 'auto', label: 'On their own', help: 'Each runs in its own automatic mode: it reads, searches and runs commands without asking, and stops only for what it finds risky.' },
  { value: 'full', label: 'Full access', help: 'They never ask. Each can run anything as you on its machine. They are told to change nothing unless you ask for it, but nothing stops them.' },
]
export interface PanelOptions {
  kinds: PanelKind[]
  depths: { id: Depth; name: string; description: string }[]
  roles: string[]
  lineups: Lineup[]
  own_kinds: OwnKind[]
  logins: PanelLogin[]
  suggested: { seats: SeatSpec[]; chair: SeatSpec | null; access?: Access }
  /** one participant per agent that works on the server, before the person chose */
  automatic: SeatSpec[]
  max_seats: number
}
export type Depth = 'quick' | 'standard' | 'deep'

export interface PanelSeat extends SeatSpec {
  id: number; idx: number; chair: boolean; label: string; thread_id: string; asked: number
  state: 'waiting' | 'working' | 'done' | 'failed' | 'skipped'; error: string
  name: string; who: string; machine: string
  activity: string; live?: string; cost_usd: number; tokens: number
  /** while it works: tool calls so far in this step, the latest in a few words, since when */
  steps?: number; doing?: string; since?: string
}
export interface PanelAnswer { id: number; seat_id: number; stage: number; text: string; created_at: string }
export interface Panel {
  id: string; kind: string; title: string; question: string; material: string
  spec: { depth: number; stages: PanelStage[]; kind_name: string; cwd: string; host_id: number; limit_min: number; quorum: number; access?: Access; images?: { id: string; name: string }[] }
  status: 'running' | 'done' | 'failed' | 'stopped'
  stage: number; stage_at: string; result: string; error: string
  from_thread_id: string; report: boolean; from_title?: string
  created_at: string; finished_at: string
  depth: Depth; seats: PanelSeat[]; answers?: PanelAnswer[]; cost_usd: number
  /** a panel that keeps the versions of what it works on (Refine): all of them, oldest first */
  versions?: PanelVersion[]
  /** the person's chat with the panel's editor; chat_live: what it is writing right now */
  chat?: PanelChatMessage[]; chat_live?: string; chat_activity?: string
  /** there is an editor to talk to */
  editor?: boolean
  url?: string
}
export interface PanelVersion { id: number; n: number; text: string; source: 'original' | 'rewrite' | 'chat' | 'edit' | 'restore'; note: string; stage: number; created_at: string }
export interface PanelChatMessage { id: number; role: 'person' | 'editor'; text: string; proposal?: string; version_n?: number; state?: 'working' | 'done' | 'failed' | ''; created_at: string }
export const VERSION_SOURCE: Record<string, string> = { original: 'Original', rewrite: 'Rewrite', chat: 'From the chat', edit: 'Your edit', restore: 'Went back' }

/** What a new panel starts with (the start sheet fills in the rest). */
export interface PanelDraft { kind?: string; question?: string; material?: string; fromThreadId?: string; cwd?: string; hostId?: number }

/** Ask the shell to open the start sheet. */
export const OPEN_PANEL = 'pai:panel-new'
export const openPanelSheet = (d: PanelDraft = {}) => window.dispatchEvent(new CustomEvent<PanelDraft>(OPEN_PANEL, { detail: d }))
/** A panel moved on (SSE "panel"): { id, status, stage, from_thread_id }. */
export const PANEL_EVENT = 'pai:panel'

/** The "/" commands that start a panel → its kind. */
export const PANEL_COMMANDS: Record<string, string> = { 'ask-all': 'ask', brainstorm: 'brainstorm', council: 'council', 'refine-panel': 'refine', debate: 'debate', 'second-opinion': 'review', 'best-of': 'build', committee: 'ask', advisor: 'review', panel: '' }

export const KIND_ICON: Record<string, string> = { refine: 'refresh', ask: 'message', brainstorm: 'sparkles', council: 'users', debate: 'repeat', review: 'eye', build: 'fork', recipe: 'list-checks' }

export const sameLogin = (a: Pick<SeatSpec, 'provider' | 'account_id' | 'host_id' | 'host_home'>, b: Pick<SeatSpec, 'provider' | 'account_id' | 'host_id' | 'host_home'>) =>
  a.provider === b.provider && (a.account_id || 0) === (b.account_id || 0) && (a.host_id || 0) === (b.host_id || 0) && (a.host_home || '') === (b.host_home || '')

const normLimitTokens = (s: string) => s.toLowerCase().replace(/[-_/[\]()\s]+/g, ' ').trim()

function windowScope(w: LimitWindow): string {
  const dot = w.name.indexOf(' · ')
  if (dot >= 0) return w.name.slice(dot + 3).trim().toLowerCase()
  const id = w.id || ''
  const col = id.indexOf(':')
  if (col >= 0 && id.slice(0, col).includes('scoped')) return id.slice(col + 1).trim().toLowerCase()
  return ''
}

function windowApplies(w: LimitWindow, models: (string | undefined)[]): boolean {
  const scope = windowScope(w)
  if (!scope) return true
  const cands = models
    .map(m => normLimitTokens(m || ''))
    .filter(t => t && t !== 'default' && !t.startsWith('default '))
    .map(t => ` ${t} `)
  if (!cands.length) return false
  const parts = scope.replace(/ and /g, ',').replace(/ & /g, ',').replace(/\//g, ',').split(',')
  for (const part of parts) {
    const p = normLimitTokens(part)
    if (!p) continue
    const needle = ` ${p} `
    if (cands.some(c => c.includes(needle))) return true
  }
  return false
}

/** Plan limit usage for a login and selected model (general windows + any
 *  model-scoped window that matches the model). */
export function loginQuota(l: PanelLogin | undefined, ...models: (string | undefined)[]): { used: number; resets_at?: string } {
  if (!l) return { used: -1 }
  if (!l.windows?.length) return { used: l.used, resets_at: l.resets_at }
  let used = -1
  let resets_at: string | undefined
  for (const w of l.windows) {
    if (!windowApplies(w, models)) continue
    if (used < 0 || w.used > used) {
      used = w.used
      resets_at = w.resets_at
    }
  }
  return { used, resets_at }
}

/** How many model runs a panel takes: every participant per round, plus the summaries. */
export function panelRuns(stages: PanelStage[], depth: number, seats: number): number {
  return stages.filter(s => (s.depth || 0) <= depth).reduce((n, s) => n + (s.who === 'chair' ? 1 : seats), 0)
}
export const DEPTHS: Depth[] = ['quick', 'standard', 'deep']

// ---------- panels as tabs ----------

/** A panel's tab id: "panel:<id>" (next to conversations, terminals and files). */
export const PANEL_TAB = 'panel:'
export const isPanelTab = (id: string) => id.startsWith(PANEL_TAB)
export const panelTab = (id: string) => PANEL_TAB + id

/** What the tab bar shows for a panel's tab, or for the conversation of one of
 * its participants (those are in no list): kept per browser, so the tabs
 * survive a reload. `gone`: it was deleted, its tab closes. */
export interface TabInfo { title: string; kind?: string; status?: string; provider?: string; gone?: boolean }
const TAB_INFO = 'pai-panel-tabs'
let tabInfo: Record<string, TabInfo> = (() => { try { return JSON.parse(localStorage.getItem(TAB_INFO) || '{}') || {} } catch { return {} } })()
const tabInfoSubs = new Set<() => void>()
function setTabInfo(next: Record<string, TabInfo>) {
  tabInfo = next
  try { localStorage.setItem(TAB_INFO, JSON.stringify(Object.fromEntries(Object.entries(next).slice(-80)))) } catch { /* full */ }
  tabInfoSubs.forEach(f => f())
}
/** Remember what a tab shows (a panel: its tab id from panelTab; a participant: its conversation id). */
export function rememberTab(id: string, info: TabInfo) {
  const cur = tabInfo[id]
  if (cur && !cur.gone && cur.title === info.title && cur.kind === info.kind && cur.status === info.status && cur.provider === info.provider) return
  setTabInfo({ ...tabInfo, [id]: info })
}
/** A panel changed state or went away (the "panel" event): its tab follows. */
export function panelTabEvent(d: { id?: string; status?: string; title?: string }) {
  const key = d.id ? panelTab(d.id) : ''
  const cur = key && tabInfo[key]
  if (!cur) return
  if (d.status === 'deleted') setTabInfo({ ...tabInfo, [key]: { ...cur, gone: true } })
  else if ((d.status && d.status !== cur.status) || (d.title && d.title !== cur.title)) setTabInfo({ ...tabInfo, [key]: { ...cur, status: d.status || cur.status, title: d.title || cur.title } })
}
export const tabInfoStore = {
  subscribe: (f: () => void) => { tabInfoSubs.add(f); return () => { tabInfoSubs.delete(f) } },
  get: () => tabInfo,
}

/** Forget a panel's tab (it was deleted here). */
export const forgetPanelTab = (id: string) => panelTabEvent({ id, status: 'deleted' })

/** How many of the person's panels are running right now (for the sidebar). */
export function useRunningPanels(on: boolean): number {
  const [n, setN] = useState(0)
  useEffect(() => {
    if (!on) return
    let live = true
    const load = () => { api.get<{ status: string }[]>(`${V1}/panels?limit=30`).then(l => { if (live) setN((l || []).filter(p => p.status === 'running').length) }).catch(() => {}) }
    load()
    window.addEventListener(PANEL_EVENT, load)
    return () => { live = false; window.removeEventListener(PANEL_EVENT, load) }
  }, [on])
  return n
}

/** What a plan, a file or an answer can be sent to, with the question the panel is started with. */
export const SEND_TO: { kind: string; label: string; hint: string; question: string }[] = [
  { kind: 'refine', label: 'Refine', hint: 'judge, rewrite, judge again', question: 'Refine this: judge it, improve it, and judge the improved version.' },
  { kind: 'council', label: 'Council', hint: 'judge it', question: 'Judge this: is it ready to act on, and what has to change?' },
  { kind: 'review', label: 'Second opinion', hint: 'find what is wrong', question: 'Review this critically: what is wrong, risky or missing?' },
  { kind: 'brainstorm', label: 'Brainstorm', hint: 'ideas on it', question: 'Brainstorm on this: what could be added, done differently or done better?' },
  { kind: 'debate', label: 'Debate', hint: 'for and against', question: 'Should we go ahead with this as it is?' },
  { kind: 'ask', label: 'Ask all', hint: 'what they make of it', question: 'What do you make of this? Give your own assessment.' },
]

/** Start a panel right away on the person's default models (no sheet, nothing posted to a conversation). */
export function startPanelNow(kind: string, material: string, name: string, fromThreadId?: string): Promise<Panel> {
  const k = SEND_TO.find(x => x.kind === kind)
  return api.post<Panel>(`${V1}/panels`, { kind, question: k?.question || '', material, title: `${k?.label || 'Panel'}: ${name}`.slice(0, 140), from_thread_id: fromThreadId || undefined, report: false })
}
