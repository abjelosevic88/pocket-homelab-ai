// The plan limits, always in view at the bottom of the sidebar (on by
// default; Settings → General turns it off, the pins on the Usage page pick
// which limits it shows).
import { useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { resetsIn } from '../lib/format'
import type { LimitWindow, Limits, ProviderId } from '../lib/types'

/** Pin key of a window; src is the limits key ("claude", "codex:3", "claude@2" for a login on another machine). */
export const limitKey = (src: string, w: LimitWindow) => `${src}:${w.id || w.name}`
export const usedFrac = (w: LimitWindow) => Math.max(0, Math.min(1, w.used > 1 ? w.used / 100 : w.used))
export const limitTone = (f: number) => f > 0.9 ? 'danger' : f > 0.7 ? 'warn' : ''

/** Your own names for accounts, and logins put with an account by hand. */
export interface UsageNaming { usage_names?: Record<string, string>; usage_links?: Record<string, string> }

/** A login on an account: one of your named logins here, the server's own, or one on another machine
 * ("athena-box · claude-athena-team" = that machine's ~/.claude-athena-team); key = put with it by hand. */
export interface Login { name: string; kind: 'account' | 'server' | 'machine'; key?: string }

/** The accounts that have limits, the one the server's own login is on
 * first per provider, other machines' last. Logins on the same account
 * arrive as one (with aliases); a login that cannot tell who it is (a token
 * login) joins the account it was put with by hand. title = your name for
 * the account, else its email / the login's name; logins = every login on it. */
export function limitSources(limits: Limits, naming?: UsageNaming | null) {
  const id = (k: string) => Number(k.split(':')[1] || 0)
  const rank = (k: string) => limits[k]?.server_login ? -1 : limits[k]?.host_id ? 1e9 + limits[k]!.host_id! : id(k)
  const loginName = (k: string) => limits[k]?.label || 'Server login'
  const login = (k: string, key?: string): Login => ({ name: loginName(k), kind: limits[k]?.host_id ? 'machine' : limits[k]?.label ? 'account' : 'server', key })
  return (['claude', 'codex'] as ProviderId[]).flatMap(p => {
    const keys = Object.keys(limits).filter(k => k === p || k.startsWith(p + ':') || k.startsWith(p + '@'))
    const byGroup = new Map(keys.filter(k => limits[k]?.group).map(k => [limits[k]!.group!, k]))
    const joined = new Map<string, string[]>() // card key → logins put with it
    const shown = keys.filter(k => {
      const to = !limits[k]?.group && byGroup.get(naming?.usage_links?.[k] || '')
      if (to) joined.set(to, [...(joined.get(to) || []), k])
      return !to
    })
    return shown.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b)).map(k => {
      const l = limits[k]
      const extra = joined.get(k) || []
      const own: Login[] = l?.by_account ? [] : [login(k), ...(l?.server_login && l.label ? [{ name: 'Server login', kind: 'server' as const }] : [])]
      return {
        src: k, p, label: l?.label || '', group: l?.group || '',
        title: (l?.group && naming?.usage_names?.[l.group]) || '',
        aliases: [...(l?.aliases || []), ...extra],
        logins: [...own, ...(l?.also || []).map((name, i): Login => ({ name, kind: l?.also_host?.[i] ? 'machine' : 'account' })), ...extra.map(x => login(x, x))],
      }
    })
  })
}

/** Every window; keys = its pin key plus the keys it had under the logins
 * folded into it, so a pin survives two logins turning out to be one. */
export function limitRows(limits: Limits, naming?: UsageNaming | null) {
  return limitSources(limits, naming).flatMap(({ src, p, title, aliases }) => (limits[src]?.windows || []).map(w => {
    const key = limitKey(src, w)
    return { src, p, label: title || shortLabel(limits[src]), w, key, keys: [key, ...aliases.map(a => limitKey(a, w))] }
  }))
}

/** A short name for a card in tight places: an account named by its email shows the part before the @. */
export function shortLabel(l?: { label?: string; by_account?: boolean }) {
  const s = l?.label || ''
  return l?.by_account && s.includes('@') ? s.slice(0, s.indexOf('@')) : s
}

/** No pins yet means every limit shows. */
export function isPinned(pinned: string[] | undefined, keys: string | string[]) {
  return !pinned?.length || (Array.isArray(keys) ? keys : [keys]).some(k => pinned.includes(k))
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
  const rows = limitRows(limits, settings).filter(r => isPinned(settings.usage_pinned, r.keys))
  if (!rows.length) return null
  const resets = rows.some(r => r.p === 'codex') ? limits.codex?.resets : undefined
  const next = resets?.credits?.find(c => c.expires_at)?.expires_at
  return (
    <button type="button" className="usage-widget" onClick={() => { navigate('/usage'); onNavigate?.() }} title="Plan limits — open Usage">
      {rows.map(({ p, label, w, key }) => {
        const f = usedFrac(w)
        return (
          <span key={key} className="uw-row">
            <span className="uw-h">
              <span className="ellipsis"><b>{p === 'claude' ? 'Claude' : 'Codex'}</b>{label && <> {label}</>} {shortWindow(w.name)}</span>
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
