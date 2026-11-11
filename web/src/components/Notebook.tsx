// Notebooks: a person's sets of sources to ask about. The pieces shared by
// the notebook page and a notebook chat's side pane: the source list, adding
// sources (files, links, Pocket notes/documents/files, pasted text) and the
// viewer with "Ask about this".
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, qs, upload, V1 } from '../lib/api'
import { useProjectsLink } from '../lib/projects'
import { ago, fmtBytes, threadTitle } from '../lib/format'
import { ThreadStatus } from './bits'
import type { NotebookFull, NotebookSource, NotebookSourceKind } from '../lib/types'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { Confirm, errMsg, Loading, Menu, Modal, rectOf, Segmented, Spinner, useDebounce, useToast, type Anchor, type MenuItem } from './ui'

export const nbURL = (id: number) => `${V1}/notebooks/${id}`

/** Put text in a chat's composer (detail: {id, text}): "Ask about this". */
export const COMPOSE_EVENT = 'pai:compose'

/** The notebook with its sources and chats; null while loading. */
export function useNotebook(id: number) {
  const [nb, setNb] = useState<NotebookFull | null>(null)
  const [error, setError] = useState('')
  const reload = useCallback(async () => {
    if (!id) return
    try { setNb(await api.get<NotebookFull>(nbURL(id))); setError('') } catch (e) { setError(errMsg(e)) }
  }, [id])
  useEffect(() => { setNb(null); setError(''); void reload() }, [reload])
  return { nb, setNb, error, reload }
}

const KIND: Record<NotebookSourceKind, { icon: string; label: string }> = {
  file: { icon: 'file', label: 'File' },
  link: { icon: 'globe', label: 'Web page' },
  note: { icon: 'note', label: 'Pocket Notes' },
  document: { icon: 'file-edit', label: 'Pocket Documents' },
  'pocket-file': { icon: 'folder', label: 'Pocket Files' },
  text: { icon: 'text', label: 'Pasted text' },
  issue: { icon: 'check-square', label: 'Pocket Projects issue' },
  project: { icon: 'layers', label: 'Pocket Projects project' },
  'shared-note': { icon: 'users', label: 'Shared note' },
  'project-doc': { icon: 'file-edit', label: 'Project doc' },
}

/** Issues and projects in it: what "Update from Pocket Projects" reads again. */
export const hasProjectsSources = (nb: NotebookFull) => nb.source_list.some(x => x.kind === 'issue' || x.kind === 'project')

export function sourceIcon(x: NotebookSource) {
  if (x.kind === 'file' && x.mime.startsWith('image/')) return 'image'
  return KIND[x.kind]?.icon || 'file'
}

/** What the composer starts with to ask about one source. */
export const askPrefix = (x: NotebookSource) => `About “${x.title}” (${x.file}): `

/** The web address a source came from, if any. */
export function sourceLink(x: NotebookSource): string {
  const m = x.origin.match(/https?:\/\/\S+/)
  return m ? m[0] : ''
}

function sourceSub(x: NotebookSource) {
  const parts = [KIND[x.kind]?.label || x.kind]
  if (x.kind === 'link') { try { parts[0] = new URL(sourceLink(x)).host } catch { /* keep the label */ } }
  if (x.size) parts.push(fmtBytes(x.size))
  if (x.chars) parts.push(x.chars >= 1000 ? `${Math.round(x.chars / 1000)}k chars` : `${x.chars} chars`)
  return parts.join(' · ')
}

// ---------- the list ----------

export function SourceList({ nb, selected, onSelect, onChanged, compact }: {
  nb: NotebookFull; selected?: number; onSelect: (x: NotebookSource) => void; onChanged: () => void; compact?: boolean
}) {
  const [drop, setDrop] = useState(false)
  const { toast } = useToast()
  const files = async (list: File[]) => { if (list.length) await uploadSources(nb.id, list, toast, onChanged) }
  return (
    <div className={`nb-sources ${drop ? 'drop' : ''} ${compact ? 'compact' : ''}`}
      onDragOver={e => { if (e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDrop(true) } }}
      onDragLeave={() => setDrop(false)}
      onDrop={e => { e.preventDefault(); setDrop(false); void files(Array.from(e.dataTransfer.files)) }}>
      <div className="nb-sources-head">
        <span className="grow nb-h">Sources <span className="muted">{nb.source_list.length || ''}</span></span>
        <AddSource nbId={nb.id} onAdded={onChanged} />
      </div>
      {nb.source_list.length === 0 ? (
        <div className="nb-empty">
          <Icon name="book" size={22} />
          <div>Add files, web links, notes from Pocket Notes or pasted text. Chats in this notebook answer from them.</div>
          <div className="muted xsmall">You can also drop files here.</div>
        </div>
      ) : (
        <div className="nb-source-list" role="list">
          {nb.source_list.map(x => (
            <button type="button" role="listitem" key={x.id} className={`nb-source ${selected === x.id ? 'on' : ''}`} onClick={() => onSelect(x)} title={x.title}>
              <span className={`nb-source-icon k-${x.kind}`}><Icon name={sourceIcon(x)} size={15} /></span>
              <span className="grow nb-source-text"><span className="nb-source-title ellipsis">{x.title}</span>
                {!compact && <span className="nb-source-sub ellipsis">{sourceSub(x)}</span>}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

async function uploadSources(id: number, list: File[], toast: ReturnType<typeof useToast>['toast'], done: () => void) {
  const big = list.find(f => f.size > 50 << 20)
  if (big) { toast(`${big.name} is larger than 50 MB`, 'err'); return }
  toast(list.length === 1 ? `Adding ${list[0].name}…` : `Adding ${list.length} files…`)
  try {
    const r = await upload<{ sources: NotebookSource[]; failed?: string[] }>(`${nbURL(id)}/sources/upload`, 'file', list)
    if (r.failed?.length) toast(`Added ${r.sources.length}; not added: ${r.failed.join('; ')}`, 'err', undefined, 9000)
    else toast(r.sources.length === 1 ? `Added ${r.sources[0].title}` : `Added ${r.sources.length} sources`)
  } catch (e) { toast(errMsg(e), 'err', undefined, 9000) }
  done()
}

// ---------- adding ----------

export function AddSource({ nbId, onAdded, label = 'Add' }: { nbId: number; onAdded: () => void; label?: string }) {
  const [menu, setMenu] = useState<Anchor | null>(null)
  const [dialog, setDialog] = useState<'' | 'link' | 'text' | 'notes' | 'documents' | 'files' | 'projects'>('')
  const input = useRef<HTMLInputElement>(null)
  const { toast } = useToast()
  const projects = useProjectsLink()
  const items: MenuItem[] = [
    { label: 'Upload files…', icon: 'upload', hint: 'PDF, Word, Excel, text, pictures', onSelect: () => input.current?.click() },
    { label: 'Web link…', icon: 'globe', onSelect: () => setDialog('link') },
    'sep',
    { label: 'From Pocket Notes…', icon: 'note', onSelect: () => setDialog('notes') },
    { label: 'From Pocket Documents…', icon: 'file-edit', onSelect: () => setDialog('documents') },
    { label: 'From Pocket Files…', icon: 'folder', onSelect: () => setDialog('files') },
    ...(projects?.configured && projects.on ? [{ label: 'From Pocket Projects…', icon: 'layers', hint: 'An issue or a project, with its files and notes', onSelect: () => setDialog('projects') }] : []),
    'sep',
    { label: 'Paste text…', icon: 'text', onSelect: () => setDialog('text') },
  ]
  return <>
    <button type="button" className="btn sm" onClick={e => setMenu(rectOf(e.currentTarget))}><Icon name="plus" size={14} />{label}</button>
    <input ref={input} type="file" multiple hidden onChange={e => { const l = Array.from(e.target.files || []); e.target.value = ''; void uploadSources(nbId, l, toast, onAdded) }} />
    {menu && <Menu anchor={menu} align="right" width={250} items={items} onClose={() => setMenu(null)} />}
    {dialog === 'link' && <LinkDialog nbId={nbId} onClose={() => setDialog('')} onAdded={onAdded} />}
    {dialog === 'text' && <PasteDialog nbId={nbId} onClose={() => setDialog('')} onAdded={onAdded} />}
    {dialog === 'projects' && <ProjectsDialog nbId={nbId} onClose={() => setDialog('')} onAdded={onAdded} />}
    {(dialog === 'notes' || dialog === 'documents' || dialog === 'files') && <PocketDialog nbId={nbId} app={dialog} onClose={() => setDialog('')} onAdded={onAdded} />}
  </>
}

function LinkDialog({ nbId, onClose, onAdded }: { nbId: number; onClose: () => void; onAdded: () => void }) {
  const [urls, setUrls] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const { toast } = useToast()
  const list = urls.split(/\s+/).map(u => u.trim()).filter(Boolean)
  const add = async () => {
    setBusy(true); setError('')
    const failed: string[] = []
    let added = 0
    for (const url of list) {
      try { await api.post(`${nbURL(nbId)}/sources/link`, { url: /^https?:\/\//i.test(url) ? url : 'https://' + url }); added++ } catch (e) { failed.push(`${url}: ${errMsg(e)}`) }
    }
    setBusy(false)
    if (added) { onAdded(); toast(added === 1 ? 'Added the page' : `Added ${added} pages`) }
    if (failed.length) { setError(failed.join('\n')); setUrls(failed.map(f => f.split(': ')[0]).join('\n')) } else onClose()
  }
  return <Modal title="Add web links" size="sm" onClose={onClose} footer={<>
    <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
    <button type="button" className="btn primary" disabled={!list.length || busy} onClick={add}>{busy && <Spinner />}Add {list.length > 1 ? `${list.length} links` : 'link'}</button>
  </>}>
    <div className="stack tight">
      <textarea className="input" rows={3} autoFocus placeholder="https://… (one per line)" value={urls} onChange={e => setUrls(e.target.value)} />
      <p className="muted small">Pocket AI reads each page now and keeps its text. Pages behind a login or built only with JavaScript may come out empty. PDFs behind a link work too.</p>
      {error && <p className="callout error" role="alert" style={{ whiteSpace: 'pre-line' }}>{error}</p>}
    </div>
  </Modal>
}

function PasteDialog({ nbId, onClose, onAdded }: { nbId: number; onClose: () => void; onAdded: () => void }) {
  const [title, setTitle] = useState('')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    setBusy(true); setError('')
    try { await api.post(`${nbURL(nbId)}/sources/text`, { title, text }); onAdded(); onClose() } catch (e) { setError(errMsg(e)) }
    setBusy(false)
  }
  return <Modal title="Paste text" onClose={onClose} footer={<>
    <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
    <button type="button" className="btn primary" disabled={!text.trim() || busy} onClick={save}>{busy && <Spinner />}Add source</button>
  </>}>
    <div className="stack tight">
      <input className="input" placeholder="Title (optional)" value={title} onChange={e => setTitle(e.target.value)} />
      <textarea className="input mono-ish" rows={12} autoFocus placeholder="Paste notes, a transcript, an email…" value={text} onChange={e => setText(e.target.value)} />
      {error && <p className="callout error" role="alert">{error}</p>}
    </div>
  </Modal>
}

interface PocketItem { app: string; id: string; title: string; subtitle?: string; url?: string; attachable: boolean }
const APP_LABEL = { notes: 'Notes', documents: 'Documents', files: 'Files' } as const
type PocketApp = keyof typeof APP_LABEL

function PocketDialog({ nbId, app: first, onClose, onAdded }: { nbId: number; app: PocketApp; onClose: () => void; onAdded: () => void }) {
  const [app, setApp] = useState<PocketApp>(first)
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 300)
  const [items, setItems] = useState<PocketItem[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<Map<string, PocketItem>>(new Map())
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  useEffect(() => {
    let live = true
    setItems(null); setError('')
    api.get<{ items: PocketItem[] }>(`${V1}/pocket/items?app=${app}&q=${encodeURIComponent(dq.trim())}`)
      .then(r => { if (live) setItems(r.items) }, e => { if (live) { setItems([]); setError(errMsg(e)) } })
    return () => { live = false }
  }, [app, dq])
  const key = (it: PocketItem) => it.app + ':' + it.id
  const toggle = (it: PocketItem) => setPicked(m => { const n = new Map(m); if (n.has(key(it))) n.delete(key(it)); else n.set(key(it), it); return n })
  const add = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ sources: NotebookSource[]; failed?: string[] }>(`${nbURL(nbId)}/sources/pocket`, { items: [...picked.values()].map(it => ({ app: it.app, id: it.id, title: it.title, url: it.url })) })
      onAdded()
      if (r.failed?.length) toast(`Added ${r.sources.length}; not added: ${r.failed.join('; ')}`, 'err', undefined, 9000)
      else toast(r.sources.length === 1 ? `Added ${r.sources[0].title}` : `Added ${r.sources.length} sources`)
      onClose()
    } catch (e) { toast(errMsg(e), 'err', undefined, 9000) }
    setBusy(false)
  }
  return <Modal title="Add from Pocket" onClose={onClose} footer={<>
    <span className="grow muted small">{picked.size ? `${picked.size} selected` : 'Select one or more'}</span>
    <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
    <button type="button" className="btn primary" disabled={!picked.size || busy} onClick={add}>{busy && <Spinner />}Add {picked.size || ''}</button>
  </>}>
    <div className="stack tight">
      <Segmented size="sm" value={app} onChange={setApp} options={(Object.keys(APP_LABEL) as PocketApp[]).map(a => ({ value: a, label: APP_LABEL[a] }))} label="App" />
      <input className="input" autoFocus placeholder={`Search Pocket ${APP_LABEL[app]}…`} value={q} onChange={e => setQ(e.target.value)} />
      <div className="nb-pick-list">
        {items === null ? <Loading /> : error ? <p className="callout error" role="alert">{error}</p>
          : items.length === 0 ? <p className="muted small center" style={{ padding: 18 }}>{dq ? 'Nothing found.' : 'Type to search.'}</p>
          : items.map(it => (
            <label key={key(it)} className={`nb-pick ${it.attachable ? '' : 'off'}`} title={it.attachable ? undefined : 'Only notes, documents and text files can be added'}>
              <input type="checkbox" disabled={!it.attachable} checked={picked.has(key(it))} onChange={() => toggle(it)} />
              <span className="grow" style={{ minWidth: 0 }}><span className="ellipsis nb-pick-title">{it.title}</span>{it.subtitle && <span className="ellipsis muted xsmall">{it.subtitle}</span>}</span>
            </label>
          ))}
      </div>
    </div>
  </Modal>
}

// ---------- from Pocket Projects ----------

export interface ProjectsPick { kind: 'issue' | 'project'; ref: string; title: string }
interface PickIssue { ref: string; identifier: string; title: string; state_type: string }
interface PickProject { ref: string; name: string; status: string; summary: string }

/** Search the person's issues or projects in Pocket Projects (the workspace open to Pocket AI). */
export function ProjectsPicker({ kind, picked, onPick }: { kind: 'issue' | 'project'; picked: ProjectsPick | null; onPick: (p: ProjectsPick) => void }) {
  const [q, setQ] = useState('')
  const dq = useDebounce(q.trim(), 250)
  const [rows, setRows] = useState<ProjectsPick[] | null>(null)
  const [sub, setSub] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [ws, setWs] = useState('')
  useEffect(() => {
    let live = true
    setRows(null); setError('')
    const done = (list: ProjectsPick[], subs: Record<string, string>, w: string) => { if (live) { setRows(list); setSub(subs); setWs(w) } }
    const fail = (e: unknown) => { if (live) { setRows([]); setError(errMsg(e)) } }
    if (kind === 'issue') api.get<{ workspace: string; issues: PickIssue[] }>(`${V1}/projects-issues${qs({ q: dq || undefined })}`)
      .then(r => done(r.issues.map(i => ({ kind, ref: i.ref, title: `${i.identifier} ${i.title}` })), Object.fromEntries(r.issues.map(i => [i.ref, i.state_type])), r.workspace), fail)
    else api.get<{ workspace: string; projects: PickProject[] }>(`${V1}/projects-projects${qs({ q: dq || undefined })}`)
      .then(r => done(r.projects.map(p => ({ kind, ref: p.ref, title: p.name })), Object.fromEntries(r.projects.map(p => [p.ref, [p.status, p.summary].filter(Boolean).join(' · ')])), r.workspace), fail)
    return () => { live = false }
  }, [kind, dq])
  return <div className="stack tight">
    <input value={q} onChange={e => setQ(e.target.value)} placeholder={kind === 'issue' ? 'Search issues, or type a key like ATH-340' : 'Search projects'} aria-label={kind === 'issue' ? 'Search issues' : 'Search projects'} spellCheck={false} />
    {ws && <div className="muted xsmall">In <strong>{ws}</strong>, the workspace open to Pocket AI in Projects.</div>}
    <div className="nb-pick-list" role="listbox">
      {rows === null ? <Loading /> : error ? <p className="callout error" role="alert">{error}</p>
        : rows.length === 0 ? <p className="muted small center" style={{ padding: 18 }}>Nothing found.</p>
        : rows.map(r => (
          <button type="button" role="option" aria-selected={picked?.ref === r.ref} key={r.ref} className={`nb-pick ${picked?.ref === r.ref ? 'on' : ''}`} onClick={() => onPick(r)}>
            <Icon name={kind === 'issue' ? 'check-square' : 'layers'} size={15} />
            <span className="grow" style={{ minWidth: 0 }}><span className="ellipsis nb-pick-title">{r.title}</span>{sub[r.ref] && <span className="ellipsis muted xsmall">{sub[r.ref]}</span>}</span>
            {picked?.ref === r.ref && <Icon name="check" size={15} />}
          </button>
        ))}
    </div>
  </div>
}

export interface ImportReport { added: number; updated: number; failed: string[] }

/** Bring an issue or project in; the toast says what came. */
export async function importProjects(nbId: number, pick: ProjectsPick, toast: ReturnType<typeof useToast>['toast']): Promise<NotebookFull | null> {
  try {
    const r = await api.post<{ report: ImportReport; notebook: NotebookFull }>(`${nbURL(nbId)}/sources/projects`, { kind: pick.kind, ref: pick.ref })
    reportToast(r.report, toast)
    return r.notebook
  } catch (e) { toast(errMsg(e), 'err', undefined, 9000); return null }
}

export function reportToast(r: ImportReport, toast: ReturnType<typeof useToast>['toast']) {
  const parts = [r.added && `${r.added} added`, r.updated && `${r.updated} updated`].filter(Boolean).join(', ') || 'Nothing new'
  if (r.failed?.length) toast(`${parts}. Not added: ${r.failed.join('; ')}`, 'err', undefined, 10000)
  else toast(parts)
}

function ProjectsDialog({ nbId, onClose, onAdded }: { nbId: number; onClose: () => void; onAdded: () => void }) {
  const [kind, setKind] = useState<'issue' | 'project'>('issue')
  const [pick, setPick] = useState<ProjectsPick | null>(null)
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const add = async () => {
    if (!pick) return
    setBusy(true)
    const nb = await importProjects(nbId, pick, toast)
    setBusy(false)
    if (nb) { onAdded(); onClose() }
  }
  return <Modal title="Add from Pocket Projects" onClose={onClose} footer={<>
    <span className="grow muted small">{kind === 'issue' ? 'Description, sub-issues, comments, shared notes, files and notes about it' : 'Overview, updates, milestones, docs and its issues'}</span>
    <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
    <button type="button" className="btn primary" disabled={!pick || busy} onClick={add}>{busy && <Spinner />}Add</button>
  </>}>
    <div className="stack tight">
      <Segmented size="sm" value={kind} onChange={k => { setKind(k); setPick(null) }} options={[{ value: 'issue', label: 'Issue' }, { value: 'project', label: 'Project' }]} label="What" />
      <ProjectsPicker kind={kind} picked={pick} onPick={setPick} />
    </div>
  </Modal>
}

// ---------- the viewer ----------

export function SourceViewer({ nbId, source: x, onAsk, onClose, onChanged }: {
  nbId: number; source: NotebookSource; onAsk?: (x: NotebookSource) => void; onClose: () => void; onChanged: (gone?: boolean) => void
}) {
  const [text, setText] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [view, setView] = useState<'text' | 'original'>('text')
  const [plain, setPlain] = useState(false)
  const [busy, setBusy] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [title, setTitle] = useState(x.title)
  const [removing, setRemoving] = useState(false)
  const { toast } = useToast()
  const url = `${nbURL(nbId)}/sources/${x.id}`
  const visual = !!x.original && (x.mime === 'application/pdf' || x.mime.startsWith('image/'))
  useEffect(() => {
    let live = true
    setText(null); setError(''); setTitle(x.title); setRenaming(false)
    setView(x.mime.startsWith('image/') ? 'original' : 'text')
    api.get<{ text: string }>(`${url}/text`).then(r => { if (live) setText(r.text) }, e => { if (live) setError(errMsg(e)) })
    return () => { live = false }
  }, [url, x.updated_at]) // eslint-disable-line react-hooks/exhaustive-deps
  const link = sourceLink(x)
  const refresh = async () => {
    setBusy(true)
    try { await api.post(`${url}/refresh`); toast('Read again'); onChanged() } catch (e) { toast(errMsg(e), 'err') }
    setBusy(false)
  }
  const rename = async () => {
    setRenaming(false)
    if (!title.trim() || title.trim() === x.title) { setTitle(x.title); return }
    try { await api.put(url, { title }); onChanged() } catch (e) { toast(errMsg(e), 'err') }
  }
  const menu: MenuItem[] = [
    { label: 'Rename', icon: 'edit', onSelect: () => setRenaming(true) },
    ...(x.kind === 'link' || x.kind === 'note' || x.kind === 'document' || x.kind === 'pocket-file' ? [{ label: 'Read it again', icon: 'refresh', hint: 'Get the latest version', onSelect: refresh }] : []),
    ...(x.kind === 'issue' || x.kind === 'project' || x.kind === 'shared-note' || x.kind === 'project-doc' ? [{ label: 'Update from Pocket Projects', icon: 'refresh', hint: x.kind === 'issue' || x.kind === 'project' ? 'With its notes, docs and files' : 'With the rest of its issue or project', onSelect: refresh }] : []),
    ...(link ? [{ label: x.kind === 'link' ? 'Open the page' : x.kind === 'issue' || x.kind === 'shared-note' ? 'Open the issue' : x.kind === 'project' || x.kind === 'project-doc' ? 'Open the project' : 'Open in Pocket', icon: 'external', onSelect: () => window.open(link, '_blank', 'noopener') }] : []),
    ...(x.original ? [{ label: 'Download the original', icon: 'download', onSelect: () => window.open(`${url}/raw`, '_blank', 'noopener') }] : []),
    'sep',
    { label: 'Remove from notebook', icon: 'trash', danger: true, onSelect: () => setRemoving(true) },
  ]
  const [menuAt, setMenuAt] = useState<Anchor | null>(null)
  return (
    <div className="nb-viewer">
      <div className="nb-viewer-head">
        <button type="button" className="btn ghost icon-only" aria-label="Close the source" title="Close" onClick={onClose}><Icon name="x" size={16} /></button>
        <span className={`nb-source-icon k-${x.kind}`}><Icon name={sourceIcon(x)} size={15} /></span>
        <div className="grow" style={{ minWidth: 0 }}>
          {renaming ? <input className="input sm" autoFocus value={title} onChange={e => setTitle(e.target.value)} onBlur={rename} onKeyDown={e => { if (e.key === 'Enter') void rename(); if (e.key === 'Escape') { setTitle(x.title); setRenaming(false) } }} aria-label="Source name" />
            : <div className="nb-viewer-title ellipsis" onDoubleClick={() => setRenaming(true)}>{x.title}</div>}
          <div className="muted xsmall ellipsis">{sourceSub(x)} · added {ago(x.created_at)}{link && <> · <a href={link} target="_blank" rel="noopener noreferrer">open ↗</a></>}</div>
        </div>
        {busy && <Spinner />}
        <button type="button" className="btn ghost icon-only" aria-label="Source menu" onClick={e => setMenuAt(rectOf(e.currentTarget))}><Icon name="more" size={16} /></button>
      </div>
      {(visual || text !== null) && <div className="nb-viewer-tools">
        {visual && <Segmented size="sm" value={view} onChange={setView} options={[{ value: 'text', label: 'Text' }, { value: 'original', label: x.mime === 'application/pdf' ? 'PDF' : 'Picture' }]} label="View" />}
        {view === 'text' && <button type="button" className={`btn ghost sm ${plain ? 'on' : ''}`} onClick={() => setPlain(!plain)}>{plain ? 'Formatted' : 'Plain text'}</button>}
        <span className="grow" />
        {onAsk && <button type="button" className="btn primary sm" onClick={() => onAsk(x)}><Icon name="sparkles" size={14} />Ask about this</button>}
      </div>}
      <div className="nb-viewer-body">
        {view === 'original' && visual ? (
          x.mime === 'application/pdf'
            ? <iframe className="nb-pdf" src={`${url}/raw`} title={x.title} />
            : <div className="nb-picture"><img src={`${url}/raw`} alt={x.title} /></div>
        ) : error ? <div className="callout error" role="alert">{error}</div>
          : text === null ? <Loading />
          : plain ? <pre className="nb-plain">{text}</pre>
          : <div className="nb-text"><Markdown text={text} /></div>}
      </div>
      {menuAt && <Menu anchor={menuAt} align="right" items={menu} onClose={() => setMenuAt(null)} />}
      {removing && <Confirm title="Remove this source?" message={`“${x.title}” leaves this notebook. Chats in it no longer see it.`} confirmLabel="Remove" onCancel={() => setRemoving(false)}
        onConfirm={async () => { setRemoving(false); try { await api.del(url); onChanged(true) } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

// ---------- beside a notebook chat ----------

/** A notebook chat's side pane: its sources (one open next to the chat;
 * "Ask about this" starts the next message about it) and its other chats. */
export function NotebookPane({ id, threadId, onAsk, onClose }: { id: number; threadId: string; onAsk: (prefix: string) => void; onClose: () => void }) {
  const { nb, error, reload } = useNotebook(id)
  const [open, setOpen] = useState<NotebookSource | null>(null)
  const [tab, setTab] = useState<'sources' | 'chats'>('sources')
  const navigate = useNavigate()
  useEffect(() => { if (open && nb) setOpen(nb.source_list.find(x => x.id === open.id) || null) }, [nb]) // eslint-disable-line react-hooks/exhaustive-deps
  // another chat of it opened (or a new one started): its list follows
  const first = useRef(true)
  useEffect(() => { if (first.current) { first.current = false; return } void reload() }, [threadId]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <aside className="nb-pane" aria-label="Notebook">
      <div className="nb-pane-head">
        <span className="page-ico"><Icon name="book" size={15} /></span>
        <a className="grow ellipsis nb-pane-name" href={`/notebooks/${id}`} onClick={e => { e.preventDefault(); navigate(`/notebooks/${id}`) }}>{nb?.name || 'Notebook'}</a>
        <button type="button" className="btn sm" title="Start another chat in this notebook" onClick={() => navigate(`/notebooks/${id}`)}><Icon name="plus" size={14} />New chat</button>
        <button type="button" className="btn ghost icon-only" aria-label="Close the notebook pane" title="Close" onClick={onClose}><Icon name="x" size={16} /></button>
      </div>
      {nb && !open && <div className="nb-pane-tabs">
        <Segmented size="sm" value={tab} onChange={setTab} label="Show" options={[
          { value: 'sources', label: <>Sources <span className="muted">{nb.source_list.length || ''}</span></> },
          { value: 'chats', label: <>Chats <span className="muted">{nb.chat_list.length || ''}</span></> },
        ]} />
      </div>}
      {error ? <div className="callout error" role="alert" style={{ margin: 12 }}>{error}</div>
        : !nb ? <Loading />
        : open ? <SourceViewer nbId={id} source={open} onClose={() => setOpen(null)} onChanged={gone => { if (gone) setOpen(null); void reload() }} onAsk={x => onAsk(askPrefix(x))} />
        : tab === 'chats' ? <div className="nb-pane-list"><NotebookChats nb={nb} current={threadId} onNew={() => navigate(`/notebooks/${id}`)} /></div>
        : <div className="nb-pane-list"><SourceList nb={nb} onSelect={setOpen} onChanged={reload} /></div>}
    </aside>
  )
}

/** A notebook's chats, newest first; each its own conversation over the same sources. */
export function NotebookChats({ nb, current, onNew }: { nb: NotebookFull; current?: string; onNew?: () => void }) {
  const navigate = useNavigate()
  return <div className="nb-chats">
    {nb.chat_list.length === 0 ? <p className="muted small">No chats yet.</p> : nb.chat_list.map(t => (
      <button type="button" key={t.id} className={`nb-chat ${t.id === current ? 'on' : ''}`} aria-current={t.id === current ? 'page' : undefined} onClick={() => { if (t.id !== current) navigate(`/t/${t.id}`) }}>
        <ThreadStatus t={t} />
        <span className="grow ellipsis">{threadTitle(t)}</span>
        <span className="muted xsmall">{ago(t.activity_at)}</span>
      </button>
    ))}
    {onNew && <button type="button" className="nb-chat nb-chat-new" onClick={onNew}><Icon name="plus" size={14} /><span className="grow">New chat in this notebook</span></button>}
  </div>
}
