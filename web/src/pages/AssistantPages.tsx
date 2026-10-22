import { Select } from '../components/Select'
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { AskButton } from '../components/AskButton'
import { PageHead } from '../components/PageHead'
import { AppCall, CallCard, DataView, friendly, safeURL } from '../components/AppEvidence'
import { errMsg, useToast } from '../components/ui'
import type { Thread } from '../lib/types'

function Frame({ title, sub, onMenu, children }: { title: string; sub: string; onMenu?: () => void; children: ReactNode }) { return <div className="page"><PageHead title={title} sub={sub} onMenu={onMenu} actions={<AskButton />} /><div className="page-scroll"><div className="page-inner assistant-page">{children}</div></div></div> }
interface Section { tool: string; data?: unknown; error?: string; checked_at: string; duration_ms: number }
interface Overview { date?: string; timezone?: string; coverage: string; unavailable?: string[]; sections: Section[] }
const sectionTitles: Record<string,string> = { pocket_tasks_summary:'Tasks', pocket_money_bills:'Bills & subscriptions', pocket_calendar_events:'Calendar', pocket_collaborator_summary:'Reviews', pocket_garage_reminders:'Car maintenance' }
function displayWhen(value: unknown) { const s=String(value); if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return new Date(s+'T12:00:00').toLocaleDateString(); if (s.includes('T') && !Number.isNaN(Date.parse(s))) return new Date(s).toLocaleString(); return s }
function formatMinor(amount: number, currency: string) { try { const f = new Intl.NumberFormat(undefined, { style:'currency', currency }); const digits = f.resolvedOptions().maximumFractionDigits ?? 2; return f.format(amount / 10 ** digits) } catch { return `${amount} minor units (${currency})` } }
function BriefingData({ section, onAsk }: { section: Section; onAsk: (text: string) => void }) {
 const envelope = (section.data && typeof section.data === 'object' ? section.data : {}) as Record<string, unknown>
 const result = envelope.result ?? section.data
 const items: Record<string, unknown>[] = []
 const visit = (v: unknown, depth = 0) => {
  if (!v || typeof v !== 'object' || depth > 4 || items.length >= 20) return
  if (Array.isArray(v)) { for (const x of v) visit(x, depth + 1); return }
  const o = v as Record<string, unknown>
  if (o.review && typeof o.review === 'object') { visit(o.review, depth + 1); return }
  if (typeof (o.title ?? o.name ?? o.summary) === 'string' && !('omitted' in o)) { items.push(o); return }
  for (const [k, x] of Object.entries(o)) if (!['participants', 'accounts', 'categories', 'members'].includes(k)) visit(x, depth + 1)
 }
 visit(result)
 const counts = result && !Array.isArray(result) && typeof result === 'object' ? Object.entries(result).filter(([k, v]) => typeof v === 'number' && /^(overdue|due_today|today|total|open|open_reviews|open_tasks|done_today|to_review|to_approve|completed|active|due|waiting)$/.test(k)) : []
 const source = safeURL(envelope.source_url) ? envelope.source_url : safeURL(envelope.app_url) ? envelope.app_url : ''
 return <>
  {counts.length > 0 && <div className="briefing-counts">{counts.map(([k, v]) => <span key={k}><b>{String(v)}</b> {friendly(k)}</span>)}</div>}
  {items.length ? <ul className="briefing-items">{items.map((item, i) => {
   const title = String(item.title ?? item.name ?? item.summary), href = safeURL(item.item_url) ? item.item_url : source
   const when = item.due ?? item.due_date ?? item.next_date ?? item.start ?? item.date
   const amount = typeof item.amount === 'number' && typeof item.currency === 'string' && section.tool.startsWith('pocket_money_') ? formatMinor(item.amount,item.currency) : ''
   return <li key={String(item.id ?? i)}><div>{href ? <a href={href} target="_blank" rel="noopener noreferrer"><b>{title}</b> ↗</a> : <b>{title}</b>}{when ? <p className="muted small">{displayWhen(when)}{item.status ? ` · ${item.status}` : ''}</p> : null}{amount && <p>{amount}</p>}</div><button className="btn sm" onClick={() => onAsk(`Help me with “${title}” in ${friendly(section.tool)}. Source: ${href}. Record ID: ${item.id ?? 'not supplied'}. Read the current record, then show any proposed change for approval.`)}>Help with this</button></li>
  })}</ul> : <p className="muted small">No items to show. Source details include any additional totals.</p>}
  {source && <a href={source} target="_blank" rel="noopener noreferrer">Open source ↗</a>}
  <details className="briefing-source"><summary>Source details</summary><DataView value={section.data} /></details>
 </>
}
export function BriefingPage({ onMenu }: { onMenu?: () => void }) {
 const [data, setData] = useState<Overview | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false), [q, setQ] = useState('')
 const navigate = useNavigate(), { toast } = useToast()
 async function load(search = false) { setBusy(true); setError(''); try { setData(await api.get<Overview>(`${V1}/${search ? `app-search?q=${encodeURIComponent(q)}` : 'briefing'}`)) } catch (e) { setError(errMsg(e)) } finally { setBusy(false) } }
 useEffect(() => { void load() }, [])
 async function ask(prompt: string) { try { const t = await api.post<Thread>(`${V1}/ask/threads`, { text: prompt }); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } }
 return <Frame title="Briefing" sub="Your day across Pocket apps. Agent runs are still on Today." onMenu={onMenu}><form className="row" onSubmit={e => { e.preventDefault(); void load(true) }}><input aria-label="Search my apps" placeholder="Search all my apps…" value={q} maxLength={200} onChange={e => setQ(e.target.value)} /><button className="btn" disabled={busy || !q.trim()}>Search</button><button type="button" className="btn" disabled={busy} onClick={() => load()}>Refresh day</button></form>{error && <p role="alert" className="callout danger">{error}</p>}{busy && <p role="status">Reading your apps…</p>}{data && <><p className="muted small">{data.date} {data.timezone} · {data.coverage}</p><button className="btn primary" onClick={() => ask('Give me a concise briefing for today: appointments, overdue and due tasks, bills, review work and maintenance. Link each item and explain any unavailable sources.')}>Ask for a concise briefing</button>{data.unavailable?.length ? <p className="muted">Unavailable: {data.unavailable.map(friendly).join(', ')}. Connect these apps to include them.</p> : null}<div className="briefing-grid">{data.sections.map(s => <article className="assistant-card" key={s.tool}><h2>{sectionTitles[s.tool] || friendly(s.tool)}</h2><p className="muted xsmall">Checked {new Date(s.checked_at).toLocaleTimeString()} · {(s.duration_ms / 1000).toFixed(1)}s</p>{s.error ? <p role="alert">{s.error}</p> : <BriefingData section={s} onAsk={ask} />}<button className="btn sm" onClick={() => ask(`Help me with ${friendly(s.tool)}. Read the current data and suggest the next action; show any proposed change for approval.`)}>Ask about this</button></article>)}</div></>}</Frame>
}
interface Memory { id: number; label: string; value: string; enabled: boolean }
export function MemoryPage({ onMenu }: { onMenu?: () => void }) {
 const [items, setItems] = useState<Memory[]>([]), [edit, setEdit] = useState<Memory>({ id: 0, label: '', value: '', enabled: true }), [busy, setBusy] = useState(false)
 const { toast } = useToast()
 async function load() { try { setItems(await api.get<Memory[]>(`${V1}/memory`)) } catch (e) { toast(errMsg(e), 'err') } }
 useEffect(() => { void load() }, [])
 async function save(m: Memory) { setBusy(true); try { if (m.id) await api.put(`${V1}/memory/${m.id}`, m); else await api.post(`${V1}/memory`, m); setEdit({ id: 0, label: '', value: '', enabled: true }); await load() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function remove(id: number) { try { await api.del(`${V1}/memory/${id}`); await load() } catch (e) { toast(errMsg(e), 'err') } }
 return <Frame title="Memory" sub="Preferences you explicitly save. Current app records always take precedence." onMenu={onMenu}><p>Nothing is saved here automatically. Forgetting a preference does not erase earlier conversation messages. Disable a preference to keep it without using it, or forget it. You can turn memory off for one conversation in its menu.</p><form className="assistant-form assistant-card" onSubmit={e => { e.preventDefault(); void save(edit) }}><label>Name<input required maxLength={100} value={edit.label} onChange={e => setEdit({ ...edit, label: e.target.value })} placeholder="My car" /></label><label>Preference<textarea required maxLength={2000} value={edit.value} onChange={e => setEdit({ ...edit, value: e.target.value })} placeholder="When I say my car, I mean the Golf in Garage." rows={3} /></label><div className="row"><button className="btn primary" disabled={busy}>{edit.id ? 'Save changes' : 'Save preference'}</button>{edit.id > 0 && <button className="btn" type="button" onClick={() => setEdit({ id: 0, label: '', value: '', enabled: true })}>Cancel</button>}</div></form>{items.map(m => <article className="assistant-card" key={m.id}><label className="row"><input type="checkbox" checked={m.enabled} disabled={busy} onChange={e => save({ ...m, enabled: e.target.checked })} /><b>{m.label}</b></label><p className="data-text">{m.value}</p><div className="row"><button className="btn sm" onClick={() => setEdit(m)}>Edit</button><button className="btn sm" onClick={() => remove(m.id)}>Forget</button></div></article>)}{!items.length && <p className="muted">No saved preferences yet.</p>}</Frame>
}
export function ActionsPage({ onMenu }: { onMenu?: () => void }) {
 const [calls, setCalls] = useState<AppCall[]>([]), [writes, setWrites] = useState(true), [error, setError] = useState('')
 useEffect(() => { api.get<AppCall[]>(`${V1}/app-calls${writes ? '?writes=1' : ''}`).then(setCalls).catch(e => setError(errMsg(e))) }, [writes])
 const times = calls.filter(c => !c.is_write && c.status === 'done').map(c => c.duration_ms).sort((a, b) => a - b)
 return <Frame title="Action history" sub="The latest 200 app calls, approvals and confirmed results." onMenu={onMenu}><label className="row"><input type="checkbox" checked={writes} onChange={e => setWrites(e.target.checked)} />Only changes</label>{error && <p role="alert">{error}</p>}{times.length > 0 && <p className="muted">Read latency: median {(times[Math.floor(times.length / 2)] / 1000).toFixed(2)}s · 95th percentile {(times[Math.min(times.length - 1, Math.floor(times.length * .95))] / 1000).toFixed(2)}s across {times.length} recorded reads. This measures app calls, excluding model reasoning.</p>}{calls.map(c => <CallCard key={c.id} call={c} />)}{!calls.length && <p>No recorded changes yet.</p>}</Frame>
}
interface Workflow { id: number; name: string; prompt: string; model: string; trigger_kind: string; spec: string; enabled: boolean; notify: boolean; last_thread_id?: string; last_status?: string; next_run_at?: string }
const emptyWorkflow: Workflow = { id: 0, name: '', prompt: '', model: 'sonnet', trigger_kind: 'manual', spec: 'daily 08:00', enabled: true, notify: true }
export function WorkflowsPage({ onMenu }: { onMenu?: () => void }) {
 const [items, setItems] = useState<Workflow[]>([]), [edit, setEdit] = useState<Workflow>({ ...emptyWorkflow }), [busy, setBusy] = useState(false)
 const { toast } = useToast(), navigate = useNavigate(), location = useLocation()
 async function load() { try { setItems(await api.get<Workflow[]>(`${V1}/workflows`)) } catch (e) { toast(errMsg(e), 'err') } }
 useEffect(() => { void load(); const v = location.state as { name?: string; prompt?: string } | null; if (v?.prompt) setEdit({ ...emptyWorkflow, name: (v.name || '').slice(0, 100), prompt: v.prompt }) }, [])
 async function save(x: Workflow) { setBusy(true); try { if (x.id) await api.put(`${V1}/workflows/${x.id}`, x); else await api.post(`${V1}/workflows`, x); setEdit({ ...emptyWorkflow }); await load() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function run(x: Workflow) { setBusy(true); try { const t = await api.post<Thread>(`${V1}/workflows/${x.id}/run`); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function remove(x: Workflow) { try { await api.del(`${V1}/workflows/${x.id}`); await load() } catch (e) { toast(errMsg(e), 'err') } }
 return <Frame title="Workflows" sub="Repeat an app task with fresh data. Every write still needs approval." onMenu={onMenu}><form className="assistant-form assistant-card" onSubmit={e => { e.preventDefault(); void save(edit) }}><label>Name<input required maxLength={100} value={edit.name} onChange={e => setEdit({ ...edit, name: e.target.value })} placeholder="Weekly spending note" /></label><label>Instructions<textarea required rows={4} maxLength={20000} value={edit.prompt} onChange={e => setEdit({ ...edit, prompt: e.target.value })} placeholder="Summarize this week’s spending and prepare a note in Quick Notes." /></label><div className="row wrap"><label>Run<Select label="Run" value={edit.trigger_kind} onChange={trigger_kind => setEdit({ ...edit, trigger_kind })} options={[{ value: 'manual', label: 'When I press Run' }, { value: 'schedule', label: 'On a schedule' }, { value: 'documents', label: 'When new documents appear' }, { value: 'tasks', label: 'When new tasks appear' }]} /></label><label>Model<Select label="Workflow model" value={edit.model} onChange={model => setEdit({ ...edit, model })} options={[{ value: 'haiku', label: 'Haiku' }, { value: 'sonnet', label: 'Sonnet' }, { value: 'opus', label: 'Opus' }]} /></label></div>{edit.trigger_kind === 'schedule' && <label>Schedule<input required value={edit.spec} onChange={e => setEdit({ ...edit, spec: e.target.value })} placeholder="daily 08:00" /><span className="muted small">Examples: daily 08:00, weekly mon 09:00, every 6h. Uses your Settings timezone.</span></label>}{['documents', 'tasks'].includes(edit.trigger_kind) && <p className="muted small">Checks up to 100 recent task events or newly added documents once a minute. Existing items form the initial baseline; only newly observed IDs trigger a run. A large import or burst of activity may exceed this window. Re-enabling starts a fresh baseline.</p>}<label className="row"><input type="checkbox" checked={edit.enabled} onChange={e => setEdit({ ...edit, enabled: e.target.checked })} />Enabled</label><label className="row"><input type="checkbox" checked={edit.notify} onChange={e => setEdit({ ...edit, notify: e.target.checked })} />Notify when finished</label><div className="row"><button className="btn primary" disabled={busy}>{edit.id ? 'Save changes' : 'Create workflow'}</button>{edit.id > 0 && <button type="button" className="btn" onClick={() => setEdit({ ...emptyWorkflow })}>Cancel</button>}</div></form>{items.map(x => <article className="assistant-card" key={x.id}><h2>{x.name}</h2><p>{x.prompt}</p><p className="muted small">{x.enabled ? friendly(x.trigger_kind) : 'Paused'} {x.trigger_kind === 'schedule' && `· ${x.spec}`} · {x.last_status || 'Not run yet'}{x.next_run_at && x.enabled && ` · Next ${new Date(x.next_run_at).toLocaleString()}`}</p><div className="row wrap"><button className="btn primary" disabled={busy} onClick={() => run(x)}>Run now</button><button className="btn" onClick={() => setEdit(x)}>Edit</button><button className="btn" onClick={() => save({ ...x, enabled: !x.enabled })}>{x.enabled ? 'Pause' : 'Enable'}</button><button className="btn" onClick={() => remove(x)}>Delete</button>{x.last_thread_id && <Link to={`/t/${x.last_thread_id}`}>Last run ↗</Link>}</div></article>)}</Frame>
}
