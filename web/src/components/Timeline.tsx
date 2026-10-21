// A thread's items, in order: what you said, what the agent said and did.
import { memo, useRef, useState } from 'react'
import { fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import type { Item, ItemImage, Todo, TurnInfo } from '../lib/types'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { RequestSummary } from './RequestCard'
import { ToolRow } from './ToolRow'
import { Modal } from './ui'

function Images({ images }: { images: ItemImage[] }) {
  const [big, setBig] = useState<ItemImage | null>(null)
  return (
    <>
      <div className="msg-imgs">
        {images.map(im => (
          <button key={im.id} type="button" className="msg-img" onClick={() => setBig(im)} aria-label={`Open ${im.name}`}>
            <img src={im.url} alt={im.name} loading="lazy" />
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

export const ItemView = memo(function ItemView({ item, showThinking }: { item: Item; showThinking: boolean }) {
  switch (item.kind) {
    case 'user':
      return (
        <div className={`msg user ${item.queued ? 'queued' : ''}`} id={`item-${item.key}`}>
          <div className="bubble">
            {item.images && item.images.length > 0 && <Images images={item.images} />}
            {item.text && <div className="user-text">{item.text}</div>}
          </div>
          {item.queued && <div className="queued-tag"><Icon name="clock" size={11} />queued — sent when the agent is done</div>}
        </div>
      )
    case 'assistant':
      return <div className="msg assistant" id={`item-${item.key}`}><Markdown text={item.text || ''} /></div>
    case 'thinking':
      return <Thinking text={item.text || ''} defaultOpen={showThinking} />
    case 'tool':
      return item.tool ? <ToolRow tool={item.tool} /> : null
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

export function Timeline({ items, showThinking, streamingKey, onFork }: { items: Item[]; showThinking: boolean; streamingKey?: string; onFork?: () => void }) {
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
      {shown.map(it => (
        <div key={it.key} className={`tl-item ${initial.current!.has(it.key) ? '' : 'appear'} ${it.key === streamingKey ? 'streaming' : ''}`}>
          {it.kind === 'turn' && it.turn ? <TurnFoot t={it.turn} onFork={it.key === lastTurn ? onFork : undefined} />
            : <ItemView item={it} showThinking={showThinking} />}
        </div>
      ))}
    </div>
  )
}
