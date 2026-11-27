// Pick things from the person's other Pocket apps (a note, a document, a
// task…) as context: one search across the apps, or inside one of them.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { AppIcon, ItemThumb, useMentionApps, type PocketItem } from './Composer'
import { Icon } from './icons'
import { errMsg, Modal, Spinner, useDebounce } from './ui'

export type PickedItem = Pick<PocketItem, 'app' | 'id' | 'title' | 'subtitle' | 'url' | 'attachable'>
const same = (a: PickedItem, b: PickedItem) => a.app === b.app && a.id === b.id

/** The apps one search covers when none is picked (server: mentionApps). */
const MAIN = ['notes', 'tasks', 'projects', 'documents', 'files', 'contacts']

export function PocketPicker({ picked, onToggle, onClose, max = 8 }: { picked: PickedItem[]; onToggle: (it: PickedItem) => void; onClose: () => void; max?: number }) {
  const { apps, off } = useMentionApps(true)
  const [app, setApp] = useState('')
  const [q, setQ] = useState('')
  const term = useDebounce(q.trim(), 250)
  const [items, setItems] = useState<PocketItem[] | null>(null)
  const [err, setErr] = useState('')
  const idle = !app && !term
  useEffect(() => {
    if (idle) { setItems(null); setErr(''); return }
    let live = true
    setItems(null); setErr('')
    api.get<{ items: PocketItem[] }>(`${V1}/pocket/items?q=${encodeURIComponent(term)}${app ? `&app=${encodeURIComponent(app)}` : ''}`)
      .then(r => { if (live) setItems(r.items || []) })
      .catch(e => { if (live) { setItems([]); setErr(errMsg(e)) } })
    return () => { live = false }
  }, [app, term, idle])
  // the apps worth searching: the ones one search covers first, then the rest that can be read
  const list = (apps || []).filter(a => a.reads > 0).sort((a, b) => (MAIN.indexOf(a.slug) + 1 || 99) - (MAIN.indexOf(b.slug) + 1 || 99) || a.name.localeCompare(b.name))
  const full = picked.length >= max
  return (
    <Modal title="Add from your apps" onClose={onClose} className="pp-modal" footer={<>
      <span className="muted small grow">{picked.length ? `${picked.length} picked` : 'Notes, documents and text files go in whole; other items by title and link.'}</span>
      <button type="button" className="btn primary" onClick={onClose}>Done</button>
    </>}>
      <div className="stack tight">
        <div className="pp-search"><Icon name="search" size={15} /><input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder={app ? `Search ${list.find(a => a.slug === app)?.name || app}…` : 'Search notes, tasks, projects, documents, files, contacts…'} aria-label="Search your apps" /></div>
        {off ? <div className="callout warn"><Icon name="alert" size={15} /><span>Pocket Ask is not connected, so your apps cannot be searched from here. {off.url && <a href={off.url} target="_blank" rel="noreferrer">Turn it on</a>}</span></div> : (
          <div className="chip-row pp-apps">
            <button type="button" className={`chip sm ${app === '' ? 'on' : ''}`} onClick={() => setApp('')}>All</button>
            {list.map(a => <button key={a.slug} type="button" className={`chip sm ${app === a.slug ? 'on' : ''}`} onClick={() => setApp(a.slug)}><img src={`/app-icons/${a.slug}.svg`} alt="" width={14} height={14} onError={e => { (e.target as HTMLImageElement).style.display = 'none' }} />{a.name.replace(/^Pocket /, '')}</button>)}
          </div>
        )}
        <div className="pp-list" role="listbox" aria-label="What was found">
          {idle ? <div className="pp-hint muted small">Type to search everything, or pick an app to see what is in it.</div>
            : !items ? <div className="pp-hint"><Spinner /></div>
            : err ? <div className="pp-hint muted small">{err}</div>
            : items.length === 0 ? <div className="pp-hint muted small">Nothing found{term ? ` for “${term}”` : ''}.</div>
            : items.map(it => {
              const on = picked.some(p => same(p, it))
              return (
                <button key={it.app + '/' + it.id} type="button" role="option" aria-selected={on} className={`pp-row ${on ? 'on' : ''}`} disabled={!on && full} onClick={() => onToggle(it)}>
                  {it.thumb ? <ItemThumb src={it.thumb} app={it.app} /> : <AppIcon slug={it.app} />}
                  <span className="grow pp-text"><span className="pp-title ellipsis">{it.title}</span>{it.subtitle && <span className="pp-sub ellipsis">{it.subtitle}</span>}</span>
                  {!it.attachable && <span className="pp-kind" title="Only its title and link go to the models; they read the rest with their Pocket tools">link</span>}
                  <span className="pp-check">{on && <Icon name="check" size={15} />}</span>
                </button>
              )
            })}
        </div>
        {full && <div className="muted xsmall">That is the most a panel takes ({max}).</div>}
      </div>
    </Modal>
  )
}

/** The picked items as chips, each removable. */
export function PickedChips({ items, onRemove }: { items: PickedItem[]; onRemove: (it: PickedItem) => void }) {
  if (!items.length) return null
  return (
    <div className="pp-chips">
      {items.map(it => (
        <span key={it.app + '/' + it.id} className="pp-chip" title={it.attachable ? 'Its whole text goes to the models' : 'Its title and link go to the models'}>
          <img src={`/app-icons/${it.app}.svg`} alt="" width={14} height={14} onError={e => { (e.target as HTMLImageElement).style.display = 'none' }} />
          <span className="ellipsis">{it.title}</span>
          <button type="button" aria-label={`Remove ${it.title}`} onClick={() => onRemove(it)}><Icon name="x" size={12} /></button>
        </span>
      ))}
    </div>
  )
}
