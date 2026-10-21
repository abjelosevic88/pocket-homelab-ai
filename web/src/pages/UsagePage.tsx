// What the agents cost and how close you are to the plan limits.
import { useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtCost, fmtTokens, providerName, when } from '../lib/format'
import type { LimitWindow, ProviderId, UsageReport } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Loading, Segmented, useAsync } from '../components/ui'

function LimitBar({ w }: { w: LimitWindow }) {
  const used = Math.max(0, Math.min(1, w.used > 1 ? w.used / 100 : w.used))
  const tone = used > 0.9 ? 'danger' : used > 0.7 ? 'warn' : ''
  return (
    <div className="limit">
      <div className="limit-h"><span>{w.name}</span><span className="tnum"><b>{Math.round(used * 100)}%</b> used{w.resets_at && <span className="muted"> · resets {when(w.resets_at)}</span>}</span></div>
      <div className={`limit-bar ${tone}`}><span style={{ width: `${Math.max(1, used * 100)}%` }} /></div>
    </div>
  )
}

export default function UsagePage({ onMenu }: { onMenu?: () => void }) {
  const { limits: liveLimits } = useApp()
  const [days, setDays] = useState(30)
  const [metric, setMetric] = useState<'cost' | 'turns' | 'tokens'>('cost')
  const r = useAsync(() => api.get<UsageReport>(`${V1}/usage${qs({ days })}`), [days])
  const d = r.data
  const limits = { ...(d?.limits || {}), ...liveLimits }
  const val = (x: UsageReport['days'][number]) => metric === 'cost' ? x.cost_usd : metric === 'turns' ? x.turns : x.input_tokens + x.output_tokens
  const fmt = (n: number) => metric === 'cost' ? fmtCost(n) : metric === 'turns' ? String(n) : fmtTokens(n)
  const series = d?.days || []
  const max = Math.max(1e-9, ...series.map(val))
  const total = series.reduce((a, x) => a + val(x), 0)
  const providers = (['claude', 'codex'] as ProviderId[])

  return (
    <div className="page">
      <PageHead title="Usage" icon="chart" onMenu={onMenu} sub="Turns, tokens and what they would cost, plus the subscription limits the CLIs report." />
      <div className="page-scroll">
        <div className="page-inner stack loose">
          <section className="card">
            <div className="card-head"><div className="grow"><h2>Plan limits</h2><p className="muted small">Reported by the CLIs while they run — fresh after any thread did something.</p></div></div>
            <div className="card-body">
              <div className="two-col">
                {providers.map(p => {
                  const l = limits[p]
                  return (
                    <div key={p} className="limit-box">
                      <div className="limit-prov"><span className={`prov-dot prov-${p}`} /><b>{providerName(p)}</b>{l?.updated_at && <span className="muted xsmall">· {when(l.updated_at)}</span>}</div>
                      {l?.windows?.length ? l.windows.map(w => <LimitBar key={w.name} w={w} />) : <p className="muted small" style={{ margin: 0 }}>No numbers yet — they show up after the next run.</p>}
                    </div>
                  )
                })}
              </div>
            </div>
          </section>

          <section className="card">
            <div className="card-head">
              <div className="grow"><h2>{fmt(total)} <span className="muted small">{metric === 'cost' ? 'in API prices' : metric} · last {days} days</span></h2></div>
              <div className="row wrap">
                <Segmented size="sm" value={metric} onChange={setMetric} options={[{ value: 'cost', label: 'Cost' }, { value: 'turns', label: 'Turns' }, { value: 'tokens', label: 'Tokens' }]} label="Metric" />
                <Segmented size="sm" value={days} onChange={setDays} options={[{ value: 7, label: '7d' }, { value: 30, label: '30d' }, { value: 90, label: '90d' }]} label="Range" />
              </div>
            </div>
            <div className="card-body">
              {r.error ? <div className="callout danger"><Icon name="alert" size={15} /><span>{r.error}</span></div> : !d ? <Loading /> : series.length === 0 ? <p className="muted">No turns in this range.</p> : (
                <div className="bars" role="img" aria-label={`${metric} per day`}>
                  {series.map(x => (
                    <div key={x.date} className="bar-col" title={`${x.date}: ${fmt(val(x))} · ${x.turns} turns`}>
                      <div className="bar"><span style={{ height: `${(val(x) / max) * 100}%` }} /></div>
                      {series.length <= 31 && <span className="bar-l">{new Date(x.date + 'T12:00:00').getDate()}</span>}
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
