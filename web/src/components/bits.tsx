// Small shared pieces: provider chips, status dots, thread meta.
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
