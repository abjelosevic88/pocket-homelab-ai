// Shared bits for pages that list CLI sessions and runs.
import { useNavigate } from 'react-router-dom'
import { api, V1 } from './api'
import { useApp } from './app-context'
import type { Origin, ProviderId, Thread } from './types'
import { errMsg, useToast } from '../components/ui'

export const ORIGINS: { id: Origin; label: string }[] = [
  { id: 'paseo', label: 'Paseo' },
  { id: 'terminal', label: 'Terminal' },
  { id: 'pocket-ai', label: 'Pocket AI' },
  { id: 'schedule', label: 'Schedule' },
  { id: 'app', label: 'Other app' },
]
export const originLabel = (o: string) => ORIGINS.find(x => x.id === o)?.label || o

/** "claude-opus-5-5" → "Opus 5.5", "gpt-6.1-sol" → "gpt-6.1-sol" */
export function shortModel(m?: string) {
  if (!m) return ''
  const c = /^claude-([a-z]+)-(\d+)-(\d+)/.exec(m)
  return c ? `${c[1][0].toUpperCase()}${c[1].slice(1)} ${c[2]}.${c[3]}` : m
}

/** Open a CLI session in Pocket AI (adding it first if it is not there yet),
 *  optionally at one message. */
export function useOpenSession() {
  const { upsertThread } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  return async (s: { provider: ProviderId; session_id: string; thread_id?: string }, itemKey?: string) => {
    const hash = itemKey ? `#item-${itemKey}` : ''
    if (s.thread_id) { navigate(`/t/${s.thread_id}${hash}`); return }
    try {
      const t = await api.post<Thread>(`${V1}/history/import`, { provider: s.provider, session_id: s.session_id })
      upsertThread(t)
      navigate(`/t/${t.id}${hash}`)
    } catch (e) { toast(errMsg(e), 'err') }
  }
}

/** "Today", "Yesterday", "Monday", "12 October" */
export function dayLabel(d: Date, now = new Date()) {
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((day(now) - day(d)) / 86400e3)
  if (diff === 0) return 'Today'
  if (diff === 1) return 'Yesterday'
  if (diff > 1 && diff < 7) return d.toLocaleDateString(undefined, { weekday: 'long' })
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'long', ...(d.getFullYear() !== now.getFullYear() ? { year: 'numeric' } : {}) })
}

/** An hour of the day the way the browser writes times: "8 PM" or "20:00" */
export const hourLabel = (h: number) => new Date(2000, 0, 1, h).toLocaleTimeString(undefined, { hour: 'numeric', ...(new Date(2000, 0, 1, 13).toLocaleTimeString().match(/AM|PM/i) ? {} : { minute: '2-digit' }) })

export const hhmm = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
