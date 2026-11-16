// Settings → Machines building blocks: a machine card (OS tile, status pill,
// CLI chips), login tiles in a grid, the dashed "add" tiles and the summary
// strip on top. This server and the other machines use the same ones.
import type React from 'react'
import { planName } from '../../lib/format'
import type { ProviderId } from '../../lib/types'
import { Icon } from '../../components/icons'
import { MenuButton, type MenuItem } from '../../components/ui'

export type Health = 'ok' | 'warn' | 'error' | 'off'
export const provName = (p: string) => p === 'codex' ? 'Codex' : 'Claude Code'
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

export interface CliChip { provider: ProviderId; version?: string; error?: string }

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
          {clis.map(c => (
            <span key={c.provider} className={`mc-cli ${c.provider} ${c.error ? 'bad' : ''}`} title={c.error || `${provName(c.provider)} ${c.version || ''}`}>
              <Icon name={c.provider} size={13} />{provName(c.provider)}<b>{c.error ? 'not installed' : shortVersion(c.version || '')}</b>
            </span>
          ))}
        </div>
      )}
      {children && <div className="mc-body">{children}</div>}
    </section>
  )
}

export function LoginGrid({ children }: { children: React.ReactNode }) {
  return <div className="lt-grid">{children}</div>
}

/** One login: which CLI, its name and plan, whose account, its folder. */
export function LoginTile({ provider, name, signedIn, plan, email, error, folder, tags, menu, menuLabel, onSignIn, onContextMenu }: {
  provider: ProviderId; name: string; signedIn: boolean; plan?: string; email?: string; error?: string; folder: string
  tags?: React.ReactNode; menu?: MenuItem[]; menuLabel?: string; onSignIn?: () => void; onContextMenu?: (e: React.MouseEvent) => void
}) {
  const state = signedIn ? 'in' : error ? 'err' : 'out'
  return (
    <div className={`lt ${provider} s-${state}`} onContextMenu={onContextMenu}>
      <div className="lt-top">
        <span className="lt-ico" title={provName(provider)}><Icon name={provider} size={16} /></span>
        <div className="lt-title">
          <span className="lt-name ellipsis" title={name}>{name}</span>
          <span className="lt-state">{signedIn ? <>Signed in{plan ? <> · <b>{planName(plan)}</b></> : null}</> : error ? 'Error' : 'Not signed in'}</span>
        </div>
        {menu && menu.length > 0 && <MenuButton label={menuLabel || `More for ${name}`} align="right" items={menu}><Icon name="more" size={16} /></MenuButton>}
      </div>
      <div className="lt-body">
        {email ? <div className="lt-email ellipsis" title={email}>{email}</div> : !signedIn && <div className="lt-email faint">No account yet</div>}
        <code className="lt-folder ellipsis" title={folder}>{tilde(folder)}</code>
        {tags && <div className="lt-tags">{tags}</div>}
        {error && <div className="lt-err" title={error}>{error}</div>}
      </div>
      {!signedIn && !error && onSignIn && <button type="button" className="btn sm lt-signin" onClick={onSignIn}><Icon name="key" size={13} />Sign in</button>}
    </div>
  )
}

/** The dashed last tile of a machine's logins. */
export function AddLoginTile({ onAdd, providers = ['claude', 'codex'] }: { onAdd: (p: ProviderId) => void; providers?: ProviderId[] }) {
  return (
    <div className="lt lt-add">
      <div className="lt-add-h"><span className="lt-add-ico"><Icon name="plus" size={16} /></span>Add a login</div>
      <p>Another account, in a folder of its own.</p>
      <div className="lt-add-btns">
        {providers.map(p => <button key={p} type="button" className={`btn sm lt-add-btn ${p}`} onClick={() => onAdd(p)}><Icon name={p} size={13} />{provName(p)}</button>)}
      </div>
    </div>
  )
}

export function Stat({ icon, value, label, sub, tone = '' }: { icon: string; value: React.ReactNode; label: string; sub?: React.ReactNode; tone?: '' | 'ok' | 'warn' | 'error' }) {
  return (
    <div className={`mstat ${tone}`}>
      <span className="mstat-ico"><Icon name={icon} size={16} /></span>
      <div className="mstat-main">
        <div className="mstat-v">{value}</div>
        <div className="mstat-l">{label}</div>
        {sub && <div className="mstat-s ellipsis">{sub}</div>}
      </div>
    </div>
  )
}
