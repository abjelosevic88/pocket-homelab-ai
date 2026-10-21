// Messages waiting for the agent to finish, above the reply box: what will go
// out next, with "Send now" (stop the current turn and send this one) and ✕.
import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Item } from '../lib/types'
import { Icon } from './icons'
import { errMsg, useToast } from './ui'

export function QueueStrip({ threadId, queued, agent, onClearAll }: { threadId: string; queued: Item[]; agent: string; onClearAll?: () => void }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState('')
  if (!queued.length) return null
  const act = async (it: Item, now: boolean) => {
    setBusy(it.key)
    try {
      if (now) await api.post(`${V1}/threads/${threadId}/queue/${encodeURIComponent(it.key)}/now`)
      else await api.del(`${V1}/threads/${threadId}/queue/${encodeURIComponent(it.key)}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  return (
    <div className="queue-strip" aria-label="Queued messages">
      <div className="queue-h"><Icon name="clock" size={12} /><span className="grow">{queued.length === 1 ? '1 message waits' : `${queued.length} messages wait`} — {queued.length === 1 ? 'it goes' : 'they go'} out when {agent} is done</span>
        {queued.length > 1 && onClearAll && <button type="button" className="link-btn small" onClick={onClearAll}>Clear all</button>}</div>
      {queued.map((it, i) => (
        <div key={it.key} className="queue-row">
          <span className="queue-n tnum">{i + 1}</span>
          <span className="queue-text">{it.images?.length ? `🖼 ${it.images.length} · ` : ''}{it.text || '(image)'}</span>
          <button type="button" className="btn sm" disabled={!!busy} onClick={() => act(it, true)} title="Stop what the agent is doing and send this now">Send now</button>
          <button type="button" className="btn ghost icon-only xs" disabled={!!busy} onClick={() => act(it, false)} aria-label="Remove from the queue" title="Remove from the queue"><Icon name="x" size={13} /></button>
        </div>
      ))}
    </div>
  )
}
