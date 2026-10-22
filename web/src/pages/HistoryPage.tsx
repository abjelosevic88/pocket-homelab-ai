// "On this server": every session Claude Code and Codex keep on the server —
// from Pocket AI, Paseo, the terminal or any app — grouped by day, with
// where it ran, how much work it was and whether it is running now.
import { useEffect, useMemo, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtDuration, shortPath, stamp } from '../lib/format'
import { dayLabel, ORIGINS, originLabel, shortModel, useOpenSession } from '../lib/sessions'
import type { Origin, ProviderId, ServerSession } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { elapsed, useNow } from '../components/RunningList'
import { Empty, errMsg, Loading, Segmented, Spinner, useDebounce } from '../components/ui'
import { PrivacyChip } from '../components/ShareDialog'

type Prov = 'all' | ProviderId

export default function HistoryPage({ onMenu }: { onMenu?: () => void }) {
  const { user, running } = useApp()
  const openSession = useOpenSession()
  const [provider, setProvider] = useState<Prov>(() => (localStorage.getItem('pai-history-prov') as Prov) || 'all')
  const [origin, setOrigin] = useState<Origin | ''>('')
  const [q, setQ] = useState('')
  const dq = useDebounce(q.trim(), 250)
  const [limit, setLimit] = useState(300)
  const [data, setData] = useState<{ sessions: ServerSession[]; total: number } | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  useEffect(() => { localStorage.setItem('pai-history-prov', provider) }, [provider])

  // reload on filter changes, and whenever something starts or stops running
  const runKey = running.map(r => r.session_id + r.status).join()
  useEffect(() => {
    let live = true
    api.get<{ sessions: ServerSession[]; total: number }>(`${V1}/history${qs({ provider, origin, q: dq, limit })}`)
      .then(d => { if (live) { setData(d); setErr('') } }).catch(e => { if (live) { setErr(errMsg(e)); setData(d => d || { sessions: [], total: 0 }) } })
    return () => { live = false }
  }, [provider, origin, dq, limit, runKey])

  const groups = useMemo(() => {
    const out: { name: string; items: ServerSession[] }[] = []
    for (const s of data?.sessions || []) {
      const name = s.running ? 'Running now' : dayLabel(new Date(s.updated_at))
      const g = out.find(x => x.name === name)
      if (g) g.items.push(s); else out.push({ name, items: [s] })
    }
    return out.sort((a, b) => (b.name === 'Running now' ? 1 : 0) - (a.name === 'Running now' ? 1 : 0))
  }, [data])

  if (user && user.role !== 'admin' && !user.can_run) return (
    <div className="page"><PageHead title="On this server" icon="history" onMenu={onMenu} />
      <Empty title="Not for this account" icon="lock">These are the server account’s own files; an admin has to allow you to run agents first.</Empty></div>
  )

  const open = async (s: ServerSession) => { setBusy(s.session_id); try { await openSession(s) } finally { setBusy('') } }
  const runningN = data?.sessions.filter(s => s.running).length || 0

  return (
    <div className="page">
      <PageHead title="On this server" icon="history" onMenu={onMenu} sub="Every Claude Code and Codex session on the server — from Pocket AI, Paseo, the terminal or any app. Open one to read it, follow it live and continue it here." />
      <div className="page-scroll">
        <div className="page-inner srv">
          <div className="srv-tools">
            <div className="search-box grow"><Icon name="search" size={14} /><input value={q} onChange={e => setQ(e.target.value)} placeholder="Search titles, prompts and folders…" aria-label="Search sessions" /></div>
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
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
          {!data ? <Loading label="Reading the session files…" /> : data.sessions.length === 0 ? (
            !err && <Empty title="No sessions found" icon="history">{dq || origin || provider !== 'all' ? 'Nothing matches these filters.' : 'Neither CLI has saved sessions on the server yet.'}</Empty>
          ) : (
            <>
              <div className="srv-sum muted small">{data.total} {data.total === 1 ? 'session' : 'sessions'}{runningN > 0 && <> · <b className="accent-text">{runningN} running now</b></>}</div>
              {groups.map(g => (
                <section key={g.name} className="srv-group">
                  <h3 className="srv-h">{g.name}<span className="muted">{g.items.length}</span></h3>
                  <div className="srv-list">
                    {g.items.map(s => <SessionRow key={s.provider + s.session_id} s={s} busy={busy === s.session_id} onOpen={() => open(s)} />)}
                  </div>
                </section>
              ))}
              {data.total > data.sessions.length && <button type="button" className="btn srv-more" onClick={() => setLimit(l => l + 300)}>Show more ({data.total - data.sessions.length} left)</button>}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function RunTimer({ since }: { since?: string }) {
  const now = useNow(1000)
  return <span className="tnum">{elapsed(since, now)}</span>
}

function SessionRow({ s, busy, onOpen }: { s: ServerSession; busy: boolean; onOpen: () => void }) {
  const showLast = s.last_prompt && s.last_prompt !== s.title && s.runs > 1
  return (
    <button type="button" className={`srv-row ${s.running ? 'is-running' : ''}`} onClick={onOpen} disabled={busy} title={s.cwd}>
      <span className={`srv-ico ${s.provider}`}><Icon name={s.provider} size={17} /></span>
      <span className="srv-main">
        <span className="srv-title">{s.title || 'Untitled session'}</span>
        {showLast && <span className="srv-last"><Icon name="message" size={11} />{s.last_prompt}</span>}
        <span className="srv-meta">
          <span className={`origin-chip o-${s.origin}`}>{originLabel(s.origin)}</span>
          <PrivacyChip sharedWith={s.shared_with} />
          <span className="mono ellipsis">{shortPath(s.cwd)}</span>
          <span>{s.runs} {s.runs === 1 ? 'run' : 'runs'}</span>
          {s.work_ms > 0 && <span title="Time the agent spent working">{fmtDuration(s.work_ms)} worked</span>}
          {s.model && <span>{shortModel(s.model)}</span>}
        </span>
      </span>
      <span className="srv-side">
        {busy ? <Spinner /> : s.running ? <span className="srv-live"><span className="spinner" /><RunTimer since={s.running_at} /></span> : <span className="srv-time tnum">{stamp(s.updated_at)}</span>}
        {s.thread_id ? <span className="srv-in" title="Already in Pocket AI"><Icon name="check" size={11} />In Pocket AI</span> : <span className="srv-open">Open<Icon name="chevron-right" size={13} /></span>}
      </span>
    </button>
  )
}
