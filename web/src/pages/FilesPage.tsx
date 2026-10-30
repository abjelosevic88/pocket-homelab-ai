// Browse the files of this server or one of your machines, upload into a
// folder (button or drop), open a file in a tab.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtBytes } from '../lib/format'
import { type FileEntry, type FileListing, fileIcon, filePath, filesPath, joinPath, rawURL, uploadFiles } from '../lib/files'
import { useTerminals } from '../lib/terminals'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Select } from '../components/Select'
import { type Anchor, Confirm, copyText, download, errMsg, Loading, Menu, type MenuItem, Modal, rectOf, Spinner, useContextMenu, useToast } from '../components/ui'

const HIDDEN = 'pai-files-hidden'

export default function FilesPage({ onMenu }: { onMenu?: () => void }) {
  const { hosts } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const term = useTerminals()
  const [params] = useSearchParams()
  const host = Number(params.get('host')) || 0
  const want = params.get('path') || ''
  const [data, setData] = useState<FileListing | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const [hidden, setHidden] = useState(() => localStorage.getItem(HIDDEN) === '1')
  const [editing, setEditing] = useState<string | null>(null)
  const [drop, setDrop] = useState(false)
  const [upload, setUpload] = useState<{ n: number; p: number } | null>(null)
  const [rowMenu, setRowMenu] = useState<{ items: MenuItem[]; at: Anchor } | null>(null)
  const input = useRef<HTMLInputElement>(null)

  const go = useCallback((h: number, p: string) => navigate(filesPath(h, p)), [navigate])
  const load = useCallback(() => {
    setLoading(true); setErr('')
    api.get<FileListing>(`${V1}/files?host=${host}&path=${encodeURIComponent(want)}`)
      .then(d => { setData(d); setLoading(false) })
      .catch(e => { setErr(errMsg(e)); setLoading(false) })
  }, [host, want])
  useEffect(() => { load() }, [load])

  const send = async (files: File[]) => {
    if (!files.length || !data) return
    setUpload({ n: files.length, p: 0 })
    try {
      const paths = await uploadFiles(host, data.path, files, p => setUpload({ n: files.length, p }))
      toast(paths.length === 1 ? `Uploaded ${paths[0].split('/').pop()}` : `Uploaded ${paths.length} files`, 'ok')
      load()
    } catch (e) { toast(errMsg(e), 'err') }
    setUpload(null)
  }

  const machines = [{ value: 0, label: 'This server' }, ...hosts.map(h => ({ value: h.id, label: h.name }))]
  const crumbs = data ? data.path.split('/').filter(Boolean).map((part, i, all) => ({ part, path: '/' + all.slice(0, i + 1).join('/') })) : []
  const shown = (data?.entries || []).filter(e => hidden || !e.name.startsWith('.'))
  const open = (e: FileEntry) => { if (!data) return; const p = joinPath(data.path, e.name); if (e.dir) go(host, p); else navigate(filePath(host, p)) }
  const openTerminal = async (dir = data?.path) => { if (!dir) return; const g = await term.open(host, dir); if (g) navigate(`/term/${g}`) }

  // right-click (or ⋯ on a row): new folder or file, upload, rename, delete…
  const ctx = useContextMenu()
  const [naming, setNaming] = useState<{ op: 'mkdir' | 'newfile' | 'rename'; entry?: FileEntry } | null>(null)
  const [deleting, setDeleting] = useState<FileEntry | null>(null)
  // '' = done; else what went wrong (the name dialog shows it in place)
  const change = async (op: string, path: string, name = '', quiet = false) => {
    try {
      await api.post(`${V1}/files/${op}`, { host, path, name })
      load()
      return ''
    } catch (e) { if (!quiet) toast(errMsg(e), 'err'); return errMsg(e) || 'It did not work' }
  }
  const copy = (s: string) => copyText(s).then(ok => toast(ok ? 'Path copied' : 'Could not copy', ok ? 'ok' : 'err'))
  const toggleHidden = () => { setHidden(!hidden); localStorage.setItem(HIDDEN, hidden ? '0' : '1') }
  const folderItems = (): MenuItem[] => !data ? [] : [
    { label: 'New folder…', icon: 'folder', onSelect: () => setNaming({ op: 'mkdir' }) },
    { label: 'New file…', icon: 'file', onSelect: () => setNaming({ op: 'newfile' }) },
    { label: 'Upload files…', icon: 'upload', onSelect: () => input.current?.click() },
    'sep',
    { label: 'Open a terminal here', icon: 'terminal', onSelect: () => openTerminal() },
    { label: 'Copy folder path', icon: 'copy', onSelect: () => copy(data.path) },
    { label: hidden ? 'Hide hidden files' : 'Show hidden files', icon: hidden ? 'eye-off' : 'eye', onSelect: toggleHidden },
    { label: 'Reload', icon: 'refresh', onSelect: load },
  ]
  const entryItems = (e: FileEntry): MenuItem[] => {
    if (!data) return []
    const p = joinPath(data.path, e.name)
    return [
      { label: e.dir ? 'Open folder' : 'Open in a tab', icon: e.dir ? 'folder' : 'file', onSelect: () => open(e) },
      ...(e.dir ? [{ label: 'Open a terminal here', icon: 'terminal', onSelect: () => openTerminal(p) }]
        : [{ label: 'Download', icon: 'download', onSelect: () => download(rawURL(host, p, true)) }]),
      { label: 'Copy path', icon: 'copy', onSelect: () => copy(p) },
      'sep',
      { label: 'Rename…', icon: 'edit', onSelect: () => setNaming({ op: 'rename', entry: e }) },
      { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDeleting(e) },
      'sep',
      { label: 'New folder…', icon: 'folder', onSelect: () => setNaming({ op: 'mkdir' }) },
      { label: 'Upload files…', icon: 'upload', onSelect: () => input.current?.click() },
    ]
  }

  return (
    <div className={`page files-page ${drop ? 'dropping' : ''}`}
      onDragOver={e => { if (e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDrop(true) } }}
      onDragLeave={e => { if (e.currentTarget === e.target) setDrop(false) }}
      onDrop={e => { e.preventDefault(); setDrop(false); send([...e.dataTransfer.files]) }}>
      <PageHead title="Files" icon="folder" onMenu={onMenu}
        sub={data ? `${data.host} · ${data.path}` : 'Files on this server or any of your machines'}
        actions={<>
          <button type="button" className="btn ghost icon-only" title="Open a terminal here" aria-label="Open a terminal here" disabled={!data} onClick={() => openTerminal()}><Icon name="terminal" size={16} /></button>
          <button type="button" className="btn ghost icon-only" title="Reload" aria-label="Reload" onClick={load}><Icon name="refresh" size={16} /></button>
          <button type="button" className="btn primary sm" disabled={!data || !!upload} onClick={() => input.current?.click()}>
            {upload ? <><Spinner /> {Math.round(upload.p * 100)}%</> : <><Icon name="upload" size={15} /> Upload</>}
          </button>
          <input ref={input} type="file" multiple hidden onChange={e => { send([...(e.target.files || [])]); e.target.value = '' }} />
        </>} />
      <div className="files-bar">
        <Select value={host} options={machines} onChange={h => go(h, '')} label="Machine" size="sm" className="files-host" />
        {editing !== null
          ? <input className="files-path-in mono" autoFocus value={editing} aria-label="Folder path" onChange={e => setEditing(e.target.value)}
              onBlur={() => setEditing(null)}
              onKeyDown={e => { if (e.key === 'Enter') { go(host, editing.trim()); setEditing(null) } if (e.key === 'Escape') setEditing(null) }} />
          : <div className="files-crumbs" onDoubleClick={() => setEditing(data?.path || '')} title="Double-click to type a path">
              <button type="button" className="files-crumb" disabled={!data?.parent} onClick={() => data?.parent && go(host, data.parent)} title="Up" aria-label="Up"><Icon name="arrow-up" size={14} /></button>
              <button type="button" className="files-crumb" onClick={() => go(host, data?.home || '')} title="Home" aria-label="Home"><Icon name="home" size={14} /></button>
              <button type="button" className="files-crumb mono" onClick={() => go(host, '/')}>/</button>
              {crumbs.map((c, i) => <span key={c.path} className="row" style={{ gap: 0 }}>
                {i > 0 && <span className="files-sep">/</span>}
                <button type="button" className="files-crumb mono" onClick={() => go(host, c.path)}>{c.part}</button>
              </span>)}
              <button type="button" className="files-crumb" onClick={() => setEditing(data?.path || '')} title="Type a path" aria-label="Type a path"><Icon name="edit" size={13} /></button>
            </div>}
        <button type="button" className={`btn ghost sm ${hidden ? 'on' : ''}`} onClick={() => { setHidden(!hidden); localStorage.setItem(HIDDEN, hidden ? '0' : '1') }} title="Show hidden files">
          <Icon name={hidden ? 'eye' : 'eye-off'} size={14} /> Hidden
        </button>
      </div>
      <div className="page-scroll">
        {err ? <div className="page-inner"><div className="files-err"><Icon name="alert" size={16} /> {err}</div></div>
          : !data ? <Loading />
          : <div className="files-list" aria-busy={loading} onContextMenu={e => ctx.open(e, folderItems)}>
              {shown.length === 0 && <div className="files-empty">This folder is empty{!hidden && data.entries.length ? ' (except hidden files)' : ''}. Drop files here to upload them.</div>}
              {shown.map(e => (
                <div key={e.name} role="button" tabIndex={0} className={`files-row ${e.dir ? 'dir' : ''}`} onClick={() => open(e)}
                  onKeyDown={k => { if (k.key === 'Enter') open(e); if (k.key === 'F2') setNaming({ op: 'rename', entry: e }); if (k.key === 'Delete') setDeleting(e) }}
                  onContextMenu={m => ctx.open(m, () => entryItems(e))}>
                  <Icon name={fileIcon(e.name, e.dir)} size={16} />
                  <span className="files-name">{e.name}{e.link && <span className="files-link" title="Symbolic link"> ↗</span>}</span>
                  <span className="files-size">{e.dir ? '' : fmtBytes(e.size)}</span>
                  <span className="files-time" title={e.mtime ? new Date(e.mtime * 1000).toLocaleString() : ''}>{e.mtime ? ago(new Date(e.mtime * 1000).toISOString()) : ''}</span>
                  <button type="button" className="files-more" aria-label={`More for ${e.name}`} title="More"
                    onClick={m => { m.stopPropagation(); setRowMenu({ items: entryItems(e), at: rectOf(m.currentTarget) }) }}><Icon name="more" size={15} /></button>
                </div>
              ))}
              {data.more && <div className="files-empty">Only the first 3000 entries are shown.</div>}
            </div>}
      </div>
      {ctx.menu}
      {rowMenu && <Menu anchor={rowMenu.at} items={rowMenu.items} align="right" onClose={() => setRowMenu(null)} />}
      {naming && data && <NameDialog op={naming.op} initial={naming.entry?.name || ''} dir={!!naming.entry?.dir} onClose={() => setNaming(null)}
        taken={data.entries.filter(e => e.name !== naming.entry?.name)}
        onOpen={e => { setNaming(null); open(e) }}
        onDone={async name => {
          const why = naming.op === 'rename' ? await change('rename', joinPath(data.path, naming.entry!.name), name, true) : await change(naming.op, data.path, name, true)
          if (!why) setNaming(null)
          return why
        }} />}
      {deleting && data && <Confirm title={`Delete ${deleting.dir ? 'folder' : 'file'}?`} confirmLabel="Delete"
        message={<><b>{deleting.name}</b>{deleting.dir ? ' and everything in it' : ''} will be deleted from {data.host}. This cannot be undone.</>}
        onCancel={() => setDeleting(null)} onConfirm={async () => { const e = deleting; setDeleting(null); if (!(await change('delete', joinPath(data.path, e.name)))) toast(`Deleted ${e.name}`, 'ok') }} />}
      {drop && <div className="files-drop"><Icon name="upload" size={28} /><b>Drop to upload</b><span className="mono">{data?.path}</span></div>}
    </div>
  )
}

// a name for a new folder or file, or a new name (the name without its
// extension is selected, like Finder). A name already in the folder is
// said right under the field (with a way to open that one), not after Create.
function NameDialog({ op, initial, dir, taken, onOpen, onClose, onDone }: {
  op: 'mkdir' | 'newfile' | 'rename'; initial: string; dir: boolean; taken: FileEntry[]
  onOpen: (e: FileEntry) => void; onClose: () => void; onDone: (name: string) => Promise<string>
}) {
  const [v, setV] = useState(initial)
  const [busy, setBusy] = useState(false)
  const [why, setWhy] = useState('')
  const title = op === 'mkdir' ? 'New folder' : op === 'newfile' ? 'New file' : `Rename ${dir ? 'folder' : 'file'}`
  const name = v.trim()
  const clash = taken.find(e => e.name === name)
  const bad = !name || name.includes('/') || name === '.' || name === '..' || !!clash
  const save = async () => { if (bad || busy) return; setBusy(true); setWhy(await onDone(name)); setBusy(false) }
  return (
    <Modal title={title} size="sm" onClose={onClose}
      footer={<><button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
        <button type="button" className="btn primary" disabled={bad || busy || (op === 'rename' && name === initial)} onClick={save}>{busy ? <Spinner /> : op === 'rename' ? 'Rename' : 'Create'}</button></>}>
      <input autoFocus aria-label="Name" aria-invalid={!!clash || !!why || undefined} style={{ width: '100%' }} value={v} placeholder={op === 'mkdir' ? 'Folder name' : 'File name'}
        onFocus={e => { const i = op === 'rename' && !dir ? v.lastIndexOf('.') : -1; e.currentTarget.setSelectionRange(0, i > 0 ? i : v.length) }}
        onChange={e => { setV(e.target.value); setWhy('') }} onKeyDown={e => { if (e.key === 'Enter') save() }} />
      {clash ? <p className="files-name-why">
          A {clash.dir ? 'folder' : 'file'} called <b>{clash.name}</b> is already here.{' '}
          <button type="button" className="link-btn" onClick={() => onOpen(clash)}>Open it</button>{' '}or pick another name.
        </p>
        : name.includes('/') ? <p className="files-name-why">Use a plain name, without slashes.</p>
        : why ? <p className="files-name-why">{why}</p> : null}
    </Modal>
  )
}
