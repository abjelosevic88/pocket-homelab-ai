// Background work: shell commands and monitors an agent left running, and
// sub-agents still at it after the turn. Calmer than "working" — a slow orbit
// in its own colour — so "busy right now" and "something is still going"
// read differently at a glance.
import type { BackgroundTask, Thread } from '../lib/types'

/** A dot circling slowly: background work, not the agent working. */
export function BgMark({ size = 14 }: { size?: number }) {
  return <span className="bg-mark" style={{ width: size, height: size }} aria-hidden="true" />
}

const KIND: Record<BackgroundTask['kind'], string> = {
  shell: 'Command',
  monitor: 'Watching',
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
  return ['Still running in the background:', ...(t.background || []).map(b => `• ${b.description || KIND[b.kind]}`),
    ...(subs ? [`• ${subs === 1 ? '1 sub-agent' : `${subs} sub-agents`}`] : [])].join('\n')
}
