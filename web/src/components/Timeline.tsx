// A thread's items, in order: what you said, what the agent said and did.
import { memo, useState } from 'react'
import { fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import type { Item, ItemImage, Todo } from '../lib/types'
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
    case 'turn': {
      const t = item.turn
      if (!t) return null
      const bits = [fmtDuration(t.duration_ms)]
      if (t.cost_usd) bits.push(fmtCost(t.cost_usd))
      if (t.input_tokens || t.output_tokens) bits.push(`${fmtTokens(t.input_tokens + t.output_tokens)} tokens`)
      return (
        <div className={`turn-foot ${t.ok ? '' : 'err'}`}>
          {!t.ok && <><Icon name="alert" size={12} /><span>{t.error || 'The turn failed'}</span><span>·</span></>}
          <span className="tnum">{bits.join(' · ')}</span>
        </div>
      )
    }
    default:
      return item.text ? <div className="notice info"><span>{item.text}</span></div> : null
  }
})

export function Timeline({ items, showThinking }: { items: Item[]; showThinking: boolean }) {
  return (
    <div className="timeline">
      {items.map(it => <ItemView key={it.key} item={it} showThinking={showThinking} />)}
    </div>
  )
}
