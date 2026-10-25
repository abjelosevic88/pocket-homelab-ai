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
import { Modal, copyText } from './ui'

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
        <Icon name="brain" size={14} /><span>Thinking</span><Icon name="chevron-down" size={14} className="tool-caret" />
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

// Under every reply, like Paseo: what it cost, copy, fork from here, and how
// long the agent worked.
function TurnFoot({ t, onFork, text }: { t: TurnInfo; onFork?: () => void; text?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    if (!text || !await copyText(text)) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  const bits = []
  if (t.cost_usd) bits.push(fmtCost(t.cost_usd))
  if (t.input_tokens || t.output_tokens) bits.push(`${fmtTokens(t.input_tokens + t.output_tokens)} tokens`)
  return (
    <div className={`turn-foot ${t.ok ? '' : 'err'}`}>
      {!t.ok && <span className="turn-err"><Icon name="alert" size={12} /><span className="ellipsis">{t.error || 'The turn failed'}</span></span>}
      <span className="grow" />
      {bits.length > 0 && <span className="tnum turn-bits">{bits.join(' · ')}</span>}
      {text && <button type="button" className="btn ghost icon-only xs" onClick={() => { void copy() }} title={copied ? 'Copied' : 'Copy response'} aria-label="Copy response"><Icon name={copied ? 'check' : 'copy'} size={14} /></button>}
      {onFork && <button type="button" className="btn ghost icon-only xs" onClick={onFork} title="Fork from here into a new tab" aria-label="Fork from here into a new tab"><Icon name="fork" size={14} /></button>}
      <span className="run-time tnum" title="How long the agent worked on this">Worked for {fmtDuration(t.duration_ms)}</span>
    </div>
  )
}

export function Timeline({ items, showThinking, streamingKey, onFork, threadId, onResend, onDropUnsent, onEdit, onRewind, rewindDisabled, owner }: { items: Item[]; showThinking: boolean; streamingKey?: string; onFork?: (after: string) => void; threadId?: string; onResend?: (it: Item) => void; onDropUnsent?: (it: Item) => void; onEdit?: (it: Item) => void; onRewind?: (it: Item) => void; rewindDisabled?: boolean; owner?: Owner }) {
  // Items there when the thread opened render still; anything that arrives
  // while you watch slides in.
  const initial = useRef<Set<string> | null>(null)
  if (initial.current === null) initial.current = new Set(items.map(i => i.key))
  // A turn footer straight after another is the same turn read twice (Pocket
  // AI's own footer and the one from the CLI's log): keep the first.
  // Items that would render nothing (Opus' thinking blocks arrive empty) are
  // left out: they would split a run of tool calls and leave empty gaps.
  const shown = items.filter((it, i) => !(it.kind === 'turn' && items[i - 1]?.kind === 'turn') && !invisible(it, streamingKey) && !repeated(items, i))
  // What each turn said, for its footer's copy button: every reply since the
  // previous footer, whole.
  const said = new Map<string, string>()
  let parts: string[] = []
  for (const it of shown) {
    if (it.kind === 'assistant' && it.text?.trim()) parts.push(it.text.trim())
    if (it.kind === 'turn') { said.set(it.key, parts.join('\n\n')); parts = [] }
  }
  return (
    <div className="timeline">
      {groupToolActivity(shown).map(entry => {
        const it = entry.type === 'item' ? entry.item : entry.items[0]
        return (
        <div key={entry.key} className={`tl-item ${initial.current!.has(it.key) ? '' : 'appear'} ${it.key === streamingKey ? 'streaming' : ''}`}>
          {entry.type === 'tools' ? <ToolActivity items={entry.items} /> : it.kind === 'turn' && it.turn ? <TurnFoot t={it.turn} text={said.get(it.key)} onFork={onFork ? () => onFork(it.key) : undefined} />
            : <ItemView item={it} showThinking={showThinking} threadId={threadId} ownerId={owner?.id} ownerName={owner?.name} />}
          {it.kind === 'user' && !it.queued && !it.unsent && (onEdit || onRewind) && <div className="message-actions">
            {onEdit && <button type="button" className="btn ghost icon-only xs" disabled={rewindDisabled} onClick={() => onEdit(it)} title={rewindDisabled ? 'Stop the agent and clear its queue before editing' : 'Edit message'} aria-label="Edit message"><Icon name="edit" size={14} /></button>}
            {onRewind && <button type="button" className="btn ghost icon-only xs" disabled={rewindDisabled} onClick={() => onRewind(it)} title={rewindDisabled ? 'Stop the agent and clear its queue before rewinding' : 'Rewind here (keeps the original conversation)'} aria-label="Rewind here"><Icon name="undo" size={14} /></button>}
          </div>}
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

/** A reply read a second time from the CLI's log after its own footer (Pocket
 * AI restarted before noting it had read it): the same text, nothing between
 * but the footer. */
function repeated(items: Item[], i: number): boolean {
  const it = items[i]
  if (it.kind !== 'assistant' || !it.text?.trim()) return false
  let j = i - 1
  while (j >= 0 && items[j].kind === 'turn') j--
  return j < i - 1 && j >= 0 && items[j].kind === 'assistant' && items[j].text?.trim() === it.text.trim()
}

/** An item that shows nothing (empty thinking or reply that is not streaming). */
function invisible(it: Item, streamingKey?: string): boolean {
  if (it.key === streamingKey) return false
  if (it.kind === 'thinking') return !it.text?.trim()
  if (it.kind === 'assistant') return !it.text?.trim() && !it.images?.length
  return false
}

/** A shared thread's owner: messages without a sender are theirs. */
export interface Owner { id: number; name: string }

// Who sent a message in a shared thread.
function ByLine({ name, id }: { name: string; id?: number }) {
  const { user } = useApp()
  return <div className="msg-by">{id && id === user?.id ? 'You' : name}</div>
}

