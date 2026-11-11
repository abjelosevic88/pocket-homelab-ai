// Notebooks: sets of sources to ask about. The list, and one notebook: its
// sources on the left, on the right either a new question and its chats, or
// the open source next to a question about it.
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, threadTitle } from '../lib/format'
import { setFocusMode } from '../lib/sidebar'
import { ThreadStatus } from '../components/bits'
import ThreadPage from './ThreadPage'
import type { Notebook, NotebookFull, NotebookSource, ProviderId, Thread } from '../lib/types'
import { AgentPicker, defaultsFor, type AgentValue } from '../components/AgentPicker'
import { Composer, type ComposerHandle } from '../components/Composer'
import { Icon } from '../components/icons'
import { AddSource, askPrefix, COMPOSE_EVENT, hasProjectsSources, sourceIcon, importProjects, nbURL, ProjectsPicker, reportToast, SourceList, SourceViewer, useNotebook, type ImportReport, type ProjectsPick } from '../components/Notebook'
import { useProjectsLink } from '../lib/projects'
import { PageHead } from '../components/PageHead'
import { Confirm, Empty, errMsg, Loading, MenuButton, Modal, Segmented, Spinner, useIsMobile, useToast, type MenuItem } from '../components/ui'

export function NotebooksPage({ onMenu }: { onMenu?: () => void }) {
  const [list, setList] = useState<Notebook[] | null>(null)
  const [error, setError] = useState('')
  const [making, setMaking] = useState(false)
  const navigate = useNavigate()
  useEffect(() => { api.get<Notebook[]>(`${V1}/notebooks`).then(setList, e => { setList([]); setError(errMsg(e)) }) }, [])
  return <div className="page">
    <PageHead title="Notebooks" icon="book" onMenu={onMenu} sub="Collect files, links and notes, then ask questions about them."
      actions={<button type="button" className="btn primary sm" onClick={() => setMaking(true)}><Icon name="plus" size={14} />New notebook</button>} />
    <div className="page-scroll"><div className="page-inner">
      {error && <div className="callout error" role="alert">{error}</div>}
      {list === null ? <Loading /> : list.length === 0 && !error ? (
        <Empty icon="book" title="No notebooks yet">
          <p>A notebook holds sources: PDFs and Office files, web pages, notes from Pocket Notes, pasted text. Chats in it read them and answer with the source named.</p>
          <button type="button" className="btn primary" style={{ marginTop: 10 }} onClick={() => setMaking(true)}><Icon name="plus" size={14} />New notebook</button>
        </Empty>
      ) : (
        <div className="nb-grid">
          {list.map(n => (
            <button type="button" key={n.id} className="nb-card" onClick={() => navigate(`/notebooks/${n.id}`)}>
              <span className="nb-card-ico"><Icon name="book" size={18} /></span>
              <span className="nb-card-name">{n.name}</span>
              <span className="muted xsmall">{n.sources} {n.sources === 1 ? 'source' : 'sources'} · {n.chats} {n.chats === 1 ? 'chat' : 'chats'} · {ago(n.updated_at)}</span>
            </button>
          ))}
        </div>
      )}
    </div></div>
    {making && <NotebookDialog onClose={() => setMaking(false)} onSaved={n => navigate(`/notebooks/${n.id}`)} />}
  </div>
}

function NotebookDialog({ nb, onClose, onSaved }: { nb?: Notebook; onClose: () => void; onSaved: (n: NotebookFull) => void }) {
  const [name, setName] = useState(nb?.name || '')
  const [instructions, setInstructions] = useState(nb?.instructions || '')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const projects = useProjectsLink()
  const canStart = !nb && !!projects?.configured && !!projects.on
  const [from, setFrom] = useState<'' | 'issue' | 'project'>('')
  const [pick, setPick] = useState<ProjectsPick | null>(null)
  const [autoName, setAutoName] = useState('')
  const { toast } = useToast()
  const choose = (p: ProjectsPick) => {
    setPick(p)
    // the picked title names it, unless the person wrote a name of their own
    if (!name.trim() || name === autoName) { setName(p.title); setAutoName(p.title) }
  }
  const save = async () => {
    if (from && !pick) { setError(from === 'issue' ? 'Choose an issue, or start from nothing' : 'Choose a project, or start from nothing'); return }
    setBusy(from && pick ? 'Creating…' : 'Saving…'); setError('')
    try {
      let n = nb ? await api.put<NotebookFull>(nbURL(nb.id), { name, instructions }) : await api.post<NotebookFull>(`${V1}/notebooks`, { name, instructions })
      if (from && pick) {
        setBusy(from === 'issue' ? 'Bringing in the issue, its notes and files…' : 'Bringing in the project and its docs…')
        n = await importProjects(n.id, pick, toast) || n
      }
      onSaved(n); onClose()
    } catch (e) { setError(errMsg(e)); setBusy('') }
  }
  return <Modal title={nb ? 'Notebook settings' : 'New notebook'} onClose={onClose} footer={<>
    {busy && from && <span className="grow muted small">{busy}</span>}
    <button type="button" className="btn" onClick={onClose} disabled={!!busy}>Cancel</button>
    <button type="button" className="btn primary" onClick={save} disabled={!!busy || (!!from && !pick)}>{busy && <Spinner />}{nb ? 'Save' : 'Create'}</button>
  </>}>
    <form className="stack tight" onSubmit={e => { e.preventDefault(); void save() }}>
      {canStart && <div className="nb-start">
        <Segmented size="sm" value={from} onChange={v => { setFrom(v); setPick(null); if (name === autoName) { setName(''); setAutoName('') } }} label="Start from"
          options={[{ value: '', label: 'Empty' }, { value: 'issue', label: 'Projects issue' }, { value: 'project', label: 'Projects project' }]} />
        {from ? <>
          <ProjectsPicker kind={from} picked={pick} onPick={choose} />
          <span className="muted xsmall">{from === 'issue' ? 'Brings in its description, sub-issues, comments, shared notes, files and the Pocket notes about it.' : 'Brings in its overview, milestones, updates, docs and the list of its issues with their descriptions.'} “Update from Pocket Projects” gets the latest later.</span>
        </> : <span className="muted xsmall">Or start from a Pocket Projects issue or project, with everything in it.</span>}
      </div>}
      <label className="stack tight"><span className="small">Name</span>
        <input autoFocus={!canStart} value={name} maxLength={120} placeholder="e.g. ATH-340 search plan" onChange={e => setName(e.target.value)} /></label>
      <label className="stack tight"><span className="small">Instructions <span className="muted">(optional)</span></span>
        <textarea rows={canStart && from ? 2 : 4} value={instructions} placeholder="How answers should be, e.g. “Answer in Serbian”, “Quote the exact wording”, “Assume I am the developer”." onChange={e => setInstructions(e.target.value)} /></label>
      {error && <p className="callout error" role="alert">{error}</p>}
    </form>
  </Modal>
}

/** New chats here: the person's last choice on the home page, else their defaults. */
function useAgentChoice() {
  const { settings, providers } = useApp()
  const [choice, setChoice] = useState<AgentValue | null>(null)
  useEffect(() => {
    if (choice || !settings || !providers.length) return
    let saved: Partial<AgentValue> = {}
    try { saved = JSON.parse(localStorage.getItem('pai-nb-agent') || '{}') } catch { /* ignore */ }
    const pid = ((saved.provider && providers.find(p => p.id === saved.provider)?.available) ? saved.provider : providers.find(p => p.id === settings.default_provider)?.available ? settings.default_provider : providers.find(p => p.available)?.id || settings.default_provider) as ProviderId
    const p = providers.find(x => x.id === pid)
    const d = defaultsFor(p, settings)
    const same = saved.provider === pid
    setChoice({ provider: pid, model: same && saved.model && p?.models?.some(m => m.id === saved.model) ? saved.model : d.model, effort: same && saved.effort ? saved.effort : d.effort, mode: d.mode, speed: d.speed, account_id: d.account_id })
  }, [settings, providers, choice])
  useEffect(() => { if (choice) localStorage.setItem('pai-nb-agent', JSON.stringify({ provider: choice.provider, model: choice.model, effort: choice.effort })) }, [choice])
  return [choice, setChoice] as const
}

const pref = (k: string, d: string) => localStorage.getItem('pai-nb-' + k) ?? d
const setPref = (k: string, v: string) => localStorage.setItem('pai-nb-' + k, v)

/** One notebook as a workspace: its sources, the open source in the middle,
 * a chat beside it (left or right, as wide as you drag it). The app's
 * sidebar can fold away (Focus). On a phone: one of the three at a time. */
export function NotebookPage({ onMenu }: { onMenu?: () => void }) {
  const id = Number(useParams().id) || 0
  const { nb, setNb, error, reload } = useNotebook(id)
  const { upsertThread, accounts } = useApp()
  const [params, setParams] = useSearchParams()
  const srcId = Number(params.get('source')) || 0
  const chatParam = params.get('chat')
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [choice, setChoice] = useAgentChoice()
  const composer = useRef<ComposerHandle>(null)
  const navigate = useNavigate()
  const { toast } = useToast()
  const mobile = useIsMobile()
  const projects = useProjectsLink()
  const [syncing, setSyncing] = useState(false)
  const [rail, setRail] = useState(() => pref('rail', '1') === '1')
  const [chatOn, setChatOn] = useState(() => pref('chat', '1') === '1')
  const [side, setSide] = useState<'left' | 'right'>(() => pref('side', 'right') === 'left' ? 'left' : 'right')
  const [width, setWidth] = useState(() => Math.min(900, Math.max(320, Number(pref('chat-w', '460')) || 460)))
  const [focus, setFocus] = useState(() => pref('focus', '0') === '1')
  const [phone, setPhone] = useState<'sources' | 'read' | 'chat'>(srcId ? 'read' : 'sources')

  useEffect(() => { setFocusMode(focus && !mobile); setPref('focus', focus ? '1' : '0') }, [focus, mobile])
  useEffect(() => () => { setFocusMode(false) }, [])
  // the latest chat unless one is asked for; '' = a new one
  const chatId = chatParam !== null ? chatParam : nb?.chat_list[0]?.id || ''
  // a chat started here, or another one opened: the list follows
  useEffect(() => { if (chatId && nb && !nb.chat_list.some(t => t.id === chatId)) void reload() }, [chatId]) // eslint-disable-line react-hooks/exhaustive-deps

  const set = (k: 'source' | 'chat', v: string) => setParams(p => { const n = new URLSearchParams(p); if (v === '' && k === 'source') n.delete(k); else n.set(k, v); return n }, { replace: true })

  if (error && !nb) return <div className="page"><PageHead title="Notebook" icon="book" onMenu={onMenu} /><div className="page-inner"><div className="callout error" role="alert">{error}</div></div></div>
  if (!nb) return <div className="page"><PageHead title="Notebook" icon="book" onMenu={onMenu} /><Loading /></div>

  const open = nb.source_list.find(x => x.id === srcId) || null
  const openSource = (x: NotebookSource | null) => { set('source', x ? String(x.id) : ''); if (x && mobile) setPhone('read') }

  const send = async (text: string, uploadIds: string[]) => {
    if (!choice) return false
    try {
      const t = await api.post<Thread>(`${nbURL(nb.id)}/chats`, {
        provider: choice.provider, model: choice.model || undefined, effort: choice.effort || undefined, mode: choice.mode || undefined, speed: choice.speed ?? undefined,
        account_id: accounts.some(a => a.provider === choice.provider && a.id === choice.account_id && a.signed_in) ? choice.account_id : undefined,
        text, upload_ids: uploadIds,
      })
      upsertThread(t)
      set('chat', t.id) // it opens right here, next to the source
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }
  const askAbout = (x: NotebookSource) => {
    if (!chatOn) { setChatOn(true); setPref('chat', '1') }
    if (mobile) setPhone('chat')
    const text = askPrefix(x)
    if (chatId) setTimeout(() => window.dispatchEvent(new CustomEvent(COMPOSE_EVENT, { detail: { id: chatId, text } })), 50)
    else setTimeout(() => composer.current?.setText(text), 50)
  }
  const sync = async () => {
    setSyncing(true)
    try { const r = await api.post<{ report: ImportReport; notebook: NotebookFull }>(`${nbURL(nb.id)}/sync`); setNb(r.notebook); reportToast(r.report, toast) } catch (e) { toast(errMsg(e), 'err') }
    setSyncing(false)
  }
  const drag = (e: React.PointerEvent) => {
    e.preventDefault()
    const x0 = e.clientX, w0 = width
    const move = (ev: PointerEvent) => {
      const dx = ev.clientX - x0
      setWidth(Math.round(Math.min(Math.max(320, side === 'right' ? w0 - dx : w0 + dx), Math.min(900, window.innerWidth * 0.7))))
    }
    const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); setWidth(w => { setPref('chat-w', String(w)); return w }) }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }
  const toggle = (k: 'rail' | 'chat', v: boolean) => { (k === 'rail' ? setRail : setChatOn)(v); setPref(k, v ? '1' : '0') }

  const current = nb.chat_list.find(t => t.id === chatId)
  const chatMenu: MenuItem[] = [
    { label: 'New chat', icon: 'plus', onSelect: () => set('chat', '') },
    ...(nb.chat_list.length ? ['sep' as const, { heading: 'Chats in this notebook' }] : []),
    ...nb.chat_list.map(t => ({ label: threadTitle(t), icon: 'message', checked: t.id === chatId, hint: ago(t.activity_at), onSelect: () => set('chat', t.id) })),
  ]
  const chatCol = (
    <section className="nb-chatcol" style={mobile ? undefined : { width }} aria-label="Chat">
      <div className="nb-chatcol-head">
        <MenuButton label="Choose a chat" className="btn ghost sm nb-chat-pick" items={chatMenu} width={320}>
          <Icon name="message" size={14} /><span className="ellipsis">{current ? threadTitle(current) : 'New chat'}</span><Icon name="chevron-down" size={13} />
        </MenuButton>
        <span className="grow" />
        {chatId && <button type="button" className="btn ghost icon-only" title="New chat" aria-label="New chat" onClick={() => set('chat', '')}><Icon name="plus" size={16} /></button>}
        {chatId && <button type="button" className="btn ghost icon-only" title="Open this chat on its own" aria-label="Open this chat on its own" onClick={() => navigate(`/t/${chatId}`)}><Icon name="external" size={15} /></button>}
        {!mobile && <button type="button" className="btn ghost icon-only" title="Hide the chat" aria-label="Hide the chat" onClick={() => toggle('chat', false)}><Icon name="x" size={16} /></button>}
      </div>
      <div className="nb-chatcol-body">
        {chatId ? <ThreadPage key={chatId} threadId={chatId} embedded /> : (
          <div className="nb-newchat">
            <div className="nb-newchat-hello">
              <Icon name="sparkles" size={18} />
              <div><strong>Ask about {nb.source_list.length === 1 ? 'this source' : `these ${nb.source_list.length} sources`}</strong>
                <div className="muted small">Each question here starts its own chat; every chat reads the same sources. “Ask about this” on a source points it at that one.</div></div>
            </div>
            {nb.chat_list.length > 0 && <div className="nb-chats-block"><div className="nb-h">Earlier chats</div>
              {nb.chat_list.slice(0, 8).map(t => <button type="button" key={t.id} className="nb-chat" onClick={() => set('chat', t.id)}><ThreadStatus t={t} /><span className="grow ellipsis">{threadTitle(t)}</span><span className="muted xsmall">{ago(t.activity_at)}</span></button>)}
            </div>}
            <div className="grow" />
            <Composer ref={composer} draftKey={`nb-${nb.id}`} autoFocus={!mobile && !open} onSend={send}
              skills={choice?.provider ? { provider: choice.provider as ProviderId, cwd: nb.folder, account_id: choice.account_id } : undefined}
              placeholder={nb.source_list.length ? 'Ask about the sources…' : 'Add a source first, or just ask…'}
              footer={choice ? <AgentPicker compact switchProvider value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} /> : undefined} />
          </div>
        )}
      </div>
    </section>
  )

  const reader = (
    <section className="nb-reader" aria-label="Source">
      {open ? <SourceViewer nbId={nb.id} source={open} onClose={() => openSource(null)} onChanged={gone => { if (gone) openSource(null); void reload() }} onAsk={askAbout} />
        : <div className="nb-overview">
          <div className="nb-overview-inner">
            <h2>{nb.source_list.length ? 'Open a source to read it here' : 'Add the first source'}</h2>
            <p className="muted small">{nb.source_list.length ? 'The chat stays beside it; “Ask about this” points your question at the open source.' : 'Files, web links, Pocket notes, documents, an issue or project from Pocket Projects, or pasted text.'}</p>
            {nb.instructions && <div className="callout info"><Icon name="info" size={14} /><span><strong>Instructions:</strong> {nb.instructions}</span></div>}
            <div className="nb-src-grid">
              {nb.source_list.map(x => (
                <button type="button" key={x.id} className="nb-src-card" onClick={() => openSource(x)}>
                  <span className={`nb-source-icon k-${x.kind}`}><Icon name={sourceIcon(x)} size={15} /></span>
                  <span className="nb-src-card-title">{x.title}</span>
                </button>
              ))}
            </div>
            <div><AddSource nbId={nb.id} onAdded={reload} label="Add a source" /></div>
          </div>
        </div>}
    </section>
  )

  const railCol = <aside className="nb-rail" aria-label="Sources"><SourceList nb={nb} selected={open?.id} onSelect={x => openSource(open?.id === x.id ? null : x)} onChanged={reload} /></aside>
  const menu: MenuItem[] = [
    { label: 'All notebooks', icon: 'book', onSelect: () => navigate('/notebooks') },
    { label: 'Name and instructions…', icon: 'edit', onSelect: () => setEditing(true) },
    ...(hasProjectsSources(nb) && projects?.configured ? [{ label: 'Update from Pocket Projects', icon: 'refresh', disabled: !projects.on || syncing, hint: projects.on ? 'Latest comments, notes, docs and files' : 'Turn on Pocket AI → Pocket Projects in Pocket Core', onSelect: sync }] as MenuItem[] : []),
    ...(!mobile ? ['sep' as const,
      { label: rail ? 'Hide the source list' : 'Show the source list', icon: 'sidebar', onSelect: () => toggle('rail', !rail) },
      { label: chatOn ? 'Hide the chat' : 'Show the chat', icon: 'message', onSelect: () => toggle('chat', !chatOn) },
      { label: side === 'right' ? 'Chat on the left' : 'Chat on the right', icon: 'split-right', onSelect: () => { const v = side === 'right' ? 'left' : 'right'; setSide(v); setPref('side', v) } },
      { label: focus ? 'Show the app sidebar' : 'Fold the app sidebar away', icon: 'maximize', onSelect: () => setFocus(!focus) }] : []),
    'sep',
    { label: 'Delete notebook…', icon: 'trash', danger: true, onSelect: () => setDeleting(true) },
  ]

  return <div className={`page nb-work chat-${side}`}>
    <header className="nb-work-head">
      {onMenu && <button type="button" className="btn ghost icon-only" aria-label="Menu" title="Menu" onClick={onMenu}><Icon name="menu" size={19} /></button>}
      {!mobile && <button type="button" className={`btn ghost icon-only ${rail ? 'on' : ''}`} aria-pressed={rail} title={rail ? 'Hide the source list' : 'Show the source list'} aria-label="Source list" onClick={() => toggle('rail', !rail)}><Icon name="sidebar" size={16} /></button>}
      <span className="page-ico"><Icon name="book" size={15} /></span>
      <div className="grow nb-work-title">
        <h1 className="ellipsis" title={nb.name}>{nb.name}</h1>
        <div className="muted xsmall ellipsis">{nb.link_label && <>{nb.link_url ? <a className="nb-from" href={nb.link_url} target="_blank" rel="noopener noreferrer" title={`Open ${nb.link_label} in Pocket Projects`}>From {nb.link_label.split(' ')[0]} ↗</a> : <>From {nb.link_label}</>} · </>}{nb.source_list.length} {nb.source_list.length === 1 ? 'source' : 'sources'} · {nb.chat_list.length} {nb.chat_list.length === 1 ? 'chat' : 'chats'}</div>
      </div>
      {syncing && <Spinner />}
      {!mobile && <>
        <button type="button" className={`btn ghost icon-only ${focus ? 'on' : ''}`} aria-pressed={focus} title={focus ? 'Show the app sidebar' : 'Use the whole width (fold the app sidebar into ☰)'} aria-label="Focus" onClick={() => setFocus(!focus)}><Icon name="maximize" size={15} /></button>
        <button type="button" className="btn ghost icon-only" title={side === 'right' ? 'Move the chat to the left' : 'Move the chat to the right'} aria-label="Swap the chat side" onClick={() => { const v = side === 'right' ? 'left' : 'right'; setSide(v); setPref('side', v) }}><Icon name="split-right" size={15} /></button>
        <button type="button" className={`btn ghost sm ${chatOn ? 'on' : ''}`} aria-pressed={chatOn} title={chatOn ? 'Hide the chat' : 'Show the chat'} onClick={() => toggle('chat', !chatOn)}><Icon name="message" size={15} />Chat</button>
      </>}
      <MenuButton label="Notebook menu" align="right" items={menu}><Icon name="more" size={17} /></MenuButton>
    </header>
    {mobile && <div className="nb-phone-tabs">
      <Segmented size="sm" value={phone} onChange={setPhone} label="Show" options={[
        { value: 'sources', label: `Sources ${nb.source_list.length || ''}` }, { value: 'read', label: open ? 'Reading' : 'Read' }, { value: 'chat', label: 'Chat' }]} />
    </div>}
    <div className="nb-work-body">
      {mobile ? (phone === 'sources' ? railCol : phone === 'read' ? reader : chatCol) : <>
        {rail && railCol}
        {reader}
        {chatOn && <div className="nb-resize" onPointerDown={drag} role="separator" aria-orientation="vertical" aria-label="Chat width" title="Drag to change the chat's width" />}
        {chatOn && chatCol}
      </>}
    </div>
    {editing && <NotebookDialog nb={nb} onClose={() => setEditing(false)} onSaved={n => setNb(n)} />}
    {deleting && <Confirm title="Delete this notebook?" message={`Its ${nb.source_list.length} sources are deleted from the server. Its chats move to the archive and can no longer continue.`}
      onCancel={() => setDeleting(false)} onConfirm={async () => {
        setDeleting(false)
        try { await api.del(nbURL(nb.id)); toast('Notebook deleted'); navigate('/notebooks') } catch (e) { toast(errMsg(e), 'err') }
      }} />}
  </div>
}
