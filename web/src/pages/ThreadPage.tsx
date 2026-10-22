// One conversation: live timeline, requests waiting above the composer, header controls.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtCost, providerName, shortPath, threadTitle } from '../lib/format'
import { openSSE } from '../lib/sse'
import type { Item, Thread, ThreadDetail, Upload } from '../lib/types'
import { RunBar, useForkNewTab } from '../components/RunBar'
import { QueueStrip } from '../components/QueueStrip'
import { AgentPicker } from '../components/AgentPicker'
import { BranchButton } from '../components/BranchPicker'
import { ContextMeter, ProviderBadge } from '../components/bits'
import { Composer, type ComposerHandle } from '../components/Composer'
import { Icon } from '../components/icons'
import { AskButton } from '../components/AskButton'
import { BatchReview, ThreadEvidence } from '../components/AppEvidence'
import { RequestCard } from '../components/RequestCard'
import { Select } from '../components/Select'
import { Timeline } from '../components/Timeline'
import { PrivacyBadge, ShareDialog } from '../components/ShareDialog'
import { accessOf } from '../lib/share'
import { Confirm, download, Empty, errMsg, Loading, type MenuItem, MenuButton, Modal, Spinner, useContextMenu, useToast, noGrammarly } from '../components/ui'

function upsert(list: Item[], it: Item): Item[] {
  const i = list.findIndex(x => x.key === it.key)
  if (i < 0) return [...list, it]
  const next = list.slice()
  next[i] = it
  return next
}

export default function ThreadPage({ onMenu }: { onMenu?: () => void }) {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const loc = useLocation()
  const { user, settings, providers, threads, upsertThread, dropThread, setViewing } = useApp()
  const { toast } = useToast()
  const [thread, setThread] = useState<Thread | null>(null)
  const [items, setItems] = useState<Item[] | null>(null)
  const [missing, setMissing] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [editMessage, setEditMessage] = useState<Item | null>(null)
  const [messageDraft, setMessageDraft] = useState('')
  const [rewinding, setRewinding] = useState(false)
  const [sharing, setSharing] = useState(false)
  const [atBottom, setAtBottom] = useState(true)
  const [newBelow, setNewBelow] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const composer = useRef<ComposerHandle>(null)
  const atBottomRef = useRef(true)
  const canRun = !!user && (user.role === 'admin' || user.can_run || thread?.source === 'ask')
  const forkNewTab = useForkNewTab(id)
  const ctx = useContextMenu()
  const { running: runningNow } = useApp()
  const otherRunning = runningNow.filter(r => r.thread_id !== id).length

  // the list's copy is fresher while the detail loads
  const listCopy = threads?.find(t => t.id === id) || null
  const t = thread || listCopy

  const load = useCallback(async () => {
    try {
      const d = await api.get<ThreadDetail>(`${V1}/threads/${id}`)
      setThread(d.thread)
      setItems(d.items || [])
      setMissing(false)
      upsertThread(d.thread)
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setMissing(true)
      else toast(errMsg(e), 'err')
    }
  }, [id, upsertThread, toast])

  useEffect(() => {
    setThread(null); setItems(null); setMissing(false); setEditingTitle(false); setEditMessage(null); atBottomRef.current = true; setAtBottom(true); setNewBelow(false)
    load()
    setViewing(id)
    return () => setViewing(null)
  }, [id, load, setViewing])

  // live updates for this thread
  useEffect(() => {
    if (!id) return
    let first = true
    const close = openSSE(`${V1}/threads/${id}/events`, {
      item: d => { const it = d as Item; if (it?.key) setItems(list => list ? upsert(list, it) : list) },
      delta: d => {
        const { key, field, text } = d as { key: string; field: 'text' | 'output'; text: string }
        setItems(list => {
          if (!list) return list
          const i = list.findIndex(x => x.key === key)
          if (i < 0) return [...list, { id: 0, key, kind: field === 'output' ? 'tool' : 'assistant', created_at: '', updated_at: '', text: field === 'text' ? text : '' }]
          const it = list[i]
          const next = list.slice()
          if (field === 'output' && it.tool) next[i] = { ...it, tool: { ...it.tool, output: (it.tool.output || '') + text } }
          else next[i] = { ...it, text: (it.text || '') + text }
          return next
        })
      },
      item_removed: d => { const { key } = d as { key: string }; setItems(list => list ? list.filter(x => x.key !== key) : list) },
      thread: d => { const th = d as Thread; if (th?.id === id) { setThread(th); upsertThread(th) } },
      revoked: () => { setMissing(true); dropThread(id) }, // no longer shared with you
      ping: () => {},
    }, { onOpen: () => { if (!first) load(); first = false } })
    return close
  }, [id, load, upsertThread, dropThread])

  // mark read while looking at it
  useEffect(() => {
    if (!t?.unread || document.hidden) return
    api.post(`${V1}/threads/${id}/read`).catch(() => {})
  }, [t?.unread, id])
  useEffect(() => {
    const h = () => { if (!document.hidden && t?.unread) api.post(`${V1}/threads/${id}/read`).catch(() => {}) }
    document.addEventListener('visibilitychange', h)
    return () => document.removeEventListener('visibilitychange', h)
  }, [t?.unread, id])

  // stick to the bottom unless the person scrolled up
  const onScroll = () => {
    const el = scroller.current
    if (!el) return
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 140
    atBottomRef.current = near
    setAtBottom(near)
    if (near) setNewBelow(false)
  }
  const toBottom = (smooth = false) => { const el = scroller.current; if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' }) }
  const firstPaint = useRef(true)
  useEffect(() => { firstPaint.current = true }, [id])
  useLayoutEffect(() => {
    if (!items) return
    const hash = loc.hash.startsWith('#item-') ? loc.hash.slice(1) : ''
    if (firstPaint.current) {
      firstPaint.current = false
      const target = hash ? document.getElementById(hash) : null
      if (target) { target.scrollIntoView({ block: 'center' }); target.classList.add('flash'); return }
      toBottom()
      return
    }
    if (atBottomRef.current) toBottom()
    else setNewBelow(true)
  }, [items]) // eslint-disable-line react-hooks/exhaustive-deps

  const allPending = useMemo(() => (items || []).filter(i => i.kind === 'request' && i.request?.status === 'pending'), [items])
  // a Pocket app change in a shared chat is answered by the person whose apps it touches
  const pending = useMemo(() => allPending.filter(i => !i.request?.for_id || i.request.for_id === user?.id), [allPending, user])
  const othersPending = allPending.filter(i => !pending.includes(i))
  // queued messages wait above the reply box, not in the conversation
  const visible = useMemo(() => (items || []).filter(i => !(i.kind === 'request' && i.request?.status === 'pending') && !(i.kind === 'user' && i.queued)), [items])
  const queuedItems = useMemo(() => (items || []).filter(i => i.kind === 'user' && i.queued), [items])
  // a message that never went out (Pocket AI restarted while it waited)
  const resend = useCallback(async (it: Item) => {
    try {
      await api.post(`${V1}/threads/${id}/messages`, { text: it.text || '', upload_ids: [...(it.images || []), ...(it.files || [])].map(im => im.id) })
      await api.del(`${V1}/threads/${id}/queue/${encodeURIComponent(it.key)}`)
    } catch (e) { toast(errMsg(e), 'err') }
  }, [id, toast])
  const dropUnsent = useCallback((it: Item) => { api.del(`${V1}/threads/${id}/queue/${encodeURIComponent(it.key)}`).catch(e => toast(errMsg(e), 'err')) }, [id, toast])

  const patch = async (p: Partial<Pick<Thread, 'title' | 'pinned' | 'archived' | 'model' | 'effort' | 'mode' | 'speed' | 'no_memory'>>) => {
    if (!t) return
    setThread({ ...t, ...p })
    try { const th = await api.patch<Thread>(`${V1}/threads/${id}`, p); setThread(th); upsertThread(th); return th } catch (e) { toast(errMsg(e), 'err'); load() }
  }
  const interrupt = useCallback(async () => {
    try { await api.post(`${V1}/threads/${id}/interrupt`); toast('Stopping…') } catch (e) { toast(errMsg(e), 'err') }
  }, [id, toast])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if ((e.metaKey || e.ctrlKey) && e.key === '.') { e.preventDefault(); interrupt() } }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [interrupt])

  const send = async (text: string, uploadIds: string[]) => {
    try {
      const r = await api.post<{ queued: boolean; thread: Thread }>(`${V1}/threads/${id}/messages`, { text, upload_ids: uploadIds })
      if (r?.thread) { setThread(r.thread); upsertThread(r.thread) }
      if (r?.queued) toast('Queued — it goes out when the agent is done')
      atBottomRef.current = true
      setTimeout(() => toBottom(true), 50)
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }
  const fork = async () => {
    try { const th = await api.post<Thread>(`${V1}/threads/${id}/fork`); upsertThread(th); navigate(`/t/${th.id}`); toast('Forked — this is the copy') } catch (e) { toast(errMsg(e), 'err') }
  }
  const rewind = async (item: Item, editedText?: string) => {
    if (rewinding) return
    setRewinding(true)
    try {
      const result = await api.post<{ thread: Thread; draft: { text: string; uploads: Upload[] } }>(`${V1}/threads/${id}/rewind`, { key: item.key })
      const next = result.thread
      const text = editedText ?? result.draft.text
      sessionStorage.setItem('pai-draft-' + next.id, text)
      sessionStorage.setItem('pai-draft-uploads-' + next.id, JSON.stringify(result.draft.uploads))
      upsertThread(next)
      if (editedText !== undefined) {
        try {
          const sent = await api.post<{ thread: Thread }>(`${V1}/threads/${next.id}/messages`, { text, upload_ids: result.draft.uploads.map(u => u.id) })
          upsertThread(sent.thread)
          sessionStorage.removeItem('pai-draft-' + next.id)
          sessionStorage.removeItem('pai-draft-uploads-' + next.id)
        } catch (e) { toast('The continuation is ready, but sending failed: ' + errMsg(e), 'err') }
      }
      setEditMessage(null)
      navigate(`/t/${next.id}`)
      toast(editedText === undefined ? 'Rewound — edit the restored draft and send when ready. Original conversation kept.' : 'Original conversation kept in your history')
    } catch (e) { toast(errMsg(e), 'err') }
    finally { setRewinding(false) }
  }
  const remove = async () => {
    setConfirmDelete(false)
    try { await api.del(`${V1}/threads/${id}`); dropThread(id); navigate('/'); toast('Thread deleted') } catch (e) { toast(errMsg(e), 'err') }
  }
  const clearQueue = async () => {
    try { await api.del(`${V1}/threads/${id}/queue`); load() } catch (e) { toast(errMsg(e), 'err') }
  }

  if (missing) return (
    <div className="thread-page">
      <header className="thread-head">{onMenu && <button type="button" className="btn ghost icon-only" aria-label="Threads" onClick={onMenu}><Icon name="menu" size={19} /></button>}<span className="grow" /><AskButton /></header>
      <Empty title="This thread is gone" icon="message">It was deleted, or it belongs to someone else and isn’t shared with you. <button type="button" className="link-btn" onClick={() => navigate('/')}>Start a new one</button></Empty>
    </div>
  )
  if (!t) return <div className="thread-page"><Loading /></div>

  const p = providers.find(x => x.id === t.provider)
  const access = accessOf(t, user)
  const owner = access === 'owner'
  const canSend = canRun && access !== 'view'
  const sharedOwner = t.shares?.length ? { id: t.owner_id, name: t.owner_name || 'Owner' } : undefined
  const running = t.status === 'running'
  const rewindDisabled = rewinding || running || t.status === 'waiting' || !!t.external || t.queued > 0 || t.pending > 0 || !!t.subagents?.length
  const lastItem = visible[visible.length - 1]
  // the text the agent is writing right now gets a blinking cursor
  const streamingKey = running && (lastItem?.kind === 'assistant' || lastItem?.kind === 'thinking') ? lastItem.key : undefined
  const menu: MenuItem[] = !owner ? [
    { label: 'Export as Markdown', icon: 'download', onSelect: () => download(`${V1}/threads/${id}/export.md`) },
  ] : [
    { label: t.shares?.length ? 'Sharing…' : 'Share…', icon: 'users', onSelect: () => setSharing(true) }, 'sep',
    { label: 'Save as workflow', icon: 'zap', onSelect: () => navigate('/workflows', { state: { name: t.title, prompt: t.last_prompt || '' } }) },
    { label: t.no_memory ? 'Enable saved memory' : 'Don’t use memory in this conversation', icon: 'bookmark', onSelect: () => patch({ no_memory: !t.no_memory }) },
    ...(document.querySelector('pocket-ask-launcher') ? [{ label: 'Ask about an item…', icon: 'sparkles' as const, onSelect: () => window.dispatchEvent(new Event('pocket-ask-pick')) }] : []),
    { label: t.pinned ? 'Unpin' : 'Pin to top', icon: 'bookmark', onSelect: () => patch({ pinned: !t.pinned }) },
    { label: 'Rename', icon: 'edit', onSelect: () => { setTitleDraft(t.title || threadTitle(t)); setEditingTitle(true) } },
    { label: 'Fork into a new thread', icon: 'fork', disabled: t.source === 'ask' || !t.provider_session_id || !canRun, onSelect: fork },
    { label: 'Export as Markdown', icon: 'download', onSelect: () => download(`${V1}/threads/${id}/export.md`) },
    'sep',
    { label: t.archived ? 'Move out of archive' : 'Archive', icon: 'archive', onSelect: async () => { const th = await patch({ archived: !t.archived }); if (th?.archived) { dropThread(id); toast('Archived') } } },
    { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setConfirmDelete(true) },
  ]

  return (
    <div className="thread-page">
      <header className="thread-head">
        {onMenu && <button type="button" className="btn ghost icon-only menu-btn" aria-label="Threads" onClick={onMenu}><Icon name="menu" size={19} />{otherRunning > 0 && <span className="menu-run-dot" />}</button>}
        <div className="grow thread-head-main" onContextMenu={e => { if (!editingTitle) ctx.open(e, menu) }}>
          {editingTitle ? (
            <form onSubmit={e => { e.preventDefault(); setEditingTitle(false); if (titleDraft.trim() !== t.title) patch({ title: titleDraft.trim() }) }}>
              <input className="title-input" autoFocus value={titleDraft} onChange={e => setTitleDraft(e.target.value)} onBlur={() => { setEditingTitle(false); if (titleDraft.trim() && titleDraft.trim() !== t.title) patch({ title: titleDraft.trim() }) }}
                onKeyDown={e => { if (e.key === 'Escape') setEditingTitle(false) }} aria-label="Thread title" />
            </form>
          ) : (
            <h1 className="thread-title ellipsis" onClick={() => { if (!owner) return; setTitleDraft(t.title || threadTitle(t)); setEditingTitle(true) }} title={owner ? 'Click to rename' : undefined}>
              {t.pinned && <Icon name="bookmark" size={13} className="pin-ico" />}{threadTitle(t)}
            </h1>
          )}
          <div className="thread-sub">
            <PrivacyBadge thread={t} access={access} onShare={owner ? () => setSharing(true) : undefined} />
            <ProviderBadge provider={t.provider} size="xs" />
            <span className="ellipsis mono" title={t.cwd}>{shortPath(t.cwd)}</span>
            {canRun && t.source !== 'ask' && t.cwd && <BranchButton cwd={t.cwd} className="btn ghost" refreshKey={`${id}:${t.status}`}
              lockedReason={running || t.status === 'waiting' || t.external || t.subagents?.length ? 'The agent is working in this folder — switch once it is done.' : undefined} />}
            {t.usage?.cost_usd > 0 && <span className="tnum" title="Cost so far">{fmtCost(t.usage.cost_usd)}</span>}
            <ContextMeter used={t.usage?.context_tokens || 0} window={t.usage?.context_window || 0} />
          </div>
        </div>
        <div className="thread-head-actions">
          <AskButton />
          <MenuButton items={menu} label="Thread menu" align="right"><Icon name="more" size={17} /></MenuButton>
        </div>
      </header>

      <div className="thread-scroll" ref={scroller} onScroll={onScroll}>
        <div className="thread-inner">
          {items === null ? <Loading /> : (
            <>
              {items.length === 0 && <div className="muted center small" style={{ padding: 30 }}>Nothing here yet.</div>}
              {t.source === 'rewind' && <div className="notice info"><Icon name="undo" size={14} /><span>Continued from an earlier message. <button type="button" className="link-btn" onClick={() => navigate(`/t/${t.source_ref}`)}>Open original conversation</button>. Files and app changes already made are unchanged.</span></div>}
              {t.source === 'import' && items.length > 0 && <div className="notice info"><Icon name="history" size={14} /><span>Imported from the server’s {providerName(t.provider)} history — sending a message continues that session.</span></div>}
              <Timeline key={id} threadId={t.provider === 'claude' ? id : undefined} items={visible} onResend={canSend ? resend : undefined} onDropUnsent={canSend ? dropUnsent : undefined} showThinking={!!settings?.show_thinking} streamingKey={streamingKey}
                onFork={owner && t.source !== 'ask' && t.provider_session_id && canRun ? forkNewTab : undefined}
                onEdit={owner && canRun ? it => { setMessageDraft(it.text || ''); setEditMessage(it) } : undefined}
                onRewind={owner && canRun ? it => { void rewind(it) } : undefined} rewindDisabled={rewindDisabled}
                owner={sharedOwner} />
              <ThreadEvidence id={id} revision={visible.filter(i => i.kind === 'turn').length} />
              {t.status === 'error' && t.error && <div className="notice error"><Icon name="alert" size={14} /><span>{t.error}</span></div>}
            </>
          )}
        </div>
      </div>

      <div className="thread-bottom">
        {newBelow && !atBottom && <button type="button" className="new-below" onClick={() => { toBottom(true); setNewBelow(false) }}><Icon name="arrow-down" size={13} />New messages</button>}
        <div className="thread-bottom-inner">
          {(running || t.status === 'waiting' || !!t.subagents?.length) && items && <RunBar t={t} items={items} />}
          <QueueStrip threadId={id} queued={queuedItems} agent={t.provider === 'codex' ? 'Codex' : 'Claude'} onClearAll={clearQueue} />
          {pending.length > 0 && !canSend && access === 'view' && <div className="muted xsmall center">The agent is waiting for an answer from {t.owner_name || 'the owner'}.</div>}
          {othersPending.length > 0 && <div className="muted xsmall center">Waiting for {othersPending[0].request?.for || 'someone'} to approve a change to their apps.</div>}
          {pending.length > 0 && canSend && (
            <div className="req-dock">
              {pending.length > 1 && pending.every(p => p.request?.id.startsWith('ask-')) ? <BatchReview threadId={id} requests={pending.map(p => p.request!)} onDone={load} /> : <>
                <RequestCard key={pending[0].key} threadId={id} req={pending[0].request!} onDone={() => setItems(list => list && list.map(x => x.key === pending[0].key && x.request ? { ...x, request: { ...x.request, status: 'answered' } } : x))} />
                {pending.length > 1 && <div className="muted xsmall center">{pending.length - 1} more waiting after this one</div>}
              </>}

            </div>
          )}
          {access === 'view' ? (
            <div className="callout info"><Icon name="eye" size={15} /><span>{t.owner_name || 'Someone'} shared this chat with you to read. They can let you chat in it too.</span></div>
          ) : canRun ? (
            <Composer key={id} ref={composer} draftKey={id} autoFocus onSend={send} running={running} onStop={t.external && !t.live ? undefined : interrupt} disabled={!!t.external && !t.live}
              placeholder={t.external && !t.live ? 'It is running in another app right now — you can reply here once it has finished' : running ? 'Add a message — it is sent when the agent is done…' : pending.length ? 'Or type an answer…' : `Reply to ${providerName(t.provider)}…`}
              footer={<>
                {t.source === 'ask' && owner && <span className="muted small">Pocket Ask · each change needs your approval · <a href={t.source_ref} target="_blank" rel="noopener noreferrer">Source item ↗</a></span>}
                {!owner && <span className="muted small">{t.source === 'ask' ? `Shared Ask chat from ${t.owner_name || 'someone'} — your questions use your own apps` : `Chatting in ${t.owner_name || 'someone'}’s chat — the agent runs on their login and settings`}</span>}
                {owner && p && t.source !== 'ask' && <span className="thread-pickers"><AgentPicker compact showProvider={false} value={{ provider: t.provider, model: t.model, effort: t.effort, mode: t.mode, speed: t.speed || '', account_id: t.account_id || 0 }}
                  onChange={ch => patch(ch as Partial<Thread>)} /></span>}
              </>} />
          ) : (
            <div className="callout info"><Icon name="lock" size={15} /><span>An admin has to allow you to run agents before you can reply.</span></div>
          )}
        </div>
      </div>

      {editMessage && <Modal title={<span id="edit-message-title">Edit message</span>} labelledBy="edit-message-title" onClose={() => { if (!rewinding) setEditMessage(null) }} footer={<>
        <button type="button" className="btn" disabled={rewinding} onClick={() => setEditMessage(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={rewindDisabled || (!messageDraft.trim() && !editMessage.images?.length && !editMessage.files?.length && !editMessage.log_images)} onClick={() => { void rewind(editMessage, messageDraft) }}>{rewinding ? <Spinner size={14} /> : <Icon name="send" size={14} />}Save & send</button>
      </>}>
        <label htmlFor="edit-message-text" className="field-label">Message</label>
        <textarea {...noGrammarly} id="edit-message-text" className="message-editor" autoFocus value={messageDraft} onChange={e => setMessageDraft(e.target.value)} disabled={rewinding} rows={6} />
        {(!!editMessage.images?.length || !!editMessage.files?.length || !!editMessage.log_images) && <p className="muted small">The original attachments will be included.</p>}
        <p className="muted small">Starts a new continuation before this message. Your original conversation is kept. Files and app changes already made are unchanged.</p>
      </Modal>}
      {sharing && <ShareDialog thread={t} onClose={() => setSharing(false)} onSaved={th => { setThread(th); upsertThread(th) }} />}
      {ctx.menu}
      {confirmDelete && <Confirm title="Delete this thread?" message="The conversation disappears from Pocket AI. The CLI’s own session file on the server stays." onCancel={() => setConfirmDelete(false)} onConfirm={remove} />}
    </div>
  )
}
