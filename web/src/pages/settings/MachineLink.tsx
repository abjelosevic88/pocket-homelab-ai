// Settings → Machines → a machine's card: "Use one connection" (the machine
// link). One press installs the relay there, connects a test copy of it,
// compares what commands see over it and over plain ssh, starts a test
// holder and makes a tools round trip, and waits for Confirm; Switch then
// moves the machine's agents (one at a time), their tools and its commands
// onto the link. A machine that has only its status checks on the link
// (switched on before agents could move) offers "Move agents too", with the
// same test and its own Confirm.
// Afterwards the card shows the link: connected or not, what runs over it,
// the round trip, the last error, a fallback to ssh per agent and why, with
// Retry link and Switch back. Each row stays on one line.
import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../../lib/api'
import type { Host } from '../../lib/types'
import { Icon } from '../../components/icons'
import { errMsg, Spinner, useToast } from '../../components/ui'
import { StatusDot } from './common'

type StepState = 'waiting' | 'running' | 'done' | 'failed' | 'skipped'
interface LinkStep { id: string; label: string; state: StepState; detail?: string; error?: string }
interface LinkFlow { id: string; mode: string; state: 'running' | 'confirm' | 'done' | 'failed' | 'cancelled'; steps: LinkStep[]; confirm?: string; error?: string }

/** GET /hosts/{id}/link */
export interface HostLink {
  host_id: number
  mode: 'off' | 'status' | 'agents'
  state: 'off' | 'connecting' | 'connected' | 'reconnecting' | 'fallen-back' | 'taken-over' | 'stopped' | 'emergency-off'
  connected: boolean
  supported: boolean
  unsupported_why?: string
  agents: number
  commands: number
  rtt_ms?: number
  last_error?: string
  last_error_at?: string
  fallback?: { at: string; why: string }
  probe_passes?: number
  taken_over?: string
  emergency_off?: boolean
  busy?: string
  flow?: LinkFlow
  /** "Move agents too" is offered: the machine has only its status checks on the link */
  move_agents: boolean
  version: number
}

const url = (id: number) => `${V1}/hosts/${id}/link`

/** The machine's link, followed while the card is open (the server answers
 *  ?wait=1 at the next change, or after 25 s). A machine with the link off
 *  and nothing under way is not followed: a held request per card would use
 *  up the browser's few connections on HTTP/1.1. It is asked again once a
 *  minute, or at once when this card starts something (set). */
function useHostLink(id: number) {
  const [link, setLink] = useState<HostLink | null>(null)
  const version = useRef(-1)
  const kick = useRef<(() => void) | null>(null)
  useEffect(() => {
    let live = true
    version.current = -1
    const idle = (v: HostLink) => v.mode === 'off' && v.state === 'off' && !v.busy &&
      (!v.flow || (v.flow.state !== 'running' && v.flow.state !== 'confirm'))
    const loop = async () => {
      let last: HostLink | null = null
      while (live) {
        try {
          if (last && idle(last)) {
            await new Promise<void>(r => { const t = setTimeout(r, 60000); kick.current = () => { clearTimeout(t); r() } })
            kick.current = null
            if (!live) return
            version.current = -1 // ask at once, not held
          }
          const v = await api.get<HostLink>(version.current < 0 ? url(id) : `${url(id)}?wait=1&since=${version.current}`)
          if (!live) return
          version.current = v.version
          last = v
          setLink(v)
        } catch {
          await new Promise(r => setTimeout(r, 5000))
        }
      }
    }
    void loop()
    return () => { live = false; kick.current?.() }
  }, [id])
  const set = (v: HostLink) => { version.current = v.version; setLink(v); kick.current?.() }
  return { link, set }
}

const STATE_LABEL: Record<HostLink['state'], string> = {
  off: 'Off', connecting: 'Connecting…', connected: 'Connected', reconnecting: 'Reconnecting…',
  'fallen-back': 'SSH per agent', 'taken-over': 'Taken over', stopped: 'Paused', 'emergency-off': 'Off on this server',
}

const at = (iso?: string) => iso ? new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''

/** The link part of a machine's card. canEdit: its person, or an admin for the server's machines. */
export function MachineLink({ host, canEdit }: { host: Host; canEdit: boolean }) {
  const { toast } = useToast()
  const { link, set } = useHostLink(host.id)
  const [busy, setBusy] = useState<string | null>(null)
  if (!link) return null
  const act = async (what: string, run: () => Promise<HostLink>) => {
    setBusy(what)
    try { set(await run()) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(null) }
  }
  const start = (mode: 'status' | 'agents' = 'agents') => act('start', () => api.post<HostLink>(url(host.id), { mode }))
  const confirm = (mode: string) => act('confirm', () => api.post<HostLink>(url(host.id), { mode, confirm: true }))
  const cancel = () => act('cancel', () => api.post<HostLink>(url(host.id), { cancel: true }))
  const retry = () => act('retry', () => api.post<HostLink>(`${url(host.id)}/retry`))
  const back = () => act('back', async () => {
    const v = await api.del<HostLink>(url(host.id))
    toast(v.last_error ? `${host.name} is back on SSH per agent — ${v.last_error}` : `${host.name} is back on SSH per agent`, v.last_error ? 'err' : undefined)
    return v
  })

  const flow = link.flow && link.flow.state !== 'done' && link.flow.state !== 'cancelled' ? link.flow : null
  if (flow) return <LinkSteps host={host} flow={flow} busy={busy} canEdit={canEdit} onConfirm={() => confirm(flow.mode)} onCancel={cancel} onAgain={() => start(flow.mode === 'status' ? 'status' : 'agents')} />

  if (link.mode === 'off') {
    if (!canEdit) return null
    return (
      <div className="ml-row">
        <span className="ml-ico" aria-hidden><Icon name="link" size={14} /></span>
        <span className="ml-main ellipsis" title={link.supported ? undefined : link.unsupported_why}>
          <b>One connection</b>
          <span className="muted">{link.supported ? ' · agents, their tools and commands share one ssh connection' : ` · ${link.unsupported_why}`}</span>
        </span>
        <button type="button" className="btn sm" disabled={!link.supported || !!busy} title={link.supported ? 'Install, test and switch, step by step' : link.unsupported_why} onClick={() => start('agents')}>
          {busy === 'start' ? <Spinner size={12} /> : <Icon name="link" size={13} />}Use one connection</button>
      </div>
    )
  }

  const fallen = link.state === 'fallen-back'
  const health = link.connected ? 'ok' : fallen || link.state === 'taken-over' ? 'warn' : link.state === 'reconnecting' ? 'error' : 'off'
  const facts = [
    link.connected && link.rtt_ms ? `round trip ${link.rtt_ms < 10 ? link.rtt_ms.toFixed(1) : Math.round(link.rtt_ms)} ms` : '',
    `${link.commands} ${link.commands === 1 ? 'command' : 'commands'}`,
    link.mode === 'agents' ? `${link.agents} ${link.agents === 1 ? 'agent' : 'agents'}` : 'status checks only',
  ].filter(Boolean)
  const note = link.emergency_off ? 'Switched off on this server (POCKET_LINK=off).'
    : fallen && link.fallback ? `Fell back to SSH per agent at ${at(link.fallback.at)} because ${link.fallback.why}${link.probe_passes ? ` (test ${link.probe_passes} of 2 passed)` : ''}`
    : link.state === 'taken-over' ? `Taken over: ${link.taken_over}`
    : !link.connected && link.last_error ? `${link.last_error}${link.last_error_at ? ` (${at(link.last_error_at)})` : ''}` : ''
  return (
    <div className="ml-box">
      <div className="ml-row">
        <span className="ml-ico"><StatusDot status={health} title={STATE_LABEL[link.state]} /></span>
        <span className="ml-main ellipsis"><b>One connection</b><span className="muted"> · {STATE_LABEL[link.state]}{facts.length ? ` · ${facts.join(' · ')}` : ''}</span></span>
        {canEdit && <span className="ml-actions">
          {(fallen || link.state === 'taken-over' || link.state === 'reconnecting') && <button type="button" className="btn sm ghost" disabled={!!busy || !!link.busy} onClick={retry}>
            {busy === 'retry' || link.busy === 'retrying' ? <Spinner size={12} /> : <Icon name="refresh" size={13} />}Retry link</button>}
          {link.move_agents && <button type="button" className="btn sm ghost" disabled={!!busy || !!link.busy} title="Test a holder and a tools round trip there, then move the running agents one at a time" onClick={() => start('agents')}>
            {busy === 'start' ? <Spinner size={12} /> : <Icon name="link" size={13} />}Move agents too</button>}
          <button type="button" className="btn sm ghost" disabled={!!busy || !!link.busy} onClick={back}>
            {busy === 'back' || link.busy === 'switching back' ? <Spinner size={12} /> : <Icon name="undo" size={13} />}Switch back</button>
        </span>}
      </div>
      {note && <p className={`ml-note ellipsis ${fallen || link.state === 'reconnecting' ? 'warn' : ''}`} title={note}>{note}</p>}
    </div>
  )
}

const STEP_ICON: Record<StepState, string> = { waiting: 'circle', running: '', done: 'check-circle', failed: 'x-circle', skipped: 'minus' }

/** Install → Connect → Test → Confirm → Switch, one line each. */
function LinkSteps({ host, flow, busy, canEdit, onConfirm, onCancel, onAgain }: { host: Host; flow: LinkFlow; busy: string | null; canEdit: boolean; onConfirm: () => void; onCancel: () => void; onAgain: () => void }) {
  const failed = flow.steps.find(s => s.state === 'failed')
  return (
    <div className="ml-box ml-flow">
      <div className="ml-row">
        <span className="ml-ico" aria-hidden><Icon name="link" size={14} /></span>
        <span className="ml-main ellipsis"><b>One connection</b><span className="muted"> · {flow.state === 'failed' ? `stopped at ${failed?.label || 'a step'}; ${host.name} stays on SSH per agent` : flow.state === 'confirm' ? 'tested, waiting for you' : 'setting up…'}</span></span>
      </div>
      <ol className="ml-steps">
        {flow.steps.map(s => {
          const text = s.error || s.detail || ''
          return (
            <li key={s.id} className={`ml-step ${s.state}`}>
              <span className="ml-step-ico">{s.state === 'running' ? <Spinner size={12} /> : <Icon name={STEP_ICON[s.state]} size={13} />}</span>
              <span className="ml-step-label">{s.label}</span>
              {text && <span className="ml-step-text ellipsis" title={text}>{text}</span>}
            </li>
          )
        })}
      </ol>
      {flow.state === 'confirm' && flow.confirm && <p className="ml-note ellipsis" title={flow.confirm}>{flow.confirm}</p>}
      {canEdit && (flow.state === 'confirm' || flow.state === 'failed' || flow.state === 'running') && (
        <div className="ml-row ml-buttons">
          <span className="ml-main" />
          <button type="button" className="btn sm ghost" disabled={!!busy} onClick={onCancel}>{flow.state === 'failed' ? 'Close' : 'Cancel'}</button>
          {flow.state === 'failed' && <button type="button" className="btn sm" disabled={!!busy} onClick={onAgain}><Icon name="refresh" size={13} />Try again</button>}
          {flow.state === 'confirm' && <button type="button" className="btn sm primary" disabled={!!busy} onClick={onConfirm}>{busy === 'confirm' ? <Spinner size={12} /> : <Icon name="check" size={13} />}Switch</button>}
        </div>
      )}
    </div>
  )
}
