import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Link, useNavigate } from 'react-router-dom'
import { errMsg, useToast } from './ui'
import type { AgentRequest, Thread } from '../lib/types'

export const friendly = (s: string) => s.replace(/^pocket_/, '').replace(/_/g, ' ').replace(/^./, c => c.toUpperCase())
export const safeURL = (s: unknown): s is string => typeof s === 'string' && /^https?:\/\//i.test(s)
export interface AppCall { id: string; thread_id: string; tool: string; input: Record<string, unknown>; result: unknown; before: unknown; is_write: boolean; status: string; duration_ms: number; created_at: string; completed_at: string }
export function unwrap(v: unknown): unknown { if (v && typeof v === 'object' && 'structuredContent' in v) return (v as { structuredContent: unknown }).structuredContent; return v }

/** Plain text/semantic values only: app content never becomes HTML. */
export function DataView({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null || value === undefined) return <span className="muted">Not set</span>
  if (typeof value === 'boolean') return <span>{value ? 'Yes' : 'No'}</span>
  if (typeof value !== 'object') return safeURL(value) ? <a href={value} target="_blank" rel="noopener noreferrer">Open source ↗</a> : <span className="data-text">{String(value)}</span>
  if (depth > 5) return <details><summary>More details</summary><pre className="data-raw">{JSON.stringify(value, null, 2)}</pre></details>
  if (Array.isArray(value)) return value.length ? <ul className="data-list">{value.slice(0, 30).map((v, i) => <li key={i}><DataView value={v} depth={depth + 1} /></li>)}{value.length > 30 && <li>{value.length - 30} more entries in the source</li>}</ul> : <span className="muted">None returned</span>
  const entries = Object.entries(value).filter(([k, v]) => !['request_id', 'etag', 'tool'].includes(k) && v !== null)
  return <dl className="data-fields">{entries.map(([k, v]) => <div key={k}><dt>{friendly(k)}</dt><dd>{typeof v === 'object' && v !== null && depth > 0 ? <details><summary>{Array.isArray(v) ? `${v.length} entries` : 'Details'}</summary><DataView value={v} depth={depth + 1} /></details> : <DataView value={v} depth={depth + 1} />}</dd></div>)}</dl>
}
export function ActionPreview({ input }: { input: unknown }) { return <div className="action-preview"><DataView value={input} /><p className="muted xsmall">Review the destination and proposed values before allowing.</p></div> }
export function BatchReview({ threadId, requests, onDone }: { threadId: string; requests: AgentRequest[]; onDone: () => void }) {
 const [selected, setSelected] = useState<string[]>([]), [busy, setBusy] = useState(false)
 const { toast } = useToast()
 async function answer(decision: 'allow' | 'deny') { setBusy(true); try { for (const r of requests.filter(r => selected.includes(r.id))) await api.post(`${V1}/threads/${threadId}/requests/${encodeURIComponent(r.id)}`, { decision }); setSelected([]); onDone() } catch (e) { toast(errMsg(e), 'err'); onDone() } finally { setBusy(false) } }
 return <details className="batch-review"><summary>Review {requests.length} proposed changes</summary><p>Select the exact changes to allow. New proposals are never selected automatically.</p>{requests.map(r => <section key={r.id}><label className="row"><input type="checkbox" checked={selected.includes(r.id)} onChange={e => setSelected(s => e.target.checked ? [...s, r.id] : s.filter(id => id !== r.id))} /><b>{r.title || friendly(r.tool_name || '')}</b></label><ActionPreview input={r.input} /></section>)}<div className="row"><button className="btn" disabled={busy || !selected.length} onClick={() => answer('deny')}>Deny selected</button><button className="btn primary" disabled={busy || !selected.length} onClick={() => answer('allow')}>Allow {selected.length} selected</button></div></details>
}
export function CallCard({ call: summary }: { call: AppCall }) {
 const [open,setOpen] = useState(false), [full,setFull] = useState<AppCall | null>(null)
 const call = full ?? summary
 useEffect(() => { if (!open) return; let alive=true; api.get<AppCall>(`${V1}/app-calls/${summary.id}`).then(v => { if(alive) setFull(v) }).catch(e => toast(errMsg(e),'err')); return () => {alive=false} },[open,summary.id,summary.status,summary.completed_at])
 const navigate = useNavigate(), { toast } = useToast(); const [busy, setBusy] = useState(false)
 async function undo() { setBusy(true); try { const t = await api.post<Thread>(`${V1}/ask/threads`, { text: `Please use pocket_undo_action to review undoing action ${call.id}. Show the exact restore for my approval. Do not make other changes.` }); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 const after = unwrap(call.result) as Record<string, unknown> | null
 const note = after?.result as Record<string, unknown> | undefined
 const undoable = call.tool === 'pocket_notes_update' && call.status === 'done' && !!note?.etag && JSON.stringify(call.before) !== '{}'
 return <article className="app-call"><div className="row wrap"><b>{friendly(call.tool)}</b><span className="badge">{call.status}</span><span className="muted small">{(call.duration_ms / 1000).toFixed(1)}s · {new Date(call.created_at).toLocaleString()}</span>{call.thread_id && <Link to={`/t/${call.thread_id}`}>Conversation ↗</Link>}</div><details open={open} onToggle={e => setOpen(e.currentTarget.open)}><summary>{call.is_write ? 'Change and result' : 'Source, query and returned data'}</summary><DataView value={call.input} /><DataView value={unwrap(call.result)} /></details>{call.status === 'unknown' && <p>Completion was not confirmed. Check the source app before retrying.</p>}{undoable && <button className="btn sm" disabled={busy} onClick={undo}>Review undo</button>}{call.is_write && !undoable && <p className="muted xsmall">{call.status === 'undone' ? 'This change was undone.' : 'Automatic undo is unavailable for this action.'}</p>}</article>
}
export function ThreadEvidence({ id, revision }: { id: string; revision: number }) {
 const [open, setOpen] = useState(false), [calls, setCalls] = useState<AppCall[]>([]), [error, setError] = useState('')
 useEffect(() => { if (!open) return; let alive = true; api.get<AppCall[]>(`${V1}/app-calls?thread=${id}`).then(v => { if (alive) { setCalls(v); setError('') } }).catch(e => { if (alive) setError(errMsg(e)) }); return () => { alive = false } }, [id, open, revision])
 return <details className="thread-evidence" open={open} onToggle={e => setOpen(e.currentTarget.open)}><summary>Sources & app activity</summary>{error && <p role="alert">{error}</p>}{calls.length ? calls.map(c => <CallCard key={c.id} call={c} />) : <p className="muted">No app calls recorded yet. Sources are recorded for new calls from this version onward.</p>}</details>
}
