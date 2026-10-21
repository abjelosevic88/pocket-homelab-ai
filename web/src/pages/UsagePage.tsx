// What the agents cost and how close you are to the plan limits.
import { useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtCost, fmtTokens, providerName, resetsIn, when } from '../lib/format'
import { isPinned, limitKey, limitRows, limitTone, usedFrac } from '../components/UsageWidget'
import type { LimitWindow, ProviderId, UsageReport } from '../lib/types'
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
  // Pins pick what the sidebar panel shows. No pins = all; unpinning the
  // last one leaves a marker that matches nothing, so the panel is empty.
  const togglePin = (key: string) => {
    const cur = settings?.usage_pinned?.length ? settings.usage_pinned.filter(k => k !== '-') : limitRows(limits).map(x => x.key)
    const next = cur.includes(key) ? cur.filter(k => k !== key) : [...cur, key]
    saveSettings({ usage_pinned: next.length ? next : ['-'] }).catch(() => {})
  }

  return (
    <div className="page">
      <PageHead title="Usage" icon="chart" onMenu={onMenu} sub="Turns, tokens and what they would cost, plus the subscription limits the CLIs report." />
      <div className="page-scroll">
        <div className="page-inner stack loose">
          <section className="card">
            <div className="card-head"><div className="grow"><h2>Plan limits</h2><p className="muted small">Read from the Claude Code and Codex accounts on the server, including per-model limits.</p></div>
              <button type="button" className="btn sm ghost icon-only" aria-label="Refresh limits" title="Refresh" disabled={refreshing}
                onClick={async () => { setRefreshing(true); try { r.setData(await api.get<UsageReport>(`${V1}/usage${qs({ days, refresh: 1 })}`)) } finally { setRefreshing(false) } }}>
                <Icon name="refresh" size={15} className={refreshing ? 'spin' : ''} /></button></div>
            <div className="card-body">
              <div className="two-col">
                {providers.map(p => {
                  const l = limits[p]
                  return (
                    <div key={p} className="limit-box">
                      <div className="limit-prov"><span className={`prov-dot prov-${p}`} /><b>{providerName(p)}</b>{l?.updated_at && <span className="muted xsmall">· {when(l.updated_at)}</span>}</div>
                      {l?.windows?.length ? l.windows.map(w => <LimitBar key={w.id || w.name} w={w}
                        pinned={isPinned(settings?.usage_pinned, limitKey(p, w))} onPin={settings?.usage_widget !== false ? () => togglePin(limitKey(p, w)) : undefined} />) : <p className="muted small" style={{ margin: 0 }}>{l?.error ? 'Could not read the limits: ' + l.error : 'Reading the limits…'}</p>}
                      {l?.note && <p className="muted xsmall" style={{ margin: 0 }}><Icon name="info" size={12} className="inline-ico" />{l.note}</p>}
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
