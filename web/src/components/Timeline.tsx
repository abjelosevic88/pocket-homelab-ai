// A thread's items, in order: what you said, what the agent said and did.
import { useApp } from '../lib/app-context'
import { memo, useRef, useState } from 'react'
import { fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import { V1 } from '../lib/api'
import type { Item, ItemImage, Todo, ToolInfo, TurnInfo } from '../lib/types'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { RequestSummary } from './RequestCard'
import { ToolRow } from './ToolRow'
import { ToolActivity } from './ToolActivity'
import { groupToolActivity } from '../lib/tool-activity'
import { Modal } from './ui'

export function Images({ images }: { images: ItemImage[] }) {
  const [big, setBig] = useState<ItemImage | null>(null)
  const [gone, setGone] = useState<Set<string>>(new Set())
  const shown = images.filter(im => !gone.has(im.id))
  if (!shown.length) return null
  return (
    <>
      <div className="msg-imgs">
        {shown.map(im => (
          <button key={im.id} type="button" className="msg-img" onClick={() => setBig(im)} aria-label={`Open ${im.name}`}>
            <img src={im.url} alt={im.name} loading="lazy" onError={() => setGone(g => new Set(g).add(im.id))} />
          </button>
        ))}
      </div>
      {big && (
        <Modal onClose={() => setBig(null)} size="xl" bare className="lightbox">
          <button type="button" className="lightbox-x btn icon-only" aria-label="Close" onClick={() => setBig(null)}><Icon name="x" size={18} /></button>
          <img src={big.url} alt={big.name} />
        </Modal>
      )}
    </>
  )
}

function Thinking({ text, defaultOpen }: { text: string; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  if (!text.trim()) return null
  return (
    <div className={`thinking ${open ? 'open' : ''}`}>
      <button type="button" className="thinking-h" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <Icon name="brain" size={13} /><span>Thinking</span><Icon name="chevron-down" size={13} className="tool-caret" />
      </button>
      {open && <div className="thinking-body">{text}</div>}
    </div>
  )
}

function Todos({ todos }: { todos: Todo[] }) {
  const done = todos.filter(t => t.status === 'completed').length
  return (
    <div className="todos">
      <div className="todos-h"><Icon name="list-checks" size={14} /><b>Plan</b><span className="muted small">{done}/{todos.length}</span></div>
      <ul>
        {todos.map((t, i) => (
          <li key={i} className={t.status}>
            <span className="todo-mark">{t.status === 'completed' ? <Icon name="check" size={12} strokeWidth={2.4} /> : t.status === 'in_progress' ? <span className="todo-dot" /> : null}</span>
            <span>{t.text}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

// Pictures that live only in the CLI's session file (a screenshot pasted in
// Paseo, what a Read of an image returned): the server reads them from there.
function logImages(threadId: string | undefined, key: string, n: number): ItemImage[] {
  if (!threadId || n <= 0) return []
  return Array.from({ length: Math.min(n, 12) }, (_, i) => ({
    id: `${key}-${i}`, name: `image ${i + 1}`, url: `${V1}/threads/${threadId}/log-image?key=${encodeURIComponent(key)}&n=${i}`,
  }) as ItemImage)
}

// Older tool rows only have "[image]" lines in their output.
const imageCount = (t: ToolInfo) => t.images ?? (t.output || '').split('\n').filter(l => l === '[image]').length

export const ItemView = memo(function ItemView({ item, showThinking, threadId, ownerId, ownerName }: { item: Item; showThinking: boolean; threadId?: string; ownerId?: number; ownerName?: string }) {
  switch (item.kind) {
    case 'user': {
      const pics = [...(item.images || []), ...logImages(threadId, item.key, item.log_images || 0)]
      return (
        <div className={`msg user ${item.queued ? 'queued' : ''} ${item.unsent ? 'unsent' : ''}`} id={`item-${item.key}`}>
          {(item.by || ownerName) && <ByLine name={item.by || ownerName!} id={item.by ? item.by_id : ownerId} />}
          <div className="bubble">
            {pics.length > 0 && <Images images={pics} />}
            {item.files?.map(f => <a className="file-chip" key={f.id} href={f.url} download={f.name}>📄 {f.name}</a>)}
            {item.text && <div className="user-text">{item.text}</div>}
          </div>
          {item.queued && <div className="queued-tag"><Icon name="clock" size={11} />queued — sent when the agent is done</div>}
        </div>
      )
    }
    case 'assistant':
      return <div className="msg assistant" id={`item-${item.key}`}><Markdown text={item.text || ''} /></div>
    case 'thinking':
      return <Thinking text={item.text || ''} defaultOpen={showThinking} />
    case 'tool':
      if (!item.tool) return null
      return (
        <>
          <ToolRow tool={item.tool} />
          {threadId && imageCount(item.tool) > 0 && <div className="tool-imgs"><Images images={logImages(threadId, item.key, imageCount(item.tool))} /></div>}
        </>
      )
    case 'todos':
      return item.todos?.length ? <Todos todos={item.todos} /> : null
    case 'request':
      return item.request ? <RequestSummary req={item.request} /> : null
    case 'notice':
      return (
        <div className={`notice ${item.level || 'info'}`}>
          <Icon name={item.level === 'error' ? 'alert' : item.level === 'warn' ? 'alert' : 'info'} size={14} />
          <span>{item.text}</span>
        </div>
      )
    case 'turn':
      return item.turn ? <TurnFoot t={item.turn} /> : null
    default:
      return item.text ? <div className="notice info"><span>{item.text}</span></div> : null
  }
})

// How long the turn took (and what it cost), under the agent's reply. The
// last one also carries the fork button.
function TurnFoot({ t, onFork }: { t: TurnInfo; onFork?: () => void }) {
  const bits = []
  if (t.cost_usd) bits.push(fmtCost(t.cost_usd))
  if (t.input_tokens || t.output_tokens) bits.push(`${fmtTokens(t.input_tokens + t.output_tokens)} tokens`)
  return (
    <div className={`turn-foot ${t.ok ? '' : 'err'}`}>
      {!t.ok && <span className="turn-err"><Icon name="alert" size={12} /><span className="ellipsis">{t.error || 'The turn failed'}</span></span>}
      <span className="grow" />
      {bits.length > 0 && <span className="tnum turn-bits">{bits.join(' · ')}</span>}
      {onFork && <button type="button" className="btn ghost icon-only xs" onClick={onFork} title="Fork into a new tab" aria-label="Fork into a new tab"><Icon name="fork" size={14} /></button>}
      <span className="run-time tnum" title="How long the agent worked on this"><Icon name="clock" size={12} />{fmtDuration(t.duration_ms)}</span>
    </div>
  )
}

export function Timeline({ items, showThinking, streamingKey, onFork, threadId, onResend, onDropUnsent, owner }: { items: Item[]; showThinking: boolean; streamingKey?: string; onFork?: () => void; threadId?: string; onResend?: (it: Item) => void; onDropUnsent?: (it: Item) => void; owner?: Owner }) {
  // Items there when the thread opened render still; anything that arrives
  // while you watch slides in.
  const initial = useRef<Set<string> | null>(null)
  if (initial.current === null) initial.current = new Set(items.map(i => i.key))
  // A turn footer straight after another is the same turn read twice (Pocket
  // AI's own footer and the one from the CLI's log): keep the first.
  const shown = items.filter((it, i) => !(it.kind === 'turn' && items[i - 1]?.kind === 'turn'))
  let lastTurn = ''
  for (let i = shown.length - 1; i >= 0; i--) {
    if (shown[i].kind === 'turn') { lastTurn = shown[i].key; break }
    if (shown[i].kind === 'user') break
  }
  return (
    <div className="timeline">
      {groupToolActivity(shown).map(entry => {
        const it = entry.type === 'item' ? entry.item : entry.items[0]
        return (
        <div key={entry.key} className={`tl-item ${initial.current!.has(it.key) ? '' : 'appear'} ${it.key === streamingKey ? 'streaming' : ''}`}>
          {entry.type === 'tools' ? <ToolActivity items={entry.items} /> : it.kind === 'turn' && it.turn ? <TurnFoot t={it.turn} onFork={it.key === lastTurn ? onFork : undefined} />
            : <ItemView item={it} showThinking={showThinking} threadId={threadId} ownerId={owner?.id} ownerName={owner?.name} />}
          {it.kind === 'user' && it.unsent && (
            <div className="unsent-tag"><Icon name="alert" size={12} /><span>Not sent — Pocket AI restarted before it went out.</span>
              {onResend && <button type="button" className="btn sm" onClick={() => onResend(it)}>Send again</button>}
              {onDropUnsent && <button type="button" className="btn ghost sm" onClick={() => onDropUnsent(it)}>Remove</button>}
            </div>
          )}
        </div>
      )})}
    </div>
  )
}

/** A shared thread's owner: messages without a sender are theirs. */
export interface Owner { id: number; name: string }

// Who sent a message in a shared thread.
function ByLine({ name, id }: { name: string; id?: number }) {
  const { user } = useApp()
  return <div className="msg-by">{id && id === user?.id ? 'You' : name}</div>
}

