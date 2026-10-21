import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { User } from '../../lib/types'
import { Icon } from '../../components/icons'
import { Avatar, errMsg, Field, useToast } from '../../components/ui'
import { Section } from './common'

export function ProfileTab() {
  const { user, status, setUser, refresh } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '' })
  const [pw, setPw] = useState({ current: '', next: '', confirm: '' })
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (user) setF({ name: user.name, email: user.email }) }, [user])
  if (!user) return null
  const dirty = f.name !== user.name || f.email !== user.email

  const save = async () => {
    setBusy(true)
    try { const u = await api.put<User>(`${V1}/auth/me`, f); setUser(u); toast('Profile saved') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const changePw = async () => {
    if (pw.next !== pw.confirm) { toast('The new passwords don’t match', 'err'); return }
    setBusy(true)
    try { await api.put(`${V1}/auth/me`, { current_password: pw.current, new_password: pw.next }); setPw({ current: '', next: '', confirm: '' }); toast('Password changed') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const signOut = async () => {
    try { await api.post(`${V1}/auth/logout`) } catch { /* signed out anyway */ }
    await refresh().catch(() => {})
    navigate('/login')
  }

  return (
    <div className="stack loose">
      <Section title="You">
        <div className="set-profile">
          <Avatar name={user.name} email={user.email} size={52} />
          <div className="grow stack">
            <div className="two-col">
              <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoComplete="name" /></Field>
              <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} autoComplete="email" /></Field>
            </div>
            <div className="row"><button type="button" className="btn primary" disabled={!dirty || busy} onClick={save}>Save</button>{user.role === 'admin' ? <span className="badge accent">Admin</span> : user.can_run ? <span className="badge ok">May run agents</span> : <span className="badge">View only</span>}</div>
          </div>
        </div>
      </Section>

      {status?.auth_mode === 'local' && (
        <Section title="Password">
          <form className="stack" onSubmit={e => { e.preventDefault(); changePw() }}>
            <input type="text" autoComplete="username" value={user.email} readOnly hidden />
            <Field label="Current password"><input type="password" value={pw.current} onChange={e => setPw({ ...pw, current: e.target.value })} autoComplete="current-password" /></Field>
            <div className="two-col">
              <Field label="New password" help="At least 8 characters."><input type="password" value={pw.next} onChange={e => setPw({ ...pw, next: e.target.value })} autoComplete="new-password" minLength={8} /></Field>
              <Field label="Repeat new password"><input type="password" value={pw.confirm} onChange={e => setPw({ ...pw, confirm: e.target.value })} autoComplete="new-password" /></Field>
            </div>
            <div className="row"><button type="submit" className="btn" disabled={busy || pw.next.length < 8 || !pw.confirm}>Change password</button></div>
          </form>
        </Section>
      )}

      <Section title="Session">
        <div className="row">
          <button type="button" className="btn danger-ghost" onClick={signOut}><Icon name="log-out" size={15} />Sign out</button>
          {status?.auth_mode !== 'local' && <span className="muted small">Sign-in is handled by {status?.auth_mode === 'proxy' ? 'the reverse proxy' : 'this box’s setup'}.</span>}
        </div>
      </Section>
    </div>
  )
}
