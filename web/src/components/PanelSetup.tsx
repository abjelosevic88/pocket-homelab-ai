// The start sheet of a panel: what to ask, who takes part (agent, model, login,
// point of view), who writes the summary, and how deep it goes.
import React, { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, uploadWithProgress, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { planName, providerName, resetsIn, shortProviderName } from '../lib/format'
import { ACCESS, DEPTHS, KIND_ICON, loginQuota, panelRuns, sameLogin, type Access, type Depth, type Lineup, type OwnKind, type Panel, type PanelDraft, type PanelLogin, type PanelOptions, type PanelStage, type SeatSpec } from '../lib/panels'
import type { Provider, ProviderId, Upload } from '../lib/types'
import { shrink } from './Composer'
import { effortLabel, effortOptions } from './AgentPicker'
import { FolderButton } from './FolderPicker'
import { HostPicker } from './HostPicker'
import { Icon } from './icons'
import { PickedChips, PocketPicker, type PickedItem } from './PocketPicker'
import { type Option, Select } from './Select'
import { Confirm, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, useToast, type MenuItem, noGrammarly } from './ui'

/** A picture pasted, dropped or picked for the question: uploaded at once, sent with Start. */
interface Pic { lid: string; name: string; preview: string; progress: number; upload?: Upload; error?: string; abort?: () => void }
let picSeq = 0
const PICS_MAX = 8
const isPicture = (f: File) => f.type.startsWith('image/') || /\.(png|jpe?g|gif|webp|hei[cf])$/i.test(f.name)

const loginKey = (l: Pick<SeatSpec, 'account_id' | 'host_id' | 'host_home'>) => `${l.host_id || 0}|${l.account_id || 0}|${l.host_home || ''}`

/** Remembered between panels on this device: the depth last used per kind. */
const lastDepth = (kind: string): Depth => (localStorage.getItem('pai-panel-depth-' + kind) as Depth) || 'standard'

function quotaNote(l: PanelLogin | undefined, model?: string, modelName?: string) {
  if (!l) return ''
  if (!l.signed_in) return 'not signed in'
  const q = loginQuota(l, model, modelName)
  if (q.used < 0) return ''
  const pct = Math.round(q.used * 100)
  return `${pct}% of the plan limit used${q.resets_at ? ', ' + resetsIn(q.resets_at) : ''}`
}

/** A thin bar: how much of the login's plan limit is used for this model. */
function Quota({ login, model, modelName }: { login: PanelLogin | undefined; model?: string; modelName?: string }) {
  const q = loginQuota(login, model, modelName)
  if (!login || q.used < 0) return null
  const pct = Math.min(100, Math.round(q.used * 100))
  const cls = q.used >= 0.999 ? 'full' : q.used >= 0.85 ? 'high' : ''
  return <span className={`pn-quota ${cls}`} title={quotaNote(login, model, modelName)}><span className="pn-quota-bar"><span style={{ width: pct + '%' }} /></span><span className="tnum">{pct}%</span></span>
}

/** One participant: agent and model, effort, login, point of view. */
function SeatRow({ seat, label, onChange, onRemove, opts, providers, roles, fixedRole, only }: {
  seat: SeatSpec; label: string; onChange: (s: SeatSpec) => void; onRemove?: () => void
  opts: PanelOptions; providers: Provider[]; roles: string[]; fixedRole?: string
  /** logins on this machine only (Best of N: where the project folder is) */
  only?: number
}) {
  const logins = opts.logins.filter(l => only === undefined || l.host_id === only)
  const provs = [...new Set(logins.map(l => l.provider))]
  const models: Option<string>[] = provs.flatMap(id => {
    const p = providers.find(x => x.id === id)
    const list = p?.models || []
    if (!list.length) return [{ value: `${id}:`, label: 'Its default model', group: providerName(id) }]
    return list.map(m => ({ value: `${id}:${m.id}`, label: m.name || m.id, description: m.description, keywords: m.id, group: providerName(id) }))
  })
  const p = providers.find(x => x.id === seat.provider)
  const modelName = p?.models?.find(m => m.id === seat.model)?.name
  const mine = logins.filter(l => l.provider === seat.provider)
  const login = mine.find(l => sameLogin(l, seat))
  const loginOpts: Option<string>[] = mine.map(l => ({
    value: loginKey(l), label: mine.some(o => o.where !== l.where) ? `${l.where} · ${l.label}` : l.label, group: mine.some(o => o.where !== l.where) ? l.where : undefined,
    description: [l.plan && planName(l.plan), l.email, quotaNote(l, seat.model, modelName)].filter(Boolean).join(' · ') || l.error, disabled: !l.signed_in && !!l.error,
  }))
  const efforts = effortOptions(p, seat.model, true)
  const pickModel = (v: string) => {
    const at = v.indexOf(':')
    const provider = v.slice(0, at) as ProviderId, model = v.slice(at + 1)
    if (provider === seat.provider) return onChange({ ...seat, model })
    const first = logins.filter(l => l.provider === provider).sort((a, b) => Number(!a.signed_in) - Number(!b.signed_in) || a.host_id - b.host_id)[0]
    onChange({ ...seat, provider, model, effort: '', account_id: first?.account_id || 0, host_id: first?.host_id || 0, host_home: first?.host_home || '' })
  }
  const pickLogin = (k: string) => {
    const l = mine.find(x => loginKey(x) === k)
    if (l) onChange({ ...seat, account_id: l.account_id, host_id: l.host_id, host_home: l.host_home })
  }
  const roleOpts: Option<string>[] = [{ value: '', label: 'No special view' }, ...roles.map(r => ({ value: r, label: r })), ...(seat.role && !roles.includes(seat.role) ? [{ value: seat.role, label: seat.role }] : [])]
  return (
    <div className="pn-seat">
      <span className={`pn-letter ${label ? '' : 'chair'}`}>{label || <Icon name="edit" size={12} />}</span>
      <div className="pn-seat-picks">
        <Select size="sm" className="inline pn-pick-model" label="Agent and model" value={`${seat.provider}:${seat.model}`} options={models} onChange={pickModel} width={300} tabs={provs.length > 1}
          groupIcon={g => { const id = providers.find(x => providerName(x.id) === g)?.id; return id && <Icon name={id} size={13} className={`prov-glyph ${id}`} /> }}
          renderValue={o => <span className="ellipsis"><Icon name={seat.provider} size={14} className={`inline-ico prov-glyph ${seat.provider}`} />{shortProviderName(seat.provider)} · {o?.label ?? (seat.model || 'default')}</span>} />
        {efforts.length > 1 && <Select size="sm" className="inline" label="Effort" value={seat.effort} options={efforts} onChange={v => onChange({ ...seat, effort: v })} width={170}
          renderValue={o => <span className="ellipsis"><Icon name="brain" size={12} className="inline-ico" />{seat.effort ? effortLabel(seat.effort) : (o?.label ?? 'Effort')}</span>} />}
        {loginOpts.length > 1 && <Select size="sm" className="inline" label="Login" value={login ? loginKey(login) : loginKey(seat)} options={loginOpts} onChange={pickLogin} width={320}
          renderValue={() => <span className="ellipsis"><Icon name={seat.host_id ? 'server' : 'user'} size={12} className="inline-ico" />{login ? (login.host_id ? `${login.where} · ${login.label}` : login.label) : 'Login'}</span>} />}
        {fixedRole ? <span className={`pn-side ${fixedRole === 'For' ? 'for' : 'against'}`}>{fixedRole}</span>
          : roles.length > 0 && <Select size="sm" className="inline" label="Point of view" value={seat.role} options={roleOpts} onChange={v => onChange({ ...seat, role: v })} width={220}
            renderValue={o => <span className="ellipsis"><Icon name="eye" size={12} className="inline-ico" />{seat.role || o?.label || 'Point of view'}</span>} />}
        {login && !login.signed_in && <span className="pn-warn"><Icon name="alert" size={12} />not signed in</span>}
      </div>
      {/* its own place at the end of the row: it never drops under the pickers */}
      <Quota login={login} model={seat.model} modelName={modelName} />
      {onRemove && <button type="button" className="btn ghost icon-only sm" aria-label={`Remove participant ${label}`} title="Remove" onClick={onRemove}><Icon name="x" size={14} /></button>}
    </div>
  )
}

const WHO: Option<string>[] = [{ value: 'all', label: 'Every participant', description: 'Each answers on its own' }, { value: 'chair', label: 'The summary writer', description: 'One model, from what the others wrote' }]
const SEES: Option<string>[] = [
  { value: '', label: 'Nothing from before', description: 'Only the question and what they said themselves' },
  { value: 'others', label: 'The others’ latest answers', description: 'By letter, not by name' },
  { value: 'all', label: 'Everyone’s latest answers' },
  { value: 'everything', label: 'Everything said so far', description: 'Every step, every participant' },
]
const blankStages: PanelStage[] = [
  { name: 'First answers', who: 'all', sees: '', prompt: 'Answer the question as well as you can.' },
  { name: 'Summary', who: 'chair', sees: 'everything', prompt: 'Sum up what the participants said: what they agree on, where they differ, and what you recommend.' },
]

/** Make or change one of the person's own kinds of panel: named steps. */
function OwnKindDialog({ kind, onClose, onSaved }: { kind: Partial<OwnKind>; onClose: () => void; onSaved: (k: OwnKind) => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(kind.name || '')
  const [description, setDescription] = useState(kind.description || '')
  const [stages, setStages] = useState<PanelStage[]>(kind.stages?.length ? kind.stages : blankStages)
  const [busy, setBusy] = useState(false)
  const set = (i: number, patch: Partial<PanelStage>) => setStages(l => l.map((s, j) => j === i ? { ...s, ...patch } : s))
  const save = async () => {
    setBusy(true)
    try {
      const body = { name, description, stages }
      onSaved(kind.id ? await api.put<OwnKind>(`${V1}/panels/kinds/${kind.id}`, body) : await api.post<OwnKind>(`${V1}/panels/kinds`, body))
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={kind.id ? 'Edit your kind of panel' : 'Your own kind of panel'} onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !name.trim() || stages.some(s => !s.prompt.trim())} onClick={save}>{busy && <Spinner />}Save</button>
    </>}>
      <div className="stack">
        <Field label="Name"><input autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="Pre-mortem" maxLength={60} /></Field>
        <Field label="What it is for (optional)"><input value={description} onChange={e => setDescription(e.target.value)} placeholder="Imagine it failed: each model says why" maxLength={300} /></Field>
        <div className="stack tight">
          <span className="label">Steps</span>
          {stages.map((s, i) => (
            <div key={i} className="pn-step-edit">
              <div className="row wrap">
                <span className="pn-letter">{i + 1}</span>
                <input className="grow" value={s.name} onChange={e => set(i, { name: e.target.value })} placeholder={`Step ${i + 1}`} maxLength={60} aria-label="Step name" />
                {stages.length > 1 && <button type="button" className="btn ghost icon-only sm" aria-label="Remove step" onClick={() => setStages(l => l.filter((_, j) => j !== i))}><Icon name="x" size={14} /></button>}
              </div>
              <div className="row wrap">
                <Select size="sm" label="Who answers" value={i === 0 ? 'all' : s.who} disabled={i === 0} options={WHO} onChange={v => set(i, { who: v as PanelStage['who'], sees: v === 'chair' && !s.sees ? 'everything' : s.sees })} width={260} searchable={false} />
                {i > 0 && <Select size="sm" label="What they are shown" value={s.sees} options={SEES} onChange={v => set(i, { sees: v as PanelStage['sees'] })} width={280} searchable={false} />}
              </div>
              <textarea {...noGrammarly} rows={3} value={s.prompt} onChange={e => set(i, { prompt: e.target.value })} placeholder="What they are told to do in this step" aria-label="Instructions" />
            </div>
          ))}
          {stages.length < 8 && <button type="button" className="btn sm" style={{ justifySelf: 'start' }} onClick={() => setStages(l => [...l, { name: '', who: 'all', sees: 'others', prompt: '' }])}><Icon name="plus" size={13} />Add a step</button>}
        </div>
      </div>
    </Modal>
  )
}

/** Who takes part when nobody is picked: a "/" command, a panel an agent starts (/refine),
 *  and what a new panel's sheet starts with. Every agent that works on the server is listed;
 *  switch off the ones that should stay out. Saved as the person's default line-up. */
// questions about something you look at: drawings come on by themselves
const VISUAL = /\b(icons?|logos?|favicons?|mock-?ups?|wireframes?|layouts?|screens?|ui|ux|designs?|redesign|sketch(es)?|drawings?|illustrations?|posters?|diagrams?|charts?|colou?rs?|palettes?|brand(ing)?|visual|look and feel|landing page)\b/i

export function PanelDefaults({ onClose }: { onClose: () => void }) {
  const { toast } = useToast()
  const { providers } = useApp()
  const [opts, setOpts] = useState<PanelOptions | null>(null)
  const [err, setErr] = useState('')
  const [rows, setRows] = useState<{ seat: SeatSpec; on: boolean }[]>([])
  const [chair, setChair] = useState<SeatSpec | null>(null)
  const [access, setAccess] = useState<Access>('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.get<PanelOptions>(`${V1}/panels/options`).then(o => {
      setOpts(o)
      const def = o.lineups.find(l => l.default)
      const on = (def ? def.seats : o.suggested.seats).map(s => ({ seat: { ...s, role: '' }, on: true }))
      // the agents that are left out today: listed, switched off
      const off = (o.automatic || []).filter(a => !on.some(r => r.seat.provider === a.provider)).map(s => ({ seat: { ...s, role: '' }, on: false }))
      setRows([...on, ...off])
      setChair(def?.chair || o.suggested.chair)
      setAccess(def?.access || '')
    }).catch(e => setErr(errMsg(e)))
  }, [])
  const def = opts?.lineups.find(l => l.default)
  const picked = rows.filter(r => r.on)
  const max = opts?.max_seats || 6
  const add = () => {
    if (!opts) return
    const pool = opts.logins.filter(l => l.signed_in)
    const fresh = pool.find(l => !rows.some(r => r.seat.provider === l.provider)) || pool[0]
    if (!fresh) return
    const p = providers.find(x => x.id === fresh.provider)
    const used = rows.filter(r => r.seat.provider === fresh.provider).map(r => r.seat.model)
    const model = (p?.models || []).map(m => m.id).find(m => !used.includes(m)) || p?.default_model || ''
    setRows(l => [...l, { seat: { provider: fresh.provider, model, effort: '', account_id: fresh.account_id, host_id: fresh.host_id, host_home: fresh.host_home, role: '' }, on: true }])
  }
  const save = async () => {
    setBusy(true)
    try {
      const body = { name: def?.name || 'My default', seats: picked.map(r => r.seat), chair, default: true, access }
      if (def) await api.put(`${V1}/panels/lineups/${def.id}`, body); else await api.post(`${V1}/panels/lineups`, body)
      toast('Saved — used for every panel you start without picking')
      onClose()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const reset = async () => {
    if (!def) return
    setBusy(true)
    try { await api.put(`${V1}/panels/lineups/${def.id}`, { name: def.name, seats: def.seats, chair: def.chair, default: false, access: def.access }); toast('Back to one model per agent'); onClose() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Who takes part by default" onClose={onClose} size="lg" className="pn-setup" footer={<>
      {def && <button type="button" className="btn ghost" disabled={busy} onClick={reset} title="One model per agent that works on the server, as before you chose">Reset</button>}
      <span className="grow" />
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !opts || picked.length < 1 || picked.length > max} onClick={save}>{busy && <Spinner />}Save</button>
    </>}>
      {err ? <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div> : !opts ? <Loading /> : (
        <div className="stack">
          <p className="muted small" style={{ margin: 0 }}>These models take part whenever you do not pick: <code>/refine</code>, <code>/council</code> and the other commands outside the web app, panels an agent starts for you, and what a new panel starts with. Switch off the ones that should stay out.</p>
          <div className="pn-seats">
            {rows.map((r, i) => (
              <div key={i} className={`pn-default-row ${r.on ? '' : 'off'}`}>
                <Switch checked={r.on} onChange={v => setRows(l => l.map((x, j) => j === i ? { ...x, on: v } : x))} label={`${shortProviderName(r.seat.provider)} takes part`} />
                <SeatRow seat={r.seat} label={r.on ? String.fromCharCode(65 + rows.slice(0, i).filter(x => x.on).length) : '–'} opts={opts} providers={providers} roles={[]}
                  onChange={ns => setRows(l => l.map((x, j) => j === i ? { ...x, seat: ns } : x))} onRemove={rows.length > 1 ? () => setRows(l => l.filter((_, j) => j !== i)) : undefined} />
              </div>
            ))}
          </div>
          <div className="row wrap">
            <button type="button" className="btn sm" onClick={add}><Icon name="plus" size={13} />Add a model</button>
            <span className="muted xsmall">{picked.length} take part{picked.length < 2 ? ' — most panels need at least two' : picked.length > max ? ` — at most ${max}` : ''}</span>
          </div>
          {chair && <div className="stack tight">
            <span className="label">Who writes the summary</span>
            <SeatRow seat={chair} label="" opts={opts} providers={providers} roles={[]} onChange={setChair} />
          </div>}
          <Field label="What they may do without asking" group>
            <Segmented value={access} onChange={setAccess} label="What they may do without asking" options={ACCESS.map(a => ({ value: a.value, label: a.label, title: a.help }))} />
            <span className={`help ${access === 'full' ? 'pn-access-full' : ''}`}>{ACCESS.find(a => a.value === access)?.help} A panel an agent starts for you is never freer than that agent itself.</span>
          </Field>
        </div>
      )}
    </Modal>
  )
}

export function PanelSetup({ draft, onClose }: { draft: PanelDraft; onClose: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { providers, hosts, threads } = useApp()
  const [opts, setOpts] = useState<PanelOptions | null>(null)
  const [err, setErr] = useState('')
  const [kind, setKind] = useState(draft.kind || localStorage.getItem('pai-panel-kind') || 'council')
  const [own, setOwn] = useState(0)
  const [question, setQuestion] = useState(draft.question || '')
  const [material, setMaterial] = useState(draft.material || '')
  const [depth, setDepth] = useState<Depth>(lastDepth(draft.kind || 'council'))
  const [seats, setSeats] = useState<SeatSpec[]>([])
  const [chair, setChair] = useState<SeatSpec | null>(null)
  const [lineup, setLineup] = useState(0)
  const [cwd, setCwd] = useState(draft.cwd || '')
  const [hostId, setHostId] = useState(draft.hostId || 0)
  const [report, setReport] = useState(!!draft.fromThreadId)
  const [mode, setMode] = useState<'' | 'edit' | 'auto' | 'full'>('edit')
  const [access, setAccess] = useState<Access>('')
  // Draw it: the models also sketch their ideas. Remembered; it comes on by itself when the question
  // sounds visual, until the person sets it
  const [drawSet, setDrawSet] = useState<boolean | null>(null)
  // things picked from the person's other Pocket apps; picking: which box "@" was typed in (to take it out again)
  const [picks, setPicks] = useState<PickedItem[]>([])
  const [picking, setPicking] = useState<'' | 'button' | 'question' | 'material'>('')
  const [pics, setPics] = useState<Pic[]>([])
  const [dragging, setDragging] = useState(false)
  const picInput = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [saveAs, setSaveAs] = useState<string | null>(null)
  const [editKind, setEditKind] = useState<Partial<OwnKind> | null>(null)
  const [delKind, setDelKind] = useState<OwnKind | null>(null)
  const from = draft.fromThreadId ? threads?.find(t => t.id === draft.fromThreadId) : undefined

  const use = (l: { seats: SeatSpec[]; chair: SeatSpec | null; access?: Access }) => { setSeats(l.seats.map(s => ({ ...s }))); setChair(l.chair ? { ...l.chair, role: '' } : null); setAccess(l.access || '') }
  const load = () => api.get<PanelOptions>(`${V1}/panels/options`).then(o => { setOpts(o); return o }).catch(e => { setErr(errMsg(e)); return null })
  useEffect(() => {
    void load().then(o => {
      if (!o) return
      const def = o.lineups.find(l => l.default)
      if (def) setLineup(def.id)
      use(def || o.suggested)
    })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const k = opts?.kinds.find(x => x.id === kind)
  const ownKind = opts?.own_kinds.find(x => x.id === own)
  const stages = ownKind ? ownKind.stages : (k?.stages || [])
  const dnum = DEPTHS.indexOf(depth)
  const running = stages.filter(s => (s.depth || 0) <= dnum)
  const needsChair = running.some(s => s.who === 'chair')
  const draw = drawSet ?? (localStorage.getItem('pai-panel-draw') === '1' || VISUAL.test(question + ' ' + material.slice(0, 2000)))
  const roles = ownKind ? (opts?.roles || []) : (k?.roles || [])
  const building = !ownKind && kind === 'build'
  const debate = !ownKind && kind === 'debate'
  const fromHost = from?.host_id ?? draft.hostId ?? 0
  const where = cwd ? hostId : fromHost
  const pickKind = (id: string, ownId = 0) => {
    setKind(id); setOwn(ownId)
    if (!ownId) { localStorage.setItem('pai-panel-kind', id); setDepth(lastDepth(id)) }
  }
  // a chair for kinds that have a summary: the suggested one
  useEffect(() => { if (opts && needsChair && !chair && opts.suggested.chair) setChair({ ...opts.suggested.chair, role: '' }) }, [opts, needsChair, chair])

  const min = !ownKind && kind === 'review' ? 1 : 2
  const max = opts?.max_seats || 6
  const loginOf = (s: SeatSpec) => opts?.logins.find(l => sameLogin(l, s))
  const full = seats.filter(s => {
    const modelName = providers.find(x => x.id === s.provider)?.models?.find(m => m.id === s.model)?.name
    return loginQuota(loginOf(s), s.model, modelName).used >= 0.999
  })
  const out = seats.filter(s => loginOf(s) && !loginOf(s)!.signed_in)
  const sameTwice = seats.some((s, i) => seats.some((o, j) => j > i && sameLogin(s, o) && s.model === o.model && (s.role || '') === (o.role || '')))
  const runs = panelRuns(stages, dnum, seats.length)
  const rounds = running.filter(s => s.who !== 'chair').length
  const problem = !opts ? '' : seats.length < min ? (min === 1 ? 'Add a participant' : 'Add at least two participants')
    : debate && seats.length % 2 === 1 && seats.length > 2 ? 'A debate needs the same number on both sides: 2 or 4'
    : building && !(cwd || from?.cwd) ? 'Pick the project folder'
    : ''
  const picsBusy = pics.some(p => !p.upload && !p.error)
  const canStart = !!opts && !busy && !picsBusy && !problem && (!!question.trim() || !!material.trim() || picks.length > 0)

  const addSeat = () => {
    if (!opts) return
    const pool = opts.logins.filter(l => l.signed_in && (!building || l.host_id === where))
    // an agent that is not in yet, else another model of the first one
    const fresh = pool.find(l => !seats.some(s => s.provider === l.provider)) || pool[0]
    if (!fresh) { toast('No login to add: sign in under Settings → Machines', 'err'); return }
    const p = providers.find(x => x.id === fresh.provider)
    const used = seats.filter(s => s.provider === fresh.provider).map(s => s.model)
    const model = (p?.models || []).map(m => m.id).find(m => !used.includes(m)) || p?.default_model || ''
    setSeats(l => [...l, { provider: fresh.provider, model, effort: '', account_id: fresh.account_id, host_id: fresh.host_id, host_home: fresh.host_home, role: '' }])
    setLineup(0)
  }
  const start = async () => {
    setBusy(true)
    try {
      localStorage.setItem('pai-panel-depth-' + kind, depth)
      if (!building && drawSet !== null) localStorage.setItem('pai-panel-draw', drawSet ? '1' : '0')
      const p = await api.post<Panel>(`${V1}/panels`, {
        kind: ownKind ? '' : kind, recipe_id: own || undefined, question, material, depth,
        seats, chair: needsChair ? chair : undefined,
        cwd: cwd || undefined, host_id: cwd ? hostId : undefined,
        from_thread_id: draft.fromThreadId || undefined, report: !!draft.fromThreadId && report,
        mode: building ? mode : undefined, access: building ? undefined : access,
        items: picks.length ? picks : undefined,
        draw: !building && draw ? true : undefined,
        upload_ids: pics.some(p => p.upload) ? pics.flatMap(p => p.upload ? [p.upload.id] : []) : undefined,
      })
      onClose()
      navigate(`/panels/${p.id}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  // "@" typed at the start or after a space opens the picker, like in a conversation
  const typed = (box: 'question' | 'material', v: string, el: HTMLTextAreaElement) => {
    (box === 'question' ? setQuestion : setMaterial)(v)
    const at = el.selectionStart
    if (v[at - 1] === '@' && (at === 1 || /\s/.test(v[at - 2])) && v.length > (box === 'question' ? question : material).length) setPicking(box)
  }
  const togglePick = (it: PickedItem) => {
    // the "@" that opened the picker has done its job
    if (picking === 'question') setQuestion(v => v.replace(/(^|\s)@(?=\s|$)/, '$1'))
    if (picking === 'material') setMaterial(v => v.replace(/(^|\s)@(?=\s|$)/, '$1'))
    setPicks(l => l.some(p => p.app === it.app && p.id === it.id) ? l.filter(p => !(p.app === it.app && p.id === it.id)) : [...l, { app: it.app, id: it.id, title: it.title, subtitle: it.subtitle, url: it.url, attachable: it.attachable }])
  }
  const addPics = async (files: File[]) => {
    const ok = files.filter(isPicture)
    if (ok.length < files.length) toast('Panels take pictures here; add documents from your apps', 'err')
    const room = PICS_MAX - pics.length
    if (ok.length > room) toast(`At most ${PICS_MAX} pictures per panel`, 'err')
    for (const raw of ok.slice(0, Math.max(0, room))) {
      const lid = 'p' + ++picSeq
      const named = raw.name && raw.name !== 'image.png' ? raw : new File([raw], `pasted-${Date.now()}-${picSeq}.${(raw.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`, { type: raw.type })
      setPics(l => [...l, { lid, name: named.name, preview: URL.createObjectURL(named), progress: 0 }])
      const file = await shrink(named)
      const up = uploadWithProgress<Upload[] | Upload>(`${V1}/uploads`, 'file', [file], p => setPics(l => l.map(x => x.lid === lid ? { ...x, progress: p } : x)))
      setPics(l => l.map(x => x.lid === lid ? { ...x, abort: up.abort } : x))
      up.promise.then(r => {
        const u = Array.isArray(r) ? r[0] : r
        setPics(l => l.map(x => x.lid === lid ? { ...x, upload: u, progress: 1, abort: undefined } : x))
      }).catch(e => setPics(l => l.map(x => x.lid === lid ? { ...x, error: errMsg(e), abort: undefined } : x)))
    }
  }
  const removePic = (p: Pic) => {
    p.abort?.()
    if (p.upload) api.del(`${V1}/uploads/${p.upload.id}`).catch(() => {})
    URL.revokeObjectURL(p.preview)
    setPics(l => l.filter(x => x.lid !== p.lid))
  }
  // a picture pasted into either box joins the question; text pastes as usual
  const pasted = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData.items).filter(i => i.kind === 'file').map(i => i.getAsFile()).filter((f): f is File => !!f)
    if (!files.length) return
    e.preventDefault()
    void addPics(files)
  }
  const hasFiles = (e: React.DragEvent) => Array.from(e.dataTransfer.types).includes('Files')
  const saveLineup = async (name: string, asDefault: boolean, id = 0) => {
    try {
      const body = { name, seats, chair, default: asDefault, access }
      const l = id ? await api.put<Lineup>(`${V1}/panels/lineups/${id}`, body) : await api.post<Lineup>(`${V1}/panels/lineups`, body)
      await load(); setLineup(l.id); setSaveAs(null)
      toast(asDefault ? `“${name}” is your default line-up` : 'Line-up saved')
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const current = opts?.lineups.find(l => l.id === lineup)
  const lineupMenu = (): MenuItem[] => {
    if (!opts) return []
    const items: MenuItem[] = [{ label: 'Suggested', icon: 'sparkles', hint: 'one per agent', checked: !lineup, onSelect: () => { setLineup(0); use(opts.suggested) } }]
    for (const l of opts.lineups) items.push({ label: l.name, icon: 'users', hint: l.default ? 'default' : `${l.seats.length}`, checked: l.id === lineup, onSelect: () => { setLineup(l.id); use(l) } })
    items.push('sep', { label: 'Save these as a line-up…', icon: 'plus', onSelect: () => setSaveAs('') })
    if (current) {
      items.push({ label: `Update “${current.name}”`, icon: 'check', onSelect: () => void saveLineup(current.name, current.default, current.id) })
      if (!current.default) items.push({ label: `Make “${current.name}” my default`, icon: 'star', onSelect: () => void saveLineup(current.name, true, current.id) })
      items.push({ label: `Delete “${current.name}”`, icon: 'trash', danger: true, onSelect: async () => { try { await api.del(`${V1}/panels/lineups/${current.id}`); setLineup(0); await load() } catch (e) { toast(errMsg(e), 'err') } } })
    }
    return items
  }
  const kindChips = useMemo(() => opts ? [...opts.kinds.map(x => ({ key: x.id, own: 0, name: x.name, icon: KIND_ICON[x.id] || 'users' })), ...opts.own_kinds.map(x => ({ key: 'own' + x.id, own: x.id, name: x.name, icon: 'list-checks' }))] : [], [opts])

  return (
    <Modal title="New panel" onClose={onClose} size="xl" className="pn-setup" footer={<>
      <span className="pn-estimate muted small grow">{opts && seats.length > 0 && <>{seats.length} {seats.length === 1 ? 'model' : 'models'} · {rounds} {rounds === 1 ? 'round' : 'rounds'}{needsChair ? ' + summary' : ''} = <b>{runs}</b> model runs</>}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!canStart} title={problem || undefined} onClick={start}>{busy ? <Spinner /> : <Icon name="play" size={13} />}Start</button>
    </>}>
      {err ? <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div> : !opts ? <Loading /> : (
        <div className="stack">
          <div className="stack tight">
            <div className="pn-kinds">
              {kindChips.map(c => <button key={c.key} type="button" className={`chip ${(c.own ? own === c.own : !own && kind === c.key) ? 'on' : ''}`} onClick={() => pickKind(c.own ? 'recipe' : c.key, c.own)}><Icon name={c.icon} size={13} />{c.name}</button>)}
              <button type="button" className="chip pn-kind-add" onClick={() => setEditKind({})} title="Make your own kind of panel: your steps, your instructions"><Icon name="plus" size={13} />Your own</button>
            </div>
            <p className="muted small pn-kind-about">{ownKind ? (ownKind.description || 'Your own steps.') : k?.description}
              {ownKind && <> <button type="button" className="link-btn" onClick={() => setEditKind(ownKind)}>Edit</button> · <button type="button" className="link-btn" onClick={() => setDelKind(ownKind)}>Delete</button></>}</p>
            <div className="pn-steps">{running.map((s, i) => <span key={i} className={`pn-step-chip ${s.who === 'chair' ? 'chair' : ''}`}>{i + 1}. {s.name}</span>)}</div>
          </div>

          <Field label={ownKind ? 'What do you want to ask?' : (k?.ask || 'The question')}>
            <textarea {...noGrammarly} autoFocus rows={3} value={question} onChange={e => typed('question', e.target.value, e.target)} onPaste={pasted} placeholder={building ? 'Add a dark mode switch to the settings page…' : 'Write it so that someone without the background understands it'} />
          </Field>
          {(ownKind || k?.material || material) && <Field label={ownKind ? 'Background or material (optional)' : (k?.material || 'Material')} help={from ? 'They do not see the conversation you came from: put what they need here. They can read files in its folder.' : undefined}>
            <textarea {...noGrammarly} rows={material.length > 400 ? 8 : 3} value={material} onChange={e => typed('material', e.target.value, e.target)} onPaste={pasted} />
          </Field>}
          <div className={`stack tight pn-context ${dragging ? 'dragging' : ''}`}
            onDragOver={e => { if (hasFiles(e)) { e.preventDefault(); setDragging(true) } }} onDragLeave={() => setDragging(false)}
            onDrop={e => { if (!hasFiles(e)) return; e.preventDefault(); setDragging(false); void addPics(Array.from(e.dataTransfer.files)) }}>
            <div className="row wrap">
              <button type="button" className="btn sm" onClick={() => setPicking('button')}><Icon name="grid" size={13} />Add from your apps</button>
              <button type="button" className="btn sm" onClick={() => picInput.current?.click()} disabled={pics.length >= PICS_MAX}><Icon name="image" size={13} />Add pictures</button>
              <input ref={picInput} type="file" accept="image/*" multiple hidden onChange={e => { void addPics(Array.from(e.target.files || [])); e.target.value = '' }} />
              <span className="muted xsmall">A note, a task… — type <kbd>@</kbd>, or paste a picture in a box above</span>
            </div>
            <PickedChips items={picks} onRemove={it => setPicks(l => l.filter(p => !(p.app === it.app && p.id === it.id)))} />
            {pics.length > 0 && <div className="att-row pn-pics">
              {pics.map(p => (
                <div key={p.lid} className={`att ${p.error ? 'err' : ''}`} title={p.error || p.name}>
                  <img src={p.preview} alt={p.name} />
                  {!p.upload && !p.error && <div className="att-progress"><span style={{ height: `${Math.round((1 - p.progress) * 100)}%` }} /><Spinner size={16} /></div>}
                  {p.error && <div className="att-error"><Icon name="alert" size={16} /></div>}
                  <button type="button" className="att-x" aria-label={`Remove ${p.name}`} onClick={() => removePic(p)}><Icon name="x" size={12} /></button>
                </div>
              ))}
            </div>}
          </div>

          {!building && <label className="inline-switch pn-draw-switch"><Switch checked={draw} onChange={setDrawSet} label="Draw it" />
            <span><b>Draw it</b> <span className="muted">— each model also sketches its ideas as pictures. For icons, logos, screens, layouts, diagrams.</span></span></label>}

          <div className="stack tight">
            <div className="row">
              <span className="label grow">Who takes part</span>
              <MenuButton label="Line-up" className="btn ghost sm" align="right" width={270} items={lineupMenu}><Icon name="users" size={13} />{current ? current.name : 'Line-up'}<Icon name="chevron-down" size={12} /></MenuButton>
            </div>
            <div className="pn-seats">
              {seats.map((s, i) => <SeatRow key={i} seat={s} label={String.fromCharCode(65 + i)} opts={opts} providers={providers} roles={debate || building ? [] : roles} fixedRole={debate ? (i % 2 ? 'Against' : 'For') : undefined}
                only={building ? where : undefined} onChange={ns => setSeats(l => l.map((x, j) => j === i ? ns : x))} onRemove={seats.length > 1 ? () => setSeats(l => l.filter((_, j) => j !== i)) : undefined} />)}
              {seats.length === 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>No agent is signed in that could take part. Sign in under Settings → Machines.</span></div>}
            </div>
            {seats.length < max && <button type="button" className="btn sm" style={{ justifySelf: 'start' }} onClick={addSeat}><Icon name="plus" size={13} />Add a participant</button>}
            {sameTwice && <div className="muted xsmall">Two participants are the same model on the same login: they will mostly agree. Another model, or a different point of view, tells you more.</div>}
            {full.length > 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>{full.map(s => shortProviderName(s.provider)).join(', ')}: the plan limit is used up, so {full.length === 1 ? 'it is' : 'they are'} left out of this panel.</span></div>}
            {out.length > 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>{out.map(s => shortProviderName(s.provider)).join(', ')}: not signed in — {out.length === 1 ? 'it' : 'they'} will drop out. Sign in under Settings → Machines.</span></div>}
          </div>

          {needsChair && chair && <div className="stack tight">
            <span className="label">{building ? 'Who judges' : debate ? 'Who rules' : !ownKind && kind === 'refine' ? 'Who rewrites it' : 'Who writes the summary'}</span>
            <SeatRow seat={chair} label="" opts={opts} providers={providers} roles={[]} only={building ? where : undefined} onChange={setChair} />
            <span className="help muted xsmall">It gets the answers by letter (A, B, C…), not by model, so it cannot favour one.</span>
          </div>}

          {!building && <Field label="How deep" group>
            <Segmented value={depth} onChange={setDepth} label="How deep" options={opts.depths.map(d => ({ value: d.id, label: d.name, title: d.description }))} />
            <span className="help">{opts.depths.find(d => d.id === depth)?.description}</span>
          </Field>}

          <Field label={building ? 'Project folder' : 'Folder they can look at (optional)'} group help={building ? 'A git repository: each participant works in its own worktree of it, on a branch of its own.' : from ? `Default: the folder of “${from.title || 'the conversation'}”.` : 'For questions about a project. Without one they answer from what they know.'}>
            <div className="row wrap">
              {hosts.length > 0 && <HostPicker value={cwd ? hostId : fromHost} onChange={h => { setHostId(h); setCwd('') }} className="btn" />}
              <FolderButton value={cwd || (hostId === fromHost ? from?.cwd || '' : '')} host={cwd ? hostId : fromHost} onChange={p => { setCwd(p); if (!cwd) setHostId(fromHost) }} className="btn" />
              {cwd && <button type="button" className="btn ghost sm" onClick={() => { setCwd(''); setHostId(draft.hostId || 0) }}>{from ? 'Use the conversation’s' : 'None'}</button>}
            </div>
          </Field>

          {building ? <Field label="What they may do without asking" group help="They work in their own copies, so nothing touches your folder. One that still has to ask shows up in the panel as waiting for you.">
            <Segmented value={mode} onChange={setMode} label="Permissions" options={[{ value: 'edit', label: 'Change files' }, { value: 'auto', label: 'And run commands' }, { value: 'full', label: 'Full access' }, { value: '', label: 'As I normally run them' }]} />
          </Field> : <Field label="What they may do without asking" group>
            <Segmented value={access} onChange={setAccess} label="What they may do without asking" options={ACCESS.map(a => ({ value: a.value, label: a.label, title: a.help }))} />
            <span className={`help ${access === 'full' ? 'pn-access-full' : ''}`}>{ACCESS.find(a => a.value === access)?.help}</span>
          </Field>}

          {draft.fromThreadId && <label className="inline-switch"><Switch checked={report} onChange={setReport} label="Bring the outcome back" />Bring the outcome back to the conversation, so its agent can carry on from it</label>}
          {problem && <div className="muted small">{problem}</div>}
        </div>
      )}
      {picking && <PocketPicker picked={picks} onToggle={togglePick} onClose={() => setPicking('')} />}
      {saveAs !== null && <Modal title="Save this line-up" onClose={() => setSaveAs(null)} size="sm" footer={<>
        <button type="button" className="btn" onClick={() => setSaveAs(null)}>Cancel</button>
        <button type="button" className="btn" disabled={!saveAs.trim()} onClick={() => void saveLineup(saveAs.trim(), false)}>Save</button>
        <button type="button" className="btn primary" disabled={!saveAs.trim()} onClick={() => void saveLineup(saveAs.trim(), true)}>Save as my default</button>
      </>}>
        <Field label="Name" help="Your default line-up is used when you start a panel with a “/” command or when an agent starts one for you.">
          <input autoFocus value={saveAs} onChange={e => setSaveAs(e.target.value)} placeholder="My council" maxLength={60} onKeyDown={e => { if (e.key === 'Enter' && saveAs.trim()) void saveLineup(saveAs.trim(), false) }} />
        </Field>
      </Modal>}
      {editKind && <OwnKindDialog kind={editKind} onClose={() => setEditKind(null)} onSaved={async nk => { setEditKind(null); await load(); pickKind('recipe', nk.id) }} />}
      {delKind && <Confirm title={`Delete “${delKind.name}”?`} message="Panels you ran with it stay." onCancel={() => setDelKind(null)}
        onConfirm={async () => { const d = delKind; setDelKind(null); try { await api.del(`${V1}/panels/kinds/${d.id}`); pickKind('council'); await load() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Modal>
  )
}
