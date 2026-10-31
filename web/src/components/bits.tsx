// Small shared pieces: provider chips, status dots, thread meta.
import { useEffect, useState } from 'react'
import type { ProviderId, Thread } from '../lib/types'
import { providerName } from '../lib/format'
import { Icon } from './icons'

export function ProviderBadge({ provider, size = '' }: { provider: ProviderId | string; size?: '' | 'xs' }) {
  return <span className={`prov prov-${provider} ${size}`}>{size === 'xs' ? (provider === 'codex' ? 'Codex' : 'Claude') : providerName(provider)}</span>
}

export function ThreadStatus({ t }: { t: Pick<Thread, 'status' | 'unread' | 'pending'> }) {
  if (t.status === 'running') return <span className="tstat running" title="Working…" aria-label="Working"><span className="spinner" /></span>
  if (t.status === 'waiting' || t.pending > 0) return <span className="tstat waiting" title="Needs your answer" aria-label="Needs your answer"><Icon name="help-circle" size={14} /></span>
  if (t.status === 'error') return <span className="tstat error" title="Stopped with an error" aria-label="Error"><Icon name="alert" size={13} /></span>
  if (t.unread) return <span className="tstat unread" title="Done — new reply" aria-label="Done, new reply"><span /></span>
  return null
}

/** A thin bar showing how full the context window is. */
export function ContextMeter({ used, window }: { used: number; window: number }) {
  if (!window) return null
  const f = Math.min(1, used / window)
  const tone = f > 0.85 ? 'danger' : f > 0.6 ? 'warn' : ''
  return (
    <span className={`ctx-meter ${tone}`} title={`Context ${Math.round(f * 100)}% full (${used.toLocaleString()} of ${window.toLocaleString()} tokens)`}>
      <span className="ctx-bar"><span style={{ width: `${Math.max(2, f * 100)}%` }} /></span>
      <span className="ctx-num tnum">{Math.round(f * 100)}%</span>
    </span>
  )
}

/** "1d 2h", "3h 5m", "45m" — how long a span of milliseconds is. */
function span(ms: number): string {
  const m = Math.max(1, Math.floor(ms / 60e3)), h = Math.floor(m / 60), d = Math.floor(h / 24)
  if (d > 0) return `${d}d ${h % 24}h`
  if (h > 0) return `${h}h ${m % 60}m`
  return `${m}m`
}

/** Whether the prompt cache is likely still warm: it lives for cache_ttl
 * seconds after the agent last called the model. A cold cache makes the next
 * message slower and dearer (the whole conversation is read in again). */
export function CacheChip({ t, onTap }: { t: Pick<Thread, 'usage' | 'model' | 'status'>; onTap?: (text: string) => void }) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => { const iv = setInterval(() => setNow(Date.now()), 30e3); return () => clearInterval(iv) }, [])
  const u = t.usage
  const at = u?.cache_at ? new Date(u.cache_at).getTime() : NaN
  if (!u?.cache_ttl || isNaN(at) || t.status === 'running') return null
  const ttl = u.cache_ttl * 1000, idle = now - at, left = ttl - idle
  const hour = u.cache_ttl >= 3600 ? '1-hour' : `${Math.round(u.cache_ttl / 60)}-minute`
  const moved = (u.cache_model ?? '') !== (t.model ?? '')
  const tone = moved || left <= 0 ? 'cold' : left < Math.min(5 * 60e3, ttl / 3) ? 'warn' : 'warm'
  const text = moved ? 'Model changed — the next message reads the whole conversation in again (slower and dearer).'
    : left > 0 ? `Prompt cache warm — about ${span(left)} left (${hour} cache). Replies stay fast and cheap.`
    : `Prompt cache likely expired (idle ${span(idle)}). The next message reads the whole conversation in again (slower and dearer).`
  return (
    <button type="button" className={`cache-chip ${tone}`} title={text} aria-label={text} onClick={() => onTap?.(text)}>
      <Icon name="clock" size={13} />{tone !== 'cold' && <span className="tnum">{span(left)}</span>}
    </button>
  )
}
