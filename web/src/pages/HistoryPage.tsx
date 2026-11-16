// "On this server": every session Claude Code and Codex keep on the server —
// from Pocket AI, Paseo, the terminal or any app — grouped by day, with
// where it ran, how much work it was and whether it is running now.
// ?host= narrows it to one machine (Settings → Servers opens it that way:
// 0 = this server, a machine's id = that machine), and "Open by ID" brings
// in a session whose id you have (claude --resume …, codex resume …).
import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtDuration, shortPath, stamp } from '../lib/format'
import { dayLabel, loginLabel, ORIGINS, originLabel, shortModel, useOpenSession } from '../lib/sessions'
import type { Origin, ProviderId, ServerSession } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { elapsed, useNow } from '../components/RunningList'
import { Empty, errMsg, Field, Loading, Modal, Segmented, Spinner, useDebounce } from '../components/ui'
import { PrivacyChip } from '../components/ShareDialog'

type Prov = 'all' | ProviderId

export default function HistoryPage({ onMenu }: { onMenu?: () => void }) {
  const { user, running, hosts } = useApp()
  const openSession = useOpenSession()
  const [params, setParams] = useSearchParams()
  const hostParam = params.get('host')
  const host = hostParam === null || hostParam === '' ? null : Number(hostParam)
  const hostName = host === null ? '' : host === 0 ? 'this server' : hosts.find(h => h.id === host)?.name || 'that server'
  const setHost = (h: number | null) => setParams(p => { const n = new URLSearchParams(p); if (h === null) n.delete('host'); else n.set('host', String(h)); return n }, { replace: true })
  const [byId, setById] = useState(false)
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
    api.get<{ sessions: ServerSession[]; total: number }>(`${V1}/history${qs({ provider, origin, q: dq, limit, ...(host !== null ? { host } : {}) })}`)
      .then(d => { if (live) { setData(d); setErr('') } }).catch(e => { if (live) { setErr(errMsg(e)); setData(d => d || { sessions: [], total: 0 }) } })
    return () => { live = false }
  }, [provider, origin, dq, limit, host, runKey])
  useEffect(() => { setData(null) }, [host])

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
      <PageHead title={host === null || host === 0 ? 'On this server' : `On ${hostName}`} icon="history" onMenu={onMenu}
        sub={host === null ? 'Every Claude Code and Codex session on the server and on your other machines — from Pocket AI, Paseo, the terminal or any app. Open one to read it and continue it here.'
          : `Every Claude Code and Codex session on ${hostName} — from Pocket AI, Paseo, the terminal or any app. Open one to read it and continue it here; it keeps running there.`}
        actions={<button type="button" className="btn sm" onClick={() => setById(true)} title="Open a session by its id"><Icon name="link" size={14} />Open by ID</button>} />
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
          {hosts.length > 0 && <div className="chip-row" role="group" aria-label="Machine">
            <button type="button" className={`fchip ${host === null ? 'on' : ''}`} onClick={() => setHost(null)}>All machines</button>
            <button type="button" className={`fchip ${host === 0 ? 'on' : ''}`} onClick={() => setHost(host === 0 ? null : 0)}><Icon name="home" size={12} />This server</button>
            {hosts.map(h => <button key={h.id} type="button" className={`fchip ${host === h.id ? 'on' : ''}`} onClick={() => setHost(host === h.id ? null : h.id)}><Icon name="server" size={12} />{h.name}</button>)}
          </div>}
          <div className="chip-row" role="group" aria-label="Where it ran">
            <button type="button" className={`fchip ${origin === '' ? 'on' : ''}`} onClick={() => setOrigin('')}>Everywhere</button>
            {ORIGINS.map(o => <button key={o.id} type="button" className={`fchip ${origin === o.id ? 'on' : ''}`} onClick={() => setOrigin(origin === o.id ? '' : o.id)}><span className={`odot o-${o.id}`} />{o.label}</button>)}
          </div>
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
          {!data ? <Loading label="Reading the session files…" /> : data.sessions.length === 0 ? (
            !err && <Empty title="No sessions found" icon="history">{dq || origin || provider !== 'all' ? 'Nothing matches these filters.' : `Neither CLI has saved sessions on ${hostName || 'the server'} yet.`}</Empty>
          ) : (
            <>
              <div className="srv-sum muted small">{data.total} {data.total === 1 ? 'session' : 'sessions'}{runningN > 0 && <> · <b className="accent-text">{runningN} running now</b></>}</div>
              {groups.map(g => (
                <section key={g.name} className="srv-group">
                  <h3 className="srv-h">{g.name}<span className="muted">{g.items.length}</span></h3>
                  <div className="srv-list">
                    {g.items.map(s => <SessionRow key={(s.host_id || 0) + s.provider + s.session_id} s={s} busy={busy === s.session_id} onOpen={() => open(s)} />)}
                  </div>
                </section>
              ))}
              {data.total > data.sessions.length && <button type="button" className="btn srv-more" onClick={() => setLimit(l => l + 300)}>Show more ({data.total - data.sessions.length} left)</button>}
            </>
          )}
        </div>
      </div>
      {byId && <OpenByIdDialog host={host && host > 0 ? host : 0} onClose={() => setById(false)} onOpen={async s => { await openSession(s) }} />}
    </div>
  )
}

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i

/** A session by its id, on this server or one of the machines: paste the id
 *  or the whole `claude --resume …` / `codex resume …` line. */
function OpenByIdDialog({ host, onClose, onOpen }: { host: number; onClose: () => void; onOpen: (s: { provider: ProviderId; session_id: string; host_id: number }) => Promise<void> }) {
  const { hosts } = useApp()
  const [where, setWhere] = useState(host)
  const [text, setText] = useState('')
  const [prov, setProv] = useState<ProviderId>('claude')
  const [busy, setBusy] = useState(false)
  const id = (UUID.exec(text)?.[0] || text.trim()).toLowerCase()
  const guessed: ProviderId | null = /\bcodex\b/i.test(text) ? 'codex' : /\bclaude\b/i.test(text) ? 'claude' : null
  useEffect(() => { if (guessed) setProv(guessed) }, [guessed])
  const ok = /^[a-z0-9-]{8,64}$/.test(id)
  const go = async () => { setBusy(true); try { await onOpen({ provider: prov, session_id: id, host_id: where }) } finally { setBusy(false) } }
  return (
    <Modal title="Open a session by ID" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!ok || busy} onClick={go}>{busy ? <><Spinner />Opening…</> : 'Open'}</button>
    </>}>
      <form className="stack" onSubmit={e => { e.preventDefault(); if (ok && !busy) void go() }}>
        <Field label="Session ID" help={<>The id, or the whole line: <code>claude --resume 1f2e…</code>, <code>codex resume 0199…</code>.</>}>
          <input value={text} onChange={e => setText(e.target.value)} autoFocus placeholder="1f2e3d4c-…" spellCheck={false} autoCapitalize="off" autoComplete="off" className="mono" />
        </Field>
        <Field label="Agent">
          <Segmented size="sm" label="Agent" value={prov} onChange={setProv} options={[
            { value: 'claude', label: <span className="row"><Icon name="claude" size={13} />Claude Code</span> },
            { value: 'codex', label: <span className="row"><Icon name="codex" size={13} />Codex</span> },
          ]} />
        </Field>
        {hosts.length > 0 && <Field label="Machine" help="Where the session was started; it keeps running there.">
          <select value={where} onChange={e => setWhere(Number(e.target.value))}>
            <option value={0}>This server</option>
            {hosts.map(h => <option key={h.id} value={h.id}>{h.name}</option>)}
          </select>
        </Field>}
      </form>
    </Modal>
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
          {s.host && <span className="badge" style={{ gap: 4 }} title={`On ${s.host} (Settings → Servers)${s.host_home ? `, login ${s.host_home}` : ''}`}><Icon name="server" size={11} />{s.host}{s.host_home && ` · ${loginLabel(s.host_home)}`}</span>}
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
