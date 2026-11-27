// Settings → Agents → More agents: Gemini CLI, OpenCode, Qwen Code (driven over the
// Agent Client Protocol). Install them, sign Gemini in with Google, give them API keys.
import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { AcpAgent } from '../../lib/types'
import { Icon } from '../../components/icons'
import { errMsg, Spinner, useAsync, useToast } from '../../components/ui'
import { Section, StatusDot } from './common'
import { SignInDialog } from './Accounts'

export function AcpAgentsSection() {
  const { reloadProviders } = useApp()
  const list = useAsync(() => api.get<AcpAgent[]>(`${V1}/agents/acp`), [])
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
  return (
    <Section title="More agents" sub="Other coding agents on this server, with Google's, open and many other models. Once one is set up it shows up next to Claude Code and Codex when you start a conversation."
      actions={<button type="button" className="btn sm ghost" onClick={refresh} disabled={refreshing} title="Ask the agents again">{refreshing ? <Spinner /> : <Icon name="refresh" size={14} />}Check again</button>}>
      {list.error && <div className="callout danger"><Icon name="alert" size={15} /><span>{list.error}</span></div>}
      {!list.data ? <div className="loading"><Spinner /> Asking the agents…</div>
        : <div className="acp-list">{list.data.map(a => <AcpCard key={a.id} a={a} onChanged={changed} />)}</div>}
    </Section>
  )
}

function status(a: AcpAgent): { dot: 'ok' | 'error' | 'off' | 'warn'; text: string } {
  if (!a.installed) return { dot: 'off', text: 'Not installed' }
  if (a.available) return { dot: 'ok', text: `Ready · ${a.models} model${a.models === 1 ? '' : 's'}${a.account ? ` · ${a.account}` : ''}` }
  return { dot: 'warn', text: 'Needs a sign-in or a key' }
}

function AcpCard({ a, onChanged }: { a: AcpAgent; onChanged: (next?: AcpAgent[]) => Promise<void> }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState('')
  const [signIn, setSignIn] = useState(false)
  const st = status(a)
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    try { await fn() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const install = () => run('install', async () => {
    const r = await api.post<{ ok: boolean; error?: string }>(`${V1}/agents/acp/${a.id}/install`)
    if (!r.ok) throw new Error(r.error || 'The install did not finish')
    toast(`${a.name} ${a.installed ? 'updated' : 'installed'}`)
    await onChanged()
  })
  const signOut = () => run('logout', async () => {
    await onChanged(await api.post<AcpAgent[]>(`${V1}/agents/acp/${a.id}/logout`))
    toast('Signed out of Google')
  })
  return (
    <div className="acp-card">
      <div className="acp-head">
        <Icon name={a.id} size={18} className={`prov-glyph ${a.id}`} />
        <div className="grow acp-about">
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            <b>{a.name}</b>{a.version && <span className="muted xsmall">{a.version}</span>}
            <span className="row muted small" style={{ gap: 6 }}><StatusDot status={st.dot} />{st.text}</span>
          </div>
          <p className="muted small" style={{ margin: '2px 0 0' }}>{a.note} <a href={a.site} target="_blank" rel="noopener noreferrer">More</a></p>
        </div>
        <button type="button" className="btn sm" onClick={install} disabled={!!busy} title={`npm install -g ${a.package}`}>
          {busy === 'install' ? <Spinner /> : <Icon name={a.installed ? 'refresh' : 'download'} size={13} />}{busy === 'install' ? (a.installed ? 'Updating…' : 'Installing…') : a.installed ? 'Update' : 'Install'}
        </button>
      </div>
      {a.installed && <>
        {a.google_login && (
          <div className="acp-row">
            <span className="grow small">{a.google_signed_in ? <>Signed in with Google{a.account && a.account !== 'Google account' ? <> as <b>{a.account}</b></> : null}</> : 'Google account (free daily allowance)'}</span>
            {a.google_signed_in
              ? <button type="button" className="btn sm ghost" onClick={signOut} disabled={!!busy}>{busy === 'logout' ? <Spinner /> : null}Sign out</button>
              : <button type="button" className="btn sm primary" onClick={() => setSignIn(true)} disabled={!!busy}><Icon name="user" size={13} />Sign in with Google</button>}
          </div>
        )}
        {a.keys.map(k => <KeyRow key={k.name} a={a} k={k} onChanged={onChanged} />)}
        {a.installed && !a.available && a.error && <div className="callout warn small"><Icon name="alert" size={14} /><span>{a.error.replace(/\s*Set it up in Settings → Agents → [^.]+\.$/, '')} {a.google_login ? 'Sign in with Google or add a key above.' : a.keys.length ? 'Add a key above.' : ''}</span></div>}
      </>}
      {signIn && <SignInDialog target={{ label: a.name, provider: a.id, base: `${V1}/agents/acp/${a.id}/login` }} onClose={() => { setSignIn(false); onChanged() }} />}
    </div>
  )
}

function KeyRow({ a, k, onChanged }: { a: AcpAgent; k: AcpAgent['keys'][number]; onChanged: (next?: AcpAgent[]) => Promise<void> }) {
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
