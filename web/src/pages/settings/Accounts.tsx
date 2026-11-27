// Settings → Machines, the "This server" card: its CLIs and logins.
// More than one Claude Code / Codex login on the server. The server login
// is the owner's; everyone else adds their own (in their own folder on the
// server, their agents sandboxed there), signs it in from here and picks the
// account per thread in the composer. Admins see and manage every login.
// Other machines (Settings → Machines) list and manage their own logins there.
import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import { type Account, type HostLogin, type LoginState, type ProviderId, type User, isAcp } from '../../lib/types'

/** How people are kept apart (GET /settings → people; server people.go). */
interface PeopleInfo { jailed: boolean; sandbox: boolean; sandbox_off: boolean; sandbox_error?: string; folder?: string; projects?: string; owner?: string }
import { ProviderToggle } from '../../components/AgentPicker'
import { planName, providerName } from '../../lib/format'
import { Select } from '../../components/Select'
import { Icon } from '../../components/icons'
import { Confirm, CopyField, copyText, errMsg, Field, MenuButton, Modal, Spinner, useAsync, useToast, type MenuItem, useContextMenu } from '../../components/ui'
import { ago, StatusDot } from './common'
import { copyLogin, CopyLoginDialog, loginSources, type LoginSource, SignInHow, sourceKey } from './AgentLogins'
import { LoginList, MachineCard, osIcon, useCLIUpdate, useLatestCLIs } from './MachineParts'

/** "max" → "Max", "PRO" → "Pro". */

export function AccountsSection({ hostLogins = {} }: { hostLogins?: Record<number, HostLogin[]> }) {
  const { user, reloadAccounts, providers: all, reloadProviders } = useApp()
  // Antigravity CLI: installed here once it says its version (signed in or not)
  const agy = all.find(p => p.id === 'antigravity')
  const agyHere = !!agy?.version
  // the CLIs with logins; ACP agents (Gemini CLI, OpenCode, …) live in Settings → Agents
  const providers = all.filter(p => !isAcp(p.id))
  const { toast } = useToast()
  const isAdmin = user?.role === 'admin'
  const [cliCheck, setCliCheck] = useState(false)
  const mayAdd = isAdmin || !!user?.can_run
  const [refresh, setRefresh] = useState(0)
  const info = useAsync(() => api.get<{ people?: PeopleInfo }>(`${V1}/settings`).then(r => r.people || null), [])
  const pi = info.data
  const list = useAsync(() => api.get<Account[]>(`${V1}/accounts?manage=${isAdmin ? 1 : 0}${refresh ? '&refresh=1' : ''}`), [refresh, isAdmin])
  const people = useAsync(() => isAdmin ? api.get<User[]>(`${V1}/users`) : Promise.resolve([] as User[]), [isAdmin])
  const [adding, setAdding] = useState<ProviderId | null>(null)
  const [edit, setEdit] = useState<Account | null>(null)
  const [signIn, setSignIn] = useState<SignInTarget | null>(null)
  const [del, setDel] = useState<Account | null>(null)
  const [copyTo, setCopyTo] = useState<Account | null>(null)
  const ctx = useContextMenu()
  const done = () => { list.reload(); reloadAccounts().catch(() => {}); reloadProviders().catch(() => {}) }
  // signed-in Antigravity logins anywhere: a new login can be a copy of one
  const sources = (p: ProviderId) => isAcp(p) ? loginSources(p, list.data || [], hostLogins) : []
  const latest = useLatestCLIs()
  const upd = useCLIUpdate()
  const mine = (a: Account) => !a.default && a.user_id != null && a.user_id === user?.id
  const canManage = (a: Account) => !a.default && (isAdmin || mine(a))
  const who = (a: Account) => a.default || a.user_id == null ? `Only ${pi?.owner || 'the server owner'}` : mine(a) ? 'Only you' : 'Only ' + (people.data?.find(p => p.id === a.user_id)?.name || 'one person')

  const signOut = async (a: Account) => {
    // an ACP agent's default login has no account row: its own sign-out
    try { await api.post(a.default ? `${V1}/agents/acp/${a.provider}/logout` : `${V1}/accounts/${a.id}/logout`); toast('Signed out'); done() } catch (e) { toast(errMsg(e), 'err') }
  }
  // the server login has no account row: its own sign-in, for an admin
  const accountSignIn = (a: Account): SignInTarget => ({ label: a.label, provider: a.provider,
    base: !a.default ? `${V1}/accounts/${a.id}/login` : isAcp(a.provider) ? `${V1}/agents/acp/${a.provider}/login` : `${V1}/accounts/server/${a.provider}/login` })

  return (
    <MachineCard icon={osIcon('', true)} name="This server"
      health={!providers.length ? 'off' : providers.every(p => p.available) ? 'ok' : providers.some(p => p.available) ? 'warn' : 'error'}
      healthLabel={providers.length && providers.every(p => p.available) ? 'Pocket AI runs here' : undefined}
      meta={[pi?.owner ? <>owned by {pi.owner}</> : 'where Pocket AI runs', ...(list.data ? [`${list.data.length} ${list.data.length === 1 ? 'login' : 'logins'}`] : [])]}
      clis={[...providers.map(p => ({ provider: p.id, version: p.available ? p.version : undefined, error: p.available ? undefined : p.error || 'not installed',
        latest: latest[p.id], updating: upd.busy(0, p.id), onUpdate: isAdmin ? () => { void upd.update(0, 'this server', p.id, () => reloadProviders()) } : undefined })),
        // Antigravity CLI: there (it updates itself), or one tap away
        ...(agyHere ? [{ provider: 'antigravity' as ProviderId, version: agy!.version }]
          : isAdmin && agy ? [{ provider: 'antigravity' as ProviderId, updating: upd.busy(0, 'antigravity'), onInstall: () => { void upd.update(0, 'this server', 'antigravity', () => { done(); return reloadProviders() }, true) } }] : [])]}
      actions={<button type="button" className="btn sm ghost icon-only" aria-label="Check this server again" title="Check again" disabled={list.loading || cliCheck}
        onClick={() => { setRefresh(n => n + 1); setCliCheck(true); reloadProviders().catch(() => {}).finally(() => setCliCheck(false)) }}>
        <Icon name="refresh" size={15} className={list.loading || cliCheck ? 'spin' : ''} /></button>}>
      {pi && <PeopleNotice info={pi} isAdmin={isAdmin} />}
      {!list.data ? <div className="loading"><Spinner /> Checking the logins…</div> : (
        <LoginList onAdd={mayAdd ? setAdding : undefined} providers={[...providers.filter(p => p.available).map(p => p.id), ...(agyHere && isAdmin ? ['antigravity' as ProviderId] : [])]}
          items={[...list.data].sort((x, y) => (x.default ? -1 : y.default ? 1 : 0)).map(a => {
            const copy: MenuItem[] = isAcp(a.provider) ? [{ label: 'Copy sign-in from…', icon: 'copy', onSelect: () => setCopyTo(a) }] : []
            const more: MenuItem[] = a.default ? [
              { label: a.signed_in ? 'Sign in again…' : 'Sign in…', icon: 'key', onSelect: () => setSignIn(accountSignIn(a)) },
              ...copy,
              ...(isAcp(a.provider) && a.signed_in ? [{ label: 'Sign out', icon: 'log-out', onSelect: () => signOut(a) }] : []),
            ] : [
              { label: a.signed_in ? 'Sign in again…' : 'Sign in…', icon: 'key', onSelect: () => setSignIn(accountSignIn(a)) },
              ...copy,
              { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(a) },
              ...(a.signed_in ? [{ label: 'Sign out', icon: 'log-out', onSelect: () => signOut(a) }] : []),
              'sep',
              { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => setDel(a) },
            ]
            const manage = canManage(a) || (a.default && isAdmin)
            return {
              key: a.default ? `server-${a.provider}` : String(a.id), provider: a.provider, name: a.label, signedIn: a.signed_in, plan: a.plan, email: a.email, error: a.error, folder: a.home,
              menu: manage ? more : undefined, onContextMenu: manage ? (e: React.MouseEvent) => ctx.open(e, more) : undefined,
              onSignIn: a.default ? (isAdmin ? () => setSignIn(accountSignIn(a)) : undefined) : manage ? () => setSignIn(accountSignIn(a)) : undefined,
              tags: <>
                {a.default && <span className="badge">Default</span>}
                {a.same_as && <span className="badge" title={`${a.label} and ${a.same_as} are signed in to the same account, so they share one set of plan limits.`}>Same account as {a.same_as}</span>}
              </>,
              meta: <span>{who(a)}</span>,
            }
          })} />
      )}
      {list.error && <p className="danger-text small">{list.error}</p>}
      {(adding || edit) && <AccountDialog account={edit} provider={adding || 'claude'} people={people.data || []} isAdmin={isAdmin} own={!!pi?.jailed} onClose={() => { setAdding(null); setEdit(null) }}
        machines={[]} onMachine={() => {}} agents={agyHere && isAdmin ? ['claude', 'codex', 'antigravity'] : ['claude', 'codex']} sources={sources}
        onSaved={a => { const wasNew = !!adding; setAdding(null); setEdit(null); done(); if (wasNew && a) setSignIn(accountSignIn(a)) }} />}
      {copyTo && <CopyLoginDialog pid={copyTo.provider} to={{ host: 0, hostName: copyTo.default ? 'this server' : copyTo.label, dir: copyTo.default ? '' : copyTo.home }} sources={sources(copyTo.provider)}
        onClose={() => setCopyTo(null)} onDone={() => { setCopyTo(null); done() }} />}
      {signIn && <SignInDialog target={signIn} onClose={() => { setSignIn(null); done() }} />}
      {del && <Confirm title={`Remove ${del.label}?`} confirmLabel="Remove"
        message={<>Its threads continue on {pi?.jailed ? 'your other login' : 'the server login'} (the conversation is copied over on their next message). The folder <code>{del.home}</code> with the login and session files stays on the server.</>}
        onCancel={() => setDel(null)} onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/accounts/${d.id}`); toast('Removed'); done() } catch (e) { toast(errMsg(e), 'err') } }} />}
      {ctx.menu}
    </MachineCard>
  )
}

/** The login picker's choice for making another login folder on the machine. */
const NEW_LOGIN = '+new'
const loginSlug = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-+$/, '')

function AccountDialog({ account, provider: initial, people, isAdmin, own, machines, where: initialWhere = 0, onMachine, onClose, onSaved, agents = ['claude', 'codex'], sources = () => [] }: { account: Account | null; provider: ProviderId; people: User[]; isAdmin: boolean; own: boolean; machines: HostLogin[]; where?: number; onMachine: (h: HostLogin, made: boolean) => void; onClose: () => void; onSaved: (a?: Account) => void
  /** the agents a login can be added for here */
  agents?: ProviderId[]; sources?: (p: ProviderId) => LoginSource[] }) {
  const { toast } = useToast()
  // Antigravity: sign in with Google, or as an account that is signed in somewhere already
  const [how, setHow] = useState<string | null>(null)
  const [provider, setProvider] = useState<ProviderId>(account?.provider || initial)
  const [label, setLabel] = useState(account?.label || '')
  const [owner, setOwner] = useState<number>(account?.user_id ?? 0)
  const [busy, setBusy] = useState(false)
  // Where the login lives: 0 = this server, else one of the machines (Settings → Machines).
  const [where, setWhere] = useState(initialWhere)
  const hosts = account ? [] : [...new Map(machines.map(h => [h.host_id, h.host])).entries()]
  // Ask the picked machine afresh: the list may be minutes old (a CLI just installed there).
  const fresh = useAsync(() => where ? api.get<HostLogin[]>(`${V1}/hosts/logins?host=${where}&refresh=1`) : Promise.resolve([] as HostLogin[]), [where])
  const checking = !!where && fresh.loading
  const logins = where ? (fresh.data?.length ? fresh.data : machines).filter(h => h.host_id === where && h.provider === provider) : []
  const machine = logins.find(h => !h.dir)
  // a machine that has the CLI and answers: sign in one of its logins, or make another
  const usable = !!machine && !machine.missing && !machine.error
  const [pick, setPick] = useState<string | null>(null) // null: a new one when the default is signed in already
  const sel = !usable ? '' : pick ?? (machine.signed_in ? NEW_LOGIN : '')
  const picked = sel === NEW_LOGIN ? undefined : logins.find(h => (h.dir || '') === sel) || machine
  const hostName = hosts.find(([id]) => id === where)?.[1] || machine?.host
  const runners = people.filter(p => p.role === 'admin' || p.can_run)
  const from = account ? [] : sources(provider)
  const pickHow = how ?? (from[0] ? sourceKey(from[0]) : '')
  const source = from.find(x => sourceKey(x) === pickHow)
  const save = async () => {
    if (machine && sel === NEW_LOGIN) {
      setBusy(true)
      try {
        const h = await api.post<HostLogin>(`${V1}/hosts/${where}/logins/${provider}`, { name: label.trim() })
        toast(`Login added on ${h.host} — sign it in`)
        onMachine(h, true)
      } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
      return
    }
    if (machine) { onMachine(picked || machine, false); return }
    setBusy(true)
    if (source && !account) { // a new login that is a copy of one signed in already: made and signed in at once
      try {
        const r = await copyLogin(provider, source, { host: 0, hostName: 'This server', name: label.trim() })
        toast(`${label.trim()} is signed in${r.email ? ` as ${r.email}` : ''}`)
        onSaved()
      } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
      return
    }
    try {
      const body = isAdmin && !isAcp(provider) ? { label: label.trim(), user_id: owner || null } : { label: label.trim() }
      const a = account ? await api.patch<Account>(`${V1}/accounts/${account.id}`, body) : await api.post<Account>(`${V1}/accounts`, { ...body, provider })
      toast(account ? 'Saved' : 'Account added — sign it in')
      onSaved(a)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={account ? `Edit ${account.label}` : 'Add an account'} size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || checking || (machine ? !usable || (sel === NEW_LOGIN && !loginSlug(label)) : !label.trim())} onClick={save}>{busy ? <Spinner /> : null}{account ? 'Save' : machine && sel !== NEW_LOGIN ? (picked?.signed_in ? 'Sign in again' : 'Sign in') : source ? 'Add with that sign-in' : 'Add and sign in'}</button>
    </>}>
      <div className="stack">
        {!account && <Field label="Agent" group><ProviderToggle only={agents} value={provider} onChange={v => { if (v) { setProvider(v); setHow(null) } }} /></Field>}
        {hosts.length > 0 && <Field label="Where" help="This server, or one of your other machines. Threads on a machine run on its login there.">
          <Select label="Where" value={where} onChange={v => { setWhere(v); setPick(null) }}
            options={[{ value: 0, label: 'This server' }, ...hosts.map(([id, name]) => ({ value: id, label: name, description: machines.find(h => h.host_id === id)?.target }))]} />
        </Field>}
        {checking ? <div className="loading"><Spinner /> Checking {hostName}…</div>
          : machine && !usable ? <MachineNote login={machine} onRecheck={fresh.reload} />
          : machine ? <>
            <Field label="Login" help={`Each login on ${hostName} is a folder of its own there. A conversation on ${hostName} picks one next to the model and can switch.`}>
              <Select label="Login" value={sel} onChange={setPick} options={[
                ...logins.filter(h => !h.missing).map(h => ({ value: h.dir || '', label: h.label || 'Default login',
                  description: [h.home.replace(/^\/(home|Users)\/[^/]+/, '~'), h.signed_in ? [h.plan && planName(h.plan), h.email].filter(Boolean).join(' · ') : 'not signed in'].filter(Boolean).join(' · ') })),
                { value: NEW_LOGIN, label: 'New login…', description: `Another ${provider === 'claude' ? 'Claude' : 'ChatGPT'} account on ${hostName}, next to the ones there` },
              ]} />
            </Field>
            {sel === NEW_LOGIN ? <>
              <Field label="Name" help={loginSlug(label) ? <>Pocket AI makes <code>~/.{provider}-{loginSlug(label)}</code> on {hostName} and signs it in.</> : 'Shown in the login picker, e.g. “max” or “personal”.'}>
                <input value={label} onChange={e => setLabel(e.target.value)} autoFocus maxLength={60} placeholder={provider === 'codex' ? 'second-pro' : 'max'}
                  onKeyDown={e => { if (e.key === 'Enter' && loginSlug(label) && !busy) save() }} />
              </Field>
              <p className="muted small" style={{ margin: 0 }}>Settings, skills, plugins{provider === 'claude' ? ' and MCP servers' : ' and config.toml'} are shared with the default login there; the sign-in, history and limits are its own.</p>
            </> : picked && <MachineNote login={picked} onRecheck={fresh.reload} />}
          </> : <>
        <Field label="Name" help="Shown in the account picker, e.g. “Work Max” or “Second Pro”.">
          <input value={label} onChange={e => setLabel(e.target.value)} autoFocus maxLength={60} placeholder={provider === 'codex' ? 'Second ChatGPT Pro' : provider === 'antigravity' ? 'Work Google' : 'Work Max'} />
        </Field>
        {!account && <SignInHow sources={from} value={pickHow} onChange={setHow} />}
        {isAcp(provider) && !account && <p className="muted small" style={{ margin: 0 }}>Pocket AI makes a folder for it next to the server’s own login (<code>~/.gemini-…</code>): its own Google sign-in, history and limits. Pick it next to the model when you start a conversation.</p>}
        {isAdmin && !isAcp(provider) && <Field label="Whose login" help="A login belongs to one person. Anyone but the server owner keeps theirs in their own folder, and their agents run sandboxed there.">
          <Select label="Whose login" value={owner} onChange={setOwner}
            options={[{ value: 0, label: 'The server owner' }, ...runners.map(p => ({ value: p.id, label: p.name || p.email, description: p.email }))]} />
        </Field>}
        {!account && (own || (isAdmin && owner !== 0 && owner !== Math.min(...people.map(p => p.id)))
          ? <p className="muted small" style={{ margin: 0 }}>Pocket AI makes a folder for it in {own ? 'your' : 'their'} own folder on the server. Sign in with {own ? 'your' : 'their'} own subscription: only {own ? 'you' : 'they'} can run on it, with {own ? 'your' : 'their'} own settings, history and limits.</p>
          : !account && !isAcp(provider) && <p className="muted small" style={{ margin: 0 }}>Pocket AI makes a new folder for it next to the server’s own login. Settings, skills, plugins{provider === 'claude' ? ' and MCP servers' : ' and config.toml'} are shared with the server login; the sign-in, history and limits are its own.</p>)}
        </>}
      </div>
    </Modal>
  )
}

/** What adding a login on a machine does: it signs in that machine's own login. */
function MachineNote({ login: h, onRecheck }: { login: HostLogin; onRecheck: () => void }) {
  const name = h.provider === 'claude' ? 'Claude Code' : 'Codex'
  const again = <button type="button" className="btn sm" onClick={onRecheck}><Icon name="refresh" size={13} />Check again</button>
  if (h.missing) return <div className="callout warn"><Icon name="alert" size={15} /><span>{name} is not installed on <b>{h.host}</b> yet. Install it there, then {again}</span></div>
  if (h.error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{h.host}: {h.error} {again}</span></div>
  return <p className="muted small" style={{ margin: 0 }}>
    {h.signed_in ? <>{machineLabel(h)} is already signed in{h.email ? <> as <b>{h.email}</b></> : null}. Signing in again replaces that login; to keep it, pick <b>New login…</b> instead.</>
      : <>Signs in {name} on <b>{h.host}</b> (<code>{h.home.replace(/^\/(home|Users)\/[^/]+/, '~')}</code> there) over ssh. {h.dir ? <>Conversations on {h.host} use it when you pick it next to the model.</> : <>Threads on {h.host} run on it.</>}</>}
  </p>
}

/** "AB-128box" for a machine's default login, "AB-128box · claude-max" for another login folder there. */
export const machineLabel = (h: { host: string; label?: string }) => h.label ? `${h.host} · ${h.label}` : h.host
/** What a sign-in is for: an account here, or another machine's own login. */
export interface SignInTarget { label: string; provider: ProviderId; base: string; query?: Record<string, string>; where?: string }

export function SignInDialog({ target, onClose }: { target: SignInTarget; onClose: () => void }) {
  const { toast } = useToast()
  const [st, setSt] = useState<LoginState | null>(null)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const alive = useRef(true)
  const { label, where } = target
  // every request of the sign-in carries the query (a machine's login folder)
  const extra = new URLSearchParams(target.query || {}).toString()
  const base = target.base
  const at = (path = '', q = '') => `${base}${path}${[q, extra].filter(Boolean).length ? '?' + [q, extra].filter(Boolean).join('&') : ''}`

  useEffect(() => {
    alive.current = true
    api.post<LoginState>(at()).then(s => alive.current && setSt(s)).catch(e => alive.current && setSt({ state: 'failed', error: errMsg(e) }))
    return () => { alive.current = false }
  }, [base, extra])
  // Codex finishes by itself once the code is entered on the page: wait for it.
  useEffect(() => {
    if (!st || (st.state !== 'waiting' && st.state !== 'starting') || st.need_code) return
    let stop = false
    const poll = async () => {
      while (!stop && alive.current) {
        try {
          const s = await api.get<LoginState>(at('', 'wait=1'))
          if (stop || !alive.current) return
          setSt(s)
          if (s.state !== 'waiting' && s.state !== 'starting') return
        } catch { await new Promise(r => setTimeout(r, 3000)) }
      }
    }
    poll()
    return () => { stop = true }
  }, [st?.state, st?.need_code, base, extra]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (st?.state === 'done') toast(`${label} is signed in`) }, [st?.state]) // eslint-disable-line react-hooks/exhaustive-deps

  const sendCode = async () => {
    setBusy(true)
    try { setSt(await api.post<LoginState>(at('/code'), { code: code.trim() })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const close = () => {
    if (st && (st.state === 'waiting' || st.state === 'starting' || st.state === 'checking')) api.del(at()).catch(() => {})
    onClose()
  }
  // Claude Code and Gemini CLI: paste the code the page shows; Codex: type its code on the page
  const isClaude = target.provider !== 'codex'
  const service = target.provider === 'claude' ? 'Claude' : target.provider === 'codex' ? 'ChatGPT' : 'Google'
  const command = target.provider === 'claude' ? 'claude auth login' : target.provider === 'codex' ? 'codex login' : `the ${providerName(target.provider)} sign-in`
  return (
    <Modal title={where ? `Sign in on ${where}: ${providerName(target.provider)}` : `Sign in: ${label}`} size="sm" onClose={close} footer={
      st?.state === 'done' ? <button type="button" className="btn primary" onClick={onClose}>Done</button>
        : <>
          <button type="button" className="btn" onClick={close}>Cancel</button>
          {st?.need_code && st.state !== 'failed' && <button type="button" className="btn primary" disabled={busy || !code.trim() || st.state === 'checking'} onClick={sendCode}>{busy ? <Spinner /> : null}Sign in</button>}
          {st?.state === 'failed' && <button type="button" className="btn primary" onClick={() => { setSt(null); setCode(''); api.post<LoginState>(at()).then(setSt).catch(e => setSt({ state: 'failed', error: errMsg(e) })) }}>Try again</button>}
        </>}>
      {!st || st.state === 'starting' && !st.url ? <div className="loading"><Spinner /> Starting {command} {where ? `on ${where}` : 'on the server'}…</div>
        : st.state === 'done' ? <div className="callout success"><Icon name="check-circle" size={15} /><span>{where && target.query?.dir ? <>Signed in. In a conversation on that machine, pick <b>{where}</b> next to the model to run on it.</> : where ? <>Signed in. Threads on <b>{where}</b> run on this login now.</> : <>Signed in. New threads can use <b>{label}</b> now.</>}</span></div>
        : st.state === 'failed' ? <div className="callout danger"><Icon name="alert" size={15} /><span style={{ overflowWrap: 'anywhere', minWidth: 0 }}>{st.error || 'The sign-in did not finish.'}</span></div>
        : (
          <div className="stack">
            <ol className="signin-steps">
              <li>Open the sign-in page and sign in with the {service} account this login should use.
                <div className="mt-s row" style={{ flexWrap: 'wrap', gap: 8 }}>
                  <a className="btn sm" href={st.url} target="_blank" rel="noopener noreferrer"><Icon name="external" size={13} />Open the sign-in page</a>
                  <button type="button" className="btn sm" title="Copy the sign-in link, to open it in another browser or on another device"
                    onClick={async () => { const ok = await copyText(st.url || ''); toast(ok ? 'Sign-in link copied' : 'Could not copy the link', ok ? 'ok' : 'err') }}><Icon name="copy" size={13} />Copy link</button>
                </div></li>
              {isClaude
                ? <li>When the page shows a code, copy it and paste it here.
                  <Field label="Code"><input value={code} onChange={e => setCode(e.target.value)} placeholder="Paste the code" autoComplete="off" spellCheck={false}
                    onKeyDown={e => { if (e.key === 'Enter' && code.trim()) sendCode() }} /></Field></li>
                : <li>Enter this one-time code on the page (it expires in 15 minutes):
                  {st.code ? <CopyField value={st.code} label="One-time code" /> : <span className="muted"> waiting for the code…</span>}</li>}
            </ol>
            {st.state === 'checking' && <div className="loading"><Spinner /> Checking the code…</div>}
            {!isClaude && <div className="loading"><Spinner /> Waiting for you to finish on the page…</div>}
            {st.error && <p className="danger-text small" style={{ margin: 0, overflowWrap: 'anywhere' }}>{st.error}</p>}
          </div>
        )}
    </Modal>
  )
}

/** Where this person's agents run and whether the sandbox works. */
function PeopleNotice({ info, isAdmin }: { info: PeopleInfo; isAdmin: boolean }) {
  if (info.jailed) {
    if (!info.sandbox && !info.sandbox_off) {
      return <div className="callout warn"><Icon name="alert" size={15} /><span>Your agents run in a sandbox that does not work on this server yet{info.sandbox_error ? ` (${info.sandbox_error})` : ''}, so they cannot start. Ask {info.owner || 'the server owner'} to set it up.</span></div>
    }
    return <div className="callout info"><Icon name="lock" size={15} /><span>Your agents work in your own folder <code>{info.projects}</code>, on your own logins. They cannot see anyone else's files, logins or chats{info.sandbox_off ? ' (the sandbox is switched off on this server, so this is not enforced)' : ''}.</span></div>
  }
  if (!isAdmin) return null
  if (info.sandbox_off) return <div className="callout warn"><Icon name="alert" size={15} /><span>SANDBOX=off: other people's agents run in their own folders but <b>without</b> a sandbox.</span></div>
  if (!info.sandbox) return <div className="callout warn"><Icon name="alert" size={15} /><span>Other people's agents run sandboxed, and the sandbox does not work here: {info.sandbox_error}. Until it does, only you can run agents. Install it with <code>sudo apt install bubblewrap</code> (Pocket AI notices within a few minutes).</span></div>
  return <div className="callout info"><Icon name="lock" size={15} /><span>The server login is yours alone. Everyone else signs in with their own subscription; their agents run sandboxed in their own folder and see nothing of yours.</span></div>
}
