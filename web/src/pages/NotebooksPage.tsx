// Notebooks: sets of sources to ask about. The list, and one notebook: its
// sources on the left, on the right either a new question and its chats, or
// the open source next to a question about it.
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago } from '../lib/format'
import type { Notebook, NotebookFull, NotebookSource, ProviderId, Thread } from '../lib/types'
import { AgentPicker, defaultsFor, type AgentValue } from '../components/AgentPicker'
import { Composer, type ComposerHandle } from '../components/Composer'
import { Icon } from '../components/icons'
import { askPrefix, hasProjectsSources, NotebookChats, importProjects, nbURL, ProjectsPicker, reportToast, SourceList, SourceViewer, useNotebook, type ImportReport, type ProjectsPick } from '../components/Notebook'
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

export function NotebookPage({ onMenu }: { onMenu?: () => void }) {
  const id = Number(useParams().id) || 0
  const { nb, setNb, error, reload } = useNotebook(id)
  const { upsertThread, accounts } = useApp()
  const [open, setOpen] = useState<NotebookSource | null>(null)
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [choice, setChoice] = useAgentChoice()
  const composer = useRef<ComposerHandle>(null)
  const navigate = useNavigate()
  const { toast } = useToast()
  const mobile = useIsMobile()
  const projects = useProjectsLink()
  const [syncing, setSyncing] = useState(false)

  // the open source follows its latest version (renamed, read again)
  useEffect(() => { if (open && nb) setOpen(nb.source_list.find(x => x.id === open.id) || null) }, [nb]) // eslint-disable-line react-hooks/exhaustive-deps

  if (error && !nb) return <div className="page"><PageHead title="Notebook" icon="book" onMenu={onMenu} /><div className="page-inner"><div className="callout error" role="alert">{error}</div></div></div>
  if (!nb) return <div className="page"><PageHead title="Notebook" icon="book" onMenu={onMenu} /><Loading /></div>

  const send = async (text: string, uploadIds: string[]) => {
    if (!choice) return false
    const body = open ? askPrefix(open) + text : text
    try {
      const t = await api.post<Thread>(`${nbURL(nb.id)}/chats`, {
        provider: choice.provider, model: choice.model || undefined, effort: choice.effort || undefined, mode: choice.mode || undefined, speed: choice.speed ?? undefined,
        account_id: accounts.some(a => a.provider === choice.provider && a.id === choice.account_id && a.signed_in) ? choice.account_id : undefined,
        text: body, upload_ids: uploadIds,
      })
      upsertThread(t)
      navigate(`/t/${t.id}`)
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }

  const sync = async () => {
    setSyncing(true)
    try { const r = await api.post<{ report: ImportReport; notebook: NotebookFull }>(`${nbURL(nb.id)}/sync`); setNb(r.notebook); reportToast(r.report, toast) } catch (e) { toast(errMsg(e), 'err') }
    setSyncing(false)
  }

  const ask = (
    <div className="nb-ask">
      <Composer ref={composer} draftKey={`nb-${nb.id}${open ? '-' + open.id : ''}`} autoFocus={!mobile} onSend={send}
        placeholder={open ? `Ask about “${open.title}”…` : nb.source_list.length ? `Ask about ${nb.source_list.length === 1 ? 'this source' : `these ${nb.source_list.length} sources`}…` : 'Add a source first, or just ask…'}
        footer={choice ? <AgentPicker compact switchProvider value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} /> : undefined} />
      {open && <div className="muted xsmall nb-ask-note"><Icon name="info" size={12} />Starts a new chat about this source; it can still look at the others.</div>}
    </div>
  )

  const chats = (
    <div className="nb-chats-block">
      <div className="nb-h">Chats <span className="muted">{nb.chat_list.length || ''}</span></div>
      <NotebookChats nb={nb} />
      {nb.chat_list.length === 0 && <p className="muted small">Each question above starts its own chat here; all of them read the same sources.</p>}
    </div>
  )

  return <div className="page nb-page">
    <PageHead title={nb.name} icon="book" onMenu={onMenu}
      sub={<>{nb.link_label && <>{nb.link_url ? <a className="nb-from" href={nb.link_url} target="_blank" rel="noopener noreferrer" title="Open in Pocket Projects">From {nb.link_label} ↗</a> : <>From {nb.link_label}</>} · </>}{nb.source_list.length} {nb.source_list.length === 1 ? 'source' : 'sources'} · {nb.chat_list.length} {nb.chat_list.length === 1 ? 'chat' : 'chats'}{nb.instructions && <> · <span title={nb.instructions}>has instructions</span></>}</>}
      actions={<>{syncing && <Spinner />}<MenuButton label="Notebook menu" align="right" items={[
        { label: 'All notebooks', icon: 'book', onSelect: () => navigate('/notebooks') },
        ...(hasProjectsSources(nb) && projects?.configured ? [{ label: 'Update from Pocket Projects', icon: 'refresh', disabled: !projects.on || syncing, hint: projects.on ? 'Latest comments, notes, docs and files' : 'Turn on Pocket AI → Pocket Projects in Pocket Core', onSelect: sync }] as MenuItem[] : []),
        { label: 'Name and instructions…', icon: 'edit', onSelect: () => setEditing(true) },
        'sep',
        { label: 'Delete notebook…', icon: 'trash', danger: true, onSelect: () => setDeleting(true) },
      ]}><Icon name="more" size={17} /></MenuButton></>} />
    <div className={`nb-body ${open ? 'has-open' : ''}`}>
      <aside className="nb-side">
        <SourceList nb={nb} selected={open?.id} onSelect={x => setOpen(o => o?.id === x.id ? null : x)} onChanged={reload} />
      </aside>
      <section className="nb-main">
        {open ? <>
          <SourceViewer nbId={nb.id} source={open} onClose={() => setOpen(null)} onChanged={gone => { if (gone) setOpen(null); void reload() }}
            onAsk={() => composer.current?.focus()} />
          {ask}
        </> : <div className="nb-main-scroll">
          {ask}
          {chats}
        </div>}
      </section>
    </div>
    {editing && <NotebookDialog nb={nb} onClose={() => setEditing(false)} onSaved={n => setNb(n)} />}
    {deleting && <Confirm title="Delete this notebook?" message={`Its ${nb.source_list.length} sources are deleted from the server. Its chats move to the archive and can no longer continue.`}
      onCancel={() => setDeleting(false)} onConfirm={async () => {
        setDeleting(false)
        try { await api.del(nbURL(nb.id)); toast('Notebook deleted'); navigate('/notebooks') } catch (e) { toast(errMsg(e), 'err') }
      }} />}
  </div>
}
