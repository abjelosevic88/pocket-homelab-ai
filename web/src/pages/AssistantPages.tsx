import { Icon } from '../components/icons'
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
function briefingContent(section: Section) {
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
 // Review activity is history, not work waiting for the user. Keep it in
 // source details while the briefing lists actionable reviews and tasks.
 if (section.tool === 'pocket_collaborator_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const review = result as Record<string, unknown>
  visit(review.waiting); visit(review.tasks)
 } else visit(result)
 const counts = result && !Array.isArray(result) && typeof result === 'object' ? Object.entries(result).filter(([k, v]) => typeof v === 'number' && /^(overdue|due_today|today|total|open|open_reviews|open_tasks|done_today|to_review|to_approve|completed|active|due|waiting)$/.test(k)) : []
 const source = safeURL(envelope.source_url) ? envelope.source_url : safeURL(envelope.app_url) ? envelope.app_url : ''
 return { items, counts, source }
}
const sectionIcons: Record<string, string> = { pocket_calendar_events: 'calendar', pocket_tasks_summary: 'check', pocket_money_bills: 'receipt', pocket_collaborator_summary: 'review', pocket_garage_reminders: 'wrench' }
function BriefingCard({ section, onAsk, asking }: { section: Section; onAsk: (text: string) => void; asking: boolean }) {
 const [expanded, setExpanded] = useState(false)
 const { items, counts, source } = briefingContent(section)
 const title = sectionTitles[section.tool] || friendly(section.tool)
 const visible = expanded ? items : items.slice(0, 3)
 return <article className="assistant-card briefing-card">
  <header className="briefing-card-head"><span className="briefing-card-icon"><Icon name={sectionIcons[section.tool] || 'search'} size={17} /></span><h2>{title}</h2><button type="button" className="briefing-ask" disabled={asking || !!section.error} aria-label={`Ask about ${title}`} onClick={() => onAsk(`Help me with ${friendly(section.tool)}. Read the current data and suggest the next action; show any proposed change for approval.`)}><Icon name="sparkles" size={14} />Ask</button></header>
  {section.error ? <p role="alert" className="briefing-empty">{section.error}</p> : <>
   {counts.length > 0 && <div className="briefing-counts">{counts.map(([k, v]) => <span key={k}><b>{String(v)}</b> {friendly(k)}</span>)}</div>}
   {items.length ? <ul className="briefing-items">{visible.map((item, i) => {
    const itemTitle = String(item.title ?? item.name ?? item.summary), href = safeURL(item.item_url) ? item.item_url : source
    const when = item.due ?? item.due_date ?? item.next_date ?? item.start ?? item.date
    const amount = typeof item.amount === 'number' && typeof item.currency === 'string' && section.tool.startsWith('pocket_money_') ? formatMinor(item.amount,item.currency) : ''
    return <li key={`${item.id ?? 'item'}-${i}`}><div className="briefing-item-content">{href ? <a href={href} target="_blank" rel="noopener noreferrer">{itemTitle}<Icon name="external" size={12} /></a> : <b>{itemTitle}</b>}<div className="briefing-item-meta">{when ? <span>{displayWhen(when)}</span> : null}{item.status ? <span>{friendly(String(item.status))}</span> : null}{amount && <span className="briefing-amount">{amount}</span>}</div></div><button type="button" className="briefing-item-ask" disabled={asking} aria-label={`Ask about ${itemTitle}`} title={`Ask about ${itemTitle}`} onClick={() => onAsk(`Help me with “${itemTitle}” in ${friendly(section.tool)}. Source: ${href}. Record ID: ${item.id ?? 'not supplied'}. Read the current record, then show any proposed change for approval.`)}><Icon name="sparkles" size={14} /><span>Ask</span></button></li>
   })}</ul> : <p className="briefing-empty">{section.tool === 'pocket_calendar_events' ? 'No calendar events in today’s overview.' : 'No items in this overview.'}</p>}
   {items.length > 3 && <button type="button" className="briefing-expand" aria-expanded={expanded} onClick={() => setExpanded(v => !v)}>{expanded ? 'Show less' : `Show ${items.length - 3} more`}<Icon name={expanded ? 'chevron-up' : 'chevron-down'} size={14} /></button>}
  </>}
  <div className="briefing-source-link">{source && <a href={source} aria-label={`Open source: ${title}`} target="_blank" rel="noopener noreferrer">Open {title}<Icon name="external" size={12} /></a>}</div>
  <details className="briefing-source"><summary>Details &amp; source</summary><p>Checked {new Date(section.checked_at).toLocaleString()} · {(section.duration_ms / 1000).toFixed(1)}s</p><DataView value={section.data} /></details>
 </article>
}
export function BriefingPage({ onMenu }: { onMenu?: () => void }) {
 const [data, setData] = useState<Overview | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false), [q, setQ] = useState(''), [query, setQuery] = useState(''), [asking, setAsking] = useState(false)
 const navigate = useNavigate(), { toast } = useToast()
 async function load(search = false) { const term = search ? q.trim() : ''; if (search && !term) return; setBusy(true); setError(''); try { const next = await api.get<Overview>(`${V1}/${search ? `app-search?q=${encodeURIComponent(term)}` : 'briefing'}`); setData(next); setQuery(term); if (!search) setQ('') } catch (e) { setError(errMsg(e)) } finally { setBusy(false) } }
 useEffect(() => { void load() }, [])
 async function ask(prompt: string) { if (asking) return; setAsking(true); try { const t = await api.post<Thread>(`${V1}/ask/threads`, { text: prompt }); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setAsking(false) } }
 const sections = [...(data?.sections || [])].sort((a,b) => {
  const hasContent = (s: Section) => !s.error && (briefingContent(s).items.length > 0 || briefingContent(s).counts.some(([,n]) => Number(n) > 0))
  return Number(hasContent(b)) - Number(hasContent(a))
 })
 const available = data?.sections.filter(s => !s.error).length || 0
 const dateLabel = data?.date ? new Date(data.date + 'T12:00:00').toLocaleDateString(undefined, { weekday:'long', month:'long', day:'numeric' }) : 'Your day'
 return <Frame title="Briefing" sub="Your day across Pocket apps, in one place." onMenu={onMenu}>
  <div className="briefing-heading"><div><h2>{query ? 'Search results' : dateLabel}</h2><p>{query ? `For “${query}”` : 'Appointments, tasks and upcoming commitments.'}</p></div><button type="button" className="briefing-summarize" disabled={busy || asking || !available} aria-busy={asking} onClick={() => ask(query ? `Search my apps for ${JSON.stringify(query)} and summarize the relevant results with source links. Explain any missing sources.` : 'Give me a concise briefing for today: appointments, overdue and due tasks, bills, review work and maintenance. Link each item and explain any unavailable sources.')}><Icon name="sparkles" size={16} />{asking ? 'Starting…' : query ? 'Ask about results' : 'Summarize my day'}<Icon name="chevron-right" size={14} /></button></div>
  <form className="briefing-search" role="search" aria-label="Search Pocket apps" onSubmit={e => { e.preventDefault(); void load(true) }}><div className="briefing-search-input"><Icon name="search" size={16} /><input aria-label="Search my apps" placeholder="Search across your apps…" value={q} maxLength={200} onChange={e => setQ(e.target.value)} /><button type="submit" disabled={busy || !q.trim()}>Search</button></div><button type="button" className="briefing-refresh" disabled={busy} onClick={() => load()}><Icon name={query ? 'arrow-left' : 'refresh'} size={15} />{query ? 'Back to today' : 'Refresh'}</button></form>
  {error && <p role="alert" className="callout danger">{error}</p>}
  {busy && <p role="status" className="muted small">Reading your apps…</p>}
  {data && <><div className="briefing-overview-meta"><span>{available} of {data.sections.length + (data.unavailable?.length || 0)} sources available</span><details><summary>About this {query ? 'search' : 'briefing'}</summary><p>{data.timezone && `${data.timezone} · `}{data.coverage}</p>{data.unavailable?.length ? <p>Not connected: {data.unavailable.map(friendly).join(', ')}.</p> : null}</details></div>{data.unavailable?.length ? <p className="muted small">Some apps aren’t connected. Open “About this briefing” for details.</p> : null}<div className="briefing-grid" aria-busy={busy}>{sections.map(s => <BriefingCard section={s} onAsk={ask} asking={asking} key={s.tool} />)}</div></>}
 </Frame>
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
