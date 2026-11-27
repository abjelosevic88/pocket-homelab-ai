import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import type { ShareAllowance } from '../../lib/types'
import { minutesLabel, shareNote } from '../../lib/aiShare'
import { planName } from '../../lib/format'
import { Icon } from '../../components/icons'
import { Select } from '../../components/Select'
import { Avatar, Confirm, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, useAsync, useContextMenu, useToast, type MenuItem } from '../../components/ui'
import { Section } from './common'

interface Lendable { id: number; label: string; email?: string; plan?: string; signed_in: boolean; plan_used: number }
interface ShareRow extends ShareAllowance { account_id: number; label: string }
interface SharePerson { id: number; name: string; email: string; role: string; can_run: boolean; own_folder: boolean; shares: ShareRow[] }
interface SharesData { keep: number; logins: Lendable[]; people: SharePerson[] }

const pct = (n: number) => (n < 10 ? n.toFixed(1).replace(/\.0$/, '') : String(Math.round(n)))

/** What the allowance is, in a few words. */
function limitText(s: ShareAllowance) {
  const parts = [s.minutes_day > 0 ? `${minutesLabel(s.minutes_day)} a day` : '', s.percent_week > 0 ? `${pct(s.percent_week)}% of your week` : ''].filter(Boolean)
  return parts.length ? parts.join(' · ') : 'No limit'
}

/** What they used so far. */
function usedText(s: ShareAllowance) {
  const parts = [`${Math.round(s.used_minutes)} min today`]
  if (s.percent_known) parts.push(`about ${pct(s.used_percent)}% of your week`)
  return parts.join(' · ')
}

/** Settings → Users → Shared AI: lend your Claude logins to people with an allowance. */
export function SharedAISection() {
  const { toast } = useToast()
  const data = useAsync(() => api.get<SharesData>(`${V1}/ai-shares`), [])
  const [edit, setEdit] = useState<{ person: SharePerson; row?: ShareRow } | null>(null)
  const [stop, setStop] = useState<{ person: SharePerson; row: ShareRow } | null>(null)
  const [keep, setKeep] = useState('')
  const ctx = useContextMenu()
  useEffect(() => { if (data.data) setKeep(String(data.data.keep)) }, [data.data])

  const saveKeep = async () => {
    const n = Number(keep)
    if (!data.data || keep === '' || !Number.isFinite(n) || n < 0 || n > 100 || n === data.data.keep) { if (data.data) setKeep(String(data.data.keep)); return }
    try { await api.put(`${V1}/ai-shares/settings`, { keep: Math.round(n) }); toast(n === 0 ? 'Shared use no longer stops for your plan' : `Shared use stops at ${Math.round(n)}% of your plan`); data.reload() } catch (e) { toast(errMsg(e), 'err') }
  }
  const pause = async (row: ShareRow, paused: boolean) => {
    try { await api.patch(`${V1}/ai-shares/${row.account_id}`, { paused }); toast(paused ? 'Paused' : 'Resumed'); data.reload() } catch (e) { toast(errMsg(e), 'err') }
  }

  const people = data.data?.people || []
  return (
    <Section title="Shared AI" sub="Let people use your Claude logins, with a limit. Pocket AI keeps your sign-in to itself and counts what they use; they can still add logins of their own and pick one per conversation.">
      {!data.data ? (data.error ? <p className="danger-text small">{data.error}</p> : <Loading />) : (
        <div className="stack">
          <div className="share-keep">
            <span className="grow"><b>Keep the rest for me</b><br /><span className="muted small">Shared use stops for everyone when your plan is this full (5-hour or weekly). 0 = never.</span></span>
            <span className="share-num"><input type="number" min={0} max={100} inputMode="numeric" value={keep} onChange={e => setKeep(e.target.value)} onBlur={saveKeep} onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} aria-label="Stop shared use at this percent of your plan" />%</span>
          </div>
          {!people.length && <p className="muted small">Nobody else has an account yet. Add people above (or in Pocket Core); they show up here.</p>}
          <div className="set-people">
            {ctx.menu}
            {people.map(p => (
              <div key={p.id} className="share-person">
                <div className="share-person-head">
                  <Avatar name={p.name} email={p.email} size={30} />
                  <div className="grow ellipsis"><b>{p.name || p.email}</b>{p.role === 'admin' && <span className="badge accent">Admin</span>}</div>
                  {p.own_folder && <button type="button" className="btn sm" onClick={() => setEdit({ person: p })}><Icon name="plus" size={13} />Share a login</button>}
                </div>
                {!p.own_folder ? <p className="muted small share-indent">Works in the server’s own folders, so they already use your logins.</p>
                  : !p.shares.length ? <p className="muted small share-indent">Nothing shared. They use logins of their own.</p>
                  : p.shares.map(row => {
                    const more: MenuItem[] = [
                      { label: 'Change the allowance…', icon: 'edit', onSelect: () => setEdit({ person: p, row }) },
                      row.paused ? { label: 'Resume', icon: 'play', onSelect: () => pause(row, false) } : { label: 'Pause', icon: 'stop', onSelect: () => pause(row, true) },
                      'sep',
                      { label: 'Stop sharing…', icon: 'trash', danger: true, onSelect: () => setStop({ person: p, row }) },
                    ]
                    return (
                      <div key={row.account_id} className="share-row share-indent" onContextMenu={e => ctx.open(e, more)}>
                        <div className="grow">
                          <div className="ellipsis"><b>{row.from_label}</b> · {limitText(row)}{row.paused ? <span className="badge">Paused</span> : row.blocked ? <span className="badge warn" title={row.blocked}>Stopped now</span> : null}</div>
                          <div className="muted small ellipsis" title={row.blocked || undefined}>{usedText(row)}{row.requests_today ? ` · ${row.requests_today} requests` : ''}</div>
                        </div>
                        <MenuButton label={`More for ${row.from_label}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                      </div>
                    )
                  })}
              </div>
            ))}
          </div>
          <p className="muted small">The weekly share is an estimate: what they used against what your plan’s week costs, from everything Pocket AI sees on that account. Use outside Pocket AI (claude.ai, the desktop app) makes their share look bigger, never smaller.</p>
        </div>
      )}
      {edit && data.data && <ShareDialog person={edit.person} row={edit.row} logins={data.data.logins} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); data.reload() }} />}
      {stop && <Confirm title={`Stop sharing ${stop.row.from_label} with ${stop.person.name || stop.person.email}?`} confirmLabel="Stop sharing"
        message="It is gone from their logins at once and stops working mid-answer. Their conversations stay; they go on with a login of their own."
        onCancel={() => setStop(null)} onConfirm={async () => { const s = stop; setStop(null); try { await api.del(`${V1}/ai-shares/${s.row.account_id}`); toast('Stopped sharing'); data.reload() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Section>
  )
}

const MINUTES = [0, 30, 60, 120, -1]
const PERCENTS = [0, 5, 10, 25, -1]

function ShareDialog({ person, row, logins, onClose, onSaved }: { person: SharePerson; row?: ShareRow; logins: Lendable[]; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const taken = new Set(person.shares.filter(s => s.account_id !== row?.account_id).map(s => s.from))
  const first = logins.find(l => l.signed_in && !taken.has(l.id))
  const [from, setFrom] = useState<number | null>(row ? row.from : first?.id ?? null)
  const [minutes, setMinutes] = useState(row ? row.minutes_day : 60)
  const [percent, setPercent] = useState(row ? row.percent_week : 0)
  const [customM, setCustomM] = useState(!MINUTES.includes(row?.minutes_day ?? 60))
  const [customP, setCustomP] = useState(!PERCENTS.includes(row?.percent_week ?? 0))
  const [busy, setBusy] = useState(false)
  const name = person.name || person.email
  const save = async () => {
    setBusy(true)
    try {
      if (row) await api.patch(`${V1}/ai-shares/${row.account_id}`, { minutes_day: minutes, percent_week: percent })
      else await api.post(`${V1}/ai-shares`, { user_id: person.id, from, minutes_day: minutes, percent_week: percent })
      toast(row ? 'Saved' : `Shared with ${name}`)
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={row ? `${row.from_label} for ${name}` : `Share a login with ${name}`} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || from == null} onClick={save}>{busy && <Spinner />}{row ? 'Save' : 'Share'}</button>
    </>}>
      <div className="stack">
        {!row && <Field label="Login" help="Their agent runs on it through Pocket AI, which keeps your sign-in to itself.">
          <Select value={from ?? -1} onChange={v => setFrom(Number(v))} label="Login" searchable={false} options={logins.map(l => ({
            value: l.id, label: l.label, disabled: !l.signed_in || taken.has(l.id),
            description: taken.has(l.id) ? 'already shared with them' : !l.signed_in ? 'not signed in here' : [l.plan && planName(l.plan), l.email, l.plan_used ? `${Math.round(l.plan_used * 100)}% used` : ''].filter(Boolean).join(' · '),
          }))} />
        </Field>}
        <Field label="Time a day" group help="Agent working time, counted while it thinks and runs its tools. Comes back at midnight.">
          <div className="share-choice">
            <Segmented size="sm" label="Time a day" value={customM ? -1 : minutes} onChange={v => { if (v === -1) { setCustomM(true) } else { setCustomM(false); setMinutes(v) } }}
              options={MINUTES.map(m => ({ value: m, label: m === 0 ? 'No limit' : m === -1 ? 'Other' : minutesLabel(m) }))} />
            {customM && <span className="share-num"><input type="number" min={1} max={1440} inputMode="numeric" value={minutes || ''} onChange={e => setMinutes(Math.max(0, Math.min(1440, Math.round(Number(e.target.value) || 0))))} aria-label="Minutes a day" autoFocus />min</span>}
          </div>
        </Field>
        <Field label="Share of your weekly limit" group help="About this much of your plan’s week. Comes back when your plan’s week starts again.">
          <div className="share-choice">
            <Segmented size="sm" label="Share of your weekly limit" value={customP ? -1 : percent} onChange={v => { if (v === -1) { setCustomP(true) } else { setCustomP(false); setPercent(v) } }}
              options={PERCENTS.map(p => ({ value: p, label: p === 0 ? 'No limit' : p === -1 ? 'Other' : `${p}%` }))} />
            {customP && <span className="share-num"><input type="number" min={0.5} max={100} step={0.5} inputMode="decimal" value={percent || ''} onChange={e => setPercent(Math.max(0, Math.min(100, Number(e.target.value) || 0)))} aria-label="Percent of the week" autoFocus />%</span>}
          </div>
        </Field>
        {row && <p className="muted small">Now: {shareNote(row)}.</p>}
        {!person.can_run && !row && <p className="muted small">Sharing also lets {name} run agents (in a folder of their own).</p>}
      </div>
    </Modal>
  )
}
