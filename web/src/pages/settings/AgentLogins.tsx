// One sign-in on many machines: an Antigravity login is its token file, so a login that is
// signed in (here or on a machine) can be copied into another login folder in one tap —
// no second trip through Google's page. Used by the Add dialogs and the login menus of
// Settings → Machines.
import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { providerName } from '../../lib/format'
import type { Account, HostLogin, ProviderId } from '../../lib/types'
import { Select } from '../../components/Select'
import { errMsg, Field, Modal, Spinner, useToast } from '../../components/ui'

/** A login that is signed in and can be copied from. host 0 = this server. */
export interface LoginSource { host: number; hostName: string; dir: string; name: string; email?: string }
/** Where a copy goes: an existing login folder (dir, '' = the default) or a new one (name). */
export interface LoginDest { host: number; hostName: string; dir?: string; name?: string }

/** Every signed-in login of an agent, on this server and on the machines. */
export function loginSources(pid: ProviderId, accounts: Account[], byHost: Record<number, HostLogin[]>): LoginSource[] {
  const out: LoginSource[] = accounts.filter(a => a.provider === pid && a.signed_in)
    .map(a => ({ host: 0, hostName: 'This server', dir: a.default ? '' : a.home, name: a.label, email: a.email }))
  for (const list of Object.values(byHost)) {
    for (const l of list) if (l.provider === pid && l.signed_in && !l.missing) out.push({ host: l.host_id, hostName: l.host, dir: l.dir || '', name: l.label || 'Default login', email: l.email })
  }
  return out
}

export const sourceKey = (s: LoginSource) => `${s.host}|${s.dir}`
export const sourceLabel = (s: LoginSource) => [s.email || s.name, `${s.hostName}${s.email ? ` · ${s.name}` : ''}`]

export async function copyLogin(pid: ProviderId, from: LoginSource, to: LoginDest) {
  return api.post<{ ok: boolean; email?: string; host: number; dir: string }>(`${V1}/agents/acp/${pid}/copy-login`, {
    from: { host: from.host, dir: from.dir }, to: { host: to.host, dir: to.dir || '', name: to.name || '' },
  })
}

/** "How to sign it in": with Google, or as one of the accounts already signed in somewhere. */
export function SignInHow({ sources, value, onChange }: { sources: LoginSource[]; value: string; onChange: (v: string) => void }) {
  if (!sources.length) return null
  return (
    <Field label="Sign in" help="Copying uses the sign-in of a login you already have: the same Google account and plan, no code to paste.">
      <Select label="Sign in" value={value} onChange={onChange} options={[
        ...sources.map(s => ({ value: sourceKey(s), label: `Same account as ${sourceLabel(s)[0]}`, description: `Copied from ${sourceLabel(s)[1]}` })),
        { value: '', label: 'Another Google account', description: 'Open Google’s page and paste the code it shows' },
      ]} />
    </Field>
  )
}

/** Copy a sign-in into an existing login (its menu → "Copy sign-in from…"). */
export function CopyLoginDialog({ pid, to, sources, onClose, onDone }: { pid: ProviderId; to: LoginDest; sources: LoginSource[]; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const list = sources.filter(s => !(s.host === to.host && s.dir === (to.dir || '')))
  const [pick, setPick] = useState(list[0] ? sourceKey(list[0]) : '')
  const [busy, setBusy] = useState(false)
  const from = list.find(s => sourceKey(s) === pick)
  const run = async () => {
    if (!from) return
    setBusy(true)
    try {
      const r = await copyLogin(pid, from, to)
      toast(`${to.hostName} is signed in${r.email ? ` as ${r.email}` : ''}`)
      onDone()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={`Copy a ${providerName(pid)} sign-in to ${to.hostName}`} size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !from} onClick={run}>{busy ? <Spinner /> : null}Copy sign-in</button>
    </>}>
      {list.length ? (
        <div className="stack">
          <Field label="From">
            <Select label="From" value={pick} onChange={setPick} options={list.map(s => ({ value: sourceKey(s), label: sourceLabel(s)[0], description: sourceLabel(s)[1] }))} />
          </Field>
          <p className="muted small" style={{ margin: 0 }}>The login there becomes the same Google account, with the same plan and limits. The sign-in goes from machine to machine over ssh; a sign-in it had before is replaced.</p>
        </div>
      ) : <p className="muted small" style={{ margin: 0 }}>No other {providerName(pid)} login is signed in yet. Sign one in with Google first; after that it can be copied to every machine.</p>}
    </Modal>
  )
}
