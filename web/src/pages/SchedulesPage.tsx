// Schedules: prompts that run on their own and tell you how it went.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, when } from '../lib/format'
import type { Schedule, SchedulePreview, Thread } from '../lib/types'
import { AgentPicker } from '../components/AgentPicker'
import { FolderButton } from '../components/FolderPicker'
import { HostPicker, HostTag } from '../components/HostPicker'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Confirm, Empty, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, useAsync, useDebounce, useToast, type MenuItem, useContextMenu, noGrammarly } from '../components/ui'
import { AgentSummary } from './RecipesPage'

export type Draft = Pick<Schedule, 'name' | 'prompt' | 'provider' | 'model' | 'effort' | 'mode' | 'speed' | 'host_id' | 'cwd' | 'spec' | 'notify' | 'enabled'> & { id?: number; from_thread_id?: string; report?: boolean }

const blank: Draft = { name: '', prompt: '', provider: '', model: '', effort: '', mode: '', speed: '', host_id: 0, cwd: '', spec: 'daily 08:00', notify: 'attention', enabled: true }

const STATUS: Record<string, { label: string; cls: string; icon: string }> = {
  ok: { label: 'OK', cls: 'ok', icon: 'check-circle' },
  attention: { label: 'Needs attention', cls: 'warn', icon: 'alert' },
  failed: { label: 'Failed', cls: 'danger', icon: 'x-circle' },
  running: { label: 'Running', cls: 'accent-soft', icon: 'refresh' },
}

const SPEC_EXAMPLES = ['every 30m', 'every 6h', 'daily 08:00', 'weekdays 08:30', 'weekly mon 09:00', '0 */4 * * *']
const ONCE_EXAMPLES = ['in 1h', 'in 3h', 'today 18:00', 'tomorrow 09:00']

/** Runs one time only: "once 2026-11-12 09:00" (or "in 2h", "tomorrow 09:00" before it is saved). */
export const isOnce = (spec: string) => /^\s*(once|in|today|tomorrow)\b/i.test(spec)
const fullWhen = (iso: string) => iso ? new Date(iso).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''

type ChatBeat = { id: number; thread_id: string; thread_title: string; prompt: string; spec: string; next_at: string; last_at: string; runs: number; max_runs: number; ends_at: string }

/** A blank one-time schedule from a conversation: its agent and folder, the answer back to it. */
export const draftFromThread = (t: Thread): Draft => ({ ...blank, provider: t.provider, model: t.model, effort: t.effort, mode: t.mode, speed: t.speed || '', host_id: t.host_id, cwd: t.cwd,
  spec: 'tomorrow 09:00', notify: 'always', from_thread_id: t.id, report: true })

export default function SchedulesPage({ onMenu }: { onMenu?: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { upsertThread, user, threads } = useApp()
  const list = useAsync(() => api.get<Schedule[]>(`${V1}/schedules`), [])
  const beats = useAsync(() => api.get<{ heartbeats: ChatBeat[] }>(`${V1}/heartbeats`).then(r => r.heartbeats || []), [])
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<Schedule | null>(null)
  const [showDone, setShowDone] = useState(false)
  const ctx = useContextMenu()
  const [running, setRunning] = useState<number | null>(null)
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  // the "running" status and next run change on their own
  useEffect(() => { const t = setInterval(() => { if (!document.hidden) { list.reload(); beats.reload() } }, 30000); return () => clearInterval(t) }, [list.reload, beats.reload]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = async (s: Schedule, enabled: boolean) => {
    list.setData(l => l && l.map(x => x.id === s.id ? { ...x, enabled } : x))
    try { await api.put(`${V1}/schedules/${s.id}`, { ...s, enabled }); list.reload() } catch (e) { toast(errMsg(e), 'err'); list.reload() }
  }
  const runNow = async (s: Schedule) => {
    setRunning(s.id)
    try { const t = await api.post<Thread>(`${V1}/schedules/${s.id}/run`); upsertThread(t); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setRunning(null) }
  }
  const removeBeat = async (b: ChatBeat) => {
    beats.setData(l => l && l.filter(x => x.id !== b.id))
    try { await api.del(`${V1}/heartbeats/${b.id}`); toast('Removed') } catch (e) { toast(errMsg(e), 'err'); beats.reload() }
  }
  const all = list.data || []
  const upcoming = all.filter(s => isOnce(s.spec) && s.enabled).sort((a, b) => a.next_run_at.localeCompare(b.next_run_at))
  const repeating = all.filter(s => !isOnce(s.spec))
  const done = all.filter(s => isOnce(s.spec) && !s.enabled).sort((a, b) => b.last_run_at.localeCompare(a.last_run_at))
  const chatTitle = (id: string) => threads?.find(x => x.id === id)?.title || 'a conversation'
  const clearDone = async () => {
    const ids = done.map(d => d.id)
    list.setData(l => l && l.filter(x => !ids.includes(x.id)))
    try { await Promise.all(ids.map(id => api.del(`${V1}/schedules/${id}`))); toast(`Cleared ${ids.length}`) } catch (e) { toast(errMsg(e), 'err'); list.reload() }
  }

  const card = (s: Schedule) => {
    const once = isOnce(s.spec)
    const more: MenuItem[] = [
      { label: 'Edit…', icon: 'edit', onSelect: () => setEdit({ ...s }) },
      { label: once && !s.enabled ? 'Schedule again…' : 'Duplicate', icon: 'copy', onSelect: () => setEdit({ ...s, id: undefined, name: once && !s.enabled ? s.name : s.name + ' (copy)', spec: once ? 'tomorrow 09:00' : s.spec, enabled: true }) },
      'sep',
      { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDel(s) },
    ]
    const st = STATUS[s.last_status]
    return (
      <div key={s.id} className={`list-card ${s.enabled || once ? '' : 'off'}`} onContextMenu={e => ctx.open(e, more)}>
        {once ? <span className="sched-once-icon" title="Runs once"><Icon name={s.enabled ? 'clock' : 'check-circle'} size={16} /></span>
          : <Switch checked={s.enabled} onChange={v => toggle(s, v)} label={`Enable ${s.name}`} />}
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="list-card-title">{s.name}{once
            ? s.enabled && <code className="spec-chip">{fullWhen(s.next_run_at)}</code>
            : <code className="spec-chip">{s.spec}</code>}</div>
          <div className="list-card-sub">{s.prompt}</div>
          <div className="sched-meta">
            {s.enabled && s.next_run_at && <span><Icon name="clock" size={11} />{once ? when(s.next_run_at) : `next ${when(s.next_run_at)}`}</span>}
            {s.last_run_at && <span>{once ? 'ran' : 'last'} {ago(s.last_run_at)}</span>}
            {st && <span className={`badge ${st.cls}`}><Icon name={st.icon} size={10} />{st.label}</span>}
            {s.last_thread_id && <button type="button" className="link-btn xsmall" onClick={() => navigate(`/t/${s.last_thread_id}`)}>{once ? 'Open the run' : 'Open last run'}</button>}
            {s.from_thread_id && <button type="button" className="link-btn xsmall" onClick={() => navigate(`/t/${s.from_thread_id}`)} title={s.report ? 'The answer goes back to this conversation' : 'Set from this conversation'}>
              <Icon name="message" size={11} />{s.report ? 'answer to ' : 'from '}{oneLine(chatTitle(s.from_thread_id), 40)}</button>}
            <span className="faint">notify: {s.notify}</span>
            <HostTag id={s.host_id} />
          </div>
          {s.last_summary && <div className={`sched-summary ${s.last_status}`}>{s.last_summary}</div>}
          <AgentSummary c={s} />
        </div>
        <div className="list-card-actions">
          {!(once && !s.enabled) && <button type="button" className="btn sm" disabled={running === s.id || !canRun} onClick={() => runNow(s)}>{running === s.id ? <Spinner size={11} /> : <Icon name="play" size={11} />}Run now</button>}
          <MenuButton label={`More for ${s.name}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
        </div>
      </div>
    )
  }

  const beatCard = (b: ChatBeat) => {
    const once = isOnce(b.spec)
    return (
      <div key={'hb' + b.id} className="list-card">
        <span className="sched-once-icon" title="Comes back to its conversation"><Icon name="activity" size={16} /></span>
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="list-card-title">{oneLine(b.thread_title || 'Conversation', 60)}<code className="spec-chip">{once ? fullWhen(b.next_at) : b.spec}</code></div>
          <div className="list-card-sub">{b.prompt}</div>
          <div className="sched-meta">
            <span><Icon name="clock" size={11} />{once ? when(b.next_at) : `next ${when(b.next_at)}`}</span>
            {b.runs > 0 && <span>{b.runs}{b.max_runs ? ` of ${b.max_runs}` : ''} sent</span>}
            {!once && <span className="faint">{b.ends_at ? `ends ${when(b.ends_at)}` : 'no end'}</span>}
          </div>
        </div>
        <div className="list-card-actions">
          <button type="button" className="btn sm" onClick={() => navigate(`/t/${b.thread_id}`)}><Icon name="message" size={11} />Open</button>
          <button type="button" className="btn ghost sm" onClick={() => void removeBeat(b)}>Remove</button>
        </div>
      </div>
    )
  }

  const beatList = beats.data || []
  const empty = list.data && all.length === 0 && beatList.length === 0
  return (
    <div className="page">
      <PageHead title="Schedules" icon="clock" onMenu={onMenu} sub="Prompts that run by themselves, once or on repeat — you hear about it when something needs attention."
        actions={<>
          <button type="button" className="btn" disabled={!canRun} onClick={() => setEdit({ ...blank, spec: 'tomorrow 09:00', notify: 'always' })}><Icon name="clock" size={14} />Once</button>
          <button type="button" className="btn primary" disabled={!canRun} onClick={() => setEdit({ ...blank })}><Icon name="plus" size={14} />New</button>
        </>} />
      <div className="page-scroll">
        <div className="page-inner">
          {list.error && <div className="callout danger"><Icon name="alert" size={15} /><span>{list.error}</span></div>}
          {!list.data ? (!list.error && <Loading />) : empty ? (
            <Empty title="Nothing scheduled" icon="clock">
              For example: every morning at 8, check disk space, backups and unhealthy containers — and only ping me if something is wrong. Or once: tomorrow at 9, see whether last night’s import finished.
              <div className="faint mt">In any chat you can also just ask: “run this tomorrow at 9” or “remind me here in 2 hours”.</div>
              <div className="row mt" style={{ justifyContent: 'center' }}><button type="button" className="btn primary" disabled={!canRun} onClick={() => setEdit({ ...blank, name: 'Morning server check', prompt: 'Check disk space, memory, failed systemd units, unhealthy Docker containers and last night’s backups. Keep it short.' })}><Icon name="plus" size={14} />New schedule</button></div>
            </Empty>
          ) : (
            <div className="stack">
              {ctx.menu}
              {upcoming.length > 0 && <section className="stack tight"><h3 className="srv-h">Coming up<span className="muted">{upcoming.length}</span></h3><div className="list-cards">{upcoming.map(card)}</div></section>}
              {beatList.length > 0 && <section className="stack tight"><h3 className="srv-h">Back to a conversation<span className="muted">{beatList.length}</span></h3><div className="list-cards">{beatList.map(beatCard)}</div></section>}
              {repeating.length > 0 && <section className="stack tight"><h3 className="srv-h">Repeating<span className="muted">{repeating.length}</span></h3><div className="list-cards">{repeating.map(card)}</div></section>}
              {done.length > 0 && <section className="stack tight">
                <h3 className="srv-h"><button type="button" className="link-btn" onClick={() => setShowDone(v => !v)}><Icon name={showDone ? 'chevron-down' : 'chevron-right'} size={12} />Done</button><span className="muted">{done.length}</span>
                  {showDone && <button type="button" className="link-btn xsmall" style={{ marginLeft: 'auto' }} onClick={() => void clearDone()}>Clear</button>}</h3>
                {showDone && <div className="list-cards">{done.map(card)}</div>}
              </section>}
            </div>
          )}
        </div>
      </div>
      {edit && <ScheduleDialog draft={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); list.reload() }} />}
      {del && <Confirm title={`Delete “${del.name}”?`} message="Threads from earlier runs stay." onCancel={() => setDel(null)}
        onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/schedules/${d.id}`); list.reload(); toast('Schedule deleted') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

const oneLine = (s: string, n: number) => { const l = s.replace(/\s+/g, ' ').trim(); return l.length > n ? l.slice(0, n - 1) + '…' : l }

/** "once 2026-11-12 09:00" → the datetime-local value "2026-11-12T09:00". */
const onceLocal = (spec: string) => { const m = /^once (\d{4}-\d{2}-\d{2})[ t](\d{2}:\d{2})$/i.exec(spec.trim()); return m ? `${m[1]}T${m[2]}` : '' }

export function ScheduleDialog({ draft, onClose, onSaved }: { draft: Draft; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState<Draft>(draft)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const { hosts } = useApp()
  const spec = useDebounce(f.spec.trim(), 250)
  useEffect(() => {
    if (!spec) { setPreview(null); return }
    let live = true
    api.get<SchedulePreview>(`${V1}/schedules/preview${qs({ spec })}`).then(p => live && setPreview(p)).catch(e => live && setPreview({ ok: false, next: [], error: errMsg(e) }))
    return () => { live = false }
  }, [spec])
  const once = isOnce(f.spec)
  const setKind = (o: boolean) => {
    if (o === once) return
    setF({ ...f, spec: o ? 'tomorrow 09:00' : 'daily 08:00', notify: o ? 'always' : 'attention', enabled: true })
  }
  const shown = onceLocal(f.spec) || (preview?.ok && preview.spec ? onceLocal(preview.spec) : '')
  const save = async () => {
    setBusy(true)
    try {
      const body = { ...f, name: f.name.trim() || (once ? 'Later: ' + oneLine(f.prompt, 50) : f.name) }
      if (f.id) await api.put(`${V1}/schedules/${f.id}`, body); else await api.post(`${V1}/schedules`, body)
      toast(f.id ? 'Schedule saved' : once && preview?.next[0] ? `Runs ${when(preview.next[0])}` : 'Schedule added')
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={f.id ? 'Edit schedule' : once ? 'Run once, later' : 'New schedule'} onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || (!once && !f.name.trim()) || !f.prompt.trim() || preview?.ok === false} onClick={save}>{busy && <Spinner />}{f.id ? 'Save' : 'Add'}</button>
    </>}>
      <div className="stack">
        <Field label="Runs" group>
          <Segmented value={once ? 'once' : 'repeat'} onChange={v => setKind(v === 'once')} options={[{ value: 'once', label: 'Once' }, { value: 'repeat', label: 'On repeat' }]} label="Runs" />
        </Field>
        <Field label="Prompt" help={once ? 'It runs as a new conversation and does not see this one — write the whole task.' : 'The agent is also asked to start its answer with OK: or ATTENTION: — that decides whether you are notified.'}>
          <textarea {...noGrammarly} autoFocus={!!f.from_thread_id} value={f.prompt} onChange={e => setF({ ...f, prompt: e.target.value })} rows={5} />
        </Field>
        {once ? (
          <Field label="When" help={<>or {ONCE_EXAMPLES.map((x, i) => <span key={x}>{i > 0 && ', '}<button type="button" className="link-btn" onClick={() => setF({ ...f, spec: x })}><code>{x}</code></button></span>)}</>}>
            <input type="datetime-local" value={shown} onChange={e => e.target.value && setF({ ...f, spec: 'once ' + e.target.value.replace('T', ' ') })} />
          </Field>
        ) : (
          <Field label="When" help={<>e.g. {SPEC_EXAMPLES.map((x, i) => <span key={x}>{i > 0 && ', '}<button type="button" className="link-btn" onClick={() => setF({ ...f, spec: x })}><code>{x}</code></button></span>)}</>}>
            <input value={f.spec} onChange={e => setF({ ...f, spec: e.target.value })} className="mono" spellCheck={false} autoCapitalize="off" />
          </Field>
        )}
        {preview && (preview.ok
          ? <div className="callout success"><Icon name="calendar" size={15} /><span>{once ? 'Runs ' : 'Next runs: '}{preview.next.slice(0, 3).map(n => fullWhen(n)).join(' · ')}{once && preview.next[0] && <span className="faint"> ({when(preview.next[0])})</span>}</span></div>
          : <div className="callout danger"><Icon name="alert" size={15} /><span>{preview.error || 'That does not look like a schedule'}</span></div>)}
        <Field label={once ? 'Name (optional)' : 'Name'}><input autoFocus={!f.from_thread_id} value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder={once ? 'Later: …' : 'Morning server check'} /></Field>
        {f.from_thread_id && <label className="inline-switch"><Switch checked={!!f.report} onChange={v => setF({ ...f, report: v })} label="Answer back to the conversation" />Send the answer back to the conversation it came from</label>}
        <Field label="Notify me" group>
          <Segmented value={f.notify} onChange={v => setF({ ...f, notify: v })} options={[
            { value: 'attention', label: 'Only when something’s wrong' }, { value: 'always', label: 'Every run' }, { value: 'never', label: 'Never' },
          ]} label="Notify" />
        </Field>
        {hosts.length > 0 && <Field label="Runs on" group help="This server, or a machine from Settings → Servers (it uses that machine’s own login).">
          <HostPicker value={f.host_id} onChange={host_id => setF({ ...f, host_id, cwd: '' })} className="btn" />
        </Field>}
        <Field label="Agent" group><AgentPicker allowDefault noAccount={!!f.host_id} value={f} onChange={p => setF(x => ({ ...x, ...p }))} /></Field>
        <Field label="Folder" group>
          <div className="row"><FolderButton value={f.cwd} host={f.host_id} onChange={cwd => setF({ ...f, cwd })} className="btn" />{f.cwd && <button type="button" className="btn ghost sm" onClick={() => setF({ ...f, cwd: '' })}>Use default</button>}</div>
        </Field>
        {!once && <label className="inline-switch"><Switch checked={f.enabled} onChange={v => setF({ ...f, enabled: v })} label="Enabled" />Enabled</label>}
      </div>
    </Modal>
  )
}
