// Schedules: prompts that run on their own and tell you how it went.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, when } from '../lib/format'
import type { Schedule, SchedulePreview, Thread } from '../lib/types'
import { AgentPicker } from '../components/AgentPicker'
import { FolderButton } from '../components/FolderPicker'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Confirm, Empty, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, useAsync, useDebounce, useToast } from '../components/ui'
import { AgentSummary } from './RecipesPage'

type Draft = Pick<Schedule, 'name' | 'prompt' | 'provider' | 'model' | 'effort' | 'mode' | 'cwd' | 'spec' | 'notify' | 'enabled'> & { id?: number }

const blank: Draft = { name: '', prompt: '', provider: '', model: '', effort: '', mode: '', cwd: '', spec: 'daily 08:00', notify: 'attention', enabled: true }

const STATUS: Record<string, { label: string; cls: string; icon: string }> = {
  ok: { label: 'OK', cls: 'ok', icon: 'check-circle' },
  attention: { label: 'Needs attention', cls: 'warn', icon: 'alert' },
  failed: { label: 'Failed', cls: 'danger', icon: 'x-circle' },
  running: { label: 'Running', cls: 'accent-soft', icon: 'refresh' },
}

const SPEC_EXAMPLES = ['every 30m', 'every 6h', 'daily 08:00', 'weekdays 08:30', 'weekly mon 09:00', '0 */4 * * *']

export default function SchedulesPage({ onMenu }: { onMenu?: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { upsertThread, user } = useApp()
  const list = useAsync(() => api.get<Schedule[]>(`${V1}/schedules`), [])
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<Schedule | null>(null)
  const [running, setRunning] = useState<number | null>(null)
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  // the "running" status and next run change on their own
  useEffect(() => { const t = setInterval(() => { if (!document.hidden) list.reload() }, 30000); return () => clearInterval(t) }, [list.reload]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = async (s: Schedule, enabled: boolean) => {
    list.setData(l => l && l.map(x => x.id === s.id ? { ...x, enabled } : x))
    try { await api.put(`${V1}/schedules/${s.id}`, { ...s, enabled }); list.reload() } catch (e) { toast(errMsg(e), 'err'); list.reload() }
  }
  const runNow = async (s: Schedule) => {
    setRunning(s.id)
    try { const t = await api.post<Thread>(`${V1}/schedules/${s.id}/run`); upsertThread(t); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setRunning(null) }
  }

  return (
    <div className="page">
      <PageHead title="Schedules" icon="clock" onMenu={onMenu} sub="Checks that run by themselves — you hear about it when something needs attention."
        actions={<button type="button" className="btn primary" disabled={!canRun} onClick={() => setEdit({ ...blank })}><Icon name="plus" size={14} />New</button>} />
      <div className="page-scroll">
        <div className="page-inner">
          {list.error && <div className="callout danger"><Icon name="alert" size={15} /><span>{list.error}</span></div>}
          {!list.data ? (!list.error && <Loading />) : list.data.length === 0 ? (
            <Empty title="Nothing scheduled" icon="clock">
              For example: every morning at 8, check disk space, backups and unhealthy containers — and only ping me if something is wrong.
              <div className="row mt" style={{ justifyContent: 'center' }}><button type="button" className="btn primary" disabled={!canRun} onClick={() => setEdit({ ...blank, name: 'Morning server check', prompt: 'Check disk space, memory, failed systemd units, unhealthy Docker containers and last night’s backups. Keep it short.' })}><Icon name="plus" size={14} />New schedule</button></div>
            </Empty>
          ) : (
            <div className="list-cards">
              {list.data.map(s => {
                const st = STATUS[s.last_status]
                return (
                  <div key={s.id} className={`list-card ${s.enabled ? '' : 'off'}`}>
                    <Switch checked={s.enabled} onChange={v => toggle(s, v)} label={`Enable ${s.name}`} />
                    <div className="grow" style={{ minWidth: 0 }}>
                      <div className="list-card-title">{s.name}<code className="spec-chip">{s.spec}</code></div>
                      <div className="list-card-sub">{s.prompt}</div>
                      <div className="sched-meta">
                        {s.enabled && s.next_run_at && <span><Icon name="clock" size={11} />next {when(s.next_run_at)}</span>}
                        {s.last_run_at && <span>last {ago(s.last_run_at)}</span>}
                        {st && <span className={`badge ${st.cls}`}><Icon name={st.icon} size={10} />{st.label}</span>}
                        {s.last_thread_id && <button type="button" className="link-btn xsmall" onClick={() => navigate(`/t/${s.last_thread_id}`)}>Open last run</button>}
                        <span className="faint">notify: {s.notify}</span>
                      </div>
                      {s.last_summary && <div className={`sched-summary ${s.last_status}`}>{s.last_summary}</div>}
                      <AgentSummary c={s} />
                    </div>
                    <div className="list-card-actions">
                      <button type="button" className="btn sm" disabled={running === s.id || !canRun} onClick={() => runNow(s)}>{running === s.id ? <Spinner size={11} /> : <Icon name="play" size={11} />}Run now</button>
                      <MenuButton label={`More for ${s.name}`} align="right" items={[
                        { label: 'Edit…', icon: 'edit', onSelect: () => setEdit({ ...s }) },
                        { label: 'Duplicate', icon: 'copy', onSelect: () => setEdit({ ...s, id: undefined, name: s.name + ' (copy)' }) },
                        'sep',
                        { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDel(s) },
                      ]}><Icon name="more" size={16} /></MenuButton>
                    </div>
                  </div>
                )
              })}
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

function ScheduleDialog({ draft, onClose, onSaved }: { draft: Draft; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState<Draft>(draft)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const spec = useDebounce(f.spec.trim(), 250)
  useEffect(() => {
    if (!spec) { setPreview(null); return }
    let live = true
    api.get<SchedulePreview>(`${V1}/schedules/preview${qs({ spec })}`).then(p => live && setPreview(p)).catch(e => live && setPreview({ ok: false, next: [], error: errMsg(e) }))
    return () => { live = false }
  }, [spec])
  const save = async () => {
    setBusy(true)
    try {
      if (f.id) await api.put(`${V1}/schedules/${f.id}`, f); else await api.post(`${V1}/schedules`, f)
      toast(f.id ? 'Schedule saved' : 'Schedule added')
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={f.id ? 'Edit schedule' : 'New schedule'} onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !f.name.trim() || !f.prompt.trim() || preview?.ok === false} onClick={save}>{busy && <Spinner />}{f.id ? 'Save' : 'Add'}</button>
    </>}>
      <div className="stack">
        <Field label="Name"><input autoFocus value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="Morning server check" /></Field>
        <Field label="Prompt" help="The agent is also asked to start its answer with OK: or ATTENTION: — that decides whether you are notified.">
          <textarea value={f.prompt} onChange={e => setF({ ...f, prompt: e.target.value })} rows={5} />
        </Field>
        <Field label="When" help={<>e.g. {SPEC_EXAMPLES.map((x, i) => <span key={x}>{i > 0 && ', '}<button type="button" className="link-btn" onClick={() => setF({ ...f, spec: x })}><code>{x}</code></button></span>)}</>}>
          <input value={f.spec} onChange={e => setF({ ...f, spec: e.target.value })} className="mono" spellCheck={false} autoCapitalize="off" />
        </Field>
        {preview && (preview.ok
          ? <div className="callout success"><Icon name="calendar" size={15} /><span>Next runs: {preview.next.slice(0, 3).map(n => new Date(n).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })).join(' · ')}</span></div>
          : <div className="callout danger"><Icon name="alert" size={15} /><span>{preview.error || 'That does not look like a schedule'}</span></div>)}
        <Field label="Notify me" group>
          <Segmented value={f.notify} onChange={v => setF({ ...f, notify: v })} options={[
            { value: 'attention', label: 'Only when something’s wrong' }, { value: 'always', label: 'Every run' }, { value: 'never', label: 'Never' },
          ]} label="Notify" />
        </Field>
        <Field label="Agent" group><AgentPicker allowDefault value={f} onChange={p => setF(x => ({ ...x, ...p }))} /></Field>
        <Field label="Folder" group>
          <div className="row"><FolderButton value={f.cwd} onChange={cwd => setF({ ...f, cwd })} className="btn" />{f.cwd && <button type="button" className="btn ghost sm" onClick={() => setF({ ...f, cwd: '' })}>Use default</button>}</div>
        </Field>
        <label className="inline-switch"><Switch checked={f.enabled} onChange={v => setF({ ...f, enabled: v })} label="Enabled" />Enabled</label>
      </div>
    </Modal>
  )
}
