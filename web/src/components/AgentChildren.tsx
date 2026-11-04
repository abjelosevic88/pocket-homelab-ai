// Agents that agents started (pocket_agent_start): each is a conversation of
// its own (source "agent", source_ref = the one that started it). The parent
// shows a chip listing them; the child links back to who started it.
import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { threadTitle } from '../lib/format'
import type { Thread } from '../lib/types'
import { ProviderBadge, ThreadStatus } from './bits'
import { Icon } from './icons'
import { useFloatAbove } from './Subagents'
import { errMsg, useToast } from './ui'

/** The agents a thread started, oldest first (from the live thread list). */
export function useAgentChildren(id: string): Thread[] {
  const { threads } = useApp()
  return (threads || []).filter(x => x.source === 'agent' && x.source_ref === id)
    .sort((a, b) => a.created_at.localeCompare(b.created_at))
}

const working = (t: Thread) => t.status === 'running' || t.status === 'waiting' || t.pending > 0

/** "Started by …" in a started agent's header. */
export function ParentChip({ t }: { t: Thread }) {
  const { threads } = useApp()
  const navigate = useNavigate()
  if (t.source !== 'agent' || !t.source_ref) return null
  const parent = (threads || []).find(x => x.id === t.source_ref)
  return (
    <button type="button" className="agent-parent-chip" onClick={() => navigate(`/t/${t.source_ref}`)} title="Another agent started this conversation — open it">
      <Icon name="bot" size={12} /><span className="ellipsis">Started by {parent ? threadTitle(parent) : 'an agent'}</span>
    </button>
  )
}

/** The chip above the composer: the agents this conversation started. */
export function AgentsChip({ t }: { t: Thread }) {
  const kids = useAgentChildren(t.id)
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLSpanElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const position = useFloatAbove(open, box)
  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node) && !pop.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', h)
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('mousedown', h); document.removeEventListener('keydown', key) }
  }, [open])
  if (!kids.length) return null
  const busy = kids.filter(working).length
  const needYou = kids.filter(k => k.status === 'waiting' || k.pending > 0).length
  return (
    <span ref={box} className="sub-anchor">
      <button type="button" className={`sub-chip agents-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open} aria-haspopup="dialog" title="Agents this conversation started">
        <Icon name="bot" size={13} />{kids.length === 1 ? '1 agent' : `${kids.length} agents`}
        {needYou > 0 ? <span className="agents-need"> · {needYou} need{needYou === 1 ? 's' : ''} you</span> : busy > 0 && <span className="muted"> · {busy} working</span>}
        <Icon name="chevron-down" size={12} />
      </button>
      {open && createPortal(
        <div ref={pop} className="sub-pop sub-list" style={position} role="dialog" aria-label="Agents this conversation started">
          {kids.map(k => (
            <button key={k.id} type="button" className="sub-row" onClick={() => { setOpen(false); navigate(`/t/${k.id}`) }}>
              <span className="sub-st"><ThreadStatus t={k} />{!working(k) && k.status !== 'error' && !k.unread && <Icon name="check" size={13} />}</span>
              <span className="sub-what"><b className="ellipsis">{threadTitle(k)}</b>
                <span className="ellipsis agents-meta"><ProviderBadge provider={k.provider} size="xs" />{k.preview || k.last_prompt}</span></span>
            </button>
          ))}
        </div>, document.body)}
    </span>
  )
}

type Heartbeat = { id: number; prompt: string; spec: string; next_at: string; last_at: string; runs: number; max_runs: number; ends_at: string }
const when = (iso: string) => iso ? new Date(iso).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' }) : ''

/** The heartbeats an agent set on this conversation (pocket_agent_heartbeat_create): a chip, its list, remove. */
export function HeartbeatsChip({ t, rev = 0, owner }: { t: Thread; rev?: number; owner: boolean }) {
  const [list, setList] = useState<Heartbeat[]>([])
  const [open, setOpen] = useState(false)
  const { toast } = useToast()
  const box = useRef<HTMLSpanElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const position = useFloatAbove(open, box)
  const load = useCallback(() => {
    api.get<{ heartbeats: Heartbeat[] }>(`${V1}/threads/${t.id}/heartbeats`).then(r => setList(r.heartbeats || [])).catch(() => {})
  }, [t.id])
  useEffect(() => { load() }, [load, rev, t.status])
  useEffect(() => { if (!list.length) setOpen(false) }, [list.length])
  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node) && !pop.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [open])
  if (!list.length) return null
  const remove = async (h: Heartbeat) => {
    try { await api.del(`${V1}/threads/${t.id}/heartbeats/${h.id}`); setList(l => l.filter(x => x.id !== h.id)); toast('Heartbeat removed') } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <span ref={box} className="sub-anchor">
      <button type="button" className={`sub-chip agents-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open} aria-haspopup="dialog" title="Prompts that come back to this conversation on a schedule">
        <Icon name="activity" size={13} />{list.length === 1 ? `Heartbeat · ${list[0].spec}` : `${list.length} heartbeats`}
        <Icon name="chevron-down" size={12} />
      </button>
      {open && createPortal(
        <div ref={pop} className="sub-pop sub-list" style={position} role="dialog" aria-label="Heartbeats">
          {list.map(h => (
            <div key={h.id} className="sub-row hb-row">
              <span className="sub-st"><Icon name="activity" size={13} /></span>
              <span className="sub-what"><b className="ellipsis">{h.spec} · next {when(h.next_at)}</b>
                <span className="ellipsis">{[h.runs ? `${h.runs}${h.max_runs ? ` of ${h.max_runs}` : ''} sent` : '', h.ends_at ? `ends ${when(h.ends_at)}` : 'no end', h.prompt].filter(Boolean).join(' · ')}</span></span>
              {owner && <button type="button" className="btn ghost xs" onClick={() => void remove(h)} title="Remove this heartbeat">Remove</button>}
            </div>
          ))}
        </div>, document.body)}
    </span>
  )
}
