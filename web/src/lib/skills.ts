// The skill library (one for every agent and machine) and how the machines
// stand against it. Server: skills_library.go, skills_sync.go.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, V1 } from './api'

export type LibFile = { path: string; size: number }
export type LibSkill = { name: string; description: string; body?: string; files?: LibFile[]; file_count: number; size: number; sum: string; modified: string; path: string; off: number[] }
export type LibAgent = { id: string; name: string; how: string }
export type Library = { dir: string; skills: LibSkill[]; agents: LibAgent[] }
export type HostSkillState = { state: 'same' | 'differs' | 'off' | 'failed'; where?: string; agent?: string; error?: string }
export type HostExtra = { name: string; description: string; where: string; agent?: string }
/** busy: what is being done there right now ("checking", "sending x" …). */
export type SyncHost = { id: number; name: string; on: boolean; at?: string; error?: string; busy?: string; skills: Record<string, HostSkillState>; extra: HostExtra[] }
export type SyncEvent = { at: string; host: string; skill: string; what: 'added' | 'updated' | 'removed' | 'taken' }
export type SyncStatus = { asked: boolean; running: boolean; at?: string; hosts: SyncHost[]; log: SyncEvent[] }

export const LIB = `${V1}/skills/library`
export const SYNC = `${V1}/skills/sync`
export const skillSlug = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+/, '').slice(0, 64)
export const validSkillName = (s: string) => /^[a-z0-9][a-z0-9-]{0,63}$/.test(s) && s !== 'synced'

/** How the machines stand. Asks again every few seconds while a sync runs, else now and then. */
export function useSkillSync() {
  const [sync, setSync] = useState<SyncStatus | null>(null)
  const [error, setError] = useState('')
  const timer = useRef(0)
  const alive = useRef(true)
  const load = useCallback(async (check = false) => {
    window.clearTimeout(timer.current)
    try {
      const s = await api.get<SyncStatus>(`${SYNC}${check ? '?check=1' : ''}`)
      if (!alive.current) return
      setSync(s); setError('')
      timer.current = window.setTimeout(() => { if (!document.hidden) void load(); else timer.current = window.setTimeout(() => void load(), 30000) }, s.running ? 1000 : 30000)
    } catch (e) {
      if (!alive.current) return
      setError((e as Error).message)
      timer.current = window.setTimeout(() => void load(), 30000)
    }
  }, [])
  useEffect(() => {
    alive.current = true
    void load()
    return () => { alive.current = false; window.clearTimeout(timer.current) }
  }, [load])
  /** A change was saved: the server syncs in a moment, so look again soon. */
  const soon = useCallback(() => { window.clearTimeout(timer.current); timer.current = window.setTimeout(() => void load(), 1200) }, [load])
  return { sync, setSync, error, reload: load, soon }
}

/** A machine that did not answer last time is tried again quietly: that is not progress to show. */
export const quietRetry = (h: SyncHost) => !!h.busy && !!h.error

export type Progress = { total: number; done: number; now: string[] }

/** What a sync is at, or null when none runs that is worth showing. */
export function syncProgress(sync: SyncStatus | null): Progress | null {
  if (!sync?.running) return null
  const on = sync.hosts.filter(h => h.on)
  const busy = on.filter(h => h.busy)
  const active = busy.filter(h => !quietRetry(h))
  if (busy.length > 0 && active.length === 0) return null
  if (on.length === 0) return null
  const now = active.map(h => {
    const [verb, ...rest] = (h.busy || '').split(' ')
    const skill = rest.join(' ')
    if (verb === 'sending') return `sending ${skill} to ${h.name}`
    if (verb === 'taking') return `taking ${skill} from ${h.name}`
    if (verb === 'removing') return `removing ${skill} from ${h.name}`
    return `waiting for ${h.name}`
  })
  return { total: on.length, done: busy.length === 0 ? 0 : on.length - busy.length, now }
}

export type Standing = { kind: 'local' | 'ok' | 'busy' | 'look' | 'partial'; label: string; title: string }

/** One line for a skill: where it is across the machines that follow the library. */
export function standing(name: string, sync: SyncStatus | null): Standing {
  const on = (sync?.hosts || []).filter(h => h.on)
  if (on.length === 0) return { kind: 'local', label: 'On this server', title: 'No other machine follows the library yet.' }
  let same = 0, look = 0, busy = 0
  const lines: string[] = []
  for (const h of on) {
    const st = h.skills[name]
    if (h.error) { lines.push(`${h.name}: did not answer`); continue }
    if (!st) { if (h.busy) busy++; lines.push(`${h.name}: ${h.busy ? 'being checked' : 'not checked yet'}`); continue }
    if (st.state === 'same') { same++; lines.push(`${h.name}: the same`) }
    else if (st.state === 'off') lines.push(`${h.name}: kept off`)
    else { look++; lines.push(`${h.name}: ${st.state === 'differs' ? 'a different version' : st.error || 'not sent'}`) }
  }
  const title = ['This server: the library', ...lines].join('\n')
  if (look) return { kind: 'look', label: look === 1 ? 'Differs on 1 machine' : `Differs on ${look} machines`, title }
  if (busy) return { kind: 'busy', label: 'Syncing…', title }
  const total = same + 1
  if (same < on.length) return { kind: 'partial', label: `On ${total} of ${on.length + 1} machines`, title }
  return { kind: 'ok', label: on.length === 1 ? 'On both machines' : `On all ${total} machines`, title }
}
