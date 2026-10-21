// The bar under a working thread: who is working, for how long, sub-agents
// on the go, and a fork button that opens the fork in a new tab.
import { useState } from 'react'
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
  return async () => {
    const w = window.open('about:blank', '_blank')
    try {
      const n = await api.post<Thread>(`${V1}/threads/${threadId}/fork`)
      upsertThread(n)
      if (w) w.location.href = `/t/${n.id}`
      else window.location.href = `/t/${n.id}`
    } catch (e) { w?.close(); toast(errMsg(e), 'err') }
  }
}

export function RunBar({ t, items }: { t: Thread; items: Item[] }) {
  const fork = useForkNewTab(t.id)
  const now = useNow(1000)
  const [open, setOpen] = useState(false)
  // live sub-agents reported by the CLI; running Agent tool calls as a fallback
  const subs = t.subagents?.length ? t.subagents.map(s => ({ key: s.id, title: s.description, activity: s.activity, since: s.started_at }))
    : items.filter(it => it.kind === 'tool' && it.tool?.kind === 'task' && it.tool.status === 'running').map(it => ({ key: it.key, title: (it.tool?.title || 'Agent').replace(/^Agent:\s*/, ''), activity: '', since: '' }))
  const tools = items.filter(it => it.kind === 'tool' && it.tool?.status === 'running' && it.tool.kind !== 'task')
  const waiting = t.status === 'waiting' || t.pending > 0
  const turnOver = t.status !== 'running' && !waiting
  const since = t.prompt_at || t.activity_at

  return (
    <div className="run-bar">
      <div className="run-bar-row">
        <RunDots waiting={waiting} size={14} />
        <span className="run-bar-what">{waiting ? 'Waiting for you' : turnOver ? 'Sub-agents working' : t.external ? 'Working elsewhere' : tools[0]?.tool?.title ? <span className="ellipsis">{tools[0].tool.title}</span> : 'Working'}</span>
        <span className="grow" />
        {t.provider_session_id && <button type="button" className="btn ghost icon-only xs" onClick={fork} title="Fork into a new tab" aria-label="Fork into a new tab"><Icon name="fork" size={15} /></button>}
        <span className="run-time tnum" title={since ? `Since ${new Date(since).toLocaleTimeString()}` : ''}>{elapsed(since, now)}</span>
      </div>
      {subs.length > 0 && (
        <>
          <button type="button" className={`sub-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open}>
            <RunDots size={10} />{subs.length === 1 ? '1 subagent' : `${subs.length} subagents`}<Icon name="chevron-down" size={12} />
          </button>
          {open && (
            <ul className="sub-list">
              {subs.map(s => (
                <li key={s.key}><RunDots size={10} /><span className="sub-what"><b className="ellipsis">{s.title}</b>{s.activity && s.activity !== s.title && <span className="ellipsis muted">{s.activity}</span>}</span>
                  {s.since && <span className="run-time tnum">{elapsed(s.since, now)}</span>}</li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
