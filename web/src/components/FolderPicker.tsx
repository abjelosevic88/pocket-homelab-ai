// Pick the folder an agent starts in: workspaces first, or browse the server
// (or, with host, another machine over ssh).
import { type FormEvent, useEffect, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { baseName, shortPath } from '../lib/format'
import type { DirListing } from '../lib/types'
import { folderHistory, rememberFolder, forgetFolder, type FolderHistory } from '../lib/folder-history'
import { Icon } from './icons'
import { errMsg, Loading, Modal, Spinner, useToast } from './ui'

export function FolderPicker({ value, host = 0, onPick, onClose }: { value: string; host?: number; onPick: (path: string) => void; onClose: () => void }) {
  const { workspaces: all, hosts } = useApp()
  const { toast } = useToast()
  const workspaces = host ? [] : all // folders on this server
  const where = host ? hosts.find(h => h.id === host)?.name : ''
  const [path, setPath] = useState(value || '')
  const [n, setN] = useState(0) // "Try again"
  const [hidden, setHidden] = useState(false)
  const [list, setList] = useState<DirListing | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(true)
  const [typed, setTyped] = useState(value || '')
  // another machine: home and the working folders found there (kept while browsing)
  const [places, setPlaces] = useState<string[]>([])
  const [make, setMake] = useState<'' | 'folder' | 'clone'>('')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState('')
  const [history, setHistory] = useState<FolderHistory | null>(null)
  const [historyError, setHistoryError] = useState('')
  const [saving, setSaving] = useState(false)
  useEffect(() => {
    let live = true
    folderHistory(host).then(h => { if (live) setHistory(h) }).catch(e => { if (live) setHistoryError(errMsg(e)) })
    return () => { live = false }
  }, [host])
  const pick = async (p: string) => {
    setSaving(true); setHistoryError('')
    onPick(p)
    try { await rememberFolder(p, host) }
    catch (e) { toast(`Folder selected. History could not sync: ${errMsg(e)}`, 'err') }
    finally { setSaving(false) }
    onClose()
  }
  const forget = async (p: string) => {
    setSaving(true); setHistoryError('')
    try { setHistory(await forgetFolder(p, host)) }
    catch (e) { setHistoryError(`Could not forget this folder: ${errMsg(e)}`) }
    finally { setSaving(false) }
  }

  useEffect(() => {
    let live = true
    setErr('')
    setLoading(true)
    api.get<DirListing>(`${V1}/fs/dirs${qs({ path, host: host || undefined, hidden: hidden ? 1 : undefined })}`)
      .then(d => { if (live) { setList(d); setTyped(d.path); if (host && d.home) setPlaces([d.home, ...(d.places || [])]) } })
      .catch(e => { if (live) { setErr(errMsg(e)); if (!list) setList({ path, parent: '', dirs: [], roots: [] }) } })
      .finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [path, host, n, hidden]) // eslint-disable-line react-hooks/exhaustive-deps

  // Typing past the current folder filters its list ("…/Projects/po").
  const here = (list?.path || '').replace(/\/$/, '')
  const rest = list && typed.startsWith(here + '/') ? typed.slice(here.length + 1) : ''
  const dirs = (list?.dirs || []).filter(d => !rest || rest.includes('/') || d.name.toLowerCase().includes(rest.toLowerCase()))
  const go = () => {
    const only = rest && !rest.includes('/') && dirs.length === 1 ? dirs[0].path : ''
    setPath(only || typed.trim())
  }

  const create = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!list) return
    const missing = !make
    setBusy(missing ? 'missing' : make)
    try {
      const d = make === 'clone'
        ? await api.post<DirListing>(`${V1}/fs/clone`, { host, path: list.path, url: name.trim() })
        : await api.post<DirListing>(`${V1}/fs/mkdir`, missing ? { host, path: list.missing } : { host, path: list.path, name: name.trim() })
      toast(make === 'clone' ? `Cloned into ${shortPath(d.path)}` : `Made ${shortPath(d.path)}`)
      setMake(''); setName('')
      setList(d); setTyped(d.path); setPath(d.path)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }

  const crumbs = (list?.path || '').split('/').filter(Boolean)
  return (
    <Modal title={where ? `Working folder on ${where}` : 'Working folder'} onClose={onClose} size="lg" className="folder-modal" footer={<>
      <span className="muted small grow ellipsis" title={list?.path}>{shortPath(list?.path)}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!list?.path || !!err || saving || loading} onClick={() => void pick(list!.path)}><span className="hide-narrow">{saving ? 'Saving…' : 'Use this folder'}</span><span className="show-narrow">Use</span></button>
    </>}>
      <div className="stack">
        {historyError && <div className="callout danger" role="alert">{historyError}</div>}
        {!!history?.folders.length && <section className="remembered-folders" aria-label="Remembered folders">
          <div className="row"><strong className="small grow">Remembered folders</strong><span className="muted xsmall">{where || 'This server'}</span></div>
          <div className="remembered-folder-list">
            {history.folders.map(f => <div key={f.path} className="remembered-folder-row">
              <button type="button" className="remembered-folder-pick" disabled={saving} onClick={() => void pick(f.path)} title={f.path}>
                <Icon name="folder" size={17} /><span><strong>{baseName(f.path) || '/'}</strong><small>{shortPath(f.path)}</small></span>
                {f.path === value && <Icon name="check" size={16} />}
              </button>
              <button type="button" className="btn ghost sm" disabled={saving} onClick={() => void forget(f.path)} aria-label={`Forget ${f.path}`} title="Forget shortcut (keeps the folder)"><Icon name="x" size={15} /></button>
            </div>)}
          </div>
          <span className="muted xsmall">Synced with your devices. Forgetting a shortcut keeps its folder and chats.</span>
        </section>}
        <button type="button" className="btn ghost sm" disabled={saving} onClick={() => void pick('')}>Use machine default</button>
        {places.length > 1 && (
          <div className="chips">
            {places.map((p, i) => (
              <button key={p} type="button" className={`chip ${p === list?.path ? 'active' : ''}`} title={p} onClick={() => setPath(p)}>
                <Icon name={i === 0 ? 'home' : 'folder'} size={13} />{i === 0 ? 'Home' : p}
              </button>
            ))}
          </div>
        )}
        {workspaces.length > 0 && (
          <div className="chips">
            {workspaces.map(w => (
              <button key={w.id} type="button" className={`chip ${w.path === list?.path ? 'active' : ''}`} title={w.path} onClick={() => setPath(w.path)}>
                <span className="dot" style={{ background: w.color || 'var(--accent)' }} />{w.name}
              </button>
            ))}
          </div>
        )}
        <form className="row" onSubmit={e => { e.preventDefault(); go() }}>
          <input value={typed} onChange={e => setTyped(e.target.value)} placeholder="/home/…" spellCheck={false} autoCapitalize="off" aria-label="Folder path — type past the folder to filter it" className="mono" />
          <button type="submit" className="btn">Go</button>
        </form>
        <div className="folder-bar">
          <div className="crumbs grow">
            <button type="button" className="link-btn" onClick={() => setPath('/')}>/</button>
            {crumbs.map((c, i) => (
              <span key={i} className="row" style={{ gap: 2 }}>
                <button type="button" className="link-btn" onClick={() => setPath('/' + crumbs.slice(0, i + 1).join('/'))}>{c}</button>
                {i < crumbs.length - 1 && <span className="faint">/</span>}
              </span>
            ))}
          </div>
          <div className="folder-actions">
            <button type="button" className={`btn ghost sm ${make === 'folder' ? 'active' : ''}`} disabled={!list?.path || !!err} onClick={() => { setMake(make === 'folder' ? '' : 'folder'); setName('') }}><Icon name="plus" size={13} />New folder</button>
            <button type="button" className={`btn ghost sm ${make === 'clone' ? 'active' : ''}`} disabled={!list?.path || !!err} onClick={() => { setMake(make === 'clone' ? '' : 'clone'); setName('') }}><Icon name="git-branch" size={13} />Clone</button>
            <button type="button" className={`btn ghost sm icon-only ${hidden ? 'active' : ''}`} aria-pressed={hidden} title={hidden ? 'Hide hidden folders' : 'Show hidden folders'} aria-label="Show hidden folders" onClick={() => setHidden(h => !h)}><Icon name={hidden ? 'eye' : 'eye-off'} size={13} /></button>
          </div>
        </div>
        {make && (
          <form className="row folder-make" onSubmit={create}>
            <input autoFocus value={name} onChange={e => setName(e.target.value)} spellCheck={false} autoCapitalize="off" className={make === 'clone' ? 'mono' : ''}
              placeholder={make === 'clone' ? 'git@github.com:owner/repo.git or https://…' : 'Folder name'} aria-label={make === 'clone' ? 'Repository URL' : 'New folder name'} />
            <button type="submit" className="btn primary" disabled={!name.trim() || !!busy}>{busy === make && <Spinner size={12} />}{make === 'clone' ? 'Clone' : 'Create'}</button>
            <button type="button" className="btn ghost" disabled={!!busy} onClick={() => setMake('')}>Cancel</button>
          </form>
        )}
        {make === 'clone' && <p className="muted xsmall" style={{ margin: '-4px 0 0' }}>Cloned into {shortPath(list?.path)} with the git and keys {where ? `of ${where}` : 'of this server'}; the folder is named after the repository.</p>}
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span className="grow">{err}</span>
          <button type="button" className="btn sm" disabled={loading} onClick={() => setN(x => x + 1)}>{loading && <Spinner size={12} />}Try again</button></div>}
        {list?.missing && !err && <div className="callout info"><Icon name="info" size={15} />
          <span className="grow"><code>{shortPath(list.missing)}</code> does not exist {where ? `on ${where}` : 'here'} — showing the nearest folder that does.</span>
          <button type="button" className="btn sm" disabled={!!busy} onClick={() => create()}>{busy === 'missing' && <Spinner size={12} />}Create it</button></div>}
        {!list ? <Loading /> : (
          <div className="dir-list">
            {list.parent && <button type="button" className="dir-row" onClick={() => setPath(list.parent)}><Icon name="arrow-left" size={15} /><span className="grow">..</span></button>}
            {dirs.length === 0 && !err && <div className="muted small" style={{ padding: 12 }}>{rest ? `No folder here matches “${rest}”.` : 'No folders inside.'}</div>}
            {dirs.map(d => (
              <button key={d.path} type="button" className="dir-row" onClick={() => setPath(d.path)}>
                <Icon name="folder" size={15} /><span className="grow ellipsis">{d.name}</span>
                {d.git && <span className="badge" title="Git repository"><Icon name="git-branch" size={11} /></span>}
                <Icon name="chevron-right" size={14} className="faint" />
              </button>
            ))}
          </div>
        )}
        {host ? <p className="muted xsmall" style={{ margin: 0 }}>Any folder the ssh account can open on {where}.</p>
          : list && list.roots?.length > 0 && <p className="muted xsmall" style={{ margin: 0 }}>Agents may start inside: {list.roots.map(shortPath).join(', ')}</p>}
      </div>
    </Modal>
  )
}

/** The compact "in ~/Conversations" button that opens the picker. */
export function FolderButton({ value, host = 0, onChange, className = 'btn ghost sm' }: { value: string; host?: number; onChange: (p: string) => void; className?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" className={`${className} folder-btn`} title={value || 'Working folder'} onClick={() => setOpen(true)}>
        <Icon name="folder" size={14} /><span className="ellipsis">{value ? baseName(value) : host ? 'Home' : 'Folder'}</span>
      </button>
      {open && <FolderPicker value={value} host={host} onPick={onChange} onClose={() => setOpen(false)} />}
    </>
  )
}
