import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import type { ProviderId, ShareAllowance } from '../../lib/types'
import { minutesLabel, shareNote } from '../../lib/aiShare'
import { planName, providerName } from '../../lib/format'
import { Icon } from '../../components/icons'
import { Select } from '../../components/Select'
import { Avatar, Confirm, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, useAsync, useContextMenu, useToast, type MenuItem } from '../../components/ui'
import { Section } from './common'

interface Lendable { id: number; provider: ProviderId; label: string; email?: string; plan?: string; signed_in: boolean; plan_used: number; plan_known: boolean; why?: string }
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
    <Section title="Shared AI" sub="Let people use your agents (Claude Code, Codex, Antigravity, Gemini CLI, OpenCode, Qwen Code), with a limit. Everything they use goes through Pocket AI. Pocket AI keeps your sign-in to itself and counts what they use; they can still add logins of their own and pick one per conversation.">
      {!data.data ? (data.error ? <p className="danger-text small">{data.error}</p> : <Loading />) : (
        <div className="stack">
          <div className="aishare-keep">
            <span className="grow"><b>Keep the rest for me</b><br /><span className="muted small">Shared use stops for everyone when your plan is this full (5-hour or weekly). 0 = never.</span></span>
            <span className="aishare-num"><input type="number" min={0} max={100} inputMode="numeric" value={keep} onChange={e => setKeep(e.target.value)} onBlur={saveKeep} onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} aria-label="Stop shared use at this percent of your plan" />%</span>
          </div>
          {!people.length && <p className="muted small">Nobody else has an account yet. Add people above (or in Pocket Core); they show up here.</p>}
          <div className="set-people">
            {ctx.menu}
            {people.map(p => (
              <div key={p.id} className="aishare-person">
                <div className="aishare-person-head">
                  <Avatar name={p.name} email={p.email} size={30} />
                  <div className="grow ellipsis"><b>{p.name || p.email}</b>{p.role === 'admin' && <span className="badge accent">Admin</span>}</div>
                  {p.own_folder && <button type="button" className="btn sm" onClick={() => setEdit({ person: p })}><Icon name="plus" size={13} />Share a login</button>}
                </div>
                {!p.own_folder ? <p className="muted small aishare-indent">Works in the server’s own folders, so they already use your logins.</p>
                  : !p.shares.length ? <p className="muted small aishare-indent">Nothing shared. They use logins of their own.</p>
                  : p.shares.map(row => {
                    const more: MenuItem[] = [
                      { label: 'Change the allowance…', icon: 'edit', onSelect: () => setEdit({ person: p, row }) },
                      row.paused ? { label: 'Resume', icon: 'play', onSelect: () => pause(row, false) } : { label: 'Pause', icon: 'stop', onSelect: () => pause(row, true) },
                      'sep',
                      { label: 'Stop sharing…', icon: 'trash', danger: true, onSelect: () => setStop({ person: p, row }) },
                    ]
                    return (
                      <div key={row.account_id} className="aishare-row aishare-indent" onContextMenu={e => ctx.open(e, more)}>
                        <div className="grow">
                          <div className="ellipsis"><Icon name={row.provider} size={13} className={`inline-ico prov-glyph ${row.provider}`} /><b>{providerName(row.provider)} · {row.from_label}</b>{row.paused ? <span className="badge">Paused</span> : row.blocked ? <span className="badge warn" title={row.blocked}>Stopped now</span> : null}</div>
                          <div className="small">{limitText(row)}</div>
                          <div className="muted small" title={row.blocked || undefined}>{usedText(row)}{row.requests_today ? ` · ${row.requests_today} requests` : ''}</div>
                        </div>
                        <MenuButton label={`More for ${row.from_label}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                      </div>
                    )
                  })}
              </div>
            ))}
          </div>
          <p className="muted small">The weekly share is an estimate: what they used against what your plan’s week costs, from everything Pocket AI sees on that account. Use outside Pocket AI (claude.ai, ChatGPT, the desktop apps) makes their share look bigger, never smaller.</p>
        </div>
      )}
      {edit && data.data && <ShareDialog person={edit.person} row={edit.row} logins={data.data.logins} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); data.reload() }} />}
      {stop && <Confirm title={`Stop sharing ${stop.row.from_label} with ${stop.person.name || stop.person.email}?`} confirmLabel="Stop sharing"
        message="It is gone from their logins at once and stops working mid-answer. Their conversations stay; they go on with a login of their own."
        onCancel={() => setStop(null)} onConfirm={async () => { const s = stop; setStop(null); try { await api.del(`${V1}/ai-shares/${s.row.account_id}`); toast('Stopped sharing'); data.reload() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Section>
  )
}

const MINUTES = [0, 30, 60, 120]
const PERCENTS = [0, 5, 10, 25]
const loginKey = (l: { provider: string; id: number }) => `${l.provider}:${l.id}`

/** Presets plus a box to type any amount in (empty = no limit). */
function Amount({ label, value, onChange, presets, presetLabel, unit, max, step = 1 }: { label: string; value: number; onChange: (v: number) => void; presets: number[]; presetLabel: (v: number) => string; unit: string; max: number; step?: number }) {
  const [text, setText] = useState(value ? String(value) : '')
  useEffect(() => { if ((Number(text) || 0) !== value) setText(value ? String(value) : '') }, [value]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="aishare-choice">
      <Segmented size="sm" label={label} value={presets.includes(value) ? value : null} onChange={v => onChange(v)} options={presets.map(p => ({ value: p, label: presetLabel(p) }))} />
      <span className="aishare-num"><input type="number" min={0} max={max} step={step} inputMode="decimal" placeholder="Any" value={text}
        onChange={e => { setText(e.target.value); const n = Number(e.target.value); onChange(Number.isFinite(n) && n > 0 ? Math.min(max, step < 1 ? Math.round(n * 10) / 10 : Math.round(n)) : 0) }} aria-label={`${label}, any amount`} />{unit}</span>
    </div>
  )
}

function ShareDialog({ person, row, logins, onClose, onSaved }: { person: SharePerson; row?: ShareRow; logins: Lendable[]; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const taken = new Set(person.shares.filter(s => s.account_id !== row?.account_id).map(s => loginKey({ provider: s.provider, id: s.from })))
  const first = logins.find(l => l.signed_in && !taken.has(loginKey(l)))
  const [pick, setPick] = useState<string>(row ? loginKey({ provider: row.provider, id: row.from }) : first ? loginKey(first) : '')
  const [minutes, setMinutes] = useState(row ? row.minutes_day : 60)
  const [percent, setPercent] = useState(row ? row.percent_week : 0)
  const [busy, setBusy] = useState(false)
  const name = person.name || person.email
  const chosen = logins.find(l => loginKey(l) === pick)
  const save = async () => {
    setBusy(true)
    try {
      if (row) await api.patch(`${V1}/ai-shares/${row.account_id}`, { minutes_day: minutes, percent_week: percent })
      else await api.post(`${V1}/ai-shares`, { user_id: person.id, provider: chosen?.provider, from: chosen?.id, minutes_day: minutes, percent_week: chosen?.plan_known ? percent : 0 })
      toast(row ? 'Saved' : `Shared with ${name}`)
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={row ? `${providerName(row.provider)} · ${row.from_label} for ${name}` : `Share a login with ${name}`} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || (!row && !chosen)} onClick={save}>{busy && <Spinner />}{row ? 'Save' : 'Share'}</button>
    </>}>
      <div className="stack">
        {!row && <Field label="Login" help="Any agent signed in here, or running on your keys. Their agent goes through Pocket AI, which keeps your sign-ins and keys to itself.">
          <Select value={pick} onChange={v => setPick(String(v))} label="Login" searchable={false}
            renderValue={o => <span className="prov-opt">{chosen && <Icon name={chosen.provider} size={14} className={`prov-glyph ${chosen.provider}`} />}{o?.label ?? 'Choose a login'}</span>}
            options={logins.map(l => ({
              value: loginKey(l), label: `${providerName(l.provider)} · ${l.label}`, disabled: !l.signed_in || taken.has(loginKey(l)),
              description: taken.has(loginKey(l)) ? 'already shared with them' : !l.signed_in ? (l.why || (l.provider === 'codex' ? 'not signed in with ChatGPT here' : 'not signed in here')) : [l.plan && planName(l.plan), l.email, l.plan_used ? `${Math.round(l.plan_used * 100)}% used` : ''].filter(Boolean).join(' · '),
            }))} />
        </Field>}
        <Field label="Time a day" group help="Agent working time, counted while it thinks and runs its tools. Comes back at midnight. Type any number of minutes.">
          <Amount label="Time a day" value={minutes} onChange={setMinutes} presets={MINUTES} presetLabel={m => (m === 0 ? 'No limit' : m < 60 ? `${m} min` : `${m / 60} h`)} unit="min" max={1440} />
        </Field>
        {(row ? ['claude', 'codex'].includes(row.provider) : chosen?.plan_known !== false) ? <Field label="Share of your weekly limit" group help="About this much of your plan’s week. Comes back when your plan’s week starts again. Type any percentage (0.5 steps).">
          <Amount label="Share of your weekly limit" value={percent} onChange={setPercent} presets={PERCENTS} presetLabel={p => (p === 0 ? 'No limit' : `${p}%`)} unit="%" max={100} step={0.5} />
        </Field> : <p className="muted small">{chosen ? providerName(chosen.provider) : 'This agent'} doesn’t say how full your plan is, so only its time a day can be limited.</p>}
        {row && <p className="muted small">Now: {shareNote(row)}.</p>}
        {!person.can_run && !row && <p className="muted small">Sharing also lets {name} run agents (in a folder of their own).</p>}
      </div>
    </Modal>
  )
}
