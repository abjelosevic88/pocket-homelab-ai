// Today: every prompt an agent worked on during a day — in Pocket AI, Paseo,
// the terminal or any app — with how long it took and how it ended. Read from
// the CLIs' own session files; page back through earlier days.
import { useEffect, useMemo, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtDuration, fmtTokens, shortPath } from '../lib/format'
import { dayLabel, hhmm, hourLabel, ORIGINS, originLabel, shortModel, useOpenSession } from '../lib/sessions'
import type { Account, Origin, ProviderId, Run } from '../lib/types'
import { accountName } from '../components/AgentPicker'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { elapsed, useNow } from '../components/RunningList'
import { Empty, errMsg, Loading, Segmented } from '../components/ui'
import { PrivacyChip } from '../components/ShareDialog'

type Prov = 'all' | ProviderId
const midnight = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
const addDays = (d: Date, n: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n)

const STATUS: Record<Run['status'], { label: string; icon: string }> = {
  running: { label: 'Running', icon: '' },
  done: { label: 'Done', icon: 'check' },
  interrupted: { label: 'Interrupted', icon: 'stop' },
  unfinished: { label: 'Never finished', icon: 'alert' },
}

/** The login a run used, by name: the server's own login and a named login on
 * the same account read as one (accountName), so they filter together. */
function runAccount(r: Run, accounts: Account[]) {
  const a = accounts.find(x => x.provider === r.provider && x.id === (r.account_id || 0))
  return a ? accountName(a, accounts) : r.account_id ? `Login ${r.account_id}` : 'Server login'
}

export default function TodayPage({ onMenu }: { onMenu?: () => void }) {
  const { user, running, accounts } = useApp()
  const openSession = useOpenSession()
  const now = useNow(1000)
  const [day, setDay] = useState(() => midnight(new Date()))
  const [provider, setProvider] = useState<Prov>('all')
  const [origin, setOrigin] = useState<Origin | ''>('')
  const [account, setAccount] = useState('') // provider + ':' + login name
  const [runs, setRuns] = useState<Run[] | null>(null)
  const [err, setErr] = useState('')
  const isToday = day.getTime() === midnight(new Date()).getTime()

  const runKey = running.map(r => r.session_id + r.status).join()
  useEffect(() => {
    let live = true
    const load = () => api.get<{ runs: Run[] }>(`${V1}/runs${qs({ from: day.toISOString(), to: addDays(day, 1).toISOString() })}`)
      .then(d => { if (live) { setRuns(d.runs || []); setErr('') } }).catch(e => { if (live) { setErr(errMsg(e)); setRuns(r => r || []) } })
    load()
    // today keeps moving: refresh now and then (and when anything starts or stops)
    const t = isToday ? setInterval(load, 30e3) : undefined
    return () => { live = false; if (t) clearInterval(t) }
  }, [day, isToday, runKey])
  useEffect(() => { setRuns(null) }, [day])

  const acctKey = (r: Run) => r.provider + ':' + runAccount(r, accounts)
  // logins that ran something this day (for the chosen agent), busiest first
  const logins = useMemo(() => {
    const m = new Map<string, { provider: ProviderId; name: string; n: number }>()
    for (const r of runs || []) {
      if (provider !== 'all' && r.provider !== provider) continue
      const k = acctKey(r), x = m.get(k)
      if (x) x.n++; else m.set(k, { provider: r.provider, name: runAccount(r, accounts), n: 1 })
    }
    return [...m.entries()].sort((a, b) => b[1].n - a[1].n)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runs, provider, accounts])
  const shown = useMemo(() => (runs || []).filter(r => (provider === 'all' || r.provider === provider) && (!origin || r.origin === origin) && (!account || acctKey(r) === account)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [runs, provider, origin, account, accounts])
  const runMS = (r: Run) => r.status === 'running' ? Math.max(0, now - new Date(r.started_at).getTime()) : r.duration_ms
  const stats = useMemo(() => {
    const work = shown.reduce((a, r) => a + runMS(r), 0)
    const longest = shown.reduce<Run | null>((a, r) => !a || runMS(r) > runMS(a) ? r : a, null)
    return {
      runs: shown.length,
      claude: shown.filter(r => r.provider === 'claude').length,
      codex: shown.filter(r => r.provider === 'codex').length,
      work, longest,
      running: shown.filter(r => r.status === 'running').length,
      sessions: new Set(shown.map(r => r.provider + r.session_id)).size,
      folders: new Set(shown.map(r => r.cwd)).size,
      tools: shown.reduce((a, r) => a + r.tools, 0),
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown, Math.floor(now / 10e3)])
  const hours = useMemo(() => {
    const h = Array.from({ length: 24 }, () => ({ n: 0, ms: 0 }))
    for (const r of shown) { const x = h[new Date(r.started_at).getHours()]; x.n++; x.ms += runMS(r) }
    return h
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown])
  const groups = useMemo(() => {
    const out: { hour: number; items: Run[] }[] = []
    for (const r of shown) {
      const hour = new Date(r.started_at).getHours()
      const g = out.find(x => x.hour === hour)
      if (g) g.items.push(r); else out.push({ hour, items: [r] })
    }
    return out
  }, [shown])

  if (user && user.role !== 'admin' && !user.can_run) return (
    <div className="page"><PageHead title="Today" icon="today" onMenu={onMenu} />
      <Empty title="Not for this account" icon="lock">An admin has to allow you to run agents first.</Empty></div>
  )

  const title = isToday ? 'Today' : dayLabel(day)
  const max = Math.max(1, ...hours.map(h => h.n))
  const curHour = new Date(now).getHours()

  return (
    <div className="page">
      <PageHead title={title} icon="today" onMenu={onMenu} sub={day.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }) + ' — every prompt an agent worked on, wherever it was sent from.'} />
      <div className="page-scroll">
        <div className="page-inner today">
          <div className="today-nav">
            <button type="button" className="btn sm icon-only" onClick={() => setDay(d => addDays(d, -1))} aria-label="Previous day" title="Previous day"><Icon name="chevron-left" size={16} /></button>
            <input type="date" className="input sm" value={`${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`}
              max={new Date().toLocaleDateString('sv-SE')} aria-label="Day"
              onChange={e => { const [y, m, d] = e.target.value.split('-').map(Number); if (y) setDay(new Date(y, m - 1, d)) }} />
            <button type="button" className="btn sm icon-only" onClick={() => setDay(d => addDays(d, 1))} disabled={isToday} aria-label="Next day" title="Next day"><Icon name="chevron-right" size={16} /></button>
            {!isToday && <button type="button" className="btn sm" onClick={() => setDay(midnight(new Date()))}>Today</button>}
            <span className="grow" />
            <Segmented size="sm" label="Agent" value={provider} onChange={setProvider} options={[
              { value: 'all', label: 'All' },
              { value: 'claude', label: <span className="row"><Icon name="claude" size={13} />Claude</span> },
              { value: 'codex', label: <span className="row"><Icon name="codex" size={13} />Codex</span> },
            ]} />
          </div>
          <div className="chip-row" role="group" aria-label="Where it ran">
            <button type="button" className={`fchip ${origin === '' ? 'on' : ''}`} onClick={() => setOrigin('')}>Everywhere</button>
            {ORIGINS.map(o => <button key={o.id} type="button" className={`fchip ${origin === o.id ? 'on' : ''}`} onClick={() => setOrigin(origin === o.id ? '' : o.id)}><span className={`odot o-${o.id}`} />{o.label}</button>)}
          </div>
          {(logins.length > 1 || account) && <div className="chip-row" role="group" aria-label="Account">
            <button type="button" className={`fchip ${account === '' ? 'on' : ''}`} onClick={() => setAccount('')}>All accounts</button>
            {logins.map(([k, l]) => <button key={k} type="button" className={`fchip ${account === k ? 'on' : ''}`} onClick={() => setAccount(account === k ? '' : k)}><Icon name={l.provider} size={12} />{l.name}<span className="muted tnum">{l.n}</span></button>)}
            {account && !logins.some(([k]) => k === account) && <button type="button" className="fchip on" onClick={() => setAccount('')}>{account.split(':').slice(1).join(':')}<Icon name="x" size={12} /></button>}
          </div>}
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
          {!runs ? <Loading label="Reading the session files…" /> : (
            <>
              <div className="today-stats">
                <div className="stat"><span className="stat-l">Runs</span><b className="stat-v tnum">{stats.runs}</b><span className="stat-s">{stats.claude} Claude · {stats.codex} Codex</span></div>
                <div className="stat"><span className="stat-l">Agent time</span><b className="stat-v tnum">{stats.work ? fmtDuration(stats.work) : '—'}</b><span className="stat-s">{stats.longest ? `longest ${fmtDuration(runMS(stats.longest))}` : 'nothing yet'}</span></div>
                <div className="stat"><span className="stat-l">Running now</span><b className="stat-v tnum">{stats.running}</b><span className="stat-s">{stats.tools} tool calls in all</span></div>
                <div className="stat"><span className="stat-l">Sessions</span><b className="stat-v tnum">{stats.sessions}</b><span className="stat-s">in {stats.folders} {stats.folders === 1 ? 'folder' : 'folders'}</span></div>
              </div>

              <figure className="hours card" aria-label="Runs per hour">
                <figcaption className="hours-h"><span>Runs per hour</span><span className="muted xsmall">hover a bar for details</span></figcaption>
                <div className="hours-bars" role="list">
                  {hours.map((h, i) => (
                    <div key={i} role="listitem" className={`hbar ${isToday && i === curHour ? 'now' : ''} ${isToday && i > curHour ? 'future' : ''}`}
                      aria-label={`${hourLabel(i)} — ${h.n} ${h.n === 1 ? 'run' : 'runs'}${h.ms ? `, ${fmtDuration(h.ms)}` : ''}`}>
                      <span className="hbar-fill" style={{ height: h.n ? `${Math.max(8, (h.n / max) * 100)}%` : 0 }} />
                      <span className="hbar-tip">{hourLabel(i)}–{hourLabel((i + 1) % 24)}<br /><b>{h.n} {h.n === 1 ? 'run' : 'runs'}</b>{h.ms > 0 && <> · {fmtDuration(h.ms)}</>}</span>
                    </div>
                  ))}
                </div>
                <div className="hours-x" aria-hidden="true">{[0, 6, 12, 18].map(h => <span key={h} style={{ left: `${(h / 24) * 100}%` }}>{hourLabel(h)}</span>)}</div>
              </figure>

              {shown.length === 0 ? (
                <Empty title={isToday ? 'Nothing yet today' : 'Nothing that day'} icon="today">{provider !== 'all' || origin || account ? 'No runs match these filters.' : 'No agent worked on a prompt this day.'}</Empty>
              ) : groups.map(g => (
                <section key={g.hour} className="tl-hour">
                  <h3 className="tl-hour-h tnum">{hourLabel(g.hour)}<span className="muted">{g.items.length} {g.items.length === 1 ? 'run' : 'runs'}</span></h3>
                  <div className="run-list">
                    {g.items.map(r => <RunCard key={r.provider + r.session_id + r.started_at} r={r} login={runAccount(r, accounts)} now={now} onOpen={() => openSession(r, r.key)} />)}
                  </div>
                </section>
              ))}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function RunCard({ r, login, now, onOpen }: { r: Run; login: string; now: number; onOpen: () => void }) {
  const st = STATUS[r.status] || STATUS.done
  const dur = r.status === 'running' ? elapsed(r.started_at, now) : r.duration_ms ? fmtDuration(r.duration_ms) : ''
  return (
    <button type="button" className={`run-item ${r.status}`} onClick={onOpen} title={r.cwd}>
      <span className="run-at tnum">{hhmm(r.started_at)}</span>
      <span className="run-body">
        <span className="run-top">
          <span className="run-at-m tnum">{hhmm(r.started_at)}</span>
          <span className={`run-st ${r.status}`}>{r.status === 'running' ? <span className="spinner" /> : <Icon name={st.icon} size={12} />}{st.label}</span>
          <span className={`srv-ico sm ${r.provider}`}><Icon name={r.provider} size={12} /></span>
          <span className="badge" title={`Ran on the ${login} login`}>{login}</span>
          <span className={`origin-chip o-${r.origin}`}>{originLabel(r.origin)}</span>
          <PrivacyChip sharedWith={r.shared_with} />
          <span className="grow" />
          {dur && <span className="run-dur tnum"><Icon name="clock" size={12} />{dur}</span>}
        </span>
        <span className="run-prompt">{r.images ? '🖼 ' : ''}{r.prompt}</span>
        <span className="run-meta">
          {r.title && r.title !== r.prompt && <span className="ellipsis run-sess"><Icon name="message" size={11} />{r.title}</span>}
          <span className="mono">{shortPath(r.cwd)}</span>
          {r.tools > 0 && <span>{r.tools} {r.tools === 1 ? 'tool call' : 'tool calls'}</span>}
          {!!r.follow_ups && <span>+{r.follow_ups} sent while working</span>}
          {!!r.output_tokens && <span>{fmtTokens(r.output_tokens)} tokens out</span>}
          {r.model && <span>{shortModel(r.model)}</span>}
        </span>
      </span>
    </button>
  )
}
