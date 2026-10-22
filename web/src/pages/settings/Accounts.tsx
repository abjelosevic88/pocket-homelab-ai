// More than one Claude Code / Codex login on the server. Admins add an
// account (its own CLI folder), sign it in from here and choose who may use
// it; everyone picks the account per thread in the composer.
import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Account, LoginState, ProviderId, User } from '../../lib/types'
import { ProviderToggle } from '../../components/AgentPicker'
import { Select } from '../../components/Select'
import { Icon } from '../../components/icons'
import { Confirm, CopyField, errMsg, Field, MenuButton, Modal, Spinner, useAsync, useToast, type MenuItem, useContextMenu } from '../../components/ui'
import { ago, Section, StatusDot } from './common'

/** "max" → "Max", "PRO" → "Pro". */
const planName = (p: string) => p.length > 1 ? p[0].toUpperCase() + p.slice(1).toLowerCase() : p

export function AccountsSection() {
  const { user, reloadAccounts } = useApp()
  const { toast } = useToast()
  const isAdmin = user?.role === 'admin'
  const [refresh, setRefresh] = useState(0)
  const list = useAsync(() => api.get<Account[]>(`${V1}/accounts?manage=${isAdmin ? 1 : 0}${refresh ? '&refresh=1' : ''}`), [refresh, isAdmin])
  const people = useAsync(() => isAdmin ? api.get<User[]>(`${V1}/users`) : Promise.resolve([] as User[]), [isAdmin])
  const [adding, setAdding] = useState<ProviderId | null>(null)
  const [edit, setEdit] = useState<Account | null>(null)
  const [signIn, setSignIn] = useState<Account | null>(null)
  const [del, setDel] = useState<Account | null>(null)
  const ctx = useContextMenu()
  const done = () => { list.reload(); reloadAccounts().catch(() => {}) }
  const who = (a: Account) => a.default ? 'Everyone' : a.user_id == null ? 'Everyone who may run agents' : 'Only ' + (people.data?.find(p => p.id === a.user_id)?.name || 'One person')

  const signOut = async (a: Account) => {
    try { await api.post(`${V1}/accounts/${a.id}/logout`); toast('Signed out'); done() } catch (e) { toast(errMsg(e), 'err') }
  }

  return (
    <Section title="Accounts" sub={<>Each Claude Code or Codex login lives in its own folder on the server. Pick the account per thread next to the model; a thread can switch accounts and carries on where it was.</>}
      actions={<>
        <button type="button" className="btn sm ghost icon-only" aria-label="Check the accounts again" title="Check again" disabled={list.loading} onClick={() => setRefresh(n => n + 1)}>
          <Icon name="refresh" size={14} className={list.loading ? 'spin' : ''} /></button>
        {isAdmin && <button type="button" className="btn primary sm" onClick={() => setAdding('claude')}><Icon name="plus" size={14} />Add account</button>}
      </>}>
      {!list.data ? <div className="loading"><Spinner /> Checking the logins…</div> : (
        <div className="acct-groups">
          {(['claude', 'codex'] as ProviderId[]).map(pid => {
            const rows = list.data!.filter(a => a.provider === pid)
            if (!rows.length) return null
            return (
              <div key={pid} className={`acct-group ${pid}`}>
                <div className="acct-group-h">
                  <span className={`acct-group-ico ${pid}`}><Icon name={pid} size={16} /></span>
                  <span className="acct-group-name">{pid === 'claude' ? 'Claude Code' : 'Codex'}</span>
                  <span className="acct-group-n">{rows.length} {rows.length === 1 ? 'login' : 'logins'}</span>
                  {isAdmin && <button type="button" className="btn sm ghost acct-group-add" onClick={() => setAdding(pid)}><Icon name="plus" size={13} />Add</button>}
                </div>
                {rows.map(a => {
                  const more: MenuItem[] = [
                    { label: a.signed_in ? 'Sign in again…' : 'Sign in…', icon: 'key', onSelect: () => setSignIn(a) },
                    { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(a) },
                    ...(a.signed_in ? [{ label: 'Sign out', icon: 'log-out', onSelect: () => signOut(a) }] : []),
                    'sep',
                    { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => setDel(a) },
                  ]
                  return (
                  <div key={a.id} className="acct-row" onContextMenu={isAdmin && !a.default ? e => ctx.open(e, more) : undefined}>
                    <StatusDot status={a.signed_in ? 'ok' : a.error ? 'error' : 'off'}
                      title={(a.signed_in ? 'Signed in' : a.error ? 'Error' : 'Not signed in') + (a.checked_at ? ` · checked ${ago(a.checked_at)}` : '')} />
                    <div className="acct-main">
                      <div className="acct-name">
                        <span className="ellipsis">{a.label}</span>
                        {a.signed_in ? (a.plan && <span className="badge ok">{planName(a.plan)}</span>) : a.error ? <span className="badge danger">Error</span> : <span className="badge">Not signed in</span>}
                        {a.default && <span className="badge">Default</span>}
                      </div>
                      {a.email && <div className="acct-email ellipsis">{a.email}</div>}
                      <div className="acct-meta"><code>{a.home.replace(/^\/home\/[^/]+/, '~')}</code>{!a.default && <span>{who(a)}</span>}</div>
                      {a.error && <div className="danger-text xsmall" title={a.error}>{a.error}</div>}
                    </div>
                    <div className="acct-actions">
                      {isAdmin && !a.default && (
                        <MenuButton label={`More for ${a.label}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                      )}
                    </div>
                  </div>
                )})}
              </div>
            )
          })}
        </div>
      )}
      {list.error && <p className="danger-text small">{list.error}</p>}
      {(adding || edit) && <AccountDialog account={edit} provider={adding || 'claude'} people={people.data || []} onClose={() => { setAdding(null); setEdit(null) }}
        onSaved={a => { const wasNew = !!adding; setAdding(null); setEdit(null); done(); if (wasNew && a) setSignIn(a) }} />}
      {signIn && <SignInDialog account={signIn} onClose={() => { setSignIn(null); done() }} />}
      {del && <Confirm title={`Remove ${del.label}?`} confirmLabel="Remove"
        message={<>Its threads continue on the server login (the conversation is copied over on their next message). The folder <code>{del.home}</code> with the login and session files stays on the server.</>}
        onCancel={() => setDel(null)} onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/accounts/${d.id}`); toast('Removed'); done() } catch (e) { toast(errMsg(e), 'err') } }} />}
      {ctx.menu}
    </Section>
  )
}

function AccountDialog({ account, provider: initial, people, onClose, onSaved }: { account: Account | null; provider: ProviderId; people: User[]; onClose: () => void; onSaved: (a?: Account) => void }) {
  const { toast } = useToast()
  const [provider, setProvider] = useState<ProviderId>(account?.provider || initial)
  const [label, setLabel] = useState(account?.label || '')
  const [owner, setOwner] = useState<number>(account?.user_id ?? 0)
  const [busy, setBusy] = useState(false)
  const runners = people.filter(p => p.role === 'admin' || p.can_run)
  const save = async () => {
    setBusy(true)
    try {
      const body = { label: label.trim(), user_id: owner || null }
      const a = account ? await api.patch<Account>(`${V1}/accounts/${account.id}`, body) : await api.post<Account>(`${V1}/accounts`, { ...body, provider })
      toast(account ? 'Saved' : 'Account added — sign it in')
      onSaved(a)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={account ? `Edit ${account.label}` : 'Add an account'} size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !label.trim()} onClick={save}>{account ? 'Save' : 'Add and sign in'}</button>
    </>}>
      <div className="stack">
        {!account && <Field label="Agent" group><ProviderToggle value={provider} onChange={v => v && setProvider(v)} /></Field>}
        <Field label="Name" help="Shown in the account picker, e.g. “Work Max” or “Second Pro”.">
          <input value={label} onChange={e => setLabel(e.target.value)} autoFocus maxLength={60} placeholder={provider === 'codex' ? 'Second ChatGPT Pro' : 'Work Max'} />
        </Field>
        <Field label="Who may use it" help="A login belongs to one person; share it only if that is allowed by its plan.">
          <Select label="Who may use it" value={owner} onChange={setOwner}
            options={[{ value: 0, label: 'Everyone who may run agents' }, ...runners.map(p => ({ value: p.id, label: p.name || p.email, description: p.email }))]} />
        </Field>
        {!account && <p className="muted small" style={{ margin: 0 }}>Pocket AI makes a new folder for it next to the server’s own login. Settings, skills, plugins{provider === 'claude' ? ' and MCP servers' : ' and config.toml'} are shared with the server login; the sign-in, history and limits are its own.</p>}
      </div>
    </Modal>
  )
}

function SignInDialog({ account, onClose }: { account: Account; onClose: () => void }) {
  const { toast } = useToast()
  const [st, setSt] = useState<LoginState | null>(null)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const alive = useRef(true)
  const base = `${V1}/accounts/${account.id}/login`

  useEffect(() => {
    alive.current = true
    api.post<LoginState>(base).then(s => alive.current && setSt(s)).catch(e => alive.current && setSt({ state: 'failed', error: errMsg(e) }))
    return () => { alive.current = false }
  }, [base])
  // Codex finishes by itself once the code is entered on the page: wait for it.
  useEffect(() => {
    if (!st || (st.state !== 'waiting' && st.state !== 'starting') || st.need_code) return
    let stop = false
    const poll = async () => {
      while (!stop && alive.current) {
        try {
          const s = await api.get<LoginState>(`${base}?wait=1`)
          if (stop || !alive.current) return
          setSt(s)
          if (s.state !== 'waiting' && s.state !== 'starting') return
        } catch { await new Promise(r => setTimeout(r, 3000)) }
      }
    }
    poll()
    return () => { stop = true }
  }, [st?.state, st?.need_code, base]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (st?.state === 'done') toast(`${account.label} is signed in`) }, [st?.state]) // eslint-disable-line react-hooks/exhaustive-deps

  const sendCode = async () => {
    setBusy(true)
    try { setSt(await api.post<LoginState>(`${base}/code`, { code: code.trim() })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const close = () => {
    if (st && (st.state === 'waiting' || st.state === 'starting' || st.state === 'checking')) api.del(base).catch(() => {})
    onClose()
  }
  const isClaude = account.provider === 'claude'
  return (
    <Modal title={`Sign in: ${account.label}`} size="sm" onClose={close} footer={
      st?.state === 'done' ? <button type="button" className="btn primary" onClick={onClose}>Done</button>
        : <>
          <button type="button" className="btn" onClick={close}>Cancel</button>
          {st?.need_code && st.state !== 'failed' && <button type="button" className="btn primary" disabled={busy || !code.trim() || st.state === 'checking'} onClick={sendCode}>{busy ? <Spinner /> : null}Sign in</button>}
          {st?.state === 'failed' && <button type="button" className="btn primary" onClick={() => { setSt(null); setCode(''); api.post<LoginState>(base).then(setSt).catch(e => setSt({ state: 'failed', error: errMsg(e) })) }}>Try again</button>}
        </>}>
      {!st || st.state === 'starting' && !st.url ? <div className="loading"><Spinner /> Starting {isClaude ? 'claude auth login' : 'codex login'} on the server…</div>
        : st.state === 'done' ? <div className="callout success"><Icon name="check-circle" size={15} /><span>Signed in. New threads can use <b>{account.label}</b> now.</span></div>
        : st.state === 'failed' ? <div className="callout danger"><Icon name="alert" size={15} /><span>{st.error || 'The sign-in did not finish.'}</span></div>
        : (
          <div className="stack">
            <ol className="signin-steps">
              <li>Open the sign-in page and sign in with the {isClaude ? 'Claude' : 'ChatGPT'} account this login should use.
                <div className="mt-s"><a className="btn sm" href={st.url} target="_blank" rel="noopener noreferrer"><Icon name="external" size={13} />Open the sign-in page</a></div></li>
              {isClaude
                ? <li>When the page shows a code, copy it and paste it here.
                  <Field label="Code"><input value={code} onChange={e => setCode(e.target.value)} placeholder="Paste the code" autoComplete="off" spellCheck={false}
                    onKeyDown={e => { if (e.key === 'Enter' && code.trim()) sendCode() }} /></Field></li>
                : <li>Enter this one-time code on the page (it expires in 15 minutes):
                  {st.code ? <CopyField value={st.code} label="One-time code" /> : <span className="muted"> waiting for the code…</span>}</li>}
            </ol>
            {st.state === 'checking' && <div className="loading"><Spinner /> Checking the code…</div>}
            {!isClaude && <div className="loading"><Spinner /> Waiting for you to finish on the page…</div>}
            {st.error && <p className="danger-text small" style={{ margin: 0 }}>{st.error}</p>}
          </div>
        )}
    </Modal>
  )
}
