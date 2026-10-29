// Provider / model / effort / mode pickers, fed by GET /providers.
import { type ReactNode, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { planName } from '../lib/format'
import type { Account, HostLogin, Provider, ProviderId, Settings } from '../lib/types'
import { type Option, Select } from './Select'
import { Segmented } from './ui'
import { Icon } from './icons'

export interface AgentValue { provider: ProviderId | ''; model: string; effort: string; mode: string; speed?: string; account_id?: number; host_home?: string }

const EFFORT_LABEL: Record<string, string> = { minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max', none: 'None' }
export const effortLabel = (e: string) => EFFORT_LABEL[e] || e

/** The person's defaults for a provider, falling back to the provider's own. */
export function defaultsFor(p: Provider | undefined, s: Settings | null): Omit<AgentValue, 'provider'> {
  const mine = p && s ? s[p.id] : undefined
  return {
    model: mine?.model || p?.default_model || '',
    effort: mine?.effort || p?.default_effort || '',
    mode: mine?.mode || p?.default_mode || '',
    speed: mine?.speed || '',
    account_id: mine?.account_id || 0,
  }
}

/** One line about a login for pickers: plan, email, and whether another
 * login is the same account (they share limits). */
export function accountNote(a: Account) {
  if (!a.signed_in) return a.error || 'Not signed in — Settings → Agents'
  return [a.plan && planName(a.plan), a.email, a.same_as && `same account as ${a.same_as}`].filter(Boolean).join(' · ')
}

/** The named login the server's own login (id 0) is signed in to the same account as. */
function serverTwin(list: Account[]) {
  const server = list.find(a => a.id === 0)
  return server?.same_as ? list.find(a => a.id !== 0 && a.provider === server.provider && a.label === server.same_as) : undefined
}

/** What a login is called: the server's own login takes the name of the login on the same
 * account ("Athena Max"), and stays "Server login" when there is none. */
export function accountName(a: Account, accounts: Account[]) {
  if (a.id !== 0) return a.label
  return serverTwin(accounts.filter(x => x.provider === a.provider))?.label || a.label
}

/** Logins to pick from; empty when the provider has only the server's own. The server's
 * login and a named login on the same account are one choice: the one in use (`current`)
 * stays, the server's own otherwise. */
export function accountOptions(accounts: Account[], pid: ProviderId, current?: number): Option<number>[] {
  const all = accounts.filter(a => a.provider === pid)
  const twin = serverTwin(all)
  const drop = twin ? (current === twin.id ? 0 : twin.id) : -1
  const list = all.filter(a => a.id !== drop)
  if (list.length < 2) return []
  return list.map(a => {
    const merged = !!twin && (a.id === 0 || a.id === twin.id)
    return {
      value: a.id, label: accountName(a, all), keywords: a.email,
      description: merged ? [a.plan && planName(a.plan), a.email, 'also the server login'].filter(Boolean).join(' · ') : accountNote(a),
      disabled: !a.signed_in,
    }
  })
}

export function modelOptions(p: Provider | undefined, allowDefault: boolean): Option<string>[] {
  const opts: Option<string>[] = (p?.models || []).map(m => ({ value: m.id, label: m.name || m.id, description: m.description, keywords: m.id }))
  if (allowDefault) opts.unshift({ value: '', label: 'My default', description: 'Whatever is set in Settings → Agents' })
  return opts
}
export function effortOptions(p: Provider | undefined, model: string, allowDefault: boolean): Option<string>[] {
  const m = p?.models?.find(x => x.id === model)
  const list = (m?.efforts?.length ? m.efforts : p?.efforts) || []
  const opts: Option<string>[] = list.map(e => ({ value: e, label: effortLabel(e) }))
  if (allowDefault) opts.unshift({ value: '', label: 'My default' })
  return opts
}
export function modeOptions(p: Provider | undefined, allowDefault: boolean): Option<string>[] {
  const opts: Option<string>[] = (p?.modes || []).map(m => ({ value: m.id, label: m.name || m.id, description: m.description }))
  if (allowDefault) opts.unshift({ value: '', label: 'My default' })
  return opts
}

/** Speeds the model can run at; empty when there is no choice to make. */
export function speedOptions(p: Provider | undefined, model: string, allowDefault: boolean): Option<string>[] {
  const speeds = p?.speeds || []
  const m = p?.models?.find(x => x.id === model)
  if (speeds.length < 2 || (m && !m.fast)) return []
  const opts: Option<string>[] = speeds.map(s => ({ value: s.id, label: s.name, description: s.available ? s.description : s.note, disabled: !s.available }))
  if (allowDefault) opts.unshift({ value: '__default', label: 'My default' })
  return opts
}

/** "Default (recommended)" → "Default": the bracket goes on narrow screens. */
function shortLabel(label: string) {
  const i = label.indexOf(' (')
  return i > 0 ? <>{label.slice(0, i)}<span className="lbl-long">{label.slice(i)}</span></> : label
}

export function modeName(p: Provider | undefined, id: string) { return p?.modes?.find(m => m.id === id)?.name || id }
export function modelName(p: Provider | undefined, id: string) { return p?.models?.find(m => m.id === id)?.name || id }

/** Claude Code / Codex toggle. */
export function ProviderToggle({ value, onChange, allowDefault = false }: { value: ProviderId | ''; onChange: (p: ProviderId | '') => void; allowDefault?: boolean }) {
  const { providers } = useApp()
  const ids: ProviderId[] = ['claude', 'codex']
  const options = ids.map(id => {
    const p = providers.find(x => x.id === id)
    const ok = !!p?.available
    return { value: id as ProviderId | '', label: <span className={`prov-opt ${ok ? '' : 'off'}`}><span className={`prov-dot prov-${id}`} />{id === 'claude' ? 'Claude Code' : 'Codex'}</span>, title: ok ? (p?.version ? `${p.name} ${p.version}` : undefined) : (p?.error || 'Not installed on the server') }
  })
  if (allowDefault) options.unshift({ value: '', label: <span>My default</span>, title: 'The provider set in Settings → Agents' })
  return <Segmented label="Agent" value={value} options={options} onChange={v => {
    const p = providers.find(x => x.id === v)
    if (v && p && !p.available) return
    onChange(v)
  }} />
}

/** Model, effort and mode for a chosen provider — a form block or a compact chip row. */
/** Ask my apps chats run on these Claude models, with fixed permissions. */
const ASK_MODELS: Option<string>[] = [
  { value: 'haiku', label: 'Haiku', description: 'Fastest' },
  { value: 'sonnet', label: 'Sonnet', description: 'Balanced' },
  { value: 'opus', label: 'Opus', description: 'Most capable' },
]

// A machine's logins (one per login folder there), shared by every picker
// and asked again after a minute.
const hostLoginCache = new Map<number, { at: number; list: Promise<HostLogin[]> }>()
function useHostLogins(host: number) {
  const [list, setList] = useState<HostLogin[]>([])
  useEffect(() => {
    if (!host) { setList([]); return }
    let c = hostLoginCache.get(host)
    if (!c || Date.now() - c.at > 60e3) {
      c = { at: Date.now(), list: api.get<HostLogin[]>(`${V1}/hosts/logins?host=${host}`).catch(() => [] as HostLogin[]) }
      hostLoginCache.set(host, c)
    }
    let live = true
    c.list.then(l => { if (live) setList(l) })
    return () => { live = false }
  }, [host])
  return list
}

/** The logins of one CLI on a machine, the default first. */
export function machineLoginOptions(list: HostLogin[], pid: ProviderId, current: string): Option<string>[] {
  const all = list.filter(h => h.provider === pid && !h.missing)
  const opts: Option<string>[] = all.map(h => ({
    value: h.dir || '', label: h.label || 'Default login', keywords: h.email,
    description: [h.home.replace(/^\/(home|Users)\/[^/]+/, '~'), h.signed_in ? [h.plan && planName(h.plan), h.email].filter(Boolean).join(' · ') : 'not signed in'].filter(Boolean).join(' · '),
  }))
  if (current && !opts.some(o => o.value === current)) opts.push({ value: current, label: current.replace(/^.*\/\./, ''), description: 'not found there any more' })
  return opts
}

export function AgentPicker({ value, onChange, allowDefault = false, compact = false, showProvider = true, noAccount = false, host = 0, ask = false }: {
  value: AgentValue; onChange: (patch: Partial<AgentValue>) => void; allowDefault?: boolean; compact?: boolean; showProvider?: boolean
  /** no account to pick */
  noAccount?: boolean
  /** runs on this machine (Settings → Servers): pick one of its logins instead of an account here */
  host?: number
  /** an Ask my apps chat: account, model and effort only */
  ask?: boolean
}) {
  const { providers, settings, accounts } = useApp()
  const pid = (value.provider || settings?.default_provider || 'claude') as ProviderId
  const p = providers.find(x => x.id === pid)
  const accts = allowDefault || noAccount || host ? [] : accountOptions(accounts, pid, value.account_id || 0)
  const machineLogins = machineLoginOptions(useHostLogins(allowDefault || noAccount ? 0 : host), pid, value.host_home || '')
  const models = ask ? ASK_MODELS : modelOptions(p, allowDefault)
  const efforts = effortOptions(p, value.model, allowDefault)
  const modes = ask ? [] : modeOptions(p, allowDefault)
  const speeds = ask ? [] : speedOptions(p, value.model, allowDefault)
  const size = compact ? 'sm' : ''
  // in a form each dropdown gets its name above it; chips carry an icon instead
  const named = (caption: string, picker: ReactNode) => compact ? picker : <div className="stack tight"><span className="muted xsmall">{caption}</span>{picker}</div>
  const cls = compact ? 'inline ghost' : ''
  const pickers = (
    <>
      {accts.length > 0 && named('Account', <Select size={size} className={cls} label="Account" value={value.account_id || 0} options={accts} onChange={v => onChange({ account_id: v })} width={compact ? 280 : 'trigger'}
        renderValue={o => <span className="ellipsis">{compact && <Icon name="user" size={12} className="inline-ico" />}{o?.label ?? 'Account'}</span>} />)}
      {machineLogins.length > 1 && named('Login', <Select size={size} className={cls} label="Login on that machine" value={value.host_home || ''} options={machineLogins} onChange={v => onChange({ host_home: v })} width={compact ? 320 : 'trigger'}
        renderValue={o => <span className="ellipsis">{compact && <Icon name="server" size={12} className="inline-ico" />}{o?.label ?? 'Login'}</span>} />)}
      {models.length > 0 && named('Model', <Select size={size} className={cls} label="Model" value={value.model} options={models} onChange={v => onChange({ model: v })} width={compact ? 280 : 'trigger'}
        renderValue={compact ? o => <span className="ellipsis"><Icon name={pid} size={14} className={`inline-ico prov-glyph ${pid}`} />{shortLabel(String(o?.label ?? 'Model'))}</span> : undefined} />)}
      {efforts.length > 0 && named('Effort', <Select size={size} className={cls} label="Effort" value={value.effort} options={efforts} onChange={v => onChange({ effort: v })} width={compact ? 180 : 'trigger'}
        renderValue={o => <span className="ellipsis">{compact ? <Icon name="brain" size={12} className="inline-ico" /> : null}{o?.label ?? 'Effort'}</span>} />)}
      {modes.length > 0 && named('Permissions', <Select size={size} className={cls} label="Permissions" value={value.mode} options={modes} onChange={v => onChange({ mode: v })} width={compact ? 300 : 'trigger'}
        renderValue={compact ? o => <span className="ellipsis"><Icon name="shield" size={12} className="inline-ico" />{o?.label ?? 'Mode'}</span> : undefined} />)}
      {speeds.length > 0 && (compact ? (() => {
        const fast = (p?.speeds || []).find(x => x.id)
        const on = !!value.speed
        return fast && <button type="button" className={`speed-toggle ${on ? 'on' : ''}`} aria-pressed={on} disabled={!fast.available && !on}
          title={fast.available ? (on ? `${fast.name} speed is on — tap for standard` : `${fast.name}: ${fast.description || 'faster replies'}`) : (fast.note || `${fast.name} is not available`)}
          onClick={() => onChange({ speed: on ? '' : fast.id })}><Icon name="zap" size={15} /></button>
      })() : named('Speed', <Select size={size} className={cls} label="Speed" value={allowDefault && !value.speed ? '__default' : (value.speed || '')} options={speeds}
        onChange={v => onChange({ speed: v === '__default' ? '' : v })} width="trigger" />))}
    </>
  )
  if (compact) return <div className="agent-chips">{pickers}</div>
  return (
    <div className="stack tight">
      {showProvider && <ProviderToggle value={value.provider} allowDefault={allowDefault} onChange={v => {
        const np = providers.find(x => x.id === v)
        onChange(allowDefault ? { provider: v, model: '', effort: '', mode: '' } : { provider: v, ...defaultsFor(np, settings) })
      }} />}
      {(!allowDefault || value.provider) && <div className="three-col">{pickers}</div>}
      {p && !p.available && <p className="danger-text small" style={{ margin: 0 }}>{p.name} is not available on the server: {p.error || 'not installed'}</p>}
    </div>
  )
}
