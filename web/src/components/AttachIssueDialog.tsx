// File a conversation under a Pocket Projects issue after the fact: the issue's
// Code panel then lists it and the sidebar groups it under the issue. Searches
// the workspace open to Pocket AI in Projects (open issues, latest first, until
// something is typed).
import { useEffect, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { refKey } from '../lib/projects'
import { Icon } from './icons'
import { errMsg, Loading, Modal, useDebounce, useToast } from './ui'

interface PickIssue { ref: string; identifier: string; title: string; state_type: string }
export interface AttachedContext { source: string; ref: string; ref_url: string; ref_label: string; unpinned?: boolean }

const closed = (st: string) => st === 'completed' || st === 'canceled'

export function AttachIssueDialog({ threadId, current, onClose, onAttached }: { threadId: string; current?: string; onClose: () => void; onAttached: (c: AttachedContext) => void }) {
  const { toast } = useToast()
  const [q, setQ] = useState('')
  const query = useDebounce(q.trim(), 250)
  const [list, setList] = useState<{ workspace: string; issues: PickIssue[] } | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  useEffect(() => {
    let live = true
    setErr('')
    api.get<{ workspace: string; issues: PickIssue[] }>(`${V1}/projects-issues${qs({ q: query || undefined })}`)
      .then(v => { if (live) setList(v) })
      .catch(e => { if (live) { setErr(errMsg(e)); setList({ workspace: '', issues: [] }) } })
    return () => { live = false }
  }, [query])

  const attach = async (ref: string) => {
    setBusy(ref)
    try {
      const c = await api.post<AttachedContext>(`${V1}/threads/${threadId}/attach`, { ref })
      onAttached(c)
      toast(`Attached to ${refKey(c.ref)}`, 'ok')
      onClose()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  // a key typed in full (AS-7) can be attached even if it isn't in the list
  const typedKey = /^[A-Za-z][A-Za-z0-9]{0,9}-[0-9]{1,9}$/.test(q.trim()) ? q.trim().toUpperCase() : ''
  const typedRef = typedKey && list?.workspace ? `${list.workspace}/${typedKey}` : typedKey
  const listed = !!list?.issues.some(i => i.ref.toUpperCase() === typedRef.toUpperCase())

  return (
    <Modal title={current ? 'Move to another issue' : 'Attach to an issue'} onClose={onClose} size="xl">
      <div className="stack attach-issue">
        <p className="muted small">The issue’s Code panel in Pocket Projects lists this conversation, and pull requests opened here are attached to it.{list?.workspace ? <> Searching <strong>{list.workspace}</strong>, the workspace open in Projects.</> : null}</p>
        <input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Search issues, or type a key like AS-7" aria-label="Search issues" spellCheck={false} />
        {err && <div className="callout danger" role="alert">{err}</div>}
        {list === null ? <Loading /> : (
          <div className="dir-list" role="list">
            {typedRef && !listed && <button type="button" className="dir-row" disabled={!!busy} onClick={() => void attach(typedRef)}>
              <Icon name="link" size={15} /><span className="grow ellipsis">Attach to <strong>{typedKey}</strong></span>
            </button>}
            {list.issues.map(i => (
              <button key={i.ref} type="button" role="listitem" className={`dir-row ${closed(i.state_type) ? 'faint' : ''}`} disabled={!!busy || i.ref === current} onClick={() => void attach(i.ref)} title={`${i.ref} · ${i.title}`}>
                <span className={`attach-state st-${i.state_type || 'unstarted'}`} aria-hidden />
                <span className="mono small attach-key">{i.identifier}</span>
                <span className="grow attach-title">{i.title}</span>
                {i.ref === current && <Icon name="check" size={15} />}
                {busy === i.ref && <span className="spinner" />}
              </button>
            ))}
            {!list.issues.length && !typedRef && !err && <div className="muted small center" style={{ padding: 14 }}>{query ? 'No issues match.' : 'No open issues.'}</div>}
          </div>
        )}
      </div>
    </Modal>
  )
}
