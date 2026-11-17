import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { Confirm, copyText, Empty, Field, Loading, Modal, Spinner, errMsg, noGrammarly, useAsync, useToast } from './ui'

export type Snippet = { id: number; title: string; body: string }
type Draft = { id?: number; title: string; body: string }

/** Every word of the query in the title or the text, any case; the title matches rank first. */
export function searchSnippets<T extends Snippet>(list: T[], query: string): T[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return list
  const hits = list.filter(s => { const all = (s.title + '\n' + s.body).toLowerCase(); return words.every(w => all.includes(w)) })
  const inTitle = (s: T) => words.every(w => s.title.toLowerCase().includes(w))
  return [...hits.filter(inTitle), ...hits.filter(s => !inTitle(s))]
}

let cache: { at: number; list: Snippet[] } | null = null
/** Snippets changed here: the composer's "/" menu asks again. */
function forgetSnippets() { cache = null }

/** The person's snippets for the composer's "/" menu, fetched when it opens (cached 30 seconds). */
export function useSnippetList(wanted: boolean): Snippet[] {
  const [list, setList] = useState<Snippet[]>(() => cache?.list ?? [])
  useEffect(() => {
    if (!wanted) return
    if (cache) setList(cache.list)
    if (cache && Date.now() - cache.at < 30_000) return
    let live = true
    api.get<Snippet[]>(`${V1}/snippets`).then(r => {
      cache = { at: Date.now(), list: r || [] }
      if (live) setList(cache.list)
    }).catch(() => {})
    return () => { live = false }
  }, [wanted])
  return list
}

/** Shared by the library and composer; insertion always leaves sending to the person. */
export function Snippets({ onInsert }: { onInsert?: (snippet: Snippet) => void }) {
  const list = useAsync(() => api.get<Snippet[]>(`${V1}/snippets`).then(r => { forgetSnippets(); return r }))
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<Snippet | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [query, setQuery] = useState('')
  const shown = list.data ? searchSnippets(list.data, query) : null
  // in the composer the search takes the keys at once (not on phones: the keyboard would cover the list)
  const focusSearch = !!onInsert && window.matchMedia?.('(pointer: fine)').matches
  const { toast } = useToast()
  const remove = async () => {
    if (!del || deleting) return
    setDeleting(true)
    try { await api.del(`${V1}/snippets/${del.id}`); setDel(null); await list.reload() }
    catch (e) { toast(errMsg(e), 'err') } finally { setDeleting(false) }
  }
  return <div className="stack">
    <div className="row"><span className="grow muted small">Saved to your server account.</span>
      <button type="button" className="btn primary" onClick={() => setEdit({ title: '', body: '' })}><Icon name="plus" size={14} />New snippet</button></div>
    {list.error && <div className="callout error" role="alert"><span className="grow">{list.error}</span><button type="button" className="btn sm" onClick={list.reload}>Try again</button></div>}
    {list.loading && !list.data && <Loading />}
    {list.data?.length === 0 && <Empty icon="bookmark" title="No snippets yet">Save text you reuse in chats.</Empty>}
    {!!list.data?.length && <div className="search-box snippet-search">
      <Icon name="search" size={15} />
      <input type="search" autoFocus={focusSearch} aria-label="Search snippets" placeholder={`Search ${list.data.length} ${list.data.length === 1 ? 'snippet' : 'snippets'}…`} value={query}
        onChange={e => setQuery(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Escape' && query) { e.preventDefault(); e.stopPropagation(); setQuery('') }
          else if (e.key === 'Enter' && onInsert && shown?.[0]) { e.preventDefault(); onInsert(shown[0]) }
        }} />
    </div>}
    {query.trim() && shown?.length === 0 && <p className="muted small">No snippet matches “{query.trim()}”.</p>}
    <div className="list-cards">{shown?.map(s => <div className="list-card" key={s.id}>
      <div className="grow" style={{ minWidth: 0 }}><div className="list-card-title">{s.title}</div><div className="list-card-sub" style={{ whiteSpace: 'pre-wrap' }}>{s.body}</div></div>
      <div className="row wrap">
        <button type="button" className="btn sm" aria-label={`${onInsert ? 'Insert' : 'Copy'} snippet: ${s.title}`} onClick={async () => {
          if (onInsert) onInsert(s)
          else if (await copyText(s.body)) toast('Snippet copied')
          else toast('Could not copy the snippet', 'err')
        }}><Icon name={onInsert ? 'plus' : 'copy'} size={14} />{onInsert ? 'Insert' : 'Copy'}</button>
        <button type="button" className="btn ghost icon-only" aria-label={`Edit snippet: ${s.title}`} onClick={() => setEdit({ ...s })}><Icon name="edit" size={15} /></button>
        <button type="button" className="btn ghost icon-only" aria-label={`Delete snippet: ${s.title}`} onClick={() => setDel(s)}><Icon name="trash" size={15} /></button>
      </div>
    </div>)}</div>
    {onInsert && <p className="muted small">Insert adds text to your draft (Enter in the search inserts the first one). You choose when to send. Or type “$” and a word anywhere in your message.</p>}
    {edit && <SnippetEditor draft={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); void list.reload() }} />}
    {del && <Confirm title={`Delete “${del.title}”?`} message={deleting ? 'Deleting…' : 'Text already inserted into a draft or chat is kept.'} onCancel={() => { if (!deleting) setDel(null) }} onConfirm={remove} />}
  </div>
}

function SnippetEditor({ draft, onClose, onSaved }: { draft: Draft; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(draft)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = !!value.title.trim() && !!value.body.trim() && [...value.title.trim()].length <= 120 && new TextEncoder().encode(value.body).length <= 65536
  const save = async () => {
    if (!valid || busy) return
    setBusy(true); setError('')
    try {
      const body = { title: value.title.trim(), body: value.body }
      if (value.id) await api.put(`${V1}/snippets/${value.id}`, body)
      else await api.post(`${V1}/snippets`, body)
      onSaved()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <Modal title={value.id ? 'Edit snippet' : 'New snippet'} onClose={() => { if (!busy) onClose() }} footer={<>
    <button type="button" className="btn" disabled={busy} onClick={onClose}>Cancel</button>
    <button type="button" className="btn primary" disabled={!valid || busy} onClick={save}>{busy && <Spinner />}Save snippet</button>
  </>}>
    <div className="stack">
      <Field label="Title" help="Up to 120 characters."><input autoFocus aria-label="Snippet title" disabled={busy} value={value.title} onChange={e => setValue({ ...value, title: e.target.value })} /></Field>
      <Field label="Text" help="Up to 64 KB. Formatting is preserved."><textarea {...noGrammarly} aria-label="Snippet text" rows={9} disabled={busy} value={value.body} onChange={e => setValue({ ...value, body: e.target.value })} /></Field>
      {error && <p className="callout error" role="alert">{error}</p>}
    </div>
  </Modal>
}
