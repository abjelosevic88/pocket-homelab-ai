// The setup of the ACP agents (Antigravity CLI, Gemini CLI, OpenCode, Qwen Code, driven
// over the Agent Client Protocol): install, Google sign-in and API keys. Agents.tsx
// shows them inside each agent's row.
import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { AcpAgent } from '../../lib/types'
import { Icon } from '../../components/icons'
import { errMsg, Spinner, useAsync, useToast } from '../../components/ui'
import { SignInDialog } from './Accounts'

/** The ACP agents as Settings sees them (admins only; others get null). */
export function useAcpAgents(enabled: boolean) {
  const { reloadProviders } = useApp()
  const list = useAsync(() => enabled ? api.get<AcpAgent[]>(`${V1}/agents/acp`) : Promise.resolve(null), [enabled])
  const [refreshing, setRefreshing] = useState(false)
  const changed = async (next?: AcpAgent[]) => {
    if (next) list.setData(next)
    else await list.reload()
    await reloadProviders()
  }
  const refresh = async () => {
    setRefreshing(true)
    try { list.setData(await api.get<AcpAgent[]>(`${V1}/agents/acp?refresh=1`)); await reloadProviders() } finally { setRefreshing(false) }
  }
  return { list: list.data, error: list.error, changed, refresh, refreshing }
}

export type AcpChanged = (next?: AcpAgent[]) => Promise<void>

/** What an ACP agent still needs, in a few words ("" = ready). */
export function acpNeeds(a: AcpAgent): string {
  if (!a.installed) return 'Not installed'
  if (a.available) return ''
  if (a.google_login && a.keys.length) return 'Needs a sign-in or a key'
  if (a.google_login) return 'Needs a Google sign-in'
  return 'Needs an API key'
}

function useRun() {
  const { toast } = useToast()
  const [busy, setBusy] = useState('')
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    try { await fn() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  return { busy, run, toast }
}

/** Install / Update button (nothing when it updates itself and is installed). */
export function AcpInstallButton({ a, onChanged, primary }: { a: AcpAgent; onChanged: AcpChanged; primary?: boolean }) {
  const { busy, run, toast } = useRun()
  if (a.installed && a.self_updates) return null
  const install = () => run('install', async () => {
    const r = await api.post<{ ok: boolean; error?: string }>(`${V1}/agents/acp/${a.id}/install`)
    if (!r.ok) throw new Error(r.error || 'The install did not finish')
    toast(`${a.name} ${a.installed ? 'updated' : 'installed'}`)
    await onChanged()
  })
  return (
    <button type="button" className={`btn sm ${primary ? 'primary' : ''}`} onClick={e => { e.stopPropagation(); install() }} disabled={!!busy}
      title={a.install_script ? `Runs ${a.install_script}` : `npm install -g ${a.package}`}>
      {busy ? <Spinner /> : <Icon name={a.installed ? 'refresh' : 'download'} size={13} />}
      {busy ? (a.installed ? 'Updating…' : 'Installing…') : a.installed ? 'Update' : 'Install'}
    </button>
  )
}

/** Sign-in and keys of an installed ACP agent. */
export function AcpSetup({ a, onChanged }: { a: AcpAgent; onChanged: AcpChanged }) {
  const { busy, run, toast } = useRun()
  const [signIn, setSignIn] = useState(false)
  if (!a.installed) return null
  const signOut = () => run('logout', async () => {
    await onChanged(await api.post<AcpAgent[]>(`${V1}/agents/acp/${a.id}/logout`))
    toast('Signed out of Google')
  })
  return (
    <div className="acp-setup">
      {a.google_login && (
        <div className="acp-row">
          <span className="grow small">{a.google_signed_in
            ? <>Signed in with Google{a.account && a.account !== 'Google account' ? <> as <b>{a.account}</b></> : null}</>
            : a.self_updates ? 'Your Google account and its Google AI plan' : 'Google account'}</span>
          {a.google_signed_in
            ? (a.can_sign_out ? <button type="button" className="btn sm ghost" onClick={signOut} disabled={!!busy}>{busy === 'logout' ? <Spinner /> : null}Sign out</button>
              : <button type="button" className="btn sm ghost" onClick={() => setSignIn(true)} disabled={!!busy} title="Sign in with another Google account">Switch account</button>)
            : <button type="button" className="btn sm primary" onClick={() => setSignIn(true)} disabled={!!busy}><Icon name="user" size={13} />Sign in with Google</button>}
        </div>
      )}
      {a.keys.map(k => <KeyRow key={k.name} a={a} k={k} onChanged={onChanged} />)}
      {!a.available && a.error && <div className="callout warn small"><Icon name="alert" size={14} />
        <span>{a.error.replace(/\s*Set it up in Settings → Agents → [^.]+\.$/, '')} {a.google_login ? (a.keys.length ? 'Sign in with Google or add a key.' : 'Sign in with Google.') : a.keys.length ? 'Add a key.' : ''}</span></div>}
      {signIn && <SignInDialog target={{ label: a.name, provider: a.id, base: `${V1}/agents/acp/${a.id}/login` }} onClose={() => { setSignIn(false); onChanged() }} />}
    </div>
  )
}

function KeyRow({ a, k, onChanged }: { a: AcpAgent; k: AcpAgent['keys'][number]; onChanged: AcpChanged }) {
  const { toast } = useToast()
  const cur = a.set[k.name] || ''
  const [val, setVal] = useState('')
  const [busy, setBusy] = useState(false)
  const put = async (v: string) => {
    setBusy(true)
    try {
      await onChanged(await api.put<AcpAgent[]>(`${V1}/agents/acp/${a.id}/env`, { env: { [k.name]: v } }))
      setVal('')
      toast(v ? `${k.label} saved` : `${k.label} removed`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <div className="acp-row">
      <label className="acp-key grow">
        <span className="small">{k.label}{k.url && <> · <a href={k.url} target="_blank" rel="noopener noreferrer">get one</a></>}</span>
        <input className="mono" type={k.secret ? 'password' : 'text'} value={val} onChange={e => setVal(e.target.value)} autoComplete="off" spellCheck={false} autoCapitalize="off"
          placeholder={cur || (k.secret ? 'Paste the key' : k.help || '')} aria-label={k.label}
          onKeyDown={e => { if (e.key === 'Enter' && val.trim()) put(val.trim()) }} />
        {k.help && (k.secret || cur) && <span className="muted xsmall">{k.help}</span>}
      </label>
      <div className="row" style={{ gap: 6, alignSelf: 'flex-end' }}>
        {val.trim() && <button type="button" className="btn sm primary" disabled={busy} onClick={() => put(val.trim())}>{busy ? <Spinner /> : null}Save</button>}
        {!val.trim() && cur && <button type="button" className="btn sm ghost" disabled={busy} onClick={() => put('')} title={`Remove ${k.label}`}>Remove</button>}
      </div>
    </div>
  )
}
