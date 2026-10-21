// The plan limits, always in view at the bottom of the sidebar (on by
// default; Settings → General turns it off, the pins on the Usage page pick
// which limits it shows).
import { useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { resetsIn } from '../lib/format'
import type { LimitWindow, Limits, ProviderId } from '../lib/types'

export const limitKey = (p: ProviderId, w: LimitWindow) => `${p}:${w.id || w.name}`
export const usedFrac = (w: LimitWindow) => Math.max(0, Math.min(1, w.used > 1 ? w.used / 100 : w.used))
export const limitTone = (f: number) => f > 0.9 ? 'danger' : f > 0.7 ? 'warn' : ''

export function limitRows(limits: Limits) {
  return (['claude', 'codex'] as ProviderId[]).flatMap(p => (limits[p]?.windows || []).map(w => ({ p, w, key: limitKey(p, w) })))
}

/** No pins yet means every limit shows. */
export function isPinned(pinned: string[] | undefined, key: string) {
  return !pinned?.length || pinned.includes(key)
}

// "7 days · Fable" → "Fable 7d", "5 hours" → "5h"
const shortWindow = (name: string) => {
  const parts = name.split(' · ')
  const span = parts[0].replace(/(\d+)\s*hours?/i, '$1h').replace(/(\d+)\s*days?/i, '$1d')
  return [...parts.slice(1), span].join(' ')
}

export function UsageWidget({ onNavigate }: { onNavigate?: () => void }) {
  const { limits, settings } = useApp()
  const navigate = useNavigate()
  if (!settings || settings.usage_widget === false) return null
  const rows = limitRows(limits).filter(r => isPinned(settings.usage_pinned, r.key))
  if (!rows.length) return null
  const resets = rows.some(r => r.p === 'codex') ? limits.codex?.resets : undefined
  const next = resets?.credits?.find(c => c.expires_at)?.expires_at
  return (
    <button type="button" className="usage-widget" onClick={() => { navigate('/usage'); onNavigate?.() }} title="Plan limits — open Usage">
      {rows.map(({ p, w, key }) => {
        const f = usedFrac(w)
        return (
          <span key={key} className="uw-row">
            <span className="uw-h">
              <span className="ellipsis"><b>{p === 'claude' ? 'Claude' : 'Codex'}</b> {shortWindow(w.name)}</span>
              <span className="tnum uw-num">{Math.round(f * 100)}%{w.resets_at && <span className="muted" title={resetsIn(w.resets_at)}> · ↻ {resetsIn(w.resets_at, Date.now(), '')}</span>}</span>
            </span>
            <span className={`limit-bar thin ${limitTone(f)}`}><span style={{ width: `${Math.max(1, f * 100)}%` }} /></span>
          </span>
        )
      })}
      {!!resets?.available && (
        <span className="uw-h uw-resets" title={next ? `The next one expires ${new Date(next).toLocaleString()}` : ''}>
          <span className="ellipsis"><b>Codex</b> free resets</span>
          <span className="tnum uw-num">{resets.available}{next && <span className="muted"> · next expires {resetsIn(next, Date.now(), 'in ')}</span>}</span>
        </span>
      )}
    </button>
  )
}
