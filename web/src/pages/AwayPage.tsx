// Away from keyboard: while you are away the agents keep working on what they
// can, and save what needs you (approvals, questions) here. Decide them when
// you are back; each conversation hears your decisions as one message.
import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { stamp } from '../lib/format'
import type { AwayEntry } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { RequestCard } from '../components/RequestCard'
import { Empty, Loading, Spinner, errMsg, useToast } from '../components/ui'

function byThread(list: AwayEntry[]) {
  const out: { id: string; title: string; items: AwayEntry[] }[] = []
  for (const e of list) {
    const g = out.find(x => x.id === e.thread_id)
    if (g) g.items.push(e); else out.push({ id: e.thread_id, title: e.thread_title, items: [e] })
  }
  return out
}

export default function AwayPage({ onMenu }: { onMenu?: () => void }) {
  const { away, setAway, reloadAway } = useApp()
  const { toast } = useToast()
  const [busy, setBusy] = useState<'toggle' | 'all' | null>(null)
  useEffect(() => { reloadAway() }, [reloadAway])
  const saved = useMemo(() => byThread(away?.items || []), [away])
  const waiting = useMemo(() => byThread(away?.waiting || []), [away])
  const approvable = (away?.items || []).filter(e => e.request.type === 'permission').length

  const toggle = async () => {
    setBusy('toggle')
    try { await setAway(!away?.on) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(null) }
  }
  const allowAll = async () => {
    setBusy('all')
    try {
      const r = await api.post<{ allowed: number }>(`${V1}/away/allow-all`)
      toast(`Approved ${r.allowed} — the agents run them now`)
      reloadAway()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(null) }
  }

  const group = (g: { id: string; title: string; items: AwayEntry[] }) => (
    <section key={g.id} className="away-thread">
      <Link to={`/t/${g.id}`} className="away-thread-h"><Icon name="message" size={13} /><span className="grow ellipsis">{g.title}</span><Icon name="chevron-right" size={13} /></Link>
      {g.items.map(e => (
        <div key={e.key} className="away-item">
          <div className="muted xsmall tnum">{stamp(e.at)}</div>
          <RequestCard threadId={e.thread_id} req={e.request} onDone={reloadAway} />
        </div>
      ))}
    </section>
  )

  return (
    <div className="page">
      <PageHead title="Away" icon="moon" onMenu={onMenu}
        sub="While you are away, agents don't stop to wait for you: they carry on with everything else, and save approvals and questions here for when you are back."
        actions={away && <button type="button" className={`btn ${away.on ? 'primary' : ''}`} disabled={!!busy} onClick={toggle}>
          {busy === 'toggle' ? <Spinner /> : <Icon name={away.on ? 'sun' : 'moon'} size={14} />}{away.on ? "I'm back" : 'Go away'}
        </button>} />
      <div className="page-scroll">
        <div className="page-inner away-page">
          {!away ? <Loading /> : <>
            {away.on && (
              <div className="callout info away-status">
                <Icon name="moon" size={15} />
                <span>Away since {stamp(away.since || '')}. {away.working > 0 ? `${away.working === 1 ? '1 conversation is' : `${away.working} conversations are`} still working.` : 'Nothing is working right now.'} You get one notification when everything is done.</span>
              </div>
            )}
            {saved.length === 0 && waiting.length === 0 ? (
              <Empty title={away.on ? 'Nothing saved yet' : 'Nothing waiting for you'} icon="moon">
                {away.on ? 'When an agent needs your OK or an answer, it is saved here and the agent keeps going with the rest.'
                  : <>Turn on <b>Away mode</b> (bottom of the sidebar) before you leave: conversations won't get stuck on an approval overnight.</>}
              </Empty>
            ) : <>
              {saved.length > 0 && (
                <div className="away-sec">
                  <h3 className="srv-h grow">Saved for you<span className="muted">{away.items.length}</span>
                    {approvable > 1 && <button type="button" className="btn sm away-all" disabled={!!busy} onClick={allowAll}>{busy === 'all' ? <Spinner /> : <Icon name="check" size={13} />}Approve all {approvable}</button>}
                  </h3>
                  <p className="muted small">The agent was told “not now” and carried on. Approve and it runs it now; deny and it finishes without it.</p>
                  {saved.map(group)}
                </div>
              )}
              {waiting.length > 0 && (
                <div className="away-sec">
                  <h3 className="srv-h">Still waiting<span className="muted">{away.waiting.length}</span></h3>
                  <p className="muted small">These conversations are paused on you (plans always wait: there is nothing else to do in plan mode).</p>
                  {waiting.map(group)}
                </div>
              )}
            </>}
          </>}
        </div>
      </div>
    </div>
  )
}

/** Across the top of every page while away mode is on (the Away page says it itself). */
export function AwayBanner() {
  const { away, setAway } = useApp()
  const { toast } = useToast()
  const loc = useLocation()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  if (!away?.on || loc.pathname === '/away') return null
  const saved = away.items.length + away.waiting.length
  const back = async () => {
    setBusy(true)
    try {
      const a = await setAway(false)
      if (a.items.length + a.waiting.length > 0) navigate('/away')
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <div className="away-banner" role="status">
      <Icon name="moon" size={14} />
      <span className="grow ellipsis"><b>Away mode</b><span className="away-banner-long"> — agents keep working and save approvals and questions for you</span></span>
      {saved > 0 && <Link to="/away" className="away-banner-saved">{saved} saved</Link>}
      <button type="button" className="btn sm" disabled={busy} onClick={back}>{busy ? <Spinner size={12} /> : <Icon name="sun" size={12} />}I&rsquo;m back</button>
    </div>
  )
}
