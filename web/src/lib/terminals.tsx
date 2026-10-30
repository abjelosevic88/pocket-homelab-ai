// Your terminals and how they are arranged: each terminal tab (in the tab
// bar, next to conversations) holds one terminal or a split of several. The
// arrangement is kept on the server so every device shows the same.
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from './api'
import { useApp } from './app-context'
import { useNavigate } from 'react-router-dom'
import { errMsg, type MenuItem, useToast } from '../components/ui'
import { filePath, isFileTab, parseFileTab } from './files'

export interface Term { id: string; host_id: number; host_name: string; title: string; cwd?: string; created_at: string; ended: boolean; exit_code: number; viewers: number }

// A tab's terminals: t = a terminal's id; dir row = side by side, col = one
// above the other; r = the first side's share.
export type Node = { t: string } | { dir: 'row' | 'col'; a: Node; b: Node; r: number }
export interface TermGroup { id: string; root: Node; focus: string }

export const isLeaf = (n: Node): n is { t: string } => 't' in n
export const idsOf = (n: Node): string[] => isLeaf(n) ? [n.t] : [...idsOf(n.a), ...idsOf(n.b)]
// without the terminals that are gone; a split with one side left becomes that side
// (each terminal in one place only: a second mention is dropped)
const prune = (n: Node, alive: Set<string>, seen: Set<string>): Node | null => {
  if (isLeaf(n)) { if (!alive.has(n.t) || seen.has(n.t)) return null; seen.add(n.t); return n }
  const a = prune(n.a, alive, seen), b = prune(n.b, alive, seen)
  return a && b ? { ...n, a, b } : a || b
}
const replaceLeaf = (n: Node, id: string, by: Node): Node =>
  isLeaf(n) ? (n.t === id ? by : n) : { ...n, a: replaceLeaf(n.a, id, by), b: replaceLeaf(n.b, id, by) }
const setRatioAt = (n: Node, path: string, r: number): Node =>
  isLeaf(n) ? n : path === '' ? { ...n, r } : path[0] === 'a' ? { ...n, a: setRatioAt(n.a, path.slice(1), r) } : { ...n, b: setRatioAt(n.b, path.slice(1), r) }
const validNode = (n: unknown): n is Node => {
  const o = n as Record<string, unknown>
  if (!o || typeof o !== 'object') return false
  if (typeof o.t === 'string') return true
  return (o.dir === 'row' || o.dir === 'col') && typeof o.r === 'number' && validNode(o.a) && validNode(o.b)
}
// saved arrangements: [{id, root, focus}] (before: bare trees, one per tab)
const readGroups = (v: unknown): TermGroup[] => !Array.isArray(v) ? [] : v.flatMap(x => {
  if (validNode(x)) return [{ id: idsOf(x)[0], root: x, focus: idsOf(x)[0] }]
  const g = x as TermGroup
  return g && typeof g.id === 'string' && validNode(g.root) ? [{ id: g.id, root: g.root, focus: typeof g.focus === 'string' ? g.focus : '' }] : []
})
// the arrangement fitted to the terminals there are: gone ones out, new ones
// (opened on another device) as tabs of their own
const reconcile = (groups: TermGroup[], list: Term[]): TermGroup[] => {
  const alive = new Set(list.map(t => t.id))
  const seen = new Set<string>()
  const out: TermGroup[] = []
  for (const g of groups) {
    if (out.some(x => x.id === g.id)) continue
    const root = prune(g.root, alive, seen)
    if (!root) continue
    const ids = idsOf(root)
    out.push({ ...g, root, focus: ids.includes(g.focus) ? g.focus : ids[0] })
  }
  const placed = new Set(out.flatMap(g => idsOf(g.root)))
  for (const t of list) if (!placed.has(t.id)) out.push({ id: t.id, root: { t: t.id }, focus: t.id })
  return out
}
const newGroupID = () => 'g' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6)

/** Tab ids in the tab bar: conversations are their thread id, terminal tabs "term:<group id>", files "file:<host>:<path>". */
export const TERM_TAB = 'term:'
export const isTermTab = (id: string) => id.startsWith(TERM_TAB)
export const tabPath = (id: string) => isTermTab(id) ? `/term/${id.slice(TERM_TAB.length)}` : isFileTab(id) ? (f => filePath(f.host, f.path))(parseFileTab(id)) : `/t/${id}`

interface Ctx {
  list: Term[] | null
  groups: TermGroup[]
  byId: Map<string, Term>
  opening: boolean
  /** a new terminal tab; its group id (null: it could not start) */
  open: (host_id: number, cwd?: string) => Promise<string | null>
  split: (gid: string, id: string, dir: 'row' | 'col') => Promise<void>
  closePanes: (ids: string[]) => Promise<void>
  closeGroups: (gids: string[]) => Promise<void>
  again: (t: Term) => Promise<void>
  focus: (gid: string, id: string) => void
  setRatio: (gid: string, path: string, r: number) => void
  ended: (id: string, code: number) => void
  renamed: (t: Term) => void
}
const TermCtx = createContext<Ctx | null>(null)
export const useTerminals = () => useContext(TermCtx)!

export function TerminalsProvider({ children }: { children: React.ReactNode }) {
  const { user, terms } = useApp()
  const { toast } = useToast()
  const [list, setList] = useState<Term[] | null>(null)
  const [groups, setGroupsRaw] = useState<TermGroup[]>([])
  const [opening, setOpening] = useState(false)
  const loaded = useRef(false)
  const saveTimer = useRef(0)
  // terminals being started here: a reload meanwhile would see one the
  // arrangement does not know yet and give it a tab of its own
  const starting = useRef(0)
  const reloadTimer = useRef(0)
  const mayRun = !!user && (user.role === 'admin' || !!user.can_run)

  const setGroups = useCallback((f: (g: TermGroup[]) => TermGroup[]) => setGroupsRaw(prev => {
    const next = f(prev)
    if (loaded.current && JSON.stringify(prev) !== JSON.stringify(next)) {
      clearTimeout(saveTimer.current)
      saveTimer.current = window.setTimeout(() => { api.put(`${V1}/terminals/layout`, { layout: next }).catch(() => {}) }, 400)
    }
    return next
  }), [])

  const load: () => Promise<void> = useCallback(() => Promise.all([
    api.get<Term[]>(`${V1}/terminals`),
    api.get<{ layout: unknown }>(`${V1}/terminals/layout`).catch(() => ({ layout: null })),
  ]).then(([l, lay]) => {
    if (starting.current > 0 && loaded.current) { clearTimeout(reloadTimer.current); reloadTimer.current = window.setTimeout(() => load(), 1500); return }
    // the first load takes the saved arrangement (decided here: the updater
    // runs later, when loaded is already true)
    const saved = loaded.current ? null : readGroups(lay.layout)
    setList(l)
    setGroupsRaw(g => reconcile(saved || g, l))
    loaded.current = true
  }).catch(() => setList(l => l || [])), []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (mayRun) load() }, [mayRun, load])
  useEffect(() => { if (list) setGroups(g => reconcile(g, list)) }, [list, setGroups])
  // opened or closed on another device: the count the server pushes differs
  const liveOpen = list ? list.filter(t => !t.ended).length : -1
  useEffect(() => { if (mayRun && loaded.current && terms.open !== liveOpen) load() }, [terms.open]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = useCallback(async (host_id: number, cwd = '') => {
    setOpening(true)
    starting.current++
    try { return await api.post<Term>(`${V1}/terminals`, { host_id, cwd }) } catch (e) { toast(errMsg(e), 'err'); return null } finally {
      setOpening(false)
      setTimeout(() => { starting.current-- }, 0) // after the caller placed it
    }
  }, [toast])

  const open = useCallback(async (host_id: number, cwd = '') => {
    const t = await create(host_id, cwd)
    if (!t) return null
    const gid = newGroupID()
    setGroups(g => [...g, { id: gid, root: { t: t.id }, focus: t.id }])
    setList(l => [...(l || []), t])
    return gid
  }, [create, setGroups])

  // a new terminal next to `id`, on the same machine and folder
  const split = useCallback(async (gid: string, id: string, dir: 'row' | 'col') => {
    const cur = list?.find(t => t.id === id)
    if (!cur) return
    const t = await create(cur.host_id, cur.cwd)
    if (!t) return
    setGroups(gs => gs.map(g => g.id === gid ? { ...g, root: replaceLeaf(g.root, id, { dir, a: { t: id }, b: { t: t.id }, r: .5 }), focus: t.id } : g))
    setList(l => [...(l || []), t])
  }, [list, create, setGroups])

  const closePanes = useCallback(async (ids: string[]) => {
    await Promise.all(ids.map(id => api.del(`${V1}/terminals/${id}`).catch(e => toast(errMsg(e), 'err'))))
    const gone = new Set(ids)
    setList(l => (l || []).filter(x => !gone.has(x.id)))
  }, [toast])
  const closeGroups = useCallback(async (gids: string[]) => {
    const ids = groups.filter(g => gids.includes(g.id)).flatMap(g => idsOf(g.root))
    if (ids.length) await closePanes(ids)
  }, [groups, closePanes])

  const again = useCallback(async (t: Term) => {
    const n = await create(t.host_id, t.cwd)
    if (!n) return
    setGroups(gs => gs.map(g => idsOf(g.root).includes(t.id) ? { ...g, root: replaceLeaf(g.root, t.id, { t: n.id }), focus: n.id } : g))
    setList(l => [...(l || []).filter(x => x.id !== t.id), n])
    api.del(`${V1}/terminals/${t.id}`).catch(() => {})
  }, [create, setGroups])

  const focus = useCallback((gid: string, id: string) => setGroups(gs => gs.map(g => g.id === gid && g.focus !== id ? { ...g, focus: id } : g)), [setGroups])
  const setRatio = useCallback((gid: string, path: string, r: number) => setGroups(gs => gs.map(g => g.id === gid ? { ...g, root: setRatioAt(g.root, path, r) } : g)), [setGroups])
  const ended = useCallback((id: string, code: number) => setList(l => l && l.map(t => t.id === id ? { ...t, ended: true, exit_code: code } : t)), [])
  const renamed = useCallback((t: Term) => setList(l => l && l.map(x => x.id === t.id ? t : x)), [])

  const byId = useMemo(() => new Map((list || []).map(t => [t.id, t])), [list])
  const value = useMemo(() => ({ list, groups, byId, opening, open, split, closePanes, closeGroups, again, focus, setRatio, ended, renamed }),
    [list, groups, byId, opening, open, split, closePanes, closeGroups, again, focus, setRatio, ended, renamed])
  return <TermCtx.Provider value={value}>{children}</TermCtx.Provider>
}

/** The machines menu: a new terminal tab on this server or one of yours. */
export function useMachineItems(onOpen: (host_id: number) => void): MenuItem[] {
  const { hosts } = useApp()
  const navigate = useNavigate()
  return [
    { heading: 'New terminal on' },
    { label: 'This server', icon: 'terminal', onSelect: () => onOpen(0) },
    ...hosts.map(h => ({ label: h.name, icon: 'terminal', hint: h.info?.ok === false ? 'not reachable' : undefined, onSelect: () => onOpen(h.id) })),
    ...(hosts.length ? [] : ['sep' as const, { label: 'Add a server…', icon: 'plus', onSelect: () => navigate('/settings/agents') }]),
  ]
}

