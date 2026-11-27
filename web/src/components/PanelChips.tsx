// Panels in a conversation: the chip for the panels started from it, and the
// link from a participant's conversation back to its panel.
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { PANEL_EVENT, type Panel } from '../lib/panels'
import type { Item, Thread } from '../lib/types'
import { Icon } from './icons'
import { Spinner } from './ui'

/** "Part of a panel" in the header of a participant's conversation. */
export function PanelParentChip({ t }: { t: Thread }) {
  const navigate = useNavigate()
  if (t.source !== 'panel' || !t.source_ref) return null
  return (
    <button type="button" className="agent-parent-chip" onClick={() => navigate(`/panels/${t.source_ref}`)} title="This conversation is one participant of a panel — open the panel">
      <Icon name="panel" size={12} /><span className="ellipsis">Part of a panel</span>
    </button>
  )
}

/** The chip above the composer: the panels started from this conversation. */
export function PanelsChip({ t }: { t: Thread }) {
  const navigate = useNavigate()
  const [list, setList] = useState<Panel[]>([])
  const load = useCallback(() => { api.get<Panel[]>(`${V1}/threads/${t.id}/panels`).then(l => setList(l || [])).catch(() => {}) }, [t.id])
  useEffect(() => { setList([]); load() }, [load])
  useEffect(() => {
    const on = (e: Event) => { if ((e as CustomEvent).detail?.from_thread_id === t.id) load() }
    window.addEventListener(PANEL_EVENT, on)
    return () => window.removeEventListener(PANEL_EVENT, on)
  }, [t.id, load])
  if (!list.length) return null
  const running = list.filter(p => p.status === 'running')
  const lead = running[0] || list[0]
  const step = lead.status === 'running' ? lead.spec.stages[lead.stage]?.name : ''
  return (
    <button type="button" className="sub-chip" onClick={() => navigate(`/panels/${lead.id}`)} title={list.length > 1 ? `${list.length} panels were started from this conversation — this opens the latest` : 'Open the panel started from this conversation'}>
      {running.length > 0 ? <Spinner size={11} /> : <Icon name="panel" size={13} />}
      {lead.spec.kind_name}{step ? <span className="muted"> · {step}</span> : lead.status === 'done' ? <span className="muted"> · done</span> : lead.status !== 'running' ? <span className="muted"> · did not finish</span> : null}
      {list.length > 1 && <span className="muted"> · {list.length}</span>}
    </button>
  )
}

/** What a second opinion on a reply is handed: what was asked, and the answer. */
export function reviewMaterial(items: Item[], turnKey: string, label = 'The answer under review'): string {
  const end = items.findIndex(i => i.key === turnKey)
  if (end < 0) return ''
  let start = end
  while (start > 0 && items[start - 1].kind !== 'user') start--
  const asked = start > 0 ? (items[start - 1].text || '').trim() : ''
  const said = items.slice(start, end).filter(i => i.kind === 'assistant' && i.text).map(i => i.text!.trim()).join('\n\n')
  return (asked && !asked.startsWith('<') ? `What was asked:\n\n${asked}\n\n---\n\n` : '') + `${label}:\n\n${said}`
}
