// What the agents cost and how close you are to the plan limits.
import { useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtCost, fmtTokens, planName, providerName, resetsIn, when } from '../lib/format'
import { isPinned, limitKey, limitRows, limitSources, limitTone, usedFrac } from '../components/UsageWidget'
import type { LimitWindow, ProviderId, Resets, UsageReport } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Loading, Segmented, useAsync } from '../components/ui'

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
  const val = (x: UsageReport['days'][number]) => metric === 'cost' ? x.cost_usd : metric === 'turns' ? x.turns : x.input_tokens + x.output_tokens
  const fmt = (n: number) => metric === 'cost' ? fmtCost(n) : metric === 'turns' ? String(n) : fmtTokens(n)
  const series = d?.days || []
  const max = Math.max(1e-9, ...series.map(val))
  const total = series.reduce((a, x) => a + val(x), 0)
  const providers = (['claude', 'codex'] as ProviderId[])
  const sources = limitSources(limits)
  // Pins pick what the sidebar panel shows. No pins = all; unpinning the
  // last one leaves a marker that matches nothing, so the panel is empty.
  // A window's keys include the ones it had under logins folded into it.
  const togglePin = (keys: string[]) => {
    const cur = settings?.usage_pinned?.length ? settings.usage_pinned.filter(k => k !== '-') : limitRows(limits).map(x => x.key)
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
                {(sources.length ? sources : providers.map(p => ({ src: p as string, p, label: '', aliases: [] as string[] }))).map(({ src, p, label, aliases }) => {
                  const l = limits[src]
                  const keys = (w: LimitWindow) => [limitKey(src, w), ...aliases.map(a => limitKey(a, w))]
                  // Who it is: the email (yours or as admin), else the org; then the plan.
                  const who = [l?.email || l?.org, l?.plan && planName(l.plan)].filter(Boolean).join(' · ')
                  return (
                    <div key={src} className="limit-box">
                      <div className="limit-who">
                        <div className="limit-prov"><span className={`prov-dot prov-${p}`} /><b className="nowrap">{providerName(p)}</b>{l?.host_id ? <Icon name="server" size={13} className="inline-ico" /> : null}<span className="ellipsis">{label || 'Server login'}</span>
                          {l?.updated_at && <span className="muted xsmall nowrap">· {when(l.updated_at)}</span>}</div>
                        {(who || (label && l?.server_login) || l?.also?.length) && <div className="muted xsmall limit-acct">
                          {label && l?.server_login && <span className="badge" title={`The server's own ${providerName(p)} login is signed in to this same account, so they share these limits.`}>Also the server login</span>}
                          {l?.also?.length ? <span className="badge" title={`${l.also.join(', ')} ${l.also.length === 1 ? 'is' : 'are'} signed in to this same account, so they share these limits.`}>Also {l.also.join(', ')}</span> : null}
                          {who && <span className="ellipsis">{who}</span>}</div>}
                      </div>
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
                    <div key={x.date} className="bar-col" title={`${x.date}: ${fmt(val(x))} · ${x.turns} turns`}>
                      <div className="bar"><span style={{ height: `${(val(x) / max) * 100}%` }} /></div>
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
                </div>
              )}
              <p className="muted xsmall mt-s">Costs are what the same tokens would cost on the API; on a Claude or ChatGPT subscription you pay the plan, and the limits above are what matters.</p>
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}
