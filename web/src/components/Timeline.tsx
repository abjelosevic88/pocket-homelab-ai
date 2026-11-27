// A thread's items, in order: what you said, what the agent said and did.
import { useApp } from '../lib/app-context'
import { memo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { fmtBytes, fmtCost, fmtDuration, fmtTokens } from '../lib/format'
import { V1 } from '../lib/api'
import type { Item, ItemImage, SentFile, Todo, ToolInfo, TurnInfo } from '../lib/types'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { RequestSummary } from './RequestCard'
import { ToolRow } from './ToolRow'
import { ToolActivity } from './ToolActivity'
import { groupToolActivity } from '../lib/tool-activity'
import { Modal, copyText } from './ui'
import { SendToPanel } from './PanelChips'
import { openPanelSheet, sendToThread } from '../lib/panels'

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

// Files the agent sent (Claude Code's SendUserFile), as cards like in the
// Claude apps. Pocket AI keeps a copy of each and serves it: pictures show
// right away, pages/PDFs/text open in a viewer, the rest downloads.
function sentURL(threadId: string, key: string, n: number, download = false) {
  return `${V1}/threads/${threadId}/sent-file?key=${encodeURIComponent(key)}&n=${n}${download ? '&download=1' : ''}`
}
const fileExt = (name: string) => { const m = name.match(/\.([A-Za-z0-9]{1,5})$/); return m ? m[1].toUpperCase() : 'FILE' }
const isImage = (f: SentFile) => f.image || (f.mime || '').startsWith('image/') || /\.(png|jpe?g|gif|webp|avif)$/i.test(f.name)
type View = 'page' | 'pdf' | 'text' | 'audio' | 'video' | ''
function viewOf(f: SentFile): View {
  const m = f.mime || '', n = f.name.toLowerCase()
  if (m === 'text/html' || /\.(html?|svg)$/.test(n) || m === 'image/svg+xml') return 'page'
  if (m === 'application/pdf' || n.endsWith('.pdf')) return 'pdf'
  if (m.startsWith('audio/')) return 'audio'
  if (m.startsWith('video/')) return 'video'
  if (m.startsWith('text/') || m === 'application/json' || /\.(md|txt|log|csv|tsv|json|jsonl|ya?ml|toml|ini|conf|xml|go|py|js|mjs|ts|tsx|jsx|css|sh|sql|rs|rb|java|kt|swift|c|h|cpp|cs|php|lua|nix|diff|patch)$/.test(n)) return 'text'
  return ''
}

function SentFiles({ tool, itemKey, threadId }: { tool: ToolInfo; itemKey: string; threadId?: string }) {
  const [open, setOpen] = useState<number | null>(null)
  const files = tool.files || []
  const caption = typeof (tool.input as { caption?: unknown } | null)?.caption === 'string' ? tool.title : ''
  if (tool.status === 'error' || tool.status === 'denied' || !threadId) {
    return <ToolRow tool={tool} />
  }
  const attach = tool.display === 'attach'
  const pics = attach ? [] : files.map((f, n) => ({ f, n })).filter(x => isImage(x.f))
  const cards = files.map((f, n) => ({ f, n })).filter(x => !pics.includes(x))
  const shown = open === null ? null : files[open]
  return (
    <div className="sent-files" id={`item-${itemKey}`}>
      {caption && <div className="sent-caption">{caption}</div>}
      {tool.status === 'running' && <div className="sent-card busy"><span className="sent-ext">{files[0] ? fileExt(files[0].name) : 'FILE'}</span><span className="sent-name ellipsis">{files.map(f => f.name).join(', ') || 'File'}</span><span className="muted small">sending…</span></div>}
      {tool.status === 'done' && pics.length > 0 && <Images images={pics.map(({ f, n }) => ({ id: `${itemKey}-${n}`, name: f.name, url: sentURL(threadId, itemKey, n) }) as ItemImage)} />}
      {tool.status === 'done' && cards.map(({ f, n }) => {
        // Text files open right here, to read and copy, and still download.
        if (viewOf(f) === 'text') return <SentText key={n} f={f} url={sentURL(threadId, itemKey, n)} download={sentURL(threadId, itemKey, n, true)} threadId={threadId} />
        const view = attach ? '' : viewOf(f)
        const body = <>
          <span className="sent-ext">{fileExt(f.name)}</span>
          <span className="sent-name ellipsis" title={f.path}>{f.name}</span>
          {f.size ? <span className="sent-size tnum">{fmtBytes(f.size)}</span> : null}
          <Icon name={view ? 'eye' : 'download'} size={14} className="sent-act" />
        </>
        return view
          ? <button key={n} type="button" className="sent-card" onClick={() => setOpen(n)} aria-label={`Open ${f.name}`}>{body}</button>
          : <a key={n} className="sent-card" href={sentURL(threadId, itemKey, n, true)} download={f.name} aria-label={`Download ${f.name}`}>{body}</a>
      })}
      {shown && open !== null && (
        <Modal size="xl" className="sent-view" onClose={() => setOpen(null)} title={<span className="sent-view-head">
          <span className="sent-ext">{fileExt(shown.name)}</span><span className="ellipsis">{shown.name}</span>
          <span className="grow" />
          <a className="btn ghost icon-only sm" href={sentURL(threadId, itemKey, open)} target="_blank" rel="noopener" title="Open in a new tab" aria-label="Open in a new tab"><Icon name="external" size={15} /></a>
          <a className="btn ghost icon-only sm" href={sentURL(threadId, itemKey, open, true)} download={shown.name} title="Download" aria-label="Download"><Icon name="download" size={15} /></a>
        </span>}>
          <SentView f={shown} url={sentURL(threadId, itemKey, open)} />
        </Modal>
      )}
    </div>
  )
}

// A text file the agent sent: the card folds open to show it (Markdown
// rendered), with Copy (the file's own text) and Download beside it.
function SentText({ f, url, download, threadId }: { f: SentFile; url: string; download: string; threadId?: string }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const loading = useRef<Promise<string> | null>(null)
  const load = () => {
    if (!loading.current) {
      loading.current = fetch(url, { credentials: 'same-origin' })
        .then(async r => { if (!r.ok) throw new Error((await r.json().catch(() => null))?.error || `Could not open ${f.name} (${r.status})`); return r.text() })
        .then(t => { setText(t); setErr(''); return t })
        .catch(e => { loading.current = null; setErr(e.message || String(e)); throw e })
    }
    return loading.current
  }
  const toggle = () => { if (!open) load().catch(() => {}); setOpen(o => !o) }
  const copy = async () => {
    try {
      if (await copyText(await load())) { setCopied(true); window.setTimeout(() => setCopied(false), 1500) }
    } catch { setOpen(true) }
  }
  // to a panel of models: its text is the material; "Refine" asks this conversation's agent to run the rounds
  const toPanel = async (kind: string) => {
    if (!threadId) return
    if (kind === 'refine') { sendToThread(threadId, `/refine the plan in ${f.name}${f.path ? ` (${f.path})` : ''}`); return }
    try { openPanelSheet({ kind, fromThreadId: threadId, material: `From the file ${f.name}:\n\n${await load()}` }) } catch { setOpen(true) }
  }
  const md = /\.(md|markdown|mdx)$/i.test(f.name)
  return (
    <div className={`sent-text${open ? ' open' : ''}`}>
      <div className="sent-text-head">
        <button type="button" className="sent-card" onClick={toggle} aria-expanded={open} aria-label={`${open ? 'Fold' : 'Show'} ${f.name}`}>
          <span className="sent-ext">{fileExt(f.name)}</span>
          <span className="sent-name ellipsis" title={f.path}>{f.name}</span>
          {f.size ? <span className="sent-size tnum">{fmtBytes(f.size)}</span> : null}
          <Icon name={open ? 'chevron-up' : 'chevron-down'} size={14} className="sent-act" />
        </button>
        <button type="button" className="btn ghost icon-only sm" onClick={copy} title={copied ? 'Copied' : 'Copy the text'} aria-label={`Copy the text of ${f.name}`}><Icon name={copied ? 'check' : 'copy'} size={15} /></button>
        <a className="btn ghost icon-only sm" href={download} download={f.name} title="Download" aria-label={`Download ${f.name}`}><Icon name="download" size={15} /></a>
        {threadId && <SendToPanel what={f.name} onPick={k => { void toPanel(k) }} />}
      </div>
      {open && <div className="sent-text-body">
        {err ? <div className="muted small">{err}</div>
          : text === null ? <div className="muted small">Opening…</div>
          : md ? <Markdown text={text} />
          : <pre>{text}</pre>}
      </div>}
    </div>
  )
}

function SentView({ f, url }: { f: SentFile; url: string }) {
  switch (viewOf(f)) {
    case 'audio': return <audio className="sent-media" src={url} controls autoPlay />
    case 'video': return <video className="sent-media" src={url} controls playsInline />
    // The server fences pages off too (CSP sandbox): no cookies, no API.
    case 'page': return <iframe className="sent-frame" src={url} title={f.name} sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads" />
    default: return <iframe className="sent-frame" src={url} title={f.name} />
  }
}

// Older tool rows only have "[image]" lines in their output.
const imageCount = (t: ToolInfo) => t.images ?? (t.output || '').split('\n').filter(l => l === '[image]').length

// Messages the CLI hands the agent on its own (a summary when a session is
// continued, notes from other sessions, sub-agent reports) fold into a quiet
// one-line row so they don't take over the conversation.
type Folded = { icon: string; label: string; body: string; rest: string; href?: string; go?: string }
const attr = (attrs: string, name: string) => attrs.match(new RegExp(`\\b${name}="([^"]*)"`))?.[1] || ''
export function foldedMessage(text: string): Folded | null {
  const t = text.trim()
  if (/^This session is being continued from a previous conversation/.test(t)) {
    return { icon: 'history', label: 'Summary of the earlier conversation', body: t, rest: '' }
  }
  const m = t.match(/^<([a-z][\w-]*)((?:\s[^>]*)?)>([\s\S]*?)<\/\1>/)
  if (!m) return null
  const [whole, tag, attrs] = m
  let body = m[3].trim()
  const rest = t.slice(whole.length).trim()
  switch (tag) {
    case 'cross-session-message':
      return { icon: 'message', label: `Message from ${attr(attrs, 'from-name') || 'another session'}`, body, rest }
    case 'teammate-message':
      return { icon: 'message', label: `Message from ${attr(attrs, 'teammate_id') || 'a teammate'}`, body, rest }
    case 'agent-message':
      body = body.replace(/^\[Subagent hand-back\][^\n]*\n+/, '').trim()
      return { icon: 'bot', label: 'Subagent report', body, rest }
    case 'pocket-heartbeat':
      return { icon: 'activity', label: `Heartbeat · ${attr(attrs, 'spec') || 'scheduled'}`, body, rest }
    case 'pocket-agent-report': {
      // an agent this conversation started answered (pocket_agent_start)
      const name = attr(attrs, 'from-name') || 'an agent'
      const state = attr(attrs, 'state')
      return { icon: 'bot', label: state === 'failed' ? `${name} stopped with an error` : state === 'stopped' ? `${name} was stopped` : `Answer from ${name}`, body, rest }
    }
    case 'pocket-panel-report': {
      // a panel of models started from this conversation has finished (Panels)
      const kind = attr(attrs, 'kind') || 'Panel'
      const state = attr(attrs, 'state')
      const id = attr(attrs, 'panel-id')
      return { icon: 'panel', label: state === 'done' ? `${kind}: the outcome` : `${kind} did not finish`, body, rest, href: id ? `/panels/${id}` : undefined, go: 'Open the panel' }
    }
    default: {
      const name = tag.replace(/-/g, ' ')
      return { icon: 'info', label: name[0].toUpperCase() + name.slice(1), body, rest }
    }
  }
}

/** What the person wrote, as written; only "[title](https://…)" (an item picked with "@app #…") becomes a link. */
function UserText({ text }: { text: string }) {
  const parts = text.split(/(\[[^\]\n]+\]\(https?:\/\/[^\s)]+\))/)
  return <div className="user-text">{parts.map((p, i) => {
    const m = i % 2 ? /^\[(.+)\]\((.+)\)$/.exec(p) : null
    return m ? <a key={i} href={m[2]} target="_blank" rel="noreferrer">{m[1]}</a> : p
  })}</div>
}

function FoldedNote({ f }: { f: Folded }) {
  const [open, setOpen] = useState(false)
  const preview = f.body.split('\n').map(l => l.trim()).find(l => l && !l.startsWith('<')) || ''
  return (
    <div className={`thinking folded-note ${open ? 'open' : ''}`}>
      <button type="button" className="thinking-h" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <Icon name={f.icon} size={14} /><span className="folded-label">{f.label}</span>
        {!open && preview && <span className="folded-preview">{preview}</span>}
        <Icon name="chevron-down" size={14} className="tool-caret" />
      </button>
      {open && <div className="thinking-body folded-body">{f.href && <p><Link to={f.href} className="folded-go">{f.go || 'Open'}<Icon name="chevron-right" size={12} /></Link></p>}{f.body}</div>}
    </div>
  )
}

export const ItemView = memo(function ItemView({ item, showThinking, threadId, ownerId, ownerName, onMoveFolder }: { item: Item; showThinking: boolean; threadId?: string; ownerId?: number; ownerName?: string; onMoveFolder?: () => void }) {
  switch (item.kind) {
    case 'user': {
      const pics = [...(item.images || []), ...logImages(threadId, item.key, item.log_images || 0)]
      const f = item.text ? foldedMessage(item.text) : null
      if (f && !f.rest && !pics.length && !item.files?.length) {
        return <div id={`item-${item.key}`}><FoldedNote f={f} /></div>
      }
      return (
        <div className={`msg user ${item.queued ? 'queued' : ''} ${item.unsent ? 'unsent' : ''}`} id={`item-${item.key}`}>
          {(item.by || ownerName) && <ByLine name={item.by || ownerName!} id={item.by ? item.by_id : ownerId} />}
          <div className="bubble">
            {pics.length > 0 && <Images images={pics} />}
            {item.files?.map(f => <a className="file-chip" key={f.id} href={f.url} download={f.name}>📄 {f.name}</a>)}
            {f ? <><FoldedNote f={f} />{f.rest && <UserText text={f.rest} />}</> : item.text && <AboutSource text={item.text} />}
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
      if (item.tool.kind === 'file') return <SentFiles tool={item.tool} itemKey={item.key} threadId={threadId} />
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
          <span className="grow">{item.text}</span>
          {item.fix === 'folder' && onMoveFolder && <button type="button" className="btn sm" onClick={onMoveFolder}><Icon name="folder" size={13} />Move to another folder…</button>}
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
function TurnFoot({ t, onFork, onRewindHere, rewindDisabled, text, onReview }: { t: TurnInfo; onFork?: () => void; onRewindHere?: () => void; rewindDisabled?: boolean; text?: string; onReview?: (kind: string) => void }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    if (!text || !await copyText(text)) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  // Stopping a turn yourself is not a failure: the footer stays quiet.
  const failed = !t.ok && !t.interrupted && !['stopped', 'stopped.', 'stoped', 'stoped.'].includes((t.error || '').trim().toLowerCase())
  const bits = []
  if (t.cost_usd) bits.push(fmtCost(t.cost_usd))
  if (t.input_tokens || t.output_tokens) bits.push(`${fmtTokens(t.input_tokens + t.output_tokens)} tokens`)
  return (
    <div className={`turn-foot ${failed ? 'err' : ''}`}>
      {failed && <span className="turn-err"><Icon name="alert" size={12} /><span className="ellipsis">{t.error || 'The turn failed'}</span></span>}
      <span className="grow" />
      {bits.length > 0 && <span className="tnum turn-bits">{bits.join(' · ')}</span>}
      {text && <button type="button" className="btn ghost icon-only xs" onClick={() => { void copy() }} title={copied ? 'Copied' : 'Copy response'} aria-label="Copy response"><Icon name={copied ? 'check' : 'copy'} size={14} /></button>}
      {text && onReview && <SendToPanel size="xs" what="this answer" onPick={onReview} />}
      {onRewindHere && <button type="button" className="btn ghost icon-only xs" disabled={rewindDisabled} onClick={onRewindHere} title={rewindDisabled ? 'Stop the agent and clear its queue before rewinding' : 'Rewind to here: remove all later messages from this chat'} aria-label="Rewind to here"><Icon name="undo" size={14} /></button>}
      {onFork && <button type="button" className="btn ghost icon-only xs" onClick={onFork} title="Fork from here into a new tab" aria-label="Fork from here into a new tab"><Icon name="fork" size={14} /></button>}
      <span className="run-time tnum" title="How long the agent worked on this">Worked for {fmtDuration(t.duration_ms)}</span>
    </div>
  )
}

export function Timeline({ items, showThinking, streamingKey, onFork, threadId, onResend, onDropUnsent, onEdit, onRewind, rewindDisabled, owner, onMoveFolder, onReview }: { items: Item[]; showThinking: boolean; streamingKey?: string; onFork?: (after: string) => void;
  /** send a reply to a panel of models: which kind ("refine" = council rounds), and the turn it ended */
  onReview?: (kind: string, turnKey: string) => void; threadId?: string; onResend?: (it: Item) => void; onDropUnsent?: (it: Item) => void; onEdit?: (it: Item) => void; onRewind?: (it: Item, keepText?: boolean) => void; rewindDisabled?: boolean; owner?: Owner; onMoveFolder?: () => void }) {
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
  // the message that came after each reply: rewinding to the reply cuts the conversation there
  const nextUser = new Map<string, Item>()
  let lastTurn = ''
  for (const it of shown) {
    if (it.kind === 'turn') lastTurn = it.key
    else if (it.kind === 'user' && lastTurn && !it.queued && !it.unsent) { nextUser.set(lastTurn, it); lastTurn = '' }
  }
  return (
    <div className="timeline">
      {groupToolActivity(shown).map(entry => {
        const it = entry.type === 'item' ? entry.item : entry.items[0]
        return (
        <div key={entry.key} className={`tl-item ${initial.current!.has(it.key) ? '' : 'appear'} ${it.key === streamingKey ? 'streaming' : ''}`}>
          {entry.type === 'tools' ? <ToolActivity items={entry.items} /> : it.kind === 'turn' && it.turn ? <TurnFoot t={it.turn} text={said.get(it.key)} onFork={onFork ? () => onFork(it.key) : undefined} onReview={onReview && said.get(it.key) ? kind => onReview(kind, it.key) : undefined}
              onRewindHere={onRewind && nextUser.has(it.key) ? () => onRewind(nextUser.get(it.key)!, false) : undefined} rewindDisabled={rewindDisabled} />
            : <ItemView item={it} showThinking={showThinking} threadId={threadId} ownerId={owner?.id} ownerName={owner?.name} onMoveFolder={onMoveFolder} />}
          {it.kind === 'user' && !it.queued && !it.unsent && (onEdit || onRewind) && <div className="message-actions">
            {onEdit && <button type="button" className="btn ghost icon-only xs" disabled={rewindDisabled} onClick={() => onEdit(it)} title={rewindDisabled ? 'Stop the agent and clear its queue before editing' : 'Edit message'} aria-label="Edit message"><Icon name="edit" size={14} /></button>}
            {onRewind && <button type="button" className="btn ghost icon-only xs" disabled={rewindDisabled} onClick={() => onRewind(it)} title={rewindDisabled ? 'Stop the agent and clear its queue before rewinding' : 'Rewind here (removes this message and everything after it)'} aria-label="Rewind here"><Icon name="undo" size={14} /></button>}
          </div>}
          {it.kind === 'user' && it.unsent && (
            <div className="unsent-tag"><Icon name="alert" size={12} /><span>{it.unsent_why || 'Not sent — Pocket AI restarted before it went out.'}</span>
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


// A notebook question about the open source: "About “Title” (sources/…): …"
// reads as a small tag with the source's name over the question.
const aboutRe = /^About “([^”]{1,300})” \((sources\/[^)\s]+)\): ([\s\S]*)$/
function AboutSource({ text }: { text: string }) {
  const m = text.match(aboutRe)
  if (!m) return <UserText text={text} />
  return <><span className="about-chip" title={m[2]}><Icon name="file" size={11} />{m[1]}</span><UserText text={m[3]} /></>
}
