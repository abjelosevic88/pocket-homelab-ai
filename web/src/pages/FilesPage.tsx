// Browse the files of this server or one of your machines, upload into a
// folder (button or drop), open a file in a tab.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtBytes } from '../lib/format'
import { type FileEntry, type FileListing, fileIcon, filePath, filesPath, joinPath, uploadFiles } from '../lib/files'
import { useTerminals } from '../lib/terminals'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Select } from '../components/Select'
import { errMsg, Loading, Spinner, useToast } from '../components/ui'

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
  const openTerminal = async () => { if (!data) return; const g = await term.open(host, data.path); if (g) navigate(`/term/${g}`) }

  return (
    <div className={`page files-page ${drop ? 'dropping' : ''}`}
      onDragOver={e => { if (e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDrop(true) } }}
      onDragLeave={e => { if (e.currentTarget === e.target) setDrop(false) }}
      onDrop={e => { e.preventDefault(); setDrop(false); send([...e.dataTransfer.files]) }}>
      <PageHead title="Files" icon="folder" onMenu={onMenu}
        sub={data ? `${data.host} · ${data.path}` : 'Files on this server or any of your machines'}
        actions={<>
          <button type="button" className="btn ghost icon-only" title="Open a terminal here" aria-label="Open a terminal here" disabled={!data} onClick={openTerminal}><Icon name="terminal" size={16} /></button>
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
          : <div className="files-list" aria-busy={loading}>
              {shown.length === 0 && <div className="files-empty">This folder is empty{!hidden && data.entries.length ? ' (except hidden files)' : ''}. Drop files here to upload them.</div>}
              {shown.map(e => (
                <button key={e.name} type="button" className={`files-row ${e.dir ? 'dir' : ''}`} onClick={() => open(e)}>
                  <Icon name={fileIcon(e.name, e.dir)} size={16} />
                  <span className="files-name">{e.name}{e.link && <span className="files-link" title="Symbolic link"> ↗</span>}</span>
                  <span className="files-size">{e.dir ? '' : fmtBytes(e.size)}</span>
                  <span className="files-time" title={e.mtime ? new Date(e.mtime * 1000).toLocaleString() : ''}>{e.mtime ? ago(new Date(e.mtime * 1000).toISOString()) : ''}</span>
                </button>
              ))}
              {data.more && <div className="files-empty">Only the first 3000 entries are shown.</div>}
            </div>}
      </div>
      {drop && <div className="files-drop"><Icon name="upload" size={28} /><b>Drop to upload</b><span className="mono">{data?.path}</span></div>}
    </div>
  )
}
