// Other machines agents run on, like Paseo's hosts: Pocket AI starts
// claude / codex there over ssh with the server's own key, and the
// conversation lives here as usual. Admins add them; everyone who may run
// agents picks one next to the folder.
import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Host } from '../../lib/types'
import { Icon } from '../../components/icons'
import { Confirm, CopyField, errMsg, Field, MenuButton, Modal, Spinner, useAsync, useContextMenu, useToast, type MenuItem } from '../../components/ui'
import { ago, Section, StatusDot } from './common'

const provName = (p: string) => p === 'codex' ? 'Codex' : 'Claude Code'

export function HostsSection() {
  const { user, hosts, reloadHosts } = useApp()
  const { toast } = useToast()
  const isAdmin = user?.role === 'admin'
  const [edit, setEdit] = useState<Host | 'new' | null>(null)
  const [del, setDel] = useState<Host | null>(null)
  const [checking, setChecking] = useState<number | null>(null)
  const ctx = useContextMenu()

  const check = async (h: Host) => {
    setChecking(h.id)
    try {
      const n = await api.post<Host>(`${V1}/hosts/${h.id}/check`)
      toast(n.info.ok ? `${n.name} is ready` : `${n.name}: ${n.info.error || 'not ready'}`, n.info.ok ? undefined : 'err')
      await reloadHosts()
    } catch (e) { toast(errMsg(e), 'err') } finally { setChecking(null) }
  }

  return (
    <Section title="Servers" sub={<>Run agents on another machine, like Paseo does: pick it next to the folder when you start a thread. Pocket AI connects with ssh as <b>this server’s</b> user; the machine needs Claude Code and/or Codex installed and signed in.</>}
      actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit('new')}><Icon name="plus" size={14} />Add server</button>}>
      {hosts.length === 0 ? (
        <p className="muted small" style={{ margin: 0 }}>Only this server so far.{isAdmin ? ' Add a machine you can reach with ssh (a tailnet name works).' : ''}</p>
      ) : (
        <div className="acct-groups">
          <div className="acct-group host-group">
            {hosts.map(h => {
              const more: MenuItem[] = [
                { label: 'Check again', icon: 'refresh', onSelect: () => check(h) },
                { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(h) },
                'sep',
                { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => setDel(h) },
              ]
              const versions = Object.entries(h.info.versions || {})
              const missing = Object.entries(h.info.missing || {})
              return (
                <div key={h.id} className="acct-row" onContextMenu={isAdmin ? e => ctx.open(e, more) : undefined}>
                  <StatusDot status={h.info.ok ? (missing.length ? 'warn' : 'ok') : h.info.checked_at ? 'error' : 'off'}
                    title={(h.info.ok ? 'Reachable' : 'Not ready') + (h.info.checked_at ? ` · checked ${ago(h.info.checked_at)}` : '')} />
                  <div className="acct-main">
                    <div className="acct-name">
                      <span className="ellipsis">{h.name}</span>
                      {versions.map(([p]) => <span key={p} className="badge ok">{provName(p)}</span>)}
                      {!h.info.ok && <span className="badge danger">Not ready</span>}
                    </div>
                    <div className="acct-meta">
                      <code>ssh {h.port ? `-p ${h.port} ` : ''}{h.target}</code>
                      {h.info.hostname && <span>{h.info.hostname}</span>}
                      {h.info.os && <span>{h.info.os}</span>}
                      {h.info.checked_at && <span>checked {ago(h.info.checked_at)}</span>}
                    </div>
                    {versions.length > 0 && <div className="acct-meta">{versions.map(([p, v]) => <span key={p}>{provName(p)} {v}</span>)}</div>}
                    {missing.map(([p, why]) => <div key={p} className="muted xsmall">{provName(p)}: {why}</div>)}
                    {h.info.error && <div className="danger-text xsmall" title={h.info.error}>{h.info.error}</div>}
                  </div>
                  <div className="acct-actions">
                    {checking === h.id ? <Spinner /> : isAdmin
                      ? <MenuButton label={`More for ${h.name}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                      : <button type="button" className="btn sm ghost icon-only" aria-label={`Check ${h.name}`} title="Check again" onClick={() => check(h)}><Icon name="refresh" size={14} /></button>}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}
      {edit && <HostDialog host={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={h => { setEdit(null); reloadHosts().catch(() => {}); toast(h.info.ok ? `${h.name} is ready` : `Saved — ${h.info.error || 'not ready yet'}`, h.info.ok ? undefined : 'err') }} />}
      {del && <Confirm title={`Remove ${del.name}?`} confirmLabel="Remove"
        message={<>Agents running there are stopped. Its threads stay in your list but cannot continue until the server is added again. Nothing on the machine itself is touched.</>}
        onCancel={() => setDel(null)} onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/hosts/${d.id}`); toast('Removed'); reloadHosts().catch(() => {}) } catch (e) { toast(errMsg(e), 'err') } }} />}
      {ctx.menu}
    </Section>
  )
}

function HostDialog({ host, onClose, onSaved }: { host: Host | null; onClose: () => void; onSaved: (h: Host) => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(host?.name || '')
  const [target, setTarget] = useState(host?.target || '')
  const [port, setPort] = useState(host?.port ? String(host.port) : '')
  const [claudeBin, setClaudeBin] = useState(host?.claude_bin || '')
  const [codexBin, setCodexBin] = useState(host?.codex_bin || '')
  const [more, setMore] = useState(!!(host?.claude_bin || host?.codex_bin || host?.port))
  const [busy, setBusy] = useState(false)
  const key = useAsync(() => api.get<{ keys: { file: string; key: string }[]; user: string }>(`${V1}/hosts/key`), [])
  const save = async () => {
    setBusy(true)
    try {
      const body = { name: name.trim(), target: target.trim(), port: port ? Number(port) : 0, claude_bin: claudeBin.trim(), codex_bin: codexBin.trim() }
      onSaved(host ? await api.patch<Host>(`${V1}/hosts/${host.id}`, body) : await api.post<Host>(`${V1}/hosts`, body))
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={host ? `Edit ${host.name}` : 'Add a server'} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !target.trim()} onClick={save}>{busy ? <><Spinner />Connecting…</> : host ? 'Save and check' : 'Add and check'}</button>
    </>}>
      <div className="stack">
        <Field label="ssh destination" help={<>As you would type it after <code>ssh</code> on this server: <code>me@box.tailnet.ts.net</code>, or a Host name from its <code>~/.ssh/config</code>.</>}>
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
            <span>The machine has to let this server in without a password: add this line to <code>~/.ssh/authorized_keys</code> of the account there{key.data?.user ? <> (it is <b>{key.data.user}</b>’s key)</> : null}.</span>
            {key.data?.keys?.length ? key.data.keys.slice(0, 1).map(k => <CopyField key={k.file} value={k.key} label={k.file} />)
              : key.data ? <span className="muted small">This server has no ssh key yet: run <code>ssh-keygen -t ed25519</code> on it as the user Pocket AI runs as.</span>
              : <span className="muted small"><Spinner /> Reading the key…</span>}
          </div>
        </div>
      </div>
    </Modal>
  )
}
