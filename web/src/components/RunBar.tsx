// The bar under a working thread: who is working, for how long, sub-agents
// on the go, and a fork button that opens the fork in a new tab.
import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Item, Thread } from '../lib/types'
import { Icon } from './icons'
import { RunDots, elapsed, useNow } from './RunningList'
import { errMsg, useToast } from './ui'

// Fork the thread and open the copy in a new tab. window.open has to happen
// in the click, before the request, or Safari blocks it.
export function useForkNewTab(threadId: string) {
  const { upsertThread } = useApp()
  const { toast } = useToast()
  // after = a turn footer's key: fork as the conversation was right after it.
  return async (after?: string) => {
    const w = window.open('about:blank', '_blank')
    try {
      const n = await api.post<Thread>(`${V1}/threads/${threadId}/fork`, after ? { after } : undefined)
      upsertThread(n)
      if (w) w.location.href = `/t/${n.id}`
      else window.location.href = `/t/${n.id}`
    } catch (e) { w?.close(); toast(errMsg(e), 'err') }
  }
}

const WHERE: Record<string, string> = { paseo: 'in Paseo', terminal: 'in a terminal', app: 'in another app' }

// One row whose height never changes: what is working, the sub-agents (a chip
// whose list floats above the bar, so opening it or agents coming and going
// does not move the conversation), fork, run time.
export function RunBar({ t, items }: { t: Thread; items: Item[] }) {
  const { running } = useApp()
  const fork = useForkNewTab(t.id)
  const now = useNow(1000)
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const popup = useRef<HTMLUListElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const listId = useId()
  const [position, setPosition] = useState<CSSProperties>({ visibility: 'hidden' })
  // The composer scrolls independently. Render outside it so its overflow
  // cannot clip the list, and follow its position as the keyboard/layout moves.
  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      if (!box.current) return
      const r = box.current.getBoundingClientRect()
      const vv = window.visualViewport
      const x = vv?.offsetLeft ?? 0, y = vv?.offsetTop ?? 0
      const width = Math.min(r.width, (vv?.width ?? innerWidth) - 16)
      const above = Math.max(0, r.top - y - 14)
      const below = Math.max(0, y + (vv?.height ?? innerHeight) - r.bottom - 14)
      const upwards = above >= 80 || above >= below
      setPosition({
        left: Math.max(x + 8, Math.min(r.left, x + (vv?.width ?? innerWidth) - width - 8)), width,
        ...(upwards ? { bottom: innerHeight - r.top + 6 } : { top: r.bottom + 6 }),
        maxHeight: Math.min((vv?.height ?? innerHeight) * .5, upwards ? above : below),
      })
    }
    place()
    const observer = new ResizeObserver(place)
    if (box.current) {
      observer.observe(box.current)
      const dock = box.current.closest('.thread-bottom')
      if (dock) observer.observe(dock)
      const app = box.current.closest('.app')
      if (app) observer.observe(app)
    }
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
  }, [open])
  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node) && !popup.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { setOpen(false); trigger.current?.focus({ preventScroll: true }) } }
    document.addEventListener('mousedown', h)
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('mousedown', h); document.removeEventListener('keydown', key) }
  }, [open])
  // live sub-agents reported by the CLI; running Agent tool calls as a fallback
  const subs = t.subagents?.length ? t.subagents.map(s => ({ key: s.id, title: s.description, activity: s.activity, since: s.started_at }))
    : items.filter(it => it.kind === 'tool' && it.tool?.kind === 'task' && it.tool.status === 'running').map(it => ({ key: it.key, title: (it.tool?.title || 'Agent').replace(/^Agent:\s*/, ''), activity: '', since: '' }))
  useEffect(() => { if (!subs.length) setOpen(false) }, [subs.length])
  const tools = items.filter(it => it.kind === 'tool' && it.tool?.status === 'running' && it.tool.kind !== 'task')
  const waiting = t.status === 'waiting' || t.pending > 0
  const turnOver = t.status !== 'running' && !waiting
  const since = t.prompt_at || t.activity_at
  const origin = running.find(r => r.session_id === t.provider_session_id)?.origin || ''
  const what = waiting ? 'Waiting for you' : turnOver ? 'Sub-agents working' : t.external ? `Working ${WHERE[origin] || 'elsewhere'}` : tools[0]?.tool?.title || 'Working'

  return (
    <div className="run-bar" ref={box}>
      {open && subs.length > 0 && createPortal(
        <ul ref={popup} id={listId} className="sub-pop" style={position} aria-label="Sub-agents" tabIndex={0}>
          {subs.map((s, i) => (
            <li key={s.key}><span className="sub-n tnum">{i + 1}</span><span className="sub-what"><b className="ellipsis">{s.title}</b>{s.activity && s.activity !== s.title && <span className="ellipsis muted">{s.activity}</span>}</span>
              {s.since && <span className="run-time tnum">{elapsed(s.since, now)}</span>}</li>
          ))}
        </ul>, document.body
      )}
      <div className="run-bar-row">
        <RunDots waiting={waiting} size={14} />
        <span className="run-bar-what ellipsis">{what}</span>
        {subs.length > 0 && (
          <button ref={trigger} type="button" className={`sub-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open} aria-controls={open ? listId : undefined}>
            {subs.length === 1 ? '1 sub-agent' : `${subs.length} sub-agents`}<Icon name="chevron-down" size={12} />
          </button>
        )}
        <span className="grow" />
        {t.provider_session_id && <button type="button" className="btn ghost icon-only xs" onClick={() => fork()} title="Fork into a new tab" aria-label="Fork into a new tab"><Icon name="fork" size={15} /></button>}
        <span className="run-time tnum" title={since ? `Since ${new Date(since).toLocaleTimeString()}` : ''}>{elapsed(since, now)}</span>
      </div>
    </div>
  )
}
