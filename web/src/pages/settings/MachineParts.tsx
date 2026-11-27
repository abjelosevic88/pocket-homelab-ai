// Settings → Machines building blocks: a machine card (OS tile, status pill,
// CLI chips) and its logins grouped by CLI; the machine picker on top uses
// osIcon and Health. This server and the other machines use the same ones.
import type React from 'react'
import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { planName, providerName } from '../../lib/format'
import type { ProviderId } from '../../lib/types'
import { Icon } from '../../components/icons'
import { errMsg, MenuButton, type MenuItem, Spinner, useToast } from '../../components/ui'
import { StatusDot } from './common'

export type Health = 'ok' | 'warn' | 'error' | 'off'
export const provName = (p: string) => providerName(p)
/** "2.1.310 (Claude Code)" → "2.1.310", "codex-cli 0.163.0" → "0.163.0" */
export const shortVersion = (v: string) => v.replace(/\s*\((Claude Code|Codex)\)$/, '').replace(/^codex-cli /, '')
export const tilde = (p: string) => p.replace(/^\/(home|Users)\/[^/]+/, '~')

/** The machine's tile: an Apple for a Mac, a house for this server, a server otherwise. */
export function osIcon(os?: string, here = false) {
  if (here) return 'home'
  if (/darwin|mac/i.test(os || '')) return 'apple'
  if (/windows/i.test(os || '')) return 'monitor'
  return 'server'
}

export interface CliChip {
  provider: ProviderId; version?: string; error?: string
  /** the newest release (GET /cli/latest) */
  latest?: string
  /** updates it there (who may: an admin, or the machine's own person) */
  onUpdate?: () => void; updating?: boolean
}

/** a newer than b ("2.1.331" vs "2.1.326 (Claude Code)") */
export function isNewer(a: string, b: string): boolean {
  const n = (v: string) => (/(\d+(?:\.\d+)*)/.exec(v)?.[1] || '').split('.').map(Number)
  const x = n(a), y = n(b)
  if (!x.length || !y.length || Number.isNaN(x[0]) || Number.isNaN(y[0])) return false
  for (let i = 0; i < Math.max(x.length, y.length); i++) {
    const d = (x[i] || 0) - (y[i] || 0)
    if (d) return d > 0
  }
  return false
}

let latestCache: { at: number; p: Promise<Partial<Record<ProviderId, string>>> } | null = null
/** The newest Claude Code and Codex (the server asks npm at most hourly). */
export function useLatestCLIs(): Partial<Record<ProviderId, string>> {
  const [v, setV] = useState<Partial<Record<ProviderId, string>>>({})
  useEffect(() => {
    if (!latestCache || Date.now() - latestCache.at > 10 * 60_000) {
      latestCache = { at: Date.now(), p: api.get<{ versions: Partial<Record<ProviderId, string>> }>(`${V1}/cli/latest`).then(r => r.versions || {}).catch(() => { latestCache = null; return {} }) }
    }
    let live = true
    void latestCache.p.then(r => { if (live) setV(r) })
    return () => { live = false }
  }, [])
  return v
}

/** Update on a machine (0 = this server): runs claude/codex update there,
 * then onDone (check it again). `updating` is "hostId:provider" while one runs. */
export function useCLIUpdate() {
  const { toast } = useToast()
  const [updating, setUpdating] = useState<string | null>(null)
  const update = async (hostId: number, name: string, p: ProviderId, onDone: () => unknown) => {
    setUpdating(`${hostId}:${p}`)
    toast(`Updating ${provName(p)} on ${name}…`)
    try {
      const r = await api.post<{ ok: boolean; version: string; error?: string }>(`${V1}/hosts/${hostId}/update/${p}`)
      if (r.ok) toast(`${provName(p)} on ${name} is ${shortVersion(r.version || '') || 'updated'}`, 'ok')
      else toast(`${provName(p)} on ${name}: ${r.error || 'the update did not finish'}`, 'err')
    } catch (e) { toast(errMsg(e), 'err') } finally {
      setUpdating(null)
      await Promise.resolve(onDone()).catch(() => {})
    }
  }
  return { updating, update, busy: (hostId: number, p: ProviderId) => updating === `${hostId}:${p}` }
}

const HEALTH_LABEL: Record<Health, string> = { ok: 'Online', warn: 'Needs a look', error: 'Unreachable', off: 'Not checked' }

export function MachineCard({ icon, name, health, healthLabel, meta, clis, actions, children }: {
  icon: string; name: React.ReactNode; health: Health; healthLabel?: string
  meta?: React.ReactNode[]; clis?: CliChip[]
  actions?: React.ReactNode; children?: React.ReactNode
}) {
  return (
    <section className={`mc h-${health}`}>
      <header className="mc-head">
        <span className="mc-ico" aria-hidden><Icon name={icon} size={22} /></span>
        <div className="mc-id">
          <div className="mc-name"><h3 className="ellipsis">{name}</h3><span className="mc-pill"><span className="mc-pulse" />{healthLabel || HEALTH_LABEL[health]}</span></div>
          {meta && meta.length > 0 && <div className="mc-meta">{meta.map((m, i) => <span key={i}>{m}</span>)}</div>}
        </div>
        {actions && <div className="mc-actions">{actions}</div>}
      </header>
      {clis && clis.length > 0 && (
        <div className="mc-clis">
          {clis.map(c => {
            const behind = !c.error && !!c.version && !!c.latest && isNewer(c.latest, c.version)
            const current = !c.error && !!c.version && !!c.latest && !behind
            return (
              <span key={c.provider} className={`mc-cli ${c.provider} ${c.error ? 'bad' : ''} ${behind ? 'behind' : ''}`} title={c.error || `${provName(c.provider)} ${c.version || ''}${current ? ' — the latest' : behind ? ` — ${c.latest} is out` : ''}`}>
                <Icon name={c.provider} size={13} />{provName(c.provider)}<b>{c.error ? 'not installed' : shortVersion(c.version || '')}</b>
                {current && <Icon name="check" size={12} className="mc-cli-ok" aria-label="latest" />}
                {behind && (c.onUpdate
                  ? <button type="button" className="mc-cli-up" disabled={c.updating} onClick={c.onUpdate} aria-label={`Update ${provName(c.provider)} to ${c.latest}`} title={`Update ${provName(c.provider)} to ${c.latest}`}>
                      {c.updating ? <Spinner size={11} /> : <Icon name="arrow-up" size={12} />}{c.updating ? 'Updating…' : `Update to ${c.latest}`}</button>
                  : <span className="mc-cli-up off">{c.latest} out</span>)}
              </span>
            )
          })}
        </div>
      )}
      {children && <div className="mc-body">{children}</div>}
    </section>
  )
}

/** One login of a machine, for LoginList. */
export interface LoginItem {
  key: string; provider: ProviderId; name: string; signedIn: boolean; plan?: string; email?: string; error?: string; folder: string
  /** badges after the name (Default, Same account as …) */
  tags?: React.ReactNode
  /** after the folder (whose login it is) */
  meta?: React.ReactNode
  menu?: MenuItem[]; menuLabel?: string; onSignIn?: () => void; onContextMenu?: (e: React.MouseEvent) => void
}

/** A machine's logins, grouped by CLI: a header with how many and Add, then a row each. */
export function LoginList({ items, providers, onAdd }: { items: LoginItem[]; providers?: ProviderId[]; onAdd?: (p: ProviderId) => void }) {
  const pids = (['claude', 'codex'] as ProviderId[]).filter(p => items.some(i => i.provider === p) || providers?.includes(p))
  return (
    <div className="acct-groups">
      {pids.map(pid => {
        const rows = items.filter(i => i.provider === pid)
        return (
          <div key={pid} className={`acct-group ${pid}`}>
            <div className="acct-group-h">
              <span className={`acct-group-ico ${pid}`}><Icon name={pid} size={16} /></span>
              <span className="acct-group-name">{provName(pid)}</span>
              <span className="acct-group-n">{rows.length} {rows.length === 1 ? 'login' : 'logins'}</span>
              {onAdd && <button type="button" className="btn sm ghost acct-group-add" onClick={() => onAdd(pid)}><Icon name="plus" size={13} />Add</button>}
            </div>
            {rows.map(l => (
              <div key={l.key} className="acct-row" onContextMenu={l.onContextMenu}>
                <StatusDot status={l.signedIn ? 'ok' : l.error ? 'error' : 'off'} title={l.signedIn ? 'Signed in' : l.error ? 'Error' : 'Not signed in'} />
                <div className="acct-main">
                  <div className="acct-name">
                    <span className="ellipsis">{l.name}</span>
                    {l.signedIn ? (l.plan && <span className="badge ok">{planName(l.plan)}</span>) : l.error ? <span className="badge danger">Error</span> : <span className="badge">Not signed in</span>}
                    {l.tags}
                  </div>
                  {l.email && <div className="acct-email ellipsis">{l.email}</div>}
                  <div className="acct-meta"><code>{tilde(l.folder)}</code>{l.meta}</div>
                  {l.error && <div className="danger-text xsmall" title={l.error}>{l.error}</div>}
                </div>
                <div className="acct-actions">
                  {!l.signedIn && !l.error && l.onSignIn && <button type="button" className="btn sm" onClick={l.onSignIn}><Icon name="key" size={13} />Sign in</button>}
                  {l.menu && l.menu.length > 0 && <MenuButton label={l.menuLabel || `More for ${l.name}`} align="right" items={l.menu}><Icon name="more" size={16} /></MenuButton>}
                </div>
              </div>
            ))}
            {!rows.length && <div className="acct-row"><span /><div className="muted small">No {provName(pid)} login yet.</div><span /></div>}
          </div>
        )
      })}
    </div>
  )
}
