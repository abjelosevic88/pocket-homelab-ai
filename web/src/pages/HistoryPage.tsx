// "On this server": sessions Claude Code and Codex already have on disk — open one here and keep going.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, shortPath } from '../lib/format'
import type { HistorySession, ProviderId, Thread } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Empty, errMsg, Loading, Spinner, Tabs, useDebounce, useToast } from '../components/ui'

export default function HistoryPage({ onMenu }: { onMenu?: () => void }) {
  const { upsertThread, user } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [provider, setProvider] = useState<ProviderId>(() => (localStorage.getItem('pai-history-tab') as ProviderId) || 'claude')
  const [q, setQ] = useState('')
  const dq = useDebounce(q.trim(), 250)
  const [list, setList] = useState<HistorySession[] | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  useEffect(() => { localStorage.setItem('pai-history-tab', provider) }, [provider])
  useEffect(() => {
    let live = true
    setList(null); setErr('')
    api.get<HistorySession[]>(`${V1}/history${qs({ provider, q: dq, limit: 200 })}`)
      .then(l => live && setList(l || [])).catch(e => { if (live) { setErr(errMsg(e)); setList([]) } })
    return () => { live = false }
  }, [provider, dq])

  const open = async (s: HistorySession) => {
    if (s.thread_id) { navigate(`/t/${s.thread_id}`); return }
    setBusy(s.session_id)
    try {
      const t = await api.post<Thread>(`${V1}/history/import`, { provider: s.provider || provider, session_id: s.session_id })
      upsertThread(t)
      navigate(`/t/${t.id}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }

  if (user && user.role !== 'admin' && !user.can_run) return (
    <div className="page"><PageHead title="On this server" icon="history" onMenu={onMenu} />
      <Empty title="Not for this account" icon="lock">These are the server account’s own files; an admin has to allow you to run agents first.</Empty></div>
  )

  return (
    <div className="page">
      <PageHead title="On this server" icon="history" onMenu={onMenu} sub="Sessions the CLIs already have — from the terminal, Paseo or anywhere. Open one to read it and continue it here." />
      <div className="page-toolbar">
        <Tabs tabs={[{ id: 'claude', label: 'Claude Code' }, { id: 'codex', label: 'Codex' }]} value={provider} onChange={setProvider} />
        <div className="search-box grow"><Icon name="search" size={14} /><input value={q} onChange={e => setQ(e.target.value)} placeholder="Search titles and folders…" aria-label="Search sessions" /></div>
      </div>
      <div className="page-scroll">
        <div className="page-inner">
          {err && <div className="callout danger mb"><Icon name="alert" size={15} /><span>{err}</span></div>}
          {!list ? <Loading /> : list.length === 0 ? (!err && <Empty title="No sessions found" icon="history">{dq ? 'Nothing matches that search.' : 'This CLI has no saved sessions on the server yet.'}</Empty>) : (
            <div className="hist-list">
              {list.map(s => (
                <button key={s.session_id} type="button" className="hist-row" onClick={() => open(s)} disabled={!!busy}>
                  <span className="hist-ico"><Icon name={s.thread_id ? 'message' : 'history'} size={15} /></span>
                  <span className="grow" style={{ minWidth: 0 }}>
                    <span className="hist-title">{s.title || 'Untitled session'}</span>
                    <span className="hist-meta"><span className="mono ellipsis">{shortPath(s.cwd)}</span><span>·</span><span>{ago(s.updated_at)}</span>{s.messages > 0 && <><span>·</span><span>{s.messages} {s.messages === 1 ? 'message' : 'messages'}</span></>}</span>
                  </span>
                  {busy === s.session_id ? <Spinner /> : <span className={`btn sm ${s.thread_id ? '' : 'primary'}`} title={s.thread_id ? 'Already in Pocket AI' : 'Add it to Pocket AI and follow it live'}>Open</span>}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
