// Background work: shell commands and monitors an agent left running, and
// sub-agents still at it after the turn. Calmer than "working" — a slow orbit
// in its own colour — so "busy right now" and "something is still going"
// read differently at a glance.
import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { BackgroundTask, Thread } from '../lib/types'
import { Icon, type IconName } from './icons'
import { elapsed, useNow } from './RunningList'
import { useFloatAbove } from './Subagents'

/** A dot circling slowly: background work, not the agent working. */
export function BgMark({ size = 14 }: { size?: number }) {
  return <span className="bg-mark" style={{ width: size, height: size }} aria-hidden="true" />
}

const KIND: Record<BackgroundTask['kind'], { icon: IconName; word: string }> = {
  shell: { icon: 'terminal', word: 'Command' },
  monitor: { icon: 'eye', word: 'Watching' },
}

/** Sub-agents or background tasks still going. */
export const hasBackground = (t: Thread) => !!t.background?.length || !!t.subagents?.length

/** When the oldest piece of background work started. */
export function backgroundSince(t: Thread) {
  return [...(t.background || []).map(b => b.started_at), ...(t.subagents || []).map(s => s.started_at)].filter(Boolean).sort()[0]
}

/** "Build the ISO" · "2 commands, 1 monitor" · "Sub-agents still working" */
export function backgroundLabel(t: Thread) {
  const tasks = t.background || []
  if (!tasks.length) return 'Sub-agents still working'
  if (tasks.length === 1) return tasks[0].description || (tasks[0].kind === 'monitor' ? 'Watching for something' : 'A command')
  const shells = tasks.filter(x => x.kind === 'shell').length, mons = tasks.length - shells
  return [shells && (shells === 1 ? '1 command' : `${shells} commands`), mons && (mons === 1 ? '1 monitor' : `${mons} monitors`)].filter(Boolean).join(', ')
}

/** Everything a thread runs in the background, for a tooltip. */
export function backgroundTitle(t: Thread) {
  const subs = t.subagents?.length || 0
  return ['Still running in the background:', ...(t.background || []).map(b => `• ${b.description || KIND[b.kind]?.word}`),
    ...(subs ? [`• ${subs === 1 ? '1 sub-agent' : `${subs} sub-agents`}`] : [])].join('\n')
}

/** The chip listing background commands and monitors, with how long each has run. */
export function BackgroundChip({ t, label }: { t: Thread; label?: string }) {
  const tasks = t.background || []
  const [open, setOpen] = useState(false)
  const now = useNow(1000)
  const box = useRef<HTMLSpanElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const position = useFloatAbove(open, box)
  useEffect(() => { if (!tasks.length) setOpen(false) }, [tasks.length])
  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node) && !pop.current?.contains(e.target as Node)) setOpen(false) }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', h)
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('mousedown', h); document.removeEventListener('keydown', key) }
  }, [open])
  if (!tasks.length) return null

  return (
    <span ref={box} className="sub-anchor">
      <button type="button" className={`sub-chip bg-chip ${open ? 'open' : ''}`} onClick={() => setOpen(o => !o)} aria-expanded={open} aria-haspopup="dialog">
        {label || `${tasks.length} in the background`}
        <Icon name="chevron-down" size={12} />
      </button>
      {open && createPortal(
        <div ref={pop} className="sub-pop sub-list" style={position} role="dialog" aria-label="Background work">
          <div className="bg-pop-h">Still running when the reply ends. The conversation stays open until these finish.</div>
          {tasks.map(b => (
            <div key={b.id} className="sub-row bg-row">
              <span className="bg-ico"><Icon name={KIND[b.kind]?.icon || 'terminal'} size={12} /></span>
              <span className="sub-what"><b className="ellipsis">{b.description || KIND[b.kind]?.word}</b>
                <span className="ellipsis">{[KIND[b.kind]?.word, elapsed(b.started_at, now)].filter(Boolean).join(' · ')}</span></span>
            </div>
          ))}
        </div>, document.body)}
    </span>
  )
}
