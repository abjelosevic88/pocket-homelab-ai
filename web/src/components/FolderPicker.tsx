// Pick the folder an agent starts in: workspaces first, or browse the server.
import { useEffect, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { baseName, shortPath } from '../lib/format'
import type { DirListing } from '../lib/types'
import { Icon } from './icons'
import { errMsg, Loading, Modal } from './ui'

export function FolderPicker({ value, onPick, onClose }: { value: string; onPick: (path: string) => void; onClose: () => void }) {
  const { workspaces } = useApp()
  const [path, setPath] = useState(value || '')
  const [list, setList] = useState<DirListing | null>(null)
  const [err, setErr] = useState('')
  const [typed, setTyped] = useState(value || '')

  useEffect(() => {
    let live = true
    setErr('')
    api.get<DirListing>(`${V1}/fs/dirs${qs({ path })}`)
      .then(d => { if (live) { setList(d); setTyped(d.path) } })
      .catch(e => { if (live) { setErr(errMsg(e)); if (!list) setList({ path, parent: '', dirs: [], roots: [] }) } })
    return () => { live = false }
  }, [path]) // eslint-disable-line react-hooks/exhaustive-deps

  const crumbs = (list?.path || '').split('/').filter(Boolean)
  return (
    <Modal title="Working folder" onClose={onClose} size="lg" className="folder-modal" footer={<>
      <span className="muted small grow ellipsis" title={list?.path}>{shortPath(list?.path)}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!list?.path || !!err} onClick={() => { onPick(list!.path); onClose() }}><span className="hide-narrow">Use this folder</span><span className="show-narrow">Use</span></button>
    </>}>
      <div className="stack">
        {workspaces.length > 0 && (
          <div className="chips">
            {workspaces.map(w => (
              <button key={w.id} type="button" className={`chip ${w.path === list?.path ? 'active' : ''}`} title={w.path} onClick={() => setPath(w.path)}>
                <span className="dot" style={{ background: w.color || 'var(--accent)' }} />{w.name}
              </button>
            ))}
          </div>
        )}
        <form className="row" onSubmit={e => { e.preventDefault(); setPath(typed.trim()) }}>
          <input value={typed} onChange={e => setTyped(e.target.value)} placeholder="/home/…" spellCheck={false} autoCapitalize="off" aria-label="Folder path" className="mono" />
          <button type="submit" className="btn">Go</button>
        </form>
        <div className="crumbs">
          <button type="button" className="link-btn" onClick={() => setPath('/')}>/</button>
          {crumbs.map((c, i) => (
            <span key={i} className="row" style={{ gap: 2 }}>
              <button type="button" className="link-btn" onClick={() => setPath('/' + crumbs.slice(0, i + 1).join('/'))}>{c}</button>
              {i < crumbs.length - 1 && <span className="faint">/</span>}
            </span>
          ))}
        </div>
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        {!list ? <Loading /> : (
          <div className="dir-list">
            {list.parent && <button type="button" className="dir-row" onClick={() => setPath(list.parent)}><Icon name="arrow-left" size={15} /><span className="grow">..</span></button>}
            {list.dirs.length === 0 && !err && <div className="muted small" style={{ padding: 12 }}>No folders inside.</div>}
            {list.dirs.map(d => (
              <button key={d.path} type="button" className="dir-row" onClick={() => setPath(d.path)}>
                <Icon name="folder" size={15} /><span className="grow ellipsis">{d.name}</span>
                {d.git && <span className="badge" title="Git repository"><Icon name="git-branch" size={11} /></span>}
                <Icon name="chevron-right" size={14} className="faint" />
              </button>
            ))}
          </div>
        )}
        {list && list.roots?.length > 0 && <p className="muted xsmall" style={{ margin: 0 }}>Agents may start inside: {list.roots.map(shortPath).join(', ')}</p>}
      </div>
    </Modal>
  )
}

/** The compact "in ~/Conversations" button that opens the picker. */
export function FolderButton({ value, onChange, className = 'btn ghost sm' }: { value: string; onChange: (p: string) => void; className?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" className={`${className} folder-btn`} title={value || 'Working folder'} onClick={() => setOpen(true)}>
        <Icon name="folder" size={14} /><span className="ellipsis">{value ? baseName(value) : 'Folder'}</span>
      </button>
      {open && <FolderPicker value={value} onPick={onChange} onClose={() => setOpen(false)} />}
    </>
  )
}
