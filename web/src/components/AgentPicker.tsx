// Provider / model / effort / mode pickers, fed by GET /providers.
import { useApp } from '../lib/app-context'
import type { Provider, ProviderId, Settings } from '../lib/types'
import { type Option, Select } from './Select'
import { Segmented } from './ui'
import { Icon } from './icons'

export interface AgentValue { provider: ProviderId | ''; model: string; effort: string; mode: string; speed?: string }

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
  }
}

export function modelOptions(p: Provider | undefined, allowDefault: boolean): Option<string>[] {
  const opts: Option<string>[] = (p?.models || []).map(m => ({ value: m.id, label: m.name || m.id, description: m.description, keywords: m.id }))
  if (allowDefault) opts.unshift({ value: '', label: 'My default', description: 'Whatever is set in Settings → Agents' })
  return opts
}
export function effortOptions(p: Provider | undefined, model: string, allowDefault: boolean): Option<string>[] {
  const m = p?.models.find(x => x.id === model)
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
  const m = p?.models.find(x => x.id === model)
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

export function modeName(p: Provider | undefined, id: string) { return p?.modes.find(m => m.id === id)?.name || id }
export function modelName(p: Provider | undefined, id: string) { return p?.models.find(m => m.id === id)?.name || id }

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
export function AgentPicker({ value, onChange, allowDefault = false, compact = false, showProvider = true }: { value: AgentValue; onChange: (patch: Partial<AgentValue>) => void; allowDefault?: boolean; compact?: boolean; showProvider?: boolean }) {
  const { providers, settings } = useApp()
  const pid = (value.provider || settings?.default_provider || 'claude') as ProviderId
  const p = providers.find(x => x.id === pid)
  const models = modelOptions(p, allowDefault)
  const efforts = effortOptions(p, value.model, allowDefault)
  const modes = modeOptions(p, allowDefault)
  const speeds = speedOptions(p, value.model, allowDefault)
  const size = compact ? 'sm' : ''
  const cls = compact ? 'inline ghost' : ''
  const pickers = (
    <>
      {models.length > 0 && <Select size={size} className={cls} label="Model" value={value.model} options={models} onChange={v => onChange({ model: v })} width={compact ? 280 : 'trigger'}
        renderValue={compact ? o => <span className="ellipsis">{shortLabel(String(o?.label ?? 'Model'))}</span> : undefined} />}
      {efforts.length > 0 && <Select size={size} className={cls} label="Effort" value={value.effort} options={efforts} onChange={v => onChange({ effort: v })} width={compact ? 180 : 'trigger'}
        renderValue={o => <span className="ellipsis">{compact ? <><Icon name="brain" size={12} className="inline-ico lbl-short" /><span className="muted lbl-long">Effort </span></> : null}{o?.label ?? 'Effort'}</span>} />}
      {modes.length > 0 && <Select size={size} className={cls} label="Permissions" value={value.mode} options={modes} onChange={v => onChange({ mode: v })} width={compact ? 300 : 'trigger'}
        renderValue={compact ? o => <span className="ellipsis"><Icon name="shield" size={12} className="inline-ico" />{o?.label ?? 'Mode'}</span> : undefined} />}
      {speeds.length > 0 && <Select size={size} className={cls} label="Speed" value={allowDefault && !value.speed ? '__default' : (value.speed || '')} options={speeds}
        onChange={v => onChange({ speed: v === '__default' ? '' : v })} width={compact ? 300 : 'trigger'}
        renderValue={o => <span className="ellipsis">{compact ? <Icon name="zap" size={12} className="inline-ico" /> : null}<span className={compact && !o?.value ? 'lbl-long' : ''}>{o?.label ?? 'Speed'}</span></span>} />}
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
