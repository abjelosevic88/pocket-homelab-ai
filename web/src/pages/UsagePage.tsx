// What the agents cost and how close you are to the plan limits.
import { useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtCost, fmtTokens, planName, providerName, resetsIn, when } from '../lib/format'
import { type Login, isPinned, limitKey, limitRows, limitSources, limitTone, usedFrac } from '../components/UsageWidget'
import type { LimitWindow, ProviderId, ProviderLimits, Resets, UsageReport } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Loading, Segmented, useAsync } from '../components/ui'

const originName = (o: string) => ({ paseo: 'Paseo', terminal: 'Terminal', app: 'Agents & apps' } as Record<string, string>)[o] || o

function LimitBar({ w, pinned, onPin }: { w: LimitWindow; pinned?: boolean; onPin?: () => void }) {
  const used = usedFrac(w)
  return (
    <div className="limit">
      <div className="limit-h">
        <span>{w.name}</span>
        <span className="tnum"><b>{Math.round(used * 100)}%</b> used{w.resets_at && <span className="muted" title={when(w.resets_at)}> · {resetsIn(w.resets_at)}</span>}</span>
        {onPin && <button type="button" className={`btn ghost icon-only xs limit-pin ${pinned ? 'on' : ''}`} onClick={onPin} aria-pressed={pinned}
          title={pinned ? 'Shown in the sidebar — click to hide it there' : 'Show in the sidebar'} aria-label={pinned ? 'Unpin from the sidebar' : 'Pin to the sidebar'}><Icon name="pushpin" size={14} /></button>}
      </div>
      <div className={`limit-bar ${limitTone(used)}`}><span style={{ width: `${Math.max(1, used * 100)}%` }} /></div>
    </div>
  )
}

// A card's head: the account (your name for it, else its email or the
// login's name; the pencil renames it), its plan, and the logins on it
// folded behind "on N logins". A login that cannot tell who it is (a token
// login) is put with one of your accounts by hand.
function AccountHead({ p, l, name, named, logins, others, onRename, onLink, onUnlink }: {
  p: ProviderId; l?: ProviderLimits; name: string; named: boolean; logins: Login[]
  others: { group: string; name: string }[]; onRename?: (name: string) => void; onLink: (group: string) => void; onUnlink: (key: string) => void
}) {
  const [editing, setEditing] = useState(false)
  const [open, setOpen] = useState(false)
  const acct = l?.email || l?.org
  const sub = [named || acct !== name ? acct : '', l?.plan && planName(l.plan)].filter(Boolean).join(' · ')
  const save = (v: string) => { setEditing(false); if (v.trim() !== (named ? name : '')) onRename?.(v.trim()) }
  return (
    <div className="limit-who">
      <div className="limit-prov"><span className={`prov-dot prov-${p}`} /><b className="nowrap">{providerName(p)}</b>
        {editing ? <input className="limit-name-input" autoFocus defaultValue={named ? name : ''} placeholder={name} maxLength={60} aria-label="Name for this account"
          onBlur={e => save(e.currentTarget.value)} onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur(); if (e.key === 'Escape') setEditing(false) }} />
          : <span className="ellipsis">{name}</span>}
        {onRename && !editing && <button type="button" className="btn ghost icon-only xs" title="Name this account" aria-label="Name this account" onClick={() => setEditing(true)}><Icon name="pen" size={13} /></button>}
        {l?.updated_at && <span className="muted xsmall nowrap">· {when(l.updated_at)}</span>}</div>
      {(sub || logins.length > 1) && <div className="muted xsmall limit-acct">
        {sub && <span className="ellipsis">{sub}</span>}
        {logins.length > 1 && <button type="button" className="link-btn nowrap" aria-expanded={open} onClick={() => setOpen(!open)}>{sub ? '· ' : ''}on {logins.length} logins</button>}</div>}
      {open && <ul className="limit-logins" aria-label="Logins on this account">{logins.map((x, i) => {
        const [where, folder] = x.kind === 'machine' ? x.name.split(' · ') : [x.name]
        return (
          <li key={i} className="login-chip" title={folder ? `${where}, folder ~/.${folder}` : x.kind === 'server' ? "This server's own login" : where}>
            <Icon name={x.kind === 'machine' ? 'server' : x.kind === 'server' ? 'home' : 'key'} size={12} />
            <span className="nowrap">{where}</span>
            {folder && <span className="login-folder nowrap">{folder}</span>}
            {x.key && <button type="button" className="login-x" title="Take this login out of this account" aria-label={`Take ${x.name} out`} onClick={() => onUnlink(x.key!)}><Icon name="x" size={11} /></button>}
          </li>
        )
      })}</ul>}
      {!l?.group && others.length > 0 && <label className="muted xsmall limit-acct">This login can't say which account it is.
        <select className="limit-link" value="" onChange={e => e.target.value && onLink(e.target.value)}>
          <option value="">Put it with…</option>
          {others.map(o => <option key={o.group} value={o.group}>{o.name}</option>)}
        </select></label>}
    </div>
  )
}

// Codex's free resets: how many, and when each one runs out (they expire).
function FreeResets({ r }: { r: Resets }) {
  const soon = (iso?: string) => !!iso && new Date(iso).getTime() - Date.now() < 48 * 3600e3
  return (
    <div className="resets">
      <div className="resets-h">
        <Icon name="refresh" size={14} />
        <span><b className="tnum">{r.available}</b> free {r.available === 1 ? 'reset' : 'resets'} available</span>
      </div>
      {r.available === 0 ? <p className="muted xsmall" style={{ margin: 0 }}>None right now — Codex grants one now and then.</p> : (
        <ul className="resets-list">
          {(r.credits || []).map((c, i) => (
            <li key={i} className={soon(c.expires_at) ? 'soon' : ''}>
              <span>{c.title}</span>
              <span className="tnum" title={c.expires_at ? new Date(c.expires_at).toLocaleString() : ''}>
                {c.expires_at ? <>{soon(c.expires_at) && <Icon name="alert" size={11} />}expires {when(c.expires_at)} · {resetsIn(c.expires_at, Date.now(), 'in ')}</> : 'does not expire'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export default function UsagePage({ onMenu }: { onMenu?: () => void }) {
  const { limits: liveLimits, settings, saveSettings } = useApp()
  const [days, setDays] = useState(30)
  const [metric, setMetric] = useState<'cost' | 'turns' | 'tokens'>('cost')
  const [refreshing, setRefreshing] = useState(false)
  const r = useAsync(() => api.get<UsageReport>(`${V1}/usage${qs({ days })}`), [days])
  const d = r.data
  const limits = { ...(d?.limits || {}), ...liveLimits }
  const val = (x: UsageReport['days'][number]) => metric === 'cost' ? x.cost_usd + (x.elsewhere_usd || 0) : metric === 'turns' ? x.turns : x.input_tokens + x.output_tokens
  const fmt = (n: number) => metric === 'cost' ? fmtCost(n) : metric === 'turns' ? String(n) : fmtTokens(n)
  const series = d?.days || []
  const max = Math.max(1e-9, ...series.map(val))
  const total = series.reduce((a, x) => a + val(x), 0)
  const providers = (['claude', 'codex'] as ProviderId[])
  // Outside Pocket AI: only money (the CLIs' files have no turn count to match)
  const away = d?.elsewhere || []
  const awayTotal = away.reduce((a, x) => a + x.cost_usd, 0)
  const awayBy = Object.entries(away.reduce<Record<string, number>>((a, x) => {
    const k = `${originName(x.origin)} · ${providerName(x.provider)}`
    a[k] = (a[k] || 0) + x.cost_usd
    return a
  }, {})).sort((a, b) => b[1] - a[1])
  const awayProv = providers.map(p => [p, away.filter(x => x.provider === p).reduce((a, x) => a + x.cost_usd, 0)] as const).filter(([, v]) => v > 0)
  const sources = limitSources(limits, settings)
  // empty values are dropped: a name cleared, a login taken out again
  const saveNaming = (patch: { usage_names?: Record<string, string>; usage_links?: Record<string, string> }) =>
    saveSettings(Object.fromEntries(Object.entries(patch).map(([k, m]) => [k, Object.fromEntries(Object.entries(m!).filter(([, v]) => v))]))).catch(() => {})
  // Pins pick what the sidebar panel shows. No pins = all; unpinning the
  // last one leaves a marker that matches nothing, so the panel is empty.
  // A window's keys include the ones it had under logins folded into it.
  const togglePin = (keys: string[]) => {
    const cur = settings?.usage_pinned?.length ? settings.usage_pinned.filter(k => k !== '-') : limitRows(limits, settings).map(x => x.key)
    const next = keys.some(k => cur.includes(k)) ? cur.filter(k => !keys.includes(k)) : [...cur, keys[0]]
    saveSettings({ usage_pinned: next.length ? next : ['-'] }).catch(() => {})
  }

  return (
    <div className="page">
      <PageHead title="Usage" icon="chart" onMenu={onMenu} sub="Turns, tokens and what they would cost, plus the subscription limits the CLIs report." />
      <div className="page-scroll">
        <div className="page-inner stack loose">
          <section className="card">
            <div className="card-head"><div className="grow"><h2>Plan limits</h2><p className="muted small">Read from every Claude Code and Codex login on the server and on your other machines, including per-model limits. Logins signed in to the same account share their limits and show once.</p></div>
              <button type="button" className="btn sm ghost icon-only" aria-label="Refresh limits" title="Refresh" disabled={refreshing}
                onClick={async () => { setRefreshing(true); try { r.setData(await api.get<UsageReport>(`${V1}/usage${qs({ days, refresh: 1 })}`)) } finally { setRefreshing(false) } }}>
                <Icon name="refresh" size={15} className={refreshing ? 'spin' : ''} /></button></div>
            <div className="card-body">
              <div className="two-col">
                {(sources.length ? sources : providers.map(p => ({ src: p as string, p, label: '', title: '', group: '', aliases: [] as string[], logins: [] as Login[] }))).map(({ src, p, label, title, group, aliases, logins }) => {
                  const l = limits[src]
                  const keys = (w: LimitWindow) => [limitKey(src, w), ...aliases.map(a => limitKey(a, w))]
                  const others = sources.filter(x => x.p === p && x.group).map(x => ({ group: x.group, name: x.title || x.label || 'Server login' }))
                  return (
                    <div key={src} className="limit-box">
                      <AccountHead p={p} l={l} name={title || label || 'Server login'} named={!!title} logins={logins}
                        others={l?.group ? [] : others}
                        onRename={group ? n => saveNaming({ usage_names: { ...(settings?.usage_names || {}), [group]: n } }) : undefined}
                        onLink={g => saveNaming({ usage_links: { ...(settings?.usage_links || {}), [src]: g } })}
                        onUnlink={k => saveNaming({ usage_links: { ...(settings?.usage_links || {}), [k]: '' } })} />
                      {l?.windows?.length ? l.windows.map(w => <LimitBar key={w.id || w.name} w={w}
                        pinned={isPinned(settings?.usage_pinned, keys(w))} onPin={settings?.usage_widget !== false ? () => togglePin(keys(w)) : undefined} />) : <p className="muted small" style={{ margin: 0 }}>{l?.error ? 'Could not read the limits: ' + l.error : 'Reading the limits…'}</p>}
                      {l?.note && <p className="muted xsmall" style={{ margin: 0 }}><Icon name="info" size={12} className="inline-ico" />{l.note}</p>}
                      {l?.resets && <FreeResets r={l.resets} />}
                    </div>
                  )
                })}
              </div>
            </div>
          </section>

          <section className="card">
            <div className="card-head usage-head">
              <div className="grow"><h2>{fmt(total)} <span className="muted small">{metric === 'cost' ? 'in API prices' : metric} · last {days} days</span></h2></div>
              <div className="row wrap usage-controls">
                <Segmented size="sm" value={metric} onChange={setMetric} options={[{ value: 'cost', label: 'Cost' }, { value: 'turns', label: 'Turns' }, { value: 'tokens', label: 'Tokens' }]} label="Metric" />
                <Segmented size="sm" value={days} onChange={setDays} options={[{ value: 7, label: '7d' }, { value: 30, label: '30d' }, { value: 90, label: '90d' }]} label="Range" />
              </div>
            </div>
            <div className="card-body">
              {r.error ? <div className="callout danger"><Icon name="alert" size={15} /><span>{r.error}</span></div> : !d ? <Loading /> : series.length === 0 ? <p className="muted">No turns in this range.</p> : (
                <div className="bars" role="img" aria-label={`${metric} per day`}>
                  {series.map((x, i) => (
                    <div key={x.date} className="bar-col" title={`${x.date}: ${fmt(val(x))} · ${x.turns} turns${metric === 'cost' && x.elsewhere_usd ? ` · ${fmtCost(x.elsewhere_usd)} outside Pocket AI` : ''}`}>
                      {metric === 'cost' && x.elsewhere_usd
                        ? <div className="bar stack"><span className="else" style={{ height: `${(x.elsewhere_usd / max) * 100}%` }} /><span style={{ height: `${(x.cost_usd / max) * 100}%` }} /></div>
                        : <div className="bar"><span style={{ height: `${(val(x) / max) * 100}%` }} /></div>}
                      {series.length <= 31 && <span className={`bar-l ${series.length > 14 && i % 5 !== (series.length - 1) % 5 ? 'bar-l-minor' : ''}`}>{new Date(x.date + 'T12:00:00').getDate()}</span>}
                    </div>
                  ))}
                </div>
              )}
              {d && (
                <div className="set-stats mt">
                  {providers.map(p => (
                    <div key={p} className="set-stat"><span className={`prov-dot prov-${p}`} /><span className="set-stat-n tnum">{fmtCost(d.providers?.[p]?.cost_usd || 0)}</span><span className="set-stat-l">{providerName(p)} · {d.providers?.[p]?.turns || 0} turns</span></div>
                  ))}
                  {awayTotal > 0 && (
                    <div className="set-stat" title={awayBy.map(([k, v]) => `${k}: ${fmtCost(v)}`).join('\n')}><span className="prov-dot prov-else" /><span className="set-stat-n tnum">{fmtCost(awayTotal)}</span>
                      <span className="set-stat-l">Outside Pocket AI · {awayProv.map(([p, v]) => `${providerName(p)} ${fmtCost(v)}`).join(' · ')}</span></div>
                  )}
                </div>
              )}
              <p className="muted xsmall mt-s">Costs are what the same tokens would cost on the API; on a Claude or ChatGPT subscription you pay the plan, and the limits above are what matters.
                {metric === 'cost' && ' Outside Pocket AI (the faint part of each bar) is what your logins here and on your other machines spent in Paseo, the terminal and agents your agents started, read from the CLIs\' own files; a Claude session counts once its process ends.'}</p>
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}
