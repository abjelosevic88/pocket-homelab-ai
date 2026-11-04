// Agents that agents started (pocket_agent_start): each is a conversation of
// its own (source "agent", source_ref = the one that started it). The parent
// shows a chip listing them; the child links back to who started it.
import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { threadTitle } from '../lib/format'
import type { Thread } from '../lib/types'
import { ProviderBadge, ThreadStatus } from './bits'
import { Icon } from './icons'
import { useFloatAbove } from './Subagents'

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
