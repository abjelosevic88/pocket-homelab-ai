// The sub-agents of a thread, like Paseo: a chip above the composer lists every
// sub-agent the session started (working or finished); a row opens what it did
// and where it is, live while it works.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtDuration, fmtTokens } from '../lib/format'
import type { Item, SubagentInfo, Thread } from '../lib/types'
import { Icon } from './icons'
import { elapsed, useNow } from './RunningList'
import { Timeline } from './Timeline'
import { errMsg, Modal, Spinner } from './ui'

const HIDDEN = 'pai-subs-hidden'
const hiddenOf = (threadId: string): string[] => { try { return JSON.parse(localStorage.getItem(HIDDEN) || '{}')[threadId] || [] } catch { return [] } }
function setHidden(threadId: string, ids: string[]) {
  let all: Record<string, string[]> = {}
  try { all = JSON.parse(localStorage.getItem(HIDDEN) || '{}') } catch { /* start over */ }
  if (ids.length) all[threadId] = ids; else delete all[threadId]
  localStorage.setItem(HIDDEN, JSON.stringify(all))
}

/** "claude-opus-5-5" → "Opus 5.5" */
export const modelShort = (m?: string) => {
  const x = /^claude-([a-z]+)-(\d+)-(\d+)/.exec(m || '')
  return x ? `${x[1][0].toUpperCase()}${x[1].slice(1)} ${x[2]}.${x[3]}` : m || ''
}

function took(s: SubagentInfo, now: number) {
  if (s.status === 'running') return elapsed(s.started_at, now)
  const a = Date.parse(s.started_at || ''), b = Date.parse(s.ended_at || '')
  return a && b && b >= a ? fmtDuration(b - a) : ''
}
const meta = (s: SubagentInfo, now: number) => [s.type, modelShort(s.model), s.tokens ? `${fmtTokens(s.tokens)} tokens` : '', took(s, now)].filter(Boolean).join(' · ')

function StatusIcon({ s }: { s: SubagentInfo }) {
  if (s.status === 'running') return <span className="sub-st running" title="Working"><span className="spinner" /></span>
  if (s.status === 'done') return <span className="sub-st done" title="Finished"><Icon name="check" size={13} /></span>
  return <span className="sub-st stopped" title="Stopped without finishing"><Icon name="x" size={12} /></span>
}

/** Every sub-agent of the thread's session, refreshed while any of them works. */
function useSubagentList(t: Thread) {
  const [list, setList] = useState<SubagentInfo[]>([])
  const can = t.provider === 'claude' && !!t.provider_session_id
  const load = useCallback(() => {
    if (!can) { setList([]); return }
    api.get<{ subagents: SubagentInfo[] }>(`${V1}/threads/${t.id}/subagents`).then(r => setList(r.subagents || [])).catch(() => {})
  }, [t.id, can])
  useEffect(() => { setList([]) }, [t.id])
  const live = (t.subagents || []).map(s => s.id).join()
  useEffect(() => { load() }, [load, live, t.status, t.provider_session_id])
  const working = list.some(s => s.status === 'running') || !!t.subagents?.length
  useEffect(() => {
    if (!working) return
    const iv = setInterval(load, 4000)
    return () => clearInterval(iv)
  }, [working, load])
  // the CLI reports a new one before its transcript exists: show it already
  const fresh: SubagentInfo[] = (t.subagents || []).filter(l => !list.some(s => s.id === l.id || (s.status === 'running' && s.description === l.description)))
    .map(l => ({ id: l.id, description: l.description, type: l.type, status: 'running', started_at: l.started_at, activity: l.activity, tool_uses: l.tool_uses }))
  return [...list, ...fresh]
}

/** The chip, its list and the panel of the sub-agent you open. In the run bar
 * while the agent works, on its own above the composer afterwards. */
export function SubagentsChip({ t }: { t: Thread }) {
  const all = useSubagentList(t)
  const [hidden, setHiddenState] = useState<string[]>(() => hiddenOf(t.id))
  useEffect(() => setHiddenState(hiddenOf(t.id)), [t.id])
  const shown = all.filter(s => s.status === 'running' || !hidden.includes(s.id))
  const working = shown.filter(s => s.status === 'running').length
  const finished = shown.filter(s => s.status !== 'running')
  const [open, setOpen] = useState(false)
  const [panel, setPanel] = useState<string | null>(null)
  const now = useNow(1000)
  const box = useRef<HTMLSpanElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const position = useFloatAbove(open, box)
  useEffect(() => { if (!shown.length) setOpen(false) }, [shown.length])
  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node) && !pop.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', h)
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('mousedown', h); document.removeEventListener('keydown', key) }
  }, [open])
  if (!shown.length) return null
  const hideFinished = () => { const ids = [...new Set([...hidden, ...finished.map(s => s.id)])]; setHidden(t.id, ids); setHiddenState(ids) }
  const current = all.find(s => s.id === panel)

  return (
    <span ref={box} className="sub-anchor">
      <button type="button" className={`sub-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open} aria-haspopup="dialog">
        {shown.length === 1 ? '1 sub-agent' : `${shown.length} sub-agents`}{working > 0 && working < shown.length && <span className="muted"> · {working} working</span>}
        <Icon name="chevron-down" size={12} />
      </button>
      {open && createPortal(
        <div ref={pop} className="sub-pop sub-list" style={position} role="dialog" aria-label="Sub-agents">
          {finished.length > 0 && <>
            <button type="button" className="sub-act" onClick={hideFinished}><Icon name="archive" size={15} />Hide finished sub-agents</button>
            <div className="sub-sep" />
          </>}
          {shown.map(s => (
            <button key={s.id} type="button" className="sub-row" onClick={() => { setOpen(false); setPanel(s.id) }}>
              <StatusIcon s={s} />
              <span className="sub-what"><b className="ellipsis">{s.description}</b><span className="ellipsis">{meta(s, now)}</span>
                {s.status === 'running' && s.activity && <span className="ellipsis sub-doing">{s.activity}</span>}</span>
            </button>
          ))}
        </div>, document.body)}
      {panel && <SubagentPanel threadId={t.id} id={panel} initial={current} onClose={() => setPanel(null)} />}
    </span>
  )
}

/** One sub-agent: its state and everything it did, refreshed while it works. */
function SubagentPanel({ threadId, id, initial, onClose }: { threadId: string; id: string; initial?: SubagentInfo; onClose: () => void }) {
  const { settings } = useApp()
  const [info, setInfo] = useState<SubagentInfo | undefined>(initial)
  const [items, setItems] = useState<Item[] | null>(null)
  const [error, setError] = useState('')
  const now = useNow(1000)
  const body = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const load = useCallback(() => {
    api.get<{ subagent: SubagentInfo; items: Item[] }>(`${V1}/threads/${threadId}/subagents/${encodeURIComponent(id)}`)
      .then(r => { setInfo(r.subagent); setItems(r.items); setError('') })
      .catch(e => setError(errMsg(e)))
  }, [threadId, id])
  useEffect(() => { load() }, [load])
  const running = (info?.status ?? 'running') === 'running'
  useEffect(() => {
    if (!running) return
    const iv = setInterval(load, 2500)
    return () => clearInterval(iv)
  }, [running, load])
  // follow the end while it grows, unless you scrolled up to read
  useLayoutEffect(() => {
    const scroller = body.current?.closest('.modal-body') as HTMLElement | null
    if (scroller && stick.current) scroller.scrollTop = scroller.scrollHeight
  }, [items])
  useEffect(() => {
    const scroller = body.current?.closest('.modal-body') as HTMLElement | null
    if (!scroller) return
    const h = () => { stick.current = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80 }
    scroller.addEventListener('scroll', h)
    return () => scroller.removeEventListener('scroll', h)
  }, [items === null]) // eslint-disable-line react-hooks/exhaustive-deps

  const state = !info ? '' : info.status === 'running' ? 'Working' : info.status === 'done' ? 'Finished' : 'Stopped without finishing'
  return (
    // the state sits in the header, so it stays in view while the transcript follows its end
    <Modal size="xl" className="sub-panel" onClose={onClose} title={<span className="sub-panel-head">
      <span className="sub-panel-title">{info && <StatusIcon s={info} />}<span className="ellipsis">{info?.description || 'Sub-agent'}</span></span>
      {info && <span className="sub-panel-meta">
        <span className={`badge ${info.status === 'done' ? 'ok' : info.status === 'running' ? 'accent-soft' : 'warn'}`}>{state}</span>
        <span className="muted small">{[meta(info, now), info.tool_uses ? `${info.tool_uses} tool call${info.tool_uses === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')}</span>
      </span>}
    </span>}>
      <div ref={body} className="stack">
        {error && <div className="callout warn"><Icon name="alert" size={15} /><span>{error}</span></div>}
        {items === null && !error && <div className="sub-loading"><Spinner /></div>}
        {items && items.length === 0 && <p className="muted">It has not written anything yet.</p>}
        {items && items.length > 0 && <div className="timeline sub-timeline"><Timeline items={items} showThinking={!!settings?.show_thinking} /></div>}
        {running && items && items.length > 0 && <div className="sub-live muted small"><span className="spinner" />Working — this updates by itself</div>}
      </div>
    </Modal>
  )
}

/** Fixed position for a list that floats above the composer area, kept in
 * place as the keyboard and the layout move. */
export function useFloatAbove(open: boolean, anchor: React.RefObject<HTMLElement | null>) {
  const [position, setPosition] = useState<CSSProperties>({ visibility: 'hidden' })
  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const el = anchor.current?.closest('.thread-bottom-inner') || anchor.current
      if (!el) return
      const r = el.getBoundingClientRect()
      const vv = window.visualViewport
      const x = vv?.offsetLeft ?? 0, y = vv?.offsetTop ?? 0
      const width = Math.min(r.width, 520, (vv?.width ?? innerWidth) - 16)
      const top = anchor.current?.getBoundingClientRect().top ?? r.top
      const above = Math.max(0, top - y - 14)
      setPosition({
        left: Math.max(x + 8, Math.min(r.left, x + (vv?.width ?? innerWidth) - width - 8)), width,
        bottom: innerHeight - top + 6, maxHeight: Math.min((vv?.height ?? innerHeight) * .6, above),
      })
    }
    place()
    const observer = new ResizeObserver(place)
    const dock = anchor.current?.closest('.thread-bottom')
    if (dock) observer.observe(dock)
    window.addEventListener('resize', place)
    document.addEventListener('scroll', place, true)
    window.visualViewport?.addEventListener('resize', place)
    window.visualViewport?.addEventListener('scroll', place)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', place)
      document.removeEventListener('scroll', place, true)
      window.visualViewport?.removeEventListener('resize', place)
      window.visualViewport?.removeEventListener('scroll', place)
    }
  }, [open, anchor])
  return position
}
