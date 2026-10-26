import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { Confirm, copyText, Empty, Field, Loading, Modal, Spinner, errMsg, noGrammarly, useAsync, useToast } from './ui'

export type Snippet = { id: number; title: string; body: string }
type Draft = { id?: number; title: string; body: string }

/** Shared by the library and composer; insertion always leaves sending to the person. */
export function Snippets({ onInsert }: { onInsert?: (snippet: Snippet) => void }) {
  const list = useAsync(() => api.get<Snippet[]>(`${V1}/snippets`))
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<Snippet | null>(null)
  const [deleting, setDeleting] = useState(false)
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
    <div className="list-cards">{list.data?.map(s => <div className="list-card" key={s.id}>
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
    {onInsert && <p className="muted small">Insert adds text to your draft. You choose when to send.</p>}
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
