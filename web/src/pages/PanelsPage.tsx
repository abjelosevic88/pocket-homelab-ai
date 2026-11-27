// Panels: one question put to several models at once — the list, and one
// panel with its outcome on top and every participant's answers per step.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtCost, providerName } from '../lib/format'
import { ACCESS, forgetPanelTab, KIND_ICON, openPanelSheet, PANEL_EVENT, panelTab, rememberTab, type Panel, type PanelSeat } from '../lib/panels'
import { pathAfterDelete } from '../components/TabBar'
import { setFocusMode } from '../lib/sidebar'
import type { ProviderId, Thread } from '../lib/types'
import { offeredProviders } from '../components/AgentPicker'
import { Icon } from '../components/icons'
import { Markdown } from '../components/Markdown'
import { PageHead } from '../components/PageHead'
import { Select } from '../components/Select'
import { PanelDefaults } from '../components/PanelSetup'
import { EditorChat, PanelPlan } from '../components/PanelPlan'
import { Confirm, copyText, download, Empty, errMsg, Loading, MenuButton, Modal, Spinner, useContextMenu, useIsMobile, useMediaQuery, useToast, type MenuItem, noGrammarly } from '../components/ui'

const STATUS: Record<string, { label: string; cls: string; icon: string }> = {
  running: { label: 'Running', cls: 'accent-soft', icon: 'refresh' },
  done: { label: 'Done', cls: 'ok', icon: 'check-circle' },
  failed: { label: 'Did not finish', cls: 'danger', icon: 'x-circle' },
  stopped: { label: 'Stopped', cls: 'warn', icon: 'x-circle' },
}

/** Refetch when the server says a panel moved on. */
function usePanelEvents(fn: (d: { id: string; status: string }) => void) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    const on = (e: Event) => ref.current((e as CustomEvent).detail || {})
    window.addEventListener(PANEL_EVENT, on)
    return () => window.removeEventListener(PANEL_EVENT, on)
  }, [])
}

function SeatDots({ seats }: { seats: PanelSeat[] }) {
  return <span className="pn-dots">{seats.filter(s => !s.chair).map(s => <span key={s.id} className={`pn-dot ${s.state}`} title={`${s.who}${s.error ? ' — ' + s.error : s.state === 'working' ? ' — working' : s.state === 'done' ? ' — answered' : ''}`}><Icon name={s.provider} size={12} className={`prov-glyph ${s.provider}`} /></span>)}</span>
}

export default function PanelsPage({ onMenu }: { onMenu?: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { user } = useApp()
  const [list, setList] = useState<Panel[] | null>(null)
  const [err, setErr] = useState('')
  const [del, setDel] = useState<Panel | null>(null)
  const [ren, setRen] = useState<Panel | null>(null)
  const [defaults, setDefaults] = useState(false)
  const ctx = useContextMenu()
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  const load = useCallback(() => api.get<Panel[]>(`${V1}/panels`).then(l => { setList(l || []); setErr('') }).catch(e => setErr(errMsg(e))), [])
  useEffect(() => { void load() }, [load])
  usePanelEvents(() => { void load() })
  const anyRunning = !!list?.some(p => p.status === 'running')
  useEffect(() => {
    if (!anyRunning) return
    const t = setInterval(() => { if (!document.hidden) void load() }, 5000)
    return () => clearInterval(t)
  }, [anyRunning, load])

  const kinds: { id: string; name: string; text: string }[] = [
    { id: 'ask', name: 'Ask all', text: 'Same question, answers side by side' },
    { id: 'brainstorm', name: 'Brainstorm', text: 'Ideas, built on each other' },
    { id: 'council', name: 'Council', text: 'Judge a plan from several views' },
    { id: 'refine', name: 'Refine', text: 'Judge, rewrite, judge again' },
    { id: 'debate', name: 'Debate', text: 'For and against, then a ruling' },
    { id: 'review', name: 'Second opinion', text: 'Other models check an answer' },
    { id: 'build', name: 'Best of N', text: 'Same coding task, best result wins' },
  ]
  return (
    <div className="page">
      <PageHead title="Panels" icon="panel" onMenu={onMenu} sub="One question, several models at once: they answer alone, see each other’s answers without names, and one writes the summary."
        actions={<>
          <button type="button" className="btn" disabled={!canRun} onClick={() => setDefaults(true)} title="Which models take part when you do not pick: /refine, /council, panels an agent starts"><Icon name="settings" size={14} /><span className="pn-defaults-label">Default models</span></button>
          <button type="button" className="btn primary" disabled={!canRun} onClick={() => openPanelSheet()}><Icon name="plus" size={14} />New panel</button>
        </>} />
      <div className="page-scroll">
        <div className="page-inner">
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
          <div className="stack">
            <div className="pn-starts">
              {kinds.map(k => <button key={k.id} type="button" className="pn-start" disabled={!canRun} onClick={() => openPanelSheet({ kind: k.id })}>
                <span className="pn-start-ico"><Icon name={KIND_ICON[k.id]} size={16} /></span>
                <span className="pn-start-text"><b>{k.name}</b><span className="muted">{k.text}</span></span>
              </button>)}
            </div>
            {!list ? (!err && <Loading />) : list.length === 0 ? (
              <Empty title="No panels yet" icon="panel">
                Pick a kind above. In any conversation you can also type <code>/council</code>, <code>/brainstorm</code>, <code>/debate</code>, <code>/ask-all</code> or <code>/second-opinion</code>.
              </Empty>
            ) : (
              <div className="list-cards">
                {ctx.menu}
                {list.map(p => {
                  const st = STATUS[p.status]
                  const more: MenuItem[] = [
                    { label: 'Open', icon: 'external', onSelect: () => navigate(`/panels/${p.id}`) },
                    { label: 'Rename…', icon: 'edit', onSelect: () => setRen(p) },
                    { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDel(p) },
                  ]
                  const step = p.status === 'running' && p.spec.stages[p.stage]
                  return (
                    <div key={p.id} className="list-card pn-card" role="button" tabIndex={0} onClick={() => navigate(`/panels/${p.id}`)} onKeyDown={e => { if (e.key === 'Enter') navigate(`/panels/${p.id}`) }} onContextMenu={e => ctx.open(e, more)}>
                      <span className="pn-start-ico"><Icon name={KIND_ICON[p.kind] || 'users'} size={16} /></span>
                      <div className="grow" style={{ minWidth: 0 }}>
                        <div className="list-card-title"><span className="ellipsis">{p.title}</span></div>
                        <div className="sched-meta">
                          <span>{p.spec.kind_name}</span>
                          <span className={`badge ${st.cls}`}>{p.status === 'running' ? <Spinner size={9} /> : <Icon name={st.icon} size={10} />}{step ? `${p.stage + 1}/${p.spec.stages.length} ${step.name}` : st.label}</span>
                          <SeatDots seats={p.seats} />
                          <span className="faint">{ago(p.created_at)}</span>
                          {p.cost_usd > 0 && <span className="faint tnum">{fmtCost(p.cost_usd)}</span>}
                        </div>
                      </div>
                      <div className="list-card-actions" onClick={e => e.stopPropagation()}>
                        <MenuButton label={`More for ${p.title}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      </div>
      {defaults && <PanelDefaults onClose={() => setDefaults(false)} />}
      {ren && <RenamePanel panel={ren} onClose={() => setRen(null)} onDone={() => { setRen(null); void load() }} />}
      {del && <Confirm title={`Delete “${del.title}”?`} message="The participants’ conversations are deleted with it." onCancel={() => setDel(null)}
        onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/panels/${d.id}`); forgetPanelTab(d.id); void load(); toast('Panel deleted') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

/** Rename a panel. */
function RenamePanel({ panel, onClose, onDone }: { panel: Panel; onClose: () => void; onDone: (p: Panel) => void }) {
  const { toast } = useToast()
  const [title, setTitle] = useState(panel.title)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    if (!title.trim() || busy) return
    setBusy(true)
    try { onDone(await api.patch<Panel>(`${V1}/panels/${panel.id}`, { title })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Rename the panel" onClose={onClose} size="sm" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!title.trim() || busy} onClick={save}>{busy && <Spinner />}Rename</button>
    </>}>
      <input autoFocus value={title} maxLength={140} aria-label="Name" onFocus={e => e.currentTarget.select()} onChange={e => setTitle(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void save() }} />
    </Modal>
  )
}

const ACTIVITY: Record<string, string> = { starting: 'Starting…', working: 'Working…', waiting_for_person: 'Needs your answer', waiting: 'Waiting for its turn', skipped: 'Left out', failed: 'Dropped out', done: 'Done' }

/** "1m 20s" since an ISO time, ticking. */
function Elapsed({ since }: { since?: string }) {
  const [, tick] = useState(0)
  useEffect(() => { const t = setInterval(() => tick(n => n + 1), 1000); return () => clearInterval(t) }, [])
  const ms = since ? Date.now() - new Date(since).getTime() : NaN
  if (!(ms >= 0)) return null
  const s = Math.floor(ms / 1000)
  return <span className="tnum">{s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`}</span>
}

/** One participant's card in a step: its answer, what it is writing, or why it dropped out. */
function SeatCard({ seat, text, live, current, onOpen, wide, onStep, place }: { seat: PanelSeat; text?: string; live?: boolean; current: boolean; onOpen: () => void
  /** the reading pane: one answer at full width, with previous / next */
  wide?: boolean; onStep?: (by: number) => void; place?: string }) {
  const [copied, setCopied] = useState(false)
  const failed = !text && (seat.state === 'failed' || seat.state === 'skipped')
  const working = current && seat.state === 'working'
  const asks = seat.activity === 'waiting_for_person'
  return (
    <article className={`pn-answer ${wide ? 'wide' : ''} ${failed ? 'failed' : ''} ${working ? 'working' : ''}`}>
      <header className="pn-answer-h">
        <span className={`pn-letter ${seat.chair ? 'chair' : ''}`}>{seat.chair ? <Icon name="edit" size={12} /> : seat.label}</span>
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="pn-who"><Icon name={seat.provider} size={14} className={`prov-glyph ${seat.provider}`} /><span className="ellipsis">{providerName(seat.provider)}{seat.model ? ` · ${seat.model}` : ''}</span></div>
          <div className="pn-sub muted xsmall">{[seat.role, seat.machine].filter(Boolean).join(' · ')}</div>
        </div>
        {wide && onStep && <span className="pn-pager"><button type="button" className="btn ghost icon-only xs" aria-label="Previous participant" title="Previous (←)" onClick={() => onStep(-1)}><Icon name="chevron-left" size={15} /></button><span className="tnum muted xsmall">{place}</span><button type="button" className="btn ghost icon-only xs" aria-label="Next participant" title="Next (→)" onClick={() => onStep(1)}><Icon name="chevron-right" size={15} /></button></span>}
        {text && !live && <button type="button" className="btn ghost icon-only xs" title={copied ? 'Copied' : 'Copy'} aria-label="Copy this answer" onClick={async () => { if (await copyText(text)) { setCopied(true); setTimeout(() => setCopied(false), 1500) } }}><Icon name={copied ? 'check' : 'copy'} size={14} /></button>}
        {seat.thread_id && <button type="button" className="btn ghost icon-only xs" title="Open its conversation in a tab: see how it works, or ask it more" aria-label="Open its conversation" onClick={onOpen}><Icon name="external" size={14} /></button>}
      </header>
      {working && (
        <button type="button" className={`pn-doing ${asks ? 'asks' : ''}`} onClick={onOpen} title="Open its conversation in a tab to watch it work">
          {asks ? <Icon name="alert" size={13} /> : <Spinner size={11} />}
          <span className="pn-doing-what ellipsis">{asks ? 'It asked something — open it to answer' : seat.doing || (seat.activity === 'starting' ? 'Starting…' : text ? 'Writing…' : 'Thinking…')}</span>
          <span className="pn-doing-meta">{!!seat.steps && <span>{seat.steps} {seat.steps === 1 ? 'step' : 'steps'}</span>}<Elapsed since={seat.since} /></span>
        </button>
      )}
      <div className="pn-answer-body">
        {text ? <Markdown text={text} className={live ? 'pn-live' : ''} />
          : failed ? <div className="pn-out"><Icon name="alert" size={14} /><span>{seat.error || ACTIVITY[seat.state]}</span></div>
          : working ? <div className="pn-skel" aria-hidden><span /><span /><span /></div>
          : <div className="pn-wait muted small">{current ? ACTIVITY[seat.state] || '' : 'No answer in this step'}</div>}
      </div>
    </article>
  )
}

/** The start of an answer in plain words, for a participant's small card. */
function gist(text: string, max = 170): string {
  const plain = text.replace(/```[\s\S]*?```/g, ' ').split('\n').map(l => l.replace(/^\s*(#{1,6}|>|[-*+]|\d+[.)])\s+/, '').replace(/[*_`~]/g, '').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').trim()).filter(Boolean).join(' ')
  return plain.length > max ? plain.slice(0, max - 1).trimEnd() + '…' : plain
}
const words = (text: string) => text.trim() ? text.trim().split(/\s+/).length : 0

/** One participant as a small card in the row above the reading pane: who, how far, and how its answer begins. */
function SeatTile({ seat, text, live, current, on, onPick }: { seat: PanelSeat; text?: string; live?: boolean; current: boolean; on: boolean; onPick: () => void }) {
  const failed = !text && (seat.state === 'failed' || seat.state === 'skipped')
  const working = current && seat.state === 'working'
  const asks = seat.activity === 'waiting_for_person'
  return (
    // a div, not a <button>: WebKit (Pocket Hub, Safari) wraps a button's children in its own centered box,
    // so this column of rows was centered and long names spilled out of the card on both sides
    <div role="tab" tabIndex={0} aria-selected={on} className={`pn-tile ${on ? 'on' : ''} ${failed ? 'failed' : ''} ${working ? 'working' : ''}`} onClick={onPick}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onPick() } }}>
      <span className="pn-tile-h">
        <span className="pn-letter">{seat.label}</span>
        <Icon name={seat.provider} size={14} className={`prov-glyph ${seat.provider}`} />
        <span className="pn-tile-name ellipsis">{providerName(seat.provider)}{seat.model ? ` · ${seat.model}` : ''}</span>
        {working ? (asks ? <Icon name="alert" size={13} className="pn-tile-warn" /> : <Spinner size={11} />) : text ? <Icon name="check" size={13} className="pn-tile-ok" /> : failed ? <Icon name="x" size={13} className="pn-tile-bad" /> : null}
      </span>
      {seat.role && <span className="pn-tile-role ellipsis">{seat.role}</span>}
      <span className="pn-tile-gist">
        {working ? <span className="pn-tile-doing">{asks ? 'It asked something — open it to answer' : seat.doing || (seat.activity === 'starting' ? 'Starting…' : text ? 'Writing…' : 'Thinking…')}</span>
          : text ? gist(text)
          : failed ? seat.error || 'Dropped out'
          : current ? ACTIVITY[seat.state] || '' : 'No answer in this step'}
      </span>
      <span className="pn-tile-foot">
        {working ? <><Elapsed since={seat.since} />{!!seat.steps && <span>{seat.steps} {seat.steps === 1 ? 'step' : 'steps'}</span>}{live && text ? <span>{words(text)} words so far</span> : null}</>
          : text ? <span>{words(text)} words</span> : <span>&nbsp;</span>}
      </span>
    </div>
  )
}

export function PanelPage({ onMenu }: { onMenu?: () => void }) {
  const { id = '' } = useParams()
  const { toast } = useToast()
  const navigate = useNavigate()
  const { providers, settings, upsertThread } = useApp()
  const [p, setP] = useState<Panel | null>(null)
  const [err, setErr] = useState('')
  // what the page shows: the result ('out'), the editor chat on narrow screens, the question, or a step by number
  const [tab, setTab] = useState<'out' | 'chat' | 'asked' | number | null>(null)
  // full screen: the app sidebar folds away and the page head goes, only the tab row stays
  const [full, setFull] = useState(() => localStorage.getItem('pai-panel-full') === '1')
  const mobile = useIsMobile()
  useEffect(() => { setFocusMode(full && !mobile); localStorage.setItem('pai-panel-full', full ? '1' : '0') }, [full, mobile])
  useEffect(() => () => { setFocusMode(false) }, [])
  // the editor chat beside the plan: when there is room and it is not hidden; its width is dragged
  const roomy = useMediaQuery(full ? '(min-width: 860px)' : '(min-width: 1100px)')
  const [dockOn, setDockOn] = useState(() => localStorage.getItem('pai-panel-chat') !== '0')
  const showDock = (v: boolean) => { setDockOn(v); localStorage.setItem('pai-panel-chat', v ? '1' : '0') }
  const [dockW, setDockW] = useState(() => Math.min(900, Math.max(300, Number(localStorage.getItem('pai-panel-chat-w')) || 400)))
  const wide = roomy && dockOn
  const panes = useRef<HTMLDivElement>(null)
  const drag = (e: React.PointerEvent) => {
    e.preventDefault()
    const x0 = e.clientX, w0 = dockW
    const max = Math.min(900, (panes.current?.clientWidth || window.innerWidth) - 360) // the plan keeps 360px
    let w = w0
    const move = (ev: PointerEvent) => { w = Math.round(Math.min(Math.max(300, w0 - (ev.clientX - x0)), Math.max(300, max))); setDockW(w) }
    const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); document.body.classList.remove('pn-dragging'); localStorage.setItem('pai-panel-chat-w', String(w)) }
    document.body.classList.add('pn-dragging')
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }
  const nudge = (by: number) => setDockW(w => { const n = Math.min(900, Math.max(300, w + by)); localStorage.setItem('pai-panel-chat-w', String(n)); return n })
  // how the answers of a step are read: one at a time in a wide pane, or all side by side
  const [view, setView] = useState<'read' | 'side'>(() => localStorage.getItem('pai-panel-view') === 'side' ? 'side' : 'read')
  const [sel, setSel] = useState(0) // the participant in the reading pane (its seat id; 0 = pick for me)
  const [del, setDel] = useState(false)
  const [ren, setRen] = useState(false)
  const [cont, setCont] = useState<{ text: string; provider: ProviderId | '' } | null>(null)
  const [busy, setBusy] = useState('')
  const load = useCallback(() => api.get<Panel>(`${V1}/panels/${id}`).then(x => { setP(x); setErr('') }).catch(e => setErr(errMsg(e))), [id])
  useEffect(() => { setP(null); setTab(null); void load() }, [load])
  usePanelEvents(d => { if (d.id === id) void load() })
  const running = p?.status === 'running'
  const chatting = !!p?.chat?.some(m => m.state === 'working') // the editor is answering in the panel's chat
  // its tab: named after the question, a spinner while it runs
  useEffect(() => { if (p) rememberTab(panelTab(p.id), { title: p.title, kind: p.kind, status: p.status }) }, [p?.id, p?.title, p?.kind, p?.status]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!running && !chatting) return
    const t = setInterval(() => { if (!document.hidden) void load() }, 1500)
    return () => clearInterval(t)
  }, [running, chatting, load])

  if (err && !p) return <div className="page"><PageHead title="Panel" icon="panel" onMenu={onMenu} /><div className="page-scroll"><div className="page-inner"><div className="callout danger"><Icon name="alert" size={15} /><span>{err === 'not found' ? 'This panel is gone.' : err}</span></div></div></div></div>
  if (!p) return <div className="page"><PageHead title="Panel" icon="panel" onMenu={onMenu} /><div className="page-scroll"><Loading /></div></div>

  const stages = p.spec.stages
  const answers = p.answers || []
  const seats = p.seats.filter(s => !s.chair)
  const chairs = p.seats.filter(s => s.chair)
  const chair = chairs.find(s => s.state !== 'failed') || chairs[chairs.length - 1]
  // the last step, when the summary writer has it, is the outcome on top; every other step is a tab
  // (also a rewrite in between, in a Refine panel)
  // a panel that keeps versions shows the text itself on top, so each of its steps is a tab
  const versioned = !!p.versions?.length
  const finalStage = !versioned && stages.length > 0 && stages[stages.length - 1].who === 'chair' ? stages.length - 1 : -1
  const rounds = stages.map((s, i) => ({ ...s, i })).filter(s => s.i !== finalStage)
  // the step shown: the one picked, else the one being answered, else the last one answered
  const lastAnswered = [...rounds].reverse().find(r => answers.some(a => a.stage === r.i))
  const auto = running && p.stage !== finalStage && p.stage < stages.length ? p.stage : (lastAnswered?.i ?? rounds[0]?.i ?? 0)
  const st = STATUS[p.status]
  const writing = running && p.stage === finalStage
  const chairLive = writing ? chair?.live : ''
  const outcome = p.result || chairLive || ''
  const hasOut = versioned || finalStage >= 0
  const outReady = versioned || !!outcome || writing
  // the default: the plan or the outcome when there is one, else the step being answered
  const picked = tab === 'chat' && wide ? 'out' : tab
  const pane = picked ?? (hasOut && outReady ? 'out' : auto)
  const setPane = (v: 'out' | 'chat' | 'asked' | number) => setTab(v)
  const stepPane = typeof pane === 'number'
  // short tab names in a Refine panel: "Verdicts 2", "Rewrite 2" (the whole name is the tooltip)
  const stepName = (i: number) => {
    if (p.kind !== 'refine') return stages[i].name
    const who = stages[i].who
    const n = stages.slice(0, i + 1).filter(s => s.who === who).length
    return `${who === 'chair' ? 'Rewrite' : 'Verdicts'}${n > 1 ? ` ${n}` : ''}`
  }
  const failedSeats = seats.filter(s => s.state === 'failed' || s.state === 'skipped')
  const openThread = (tid: string) => {
    const s = p.seats.find(x => x.thread_id === tid)
    if (s) rememberTab(tid, { title: `${p.spec.kind_name} · ${s.chair ? 'summary' : s.label}${s.role ? ' ' + s.role : ''}: ${p.title}`, provider: s.provider })
    navigate(`/t/${tid}`)
  }

  const act = async (what: string, fn: () => Promise<void>) => { setBusy(what); try { await fn() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') } }
  const stop = () => act('stop', async () => { setP(await api.post<Panel>(`${V1}/panels/${p.id}/stop`)) })
  const again = () => act('again', async () => { const np = await api.post<Panel>(`${V1}/panels/${p.id}/again`); navigate(`/panels/${np.id}`) })
  const copyAll = async () => { try { if (await copyText(await api.getText(`${V1}/panels/${p.id}/export.md`))) toast('Copied as Markdown') } catch (e) { toast(errMsg(e), 'err') } }
  const goOn = () => act('cont', async () => {
    const t = await api.post<Thread>(`${V1}/panels/${p.id}/continue`, { text: cont?.text || '', provider: cont?.provider || undefined })
    upsertThread(t); setCont(null); navigate(`/t/${t.id}`)
  })
  const more: MenuItem[] = [
    { label: 'Rename…', icon: 'edit', onSelect: () => setRen(true) },
    { label: 'Copy everything as Markdown', icon: 'copy', onSelect: () => void copyAll() },
    { label: 'Download as Markdown', icon: 'download', onSelect: () => download(`${V1}/panels/${p.id}/export.md?download=1`) },
    { label: 'Run it again', icon: 'refresh', onSelect: () => void again(), disabled: running },
    ...(p.from_thread_id && p.from_title ? [{ label: 'Back to the conversation', icon: 'message', hint: p.from_title, onSelect: () => openThread(p.from_thread_id) } as MenuItem] : []),
    'sep',
    { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDel(true) },
  ]
  const provOpts = [{ value: '' as ProviderId | '', label: 'My default agent' }, ...offeredProviders(providers, settings?.default_provider).map(x => ({ value: x as ProviderId | '', label: providerName(x), disabled: !providers.find(q => q.id === x)?.available }))]

  return (
    <div className="page">
      {!full && <PageHead title={p.spec.kind_name} icon={KIND_ICON[p.kind] || 'users'} onMenu={onMenu} sub={<button type="button" className="pn-head-sub" title="Rename" onClick={() => setRen(true)}>{p.title}</button>}
        actions={<>
          <button type="button" className="btn ghost sm pn-back" onClick={() => navigate('/panels')}><Icon name="chevron-left" size={14} />All panels</button>
          {running ? <button type="button" className="btn" disabled={busy === 'stop'} onClick={stop}>{busy === 'stop' ? <Spinner size={11} /> : <Icon name="x" size={13} />}Stop</button>
            : <button type="button" className="btn" disabled={busy === 'again'} onClick={again}>{busy === 'again' ? <Spinner size={11} /> : <Icon name="refresh" size={13} />}Run again</button>}
          {!mobile && <button type="button" className="btn ghost icon-only" title="Full screen: fold the app sidebar and this header away" aria-label="Full screen" onClick={() => setFull(true)}><Icon name="maximize" size={15} /></button>}
          <MenuButton label="More" align="right" items={more}><Icon name="more" size={16} /></MenuButton>
        </>} />}
      <div className="pn-body">
        {(p.error || (p.status === 'done' && failedSeats.length > 0)) && <div className="pn-notes">
          {p.error && <div className={`callout ${p.status === 'failed' ? 'danger' : 'warn'}`}><Icon name="alert" size={15} /><span>{p.error}</span></div>}
          {p.status === 'done' && failedSeats.length > 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>{seats.length - failedSeats.length} of {seats.length} answered. {failedSeats.map(s => `${s.who}: ${s.error}`).join('; ')}</span></div>}
        </div>}
        {/* one row for everything there is to look at: the result, the editor (when it has no column), every step, the question */}
        <div className="pn-bar">
          <div className="pn-tabs" role="tablist" aria-label="What to show">
            {hasOut && <button type="button" role="tab" aria-selected={pane === 'out'} disabled={!outReady} className={`pn-tab pn-tab-out ${pane === 'out' ? 'on' : ''}`} onClick={() => setPane('out')}>
              <Icon name="sparkles" size={13} />{versioned ? (p.kind === 'refine' ? 'Plan' : 'Result') : stages[finalStage].name}
              {versioned ? <span className="pn-tab-n tnum">v{p.versions!.length}</span> : writing && <Spinner size={9} />}
            </button>}
            {versioned && !wide && <button type="button" role="tab" aria-selected={pane === 'chat'} className={`pn-tab ${pane === 'chat' ? 'on' : ''}`} onClick={() => setPane('chat')}>
              <Icon name="message" size={13} />Editor{chatting ? <Spinner size={9} /> : !!p.chat?.length && <span className="pn-tab-n tnum">{p.chat.length}</span>}
            </button>}
            {rounds.length > 0 && hasOut && <span className="pn-tab-sep" aria-hidden />}
            {rounds.map(r => {
              const n = answers.filter(a => a.stage === r.i).length
              const reached = r.i <= p.stage || n > 0
              const state = running && p.stage === r.i ? 'now' : p.status === 'done' || r.i < p.stage || (n > 0 && r.i !== p.stage) ? 'done' : r.i === p.stage && !running ? 'halt' : ''
              return <button key={r.i} type="button" role="tab" aria-selected={pane === r.i} disabled={!reached} title={r.name} className={`pn-tab pn-step ${state} ${pane === r.i ? 'on' : ''}`} onClick={() => setPane(r.i)}>
                {state === 'now' ? <Spinner size={9} /> : state === 'done' ? <Icon name="check" size={12} className="pn-step-ok" /> : state === 'halt' ? <Icon name="x" size={12} className="pn-step-bad" /> : <span className="pn-prog-n">{r.i + 1}</span>}
                {stepName(r.i)}
              </button>
            })}
            <button type="button" role="tab" aria-selected={pane === 'asked'} className={`pn-tab ${pane === 'asked' ? 'on' : ''}`} onClick={() => setPane('asked')}><Icon name="help-circle" size={13} />Question</button>
          </div>
          <span className="pn-bar-end">
            {stepPane && seats.length > 1 && stages[pane]?.who !== 'chair' && <span className="pn-view">
              <button type="button" className={`btn ghost icon-only xs ${view === 'read' ? 'on' : ''}`} aria-pressed={view === 'read'} title="One answer at a time, wide" aria-label="One answer at a time" onClick={() => { setView('read'); localStorage.setItem('pai-panel-view', 'read') }}><Icon name="sidebar" size={15} /></button>
              <button type="button" className={`btn ghost icon-only xs ${view === 'side' ? 'on' : ''}`} aria-pressed={view === 'side'} title="All answers side by side" aria-label="All answers side by side" onClick={() => { setView('side'); localStorage.setItem('pai-panel-view', 'side') }}><Icon name="split-right" size={15} /></button>
            </span>}
            {/* done needs no badge: every step in the row has its tick */}
            {p.status !== 'done' && <span className={`badge ${st.cls}`}><Icon name={st.icon} size={10} />{st.label}</span>}
            {p.cost_usd > 0 && <span className="faint xsmall tnum pn-cost" title="What the participants’ runs cost so far">{fmtCost(p.cost_usd)}</span>}
            {versioned && roomy && <button type="button" className={`btn ghost icon-only xs ${dockOn ? 'on' : ''}`} aria-pressed={dockOn} title={dockOn ? 'Hide the editor chat' : 'Show the editor chat beside the plan'} aria-label={dockOn ? 'Hide the editor chat' : 'Show the editor chat'} onClick={() => { showDock(!dockOn); if (!dockOn && tab === 'chat') setTab('out') }}><Icon name="sidebar-right" size={15} /></button>}
            {full && <>
              <MenuButton label="More" align="right" items={more}><Icon name="more" size={15} /></MenuButton>
              <button type="button" className="btn sm" title="Leave full screen" onClick={() => setFull(false)}><Icon name="minimize" size={13} />Exit full screen</button>
            </>}
          </span>
        </div>

        <div ref={panes} className={`pn-panes ${versioned && wide ? 'with-side' : ''}`} style={versioned && wide ? { gridTemplateColumns: `minmax(0, 1fr) 10px ${dockW}px` } : undefined}>
          <div className={`pn-main ${pane === 'chat' ? 'fill' : ''}`} key={String(pane)}>
            {pane === 'asked' ? (
              <div className="pn-asked-body">
                {p.question && <div className="pn-q">{p.question}</div>}
                {p.material && <><div className="label muted xsmall">The material</div><Markdown text={p.material} /></>}
                <div className="muted xsmall">{p.depth[0].toUpperCase() + p.depth.slice(1)}{p.kind !== 'build' ? ` · ${ACCESS.find(a => a.value === (p.spec.access || ''))?.label.toLowerCase()}` : ''} · {ago(p.created_at)}{p.spec.cwd ? ` · ${p.spec.cwd}` : ''}
                  {p.from_thread_id && p.from_title && <> · from <button type="button" className="link-btn" onClick={() => openThread(p.from_thread_id)}>{p.from_title}</button></>}</div>
              </div>
            ) : pane === 'chat' ? (
              <EditorChat p={p} onChange={setP} onOpenThread={openThread} />
            ) : pane === 'out' ? (versioned ? <PanelPlan p={p} onChange={setP} /> : (
              <section className={`pn-outcome ${writing ? 'writing' : ''}`}>
                <header className="pn-outcome-h">
                  <Icon name="sparkles" size={15} /><b className="grow">{stages[finalStage].name}</b>
                  {writing ? <span className="badge accent-soft"><Spinner size={9} />{chair?.activity === 'starting' ? 'Starting…' : 'Being written…'}</span>
                    : chair && <span className="muted xsmall pn-by" title="It got the answers by letter, without the models’ names"><Icon name={chair.provider} size={12} className={`prov-glyph ${chair.provider}`} />{providerName(chair.provider)}{chair.model ? ` · ${chair.model}` : ''}</span>}
                  {p.result && !running && <button type="button" className="btn ghost icon-only xs" title="Copy" aria-label="Copy the result" onClick={async () => { if (await copyText(p.result)) toast('Copied') }}><Icon name="copy" size={14} /></button>}
                  {chair?.thread_id && <button type="button" className="btn ghost icon-only xs" title="Open the summary writer’s conversation" aria-label="Open the summary writer’s conversation" onClick={() => openThread(chair.thread_id)}><Icon name="external" size={14} /></button>}
                  {p.result && !running && <button type="button" className="btn primary sm" onClick={() => setCont({ text: '', provider: '' })}><Icon name="arrow-right" size={13} />Continue with this</button>}
                </header>
                {writing && chair && <div className="pn-doing flat"><Spinner size={11} /><span className="pn-doing-what ellipsis">{chair.doing || (outcome ? 'Writing…' : 'Reading the answers…')}</span><span className="pn-doing-meta">{!!chair.steps && <span>{chair.steps} {chair.steps === 1 ? 'step' : 'steps'}</span>}<Elapsed since={chair.since} /></span></div>}
                {outcome ? <Markdown text={outcome} className={writing ? 'pn-live' : ''} /> : <div className="pn-skel" aria-hidden><span /><span /><span /></div>}
              </section>
            )) : stages[pane]?.who === 'chair' ? (
              // a step of the summary writer in between (a rewrite): one pane
              chair && (() => {
                const a = answers.find(x => x.stage === pane && chairs.some(c => c.id === x.seat_id))
                const current = running && p.stage === pane
                const live = current && !a && chair.state === 'working' ? chair.live : ''
                return <SeatCard wide seat={chair} text={a?.text || live || undefined} live={!!live} current={current} onOpen={() => openThread(chair.thread_id)} />
              })()
            ) : (() => {
              const current = running && p.stage === pane
              const rows = seats.map(s => {
                const a = answers.find(x => x.stage === pane && x.seat_id === s.id)
                const live = current && !a && s.state === 'working' ? s.live || '' : ''
                return { s, text: a?.text || live || undefined, live: !!live }
              })
              // in the pane: the one picked, else the first that has something to read
              const at = Math.max(0, rows.findIndex(r => r.s.id === sel) >= 0 ? rows.findIndex(r => r.s.id === sel) : rows.findIndex(r => r.text))
              const cur = rows[at]
              const step = (by: number) => setSel(rows[(at + by + rows.length) % rows.length].s.id)
              const continueRow = !p.result && !running && answers.length > 0 && finalStage < 0 && <div className="row"><button type="button" className="btn primary sm" onClick={() => setCont({ text: '', provider: '' })}><Icon name="arrow-right" size={13} />Continue with these answers</button></div>
              if (view === 'side' || rows.length === 1) return (
                <>{continueRow}<div className={`pn-cols n${Math.min(rows.length, 4)}`}>
                  {rows.map(r => <SeatCard key={r.s.id} wide={rows.length === 1} seat={r.s} text={r.text} live={r.live} current={current} onOpen={() => openThread(r.s.thread_id)} />)}
                </div></>
              )
              return (
                <div className="pn-read" onKeyDown={e => { if (e.key === 'ArrowRight') { e.preventDefault(); step(1) } else if (e.key === 'ArrowLeft') { e.preventDefault(); step(-1) } }}>
                  {continueRow}
                  <div className="pn-tiles" role="tablist" aria-label="Participants">
                    {rows.map(r => <SeatTile key={r.s.id} seat={r.s} text={r.text} live={r.live} current={current} on={r.s.id === cur.s.id} onPick={() => setSel(r.s.id)} />)}
                  </div>
                  {cur && <SeatCard wide key={cur.s.id} seat={cur.s} text={cur.text} live={cur.live} current={current} onOpen={() => openThread(cur.s.thread_id)} onStep={rows.length > 1 ? step : undefined} place={`${at + 1} / ${rows.length}`} />}
                </div>
              )
            })()}
          </div>
          {versioned && wide && <div className="pn-resize" role="separator" aria-orientation="vertical" aria-label="Chat width" tabIndex={0} title="Drag to change the chat’s width (double-click: back to normal)"
            onPointerDown={drag} onDoubleClick={() => { setDockW(400); localStorage.setItem('pai-panel-chat-w', '400') }}
            onKeyDown={e => { if (e.key === 'ArrowLeft') { e.preventDefault(); nudge(24) } else if (e.key === 'ArrowRight') { e.preventDefault(); nudge(-24) } }} />}
          {versioned && wide && <aside className="pn-dock" aria-label="Talk to the editor"><EditorChat p={p} onChange={setP} onOpenThread={openThread} onHide={() => showDock(false)} /></aside>}
        </div>
      </div>
      {ren && <RenamePanel panel={p} onClose={() => setRen(false)} onDone={np => { setRen(false); setP(np) }} />}
      {del && <Confirm title="Delete this panel?" message="The participants’ conversations are deleted with it." onCancel={() => setDel(false)}
        onConfirm={async () => { setDel(false); try { await api.del(`${V1}/panels/${p.id}`); const to = pathAfterDelete(panelTab(p.id)); forgetPanelTab(p.id); navigate(to === '/' ? '/panels' : to); toast('Panel deleted') } catch (e) { toast(errMsg(e), 'err') } }} />}
      {cont && <Modal title="Continue with this" onClose={() => setCont(null)} footer={<>
        <button type="button" className="btn" onClick={() => setCont(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={busy === 'cont'} onClick={goOn}>{busy === 'cont' && <Spinner />}Start the conversation</button>
      </>}>
        <div className="stack">
          <p className="muted small" style={{ margin: 0 }}>A normal conversation starts with the question and this outcome in it, so one agent can take it from here.</p>
          <textarea {...noGrammarly} autoFocus rows={4} value={cont.text} onChange={e => setCont({ ...cont, text: e.target.value })} placeholder="What next? e.g. “Turn this into a step-by-step plan” (optional)" />
          <Select label="Agent" value={cont.provider} options={provOpts} onChange={v => setCont({ ...cont, provider: v })} searchable={false} />
        </div>
      </Modal>}
    </div>
  )
}
