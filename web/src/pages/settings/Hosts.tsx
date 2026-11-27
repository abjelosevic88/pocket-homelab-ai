// Other machines agents run on, like Paseo's hosts: Pocket AI starts
// claude / codex there over ssh, and the conversation lives here as usual.
// Each machine is its adder's: the owner's go over the server's own key
// (admins manage them), everyone else's over a key of their own that
// Pocket AI makes in their folder. Nobody sees anyone else's.
// Settings → Machines: each machine is a card with its Claude Code / Codex
// logins (the default ~/.claude and extra folders like ~/.claude-max) to
// add, sign in, sign out and remove, laid out like This server's.
import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import { planName } from '../../lib/format'
import { type Host, type HostLogin, LOGIN_PROVIDERS, type ProviderId, isAcp } from '../../lib/types'
import { Icon } from '../../components/icons'
import { ProviderToggle } from '../../components/AgentPicker'
import { Confirm, CopyField, errMsg, Field, MenuButton, Modal, Spinner, useAsync, useToast, type MenuItem } from '../../components/ui'
import { SignInDialog, type SignInTarget } from './Accounts'
import { ago } from './common'
import { copyLogin, CopyLoginDialog, type LoginDest, loginSources, type LoginSource, SignInHow, sourceKey } from './AgentLogins'
import { LoginList, MachineCard, osIcon, provName, tilde, type CliChip, useCLIUpdate, useLatestCLIs } from './MachineParts'


/** Settings → Machines after "This server": a card per machine (status,
 *  versions, its logins by CLI) and a last card to add one. */
/** only: the one machine to show (Settings → Machines picks it); 'new' shows
 *  the card to add one. onAdded opens the machine just added. */
export function HostsSection({ logins, only, onAdded, onRemoved }: { logins: HostLoginsState; only?: number | 'new'; onAdded?: (id: number) => void; onRemoved?: () => void }) {
  const { user, hosts, accounts, reloadHosts, reloadAccounts } = useApp()
  const { toast } = useToast()
  const own = useAsync(() => api.get<{ people?: { jailed?: boolean } }>(`${V1}/settings`).then(r => !!r.people?.jailed), [])
  const jailed = !!own.data
  // their own machines, or the server's for an admin
  const isAdmin = jailed ? !!user?.can_run : user?.role === 'admin'
  const latest = useLatestCLIs()
  const upd = useCLIUpdate()
  const [edit, setEdit] = useState<Host | 'new' | null>(null)
  const [del, setDel] = useState<Host | null>(null)
  const [checking, setChecking] = useState<number | null>(null)
  const [addLogin, setAddLogin] = useState<{ host: Host; provider: ProviderId } | null>(null)
  const [signIn, setSignIn] = useState<SignInTarget | null>(null)
  const [rmLogin, setRmLogin] = useState<HostLogin | null>(null)
  const [copyTo, setCopyTo] = useState<HostLogin | null>(null)
  // signed-in Antigravity logins anywhere: a new login can be a copy of one
  const sources = (p: ProviderId) => isAcp(p) ? loginSources(p, accounts, logins.byHost) : []
  const loginsDone = (id: number) => { logins.refresh(id); reloadAccounts().catch(() => {}) }
  const signInTarget = (l: HostLogin): SignInTarget => ({ label: loginName(l), provider: l.provider, base: `${V1}/hosts/${l.host_id}/login/${l.provider}`, query: l.dir ? { dir: l.dir } : undefined, where: l.dir ? `${l.host} · ${loginName(l)}` : l.host })
  const signOut = async (l: HostLogin) => {
    try { await api.post(`${V1}/hosts/${l.host_id}/logout/${l.provider}${dirQ(l)}`); toast(`Signed out ${loginName(l)} on ${l.host}`); loginsDone(l.host_id) } catch (e) { toast(errMsg(e), 'err') }
  }

  // the machine (reachable, CLI versions) and its logins, both asked afresh
  const check = async (h: Host) => {
    setChecking(h.id)
    logins.refresh(h.id)
    try {
      const n = await api.post<Host>(`${V1}/hosts/${h.id}/check`)
      if (!n.info.ok) toast(`${n.name}: ${n.info.error || 'not ready'}`, 'err')
      await reloadHosts()
    } catch (e) { toast(errMsg(e), 'err') } finally { setChecking(null) }
  }

  return (
    <>
      {hosts.filter(h => only === undefined || h.id === only).map(h => {
        const more: MenuItem[] = [
          { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(h) },
          'sep',
          { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => setDel(h) },
        ]
        const missing = Object.entries(h.info.missing || {})
        const busy = checking === h.id || logins.asking(h.id)
        const clis: CliChip[] = (['claude', 'codex'] as ProviderId[]).flatMap((p): CliChip[] => h.info.versions?.[p] ? [{ provider: p, version: h.info.versions[p], latest: latest[p], updating: upd.busy(h.id, p),
          onUpdate: isAdmin ? () => { void upd.update(h.id, h.name, p, () => { logins.refresh(h.id); return reloadHosts() }) } : undefined }] : h.info.missing?.[p] ? [{ provider: p, error: h.info.missing[p] }] : [])
        // Antigravity CLI: there (it updates itself), or one tap away
        if (h.info.versions?.antigravity) clis.push({ provider: 'antigravity', version: h.info.versions.antigravity })
        else if (isAdmin && h.info.ok) clis.push({ provider: 'antigravity', updating: upd.busy(h.id, 'antigravity'),
          onInstall: () => { void upd.update(h.id, h.name, 'antigravity', () => { logins.refresh(h.id); return reloadHosts() }, true) } })
        const list = logins.byHost[h.id]
        const signed = list?.filter(l => !l.missing && l.signed_in).length
        return (
          <MachineCard key={h.id} icon={osIcon(h.info.os)} name={h.name}
            health={h.info.ok ? (missing.length ? 'warn' : 'ok') : h.info.checked_at ? 'error' : 'off'}
            meta={[<code key="ssh">ssh {h.port ? `-p ${h.port} ` : ''}{h.target}</code>, ...(h.info.os ? [h.info.os] : []),
              ...(list && signed !== undefined && !(h.info.error) ? [`${signed} signed in`] : []), ...(h.info.checked_at ? [`checked ${ago(h.info.checked_at)}`] : [])]}
            clis={clis}
            actions={<>
              <button type="button" className="btn sm ghost icon-only" aria-label={`Check ${h.name} again`} title="Check again" disabled={busy} onClick={() => check(h)}>
                <Icon name="refresh" size={15} className={busy ? 'spin' : ''} /></button>
              {isAdmin && <MenuButton label={`More for ${h.name}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>}
            </>}>
            {h.info.error && <p className="mc-note bad" title={h.info.error}><Icon name="wifi-off" size={14} /><span>{h.info.error}</span></p>}
            <HostLogins host={h} logins={list}
              onAdd={p => setAddLogin({ host: h, provider: p })} onSignIn={l => setSignIn(signInTarget(l))} onSignOut={signOut} onRemove={setRmLogin}
              onCopy={l => setCopyTo(l)} />
          </MachineCard>
        )
      })}
      {isAdmin && (only === undefined || only === 'new') && (
        <div className="mc-new">
          <span className="mc-ico" aria-hidden><Icon name="server" size={22} /></span>
          <div className="mc-new-main">
            <h3>{hosts.length ? 'Add another machine' : 'Add a machine'}</h3>
            <p>{jailed
              ? <>A machine of yours that lets <b>your own key</b> in over ssh, with Claude Code or Codex installed. Pick it next to the folder when you start a thread; only you see it.</>
              : <>Anything this server reaches over ssh — a Mac, a VM, a tailnet name — with Claude Code or Codex installed. Pick it next to the folder when you start a thread.</>}</p>
          </div>
          <button type="button" className="btn primary" onClick={() => setEdit('new')}><Icon name="plus" size={14} />Add machine</button>
        </div>
      )}
      {edit && <HostDialog own={jailed} host={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={h => { const added = edit === 'new'; setEdit(null); reloadHosts().catch(() => {}); logins.refresh(h.id); if (added) onAdded?.(h.id); toast(h.info.ok ? `${h.name} is ready` : `Saved — ${h.info.error || 'not ready yet'}`, h.info.ok ? undefined : 'err') }} />}
      {del && <Confirm title={`Remove ${del.name}?`} confirmLabel="Remove"
        message={<>Agents running there are stopped. Its threads stay in your list but cannot continue until the machine is added again. Nothing on the machine itself is touched.</>}
        onCancel={() => setDel(null)} onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/hosts/${d.id}`); toast('Removed'); onRemoved?.(); reloadHosts().catch(() => {}) } catch (e) { toast(errMsg(e), 'err') } }} />}
      {addLogin && <AddLoginDialog host={addLogin.host} provider={addLogin.provider} logins={logins.byHost[addLogin.host.id] || []} sources={sources} onClose={() => setAddLogin(null)}
        onAdded={(l, made) => { setAddLogin(null); if (made) loginsDone(l.host_id); setSignIn(signInTarget(l)) }}
        onCopied={id => { setAddLogin(null); loginsDone(id) }} />}
      {copyTo && <CopyLoginDialog pid={copyTo.provider} to={{ host: copyTo.host_id, hostName: copyTo.dir ? `${copyTo.host} · ${loginName(copyTo)}` : copyTo.host, dir: copyTo.dir || '' }} sources={sources(copyTo.provider)}
        onClose={() => setCopyTo(null)} onDone={() => { const id = copyTo.host_id; setCopyTo(null); loginsDone(id) }} />}
      {signIn && <SignInDialog target={signIn} onClose={() => { const t = signIn; setSignIn(null); const id = Number(/\/hosts\/(\d+)\//.exec(t.base)?.[1]); if (id) loginsDone(id) }} />}
      {rmLogin && <Confirm title={`Remove ${loginName(rmLogin)} from ${rmLogin.host}?`} confirmLabel="Remove"
        message={<>Conversations on {rmLogin.host} that used it go back to its default login. Nothing is deleted: the folder <code>{tilde(rmLogin.home)}</code>, with its sign-in and history, is moved to <code>~/.pocket-ai/removed-logins</code> there; move it back to bring the login back.</>}
        onCancel={() => setRmLogin(null)} onConfirm={async () => {
          const l = rmLogin; setRmLogin(null)
          try { await api.del(`${V1}/hosts/${l.host_id}/logins/${l.provider}${dirQ(l)}`); toast(`Removed ${loginName(l)} from ${l.host}`); loginsDone(l.host_id) } catch (e) { toast(errMsg(e), 'err') }
        }} />}
    </>
  )
}

/** Each machine's logins, asked one machine at a time so a machine that is
 *  off (ssh waits 15s for it) does not hold up the others. */
export type HostLoginsState = ReturnType<typeof useHostLogins>

export function useHostLogins(hosts: Host[]) {
  const [byHost, setByHost] = useState<Record<number, HostLogin[]>>({})
  const [busy, setBusy] = useState<Record<number, boolean>>({})
  const ask = (id: number, fresh: boolean, live = () => true) => {
    setBusy(b => ({ ...b, [id]: true }))
    api.get<HostLogin[]>(`${V1}/hosts/logins?host=${id}${fresh ? '&refresh=1' : ''}`)
      .catch(() => [] as HostLogin[])
      .then(l => { if (live()) { setByHost(m => ({ ...m, [id]: l })); setBusy(b => ({ ...b, [id]: false })) } })
  }
  const ids = hosts.map(h => h.id).join(',')
  useEffect(() => {
    let on = true
    for (const id of ids ? ids.split(',').map(Number) : []) ask(id, false, () => on)
    return () => { on = false }
  }, [ids]) // eslint-disable-line react-hooks/exhaustive-deps
  return { byHost, asking: (id: number) => !!busy[id], refresh: (id: number) => ask(id, true) }
}

/** "Default login" for ~/.claude, "claude-max" for ~/.claude-max. */
const loginName = (l: HostLogin) => l.label || 'Default login'
const dirQ = (l: HostLogin) => l.dir ? `?dir=${encodeURIComponent(l.dir)}` : ''

/** A machine's logins grouped by CLI, like this server's. */
function HostLogins({ host, logins, onAdd, onSignIn, onSignOut, onRemove, onCopy }: { host: Host; logins?: HostLogin[]; onAdd: (p: ProviderId) => void; onSignIn: (l: HostLogin) => void; onSignOut: (l: HostLogin) => void; onRemove: (l: HostLogin) => void; onCopy: (l: HostLogin) => void }) {
  if (!logins) return <div className="loading"><Spinner /> Asking {host.name} about its logins…</div>
  const down = logins.length > 0 && logins.every(l => l.error && !l.missing)
  // the card already says why the machine cannot be reached
  if (down) return host.info.error ? null : <p className="mc-note">Could not ask {host.name} about its logins{logins[0].error ? `: ${logins[0].error}` : ''}.</p>
  const order = (p: ProviderId) => LOGIN_PROVIDERS.indexOf(p)
  const rows = logins.filter(l => !l.missing).sort((a, b) => order(a.provider) - order(b.provider) || (a.dir ? 1 : 0) - (b.dir ? 1 : 0) || loginName(a).localeCompare(loginName(b)))
  const installed = LOGIN_PROVIDERS.filter(p => rows.some(l => l.provider === p))
  if (!installed.length) return <p className="mc-note">No agent is installed on {host.name} yet.</p>
  return (
    <LoginList onAdd={onAdd} providers={installed} items={rows.map(l => ({
      key: l.provider + ':' + (l.dir || 'default'), provider: l.provider, name: loginName(l), signedIn: l.signed_in, plan: l.plan, email: l.email, error: l.error, folder: l.home,
      menuLabel: `More for ${loginName(l)} on ${host.name}`, onSignIn: () => onSignIn(l),
      menu: [
        { label: l.signed_in ? 'Sign in again…' : 'Sign in…', icon: 'key', onSelect: () => onSignIn(l) },
        ...(isAcp(l.provider) ? [{ label: 'Copy sign-in from…', icon: 'copy', onSelect: () => onCopy(l) }] : []),
        ...(l.signed_in ? [{ label: 'Sign out', icon: 'log-out', onSelect: () => onSignOut(l) }] : []),
        ...(l.dir ? ['sep' as const, { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => onRemove(l) }] : []),
      ] as MenuItem[],
    }))} />
  )
}

const slug = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-+$/, '')

/** Another login on a machine: ~/.claude-<name> (or the default, while it is not signed in), then its sign-in. */
function AddLoginDialog({ host, provider: initial, logins, sources, onClose, onAdded, onCopied }: { host: Host; provider: ProviderId; logins: HostLogin[]; sources: (p: ProviderId) => LoginSource[]; onClose: () => void; onAdded: (l: HostLogin, made: boolean) => void; onCopied: (hostId: number) => void }) {
  const { toast } = useToast()
  const installed = LOGIN_PROVIDERS.filter(p => logins.some(l => l.provider === p && !l.missing))
  const [provider, setProvider] = useState<ProviderId>(initial)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const def = logins.find(l => l.provider === provider && !l.dir && !l.missing)
  const useDefault = !!def && !def.signed_in && !def.error
  const missing = !installed.includes(provider)
  const s = slug(name)
  // the folder a login gets there: ~/.claude-max, ~/.codex-pro, ~/.gemini-work (Antigravity)
  const prefix = provider === 'antigravity' ? 'gemini' : provider
  const taken = logins.some(l => l.provider === provider && !!l.dir && l.label === `${prefix}-${s}`)
  const ready = !busy && !missing && (useDefault || (!!s && !taken))
  // Antigravity: sign in with Google, or as an account that is signed in somewhere already
  const from = sources(provider)
  const [how, setHow] = useState<string | null>(null)
  const pick = how ?? (from[0] ? sourceKey(from[0]) : '')
  const source = from.find(x => sourceKey(x) === pick)
  const add = async () => {
    if (source) {
      setBusy(true)
      const to: LoginDest = useDefault ? { host: host.id, hostName: host.name, dir: '' } : { host: host.id, hostName: host.name, name: name.trim() }
      try {
        const r = await copyLogin(provider, source, to)
        toast(`${host.name} is signed in${r.email ? ` as ${r.email}` : ''}`)
        onCopied(host.id)
      } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
      return
    }
    if (useDefault) { onAdded(def!, false); return }
    setBusy(true)
    try { onAdded(await api.post<HostLogin>(`${V1}/hosts/${host.id}/logins/${provider}`, { name: name.trim() }), true) }
    catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const service = provider === 'claude' ? 'Claude' : provider === 'codex' ? 'ChatGPT' : 'Google'
  return (
    <Modal title={`Add a login on ${host.name}`} size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!ready} onClick={add}>{busy ? <Spinner /> : null}{source ? (useDefault ? 'Copy sign-in' : 'Add with that sign-in') : useDefault ? 'Sign in' : 'Add and sign in'}</button>
    </>}>
      <form className="stack" onSubmit={e => { e.preventDefault(); if (ready) void add() }}>
        <Field label="Agent" group><ProviderToggle only={installed.length ? installed : ['claude', 'codex']} value={provider} onChange={v => { if (v) { setProvider(v); setHow(null) } }} /></Field>
        {missing ? <div className="callout warn"><Icon name="alert" size={15} /><span>{provName(provider)} is not installed on <b>{host.name}</b>. Install it there, then check the machine again.</span></div>
          : useDefault ? <>
            <p className="muted small" style={{ margin: 0 }}>The default login on {host.name} (<code>{tilde(def!.home)}</code>) is not signed in yet: sign it in first, then add more.</p>
            <SignInHow sources={from} value={pick} onChange={setHow} />
          </>
          : <>
            <Field label="Name" help={s ? <>Makes <code>~/.{prefix}-{s}</code> on {host.name}{taken ? ' — that one is there already' : ''}.</> : 'Shown in the login picker, e.g. “max” or “work”.'}>
              <input value={name} onChange={e => setName(e.target.value)} autoFocus maxLength={60} placeholder={provider === 'codex' ? 'second-pro' : provider === 'antigravity' ? 'work' : 'max'} spellCheck={false} autoCapitalize="off" />
            </Field>
            <SignInHow sources={from} value={pick} onChange={setHow} />
            <p className="muted small" style={{ margin: 0 }}>{isAcp(provider)
              ? <>Its own Google sign-in, history and limits. Conversations on {host.name} pick it next to the model.</>
              : <>Its own {service} sign-in, history and limits; settings, skills and plugins{provider === 'claude' ? ' and MCP servers' : ''} are shared with the default login there. Conversations on {host.name} pick it next to the model.</>}</p>
          </>}
      </form>
    </Modal>
  )
}

function HostDialog({ own, host, onClose, onSaved }: { own: boolean; host: Host | null; onClose: () => void; onSaved: (h: Host) => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(host?.name || '')
  const [target, setTarget] = useState(host?.target || '')
  const [port, setPort] = useState(host?.port ? String(host.port) : '')
  const [claudeBin, setClaudeBin] = useState(host?.claude_bin || '')
  const [codexBin, setCodexBin] = useState(host?.codex_bin || '')
  const [more, setMore] = useState(!!(host?.claude_bin || host?.codex_bin || host?.port))
  const [busy, setBusy] = useState(false)
  const key = useAsync(() => api.get<{ keys: { file: string; key: string }[]; user: string; own?: boolean }>(`${V1}/hosts/key`), [])
  const save = async () => {
    setBusy(true)
    try {
      const body = { name: name.trim(), target: target.trim(), port: port ? Number(port) : 0, claude_bin: claudeBin.trim(), codex_bin: codexBin.trim() }
      onSaved(host ? await api.patch<Host>(`${V1}/hosts/${host.id}`, body) : await api.post<Host>(`${V1}/hosts`, body))
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={host ? `Edit ${host.name}` : 'Add a machine'} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !target.trim()} onClick={save}>{busy ? <><Spinner />Connecting…</> : host ? 'Save and check' : 'Add and check'}</button>
    </>}>
      <div className="stack">
        <Field label="ssh destination" help={own
          ? <>The account and machine, as you would type them after <code>ssh</code>: <code>me@box.example.com</code> or <code>me@203.0.113.7</code>.</>
          : <>As you would type it after <code>ssh</code> on this server: <code>me@box.tailnet.ts.net</code>, or a Host name from its <code>~/.ssh/config</code>.</>}>
          <input value={target} onChange={e => setTarget(e.target.value)} autoFocus={!host} placeholder="me@build-box" spellCheck={false} autoCapitalize="off" autoComplete="off" className="mono" />
        </Field>
        <Field label="Name" help="Shown in the picker, e.g. “Mac mini” or “Build box”.">
          <input value={name} onChange={e => setName(e.target.value)} maxLength={60} placeholder={target.replace(/^.*@/, '') || 'Build box'} />
        </Field>
        {more ? <>
          <Field label="Port" help="Empty = 22 or what ~/.ssh/config says.">
            <input value={port} onChange={e => setPort(e.target.value.replace(/\D/g, ''))} inputMode="numeric" placeholder="22" style={{ maxWidth: 120 }} />
          </Field>
          <Field label="Claude Code command" help="Only if claude is somewhere unusual there; empty = claude on its PATH.">
            <input value={claudeBin} onChange={e => setClaudeBin(e.target.value)} placeholder="claude" spellCheck={false} autoCapitalize="off" className="mono" />
          </Field>
          <Field label="Codex command" help="Empty = codex on its PATH.">
            <input value={codexBin} onChange={e => setCodexBin(e.target.value)} placeholder="codex" spellCheck={false} autoCapitalize="off" className="mono" />
          </Field>
        </> : <button type="button" className="link-btn small" style={{ alignSelf: 'flex-start' }} onClick={() => setMore(true)}>Port and CLI paths…</button>}
        <div className="callout info host-key">
          <Icon name="key" size={15} />
          <div className="stack" style={{ gap: 6, minWidth: 0, flex: 1 }}>
            {own || key.data?.own
              ? <span>The machine has to let you in without a password: add this line to <code>~/.ssh/authorized_keys</code> of your account there. It is <b>your own key</b>, made for you by Pocket AI; only your agents use it.</span>
              : <span>The machine has to let this server in without a password: add this line to <code>~/.ssh/authorized_keys</code> of the account there{key.data?.user ? <> (it is <b>{key.data.user}</b>’s key)</> : null}.</span>}
            {key.data?.keys?.length ? key.data.keys.slice(0, 1).map(k => <CopyField key={k.file} value={k.key} label={k.file} />)
              : key.error ? <span className="danger-text small">{key.error}</span>
              : key.data ? <span className="muted small">This server has no ssh key yet: run <code>ssh-keygen -t ed25519</code> on it as the user Pocket AI runs as.</span>
              : <span className="muted small"><Spinner /> Reading the key…</span>}
          </div>
        </div>
      </div>
    </Modal>
  )
}
