// Skills: what the agents can use (as the CLI reports it), and your own skills to add, edit and remove.
import { useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { providerName } from '../lib/format'
import type { ProviderId, Skill } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Select } from '../components/Select'
import { Confirm, copyText, Empty, errMsg, Field, Loading, Modal, Segmented, Spinner, noGrammarly, useAsync, useToast } from '../components/ui'

type OwnSkill = { name: string; description: string; body?: string; files: number; path: string }
type Draft = { name: string; description: string; body: string; files: number; creating: boolean }

const blank: Draft = { name: '', description: '', body: '', files: 0, creating: true }
const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+/, '').slice(0, 64)

export default function SkillsPage({ onMenu }: { onMenu?: () => void }) {
  const { accounts } = useApp()
  const { toast } = useToast()
  const [provider, setProvider] = useState<ProviderId>(() => (localStorage.getItem('pai-skills-provider') as ProviderId) || 'claude')
  const [account, setAccount] = useState(0)
  const [refresh, setRefresh] = useState(0)
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<OwnSkill | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [q, setQ] = useState('')
  const scope = qs({ provider, account: account || undefined })
  const own = useAsync(() => api.get<{ dir: string; skills: OwnSkill[] }>(`${V1}/skills/own${scope}`), [scope])
  const cli = useAsync(() => api.get<{ skills: Skill[]; error?: string }>(`${V1}/skills${qs({ provider, account: account || undefined, refresh: refresh ? 1 : undefined })}`), [scope, refresh])
  const logins = accounts.filter(a => a.provider === provider)
  const mine = new Set((own.data?.skills || []).map(s => s.name))
  const match = (s: { name: string; description: string }) => !q.trim() || `${s.name} ${s.description}`.toLowerCase().includes(q.trim().toLowerCase())
  const others = (cli.data?.skills || []).filter(s => !mine.has(s.name) && match(s))
  const ownShown = (own.data?.skills || []).filter(match)
  const invoke = (name: string) => (provider === 'codex' ? '$' : '/') + name

  const pick = (p: ProviderId) => { setProvider(p); setAccount(0); localStorage.setItem('pai-skills-provider', p) }
  const reload = () => { void own.reload(); setRefresh(n => n + 1) }
  const copy = async (text: string) => { if (await copyText(text)) toast(`Copied ${text}`); else toast('Could not copy', 'err') }
  const open = async (s: OwnSkill) => {
    try {
      const full = await api.get<OwnSkill>(`${V1}/skills/own/${s.name}${scope}`)
      setEdit({ name: full.name, description: full.description, body: full.body || '', files: full.files, creating: false })
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const remove = async () => {
    if (!del || deleting) return
    setDeleting(true)
    try {
      await api.del(`${V1}/skills/own/${del.name}${scope}`)
      toast(`Removed ${del.name} (kept in the trash folder)`)
      setDel(null); reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setDeleting(false) }
  }

  return <div className="page">
    <PageHead title="Skills" icon="sparkles" onMenu={onMenu} sub={<>What the agents can use. Type <code>{invoke('name')}</code> in a message, or let the agent pick one when the task fits.</>}
      actions={<button type="button" className="btn primary" onClick={() => setEdit({ ...blank })}><Icon name="plus" size={14} />New skill</button>} />
    <div className="page-scroll"><div className="page-inner stack">
      <div className="row wrap">
        <Segmented label="Agent" value={provider} onChange={pick} options={(['claude', 'codex'] as ProviderId[]).map(p => ({ value: p, label: providerName(p) }))} />
        {logins.length > 0 && <Select size="sm" value={account} onChange={setAccount} label="Login" width={240}
          options={[{ value: 0, label: 'Server login' }, ...logins.map(a => ({ value: a.id, label: a.label || a.email || `Login ${a.id}`, description: a.home }))]} />}
        <span className="grow" />
        <div className="sb-search" style={{ minWidth: 160 }}>
          <Icon name="search" size={14} />
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Filter skills…" aria-label="Filter skills" onKeyDown={e => { if (e.key === 'Escape') setQ('') }} />
        </div>
        <button type="button" className="btn ghost sm" onClick={reload} disabled={cli.loading}><Icon name="refresh" size={14} />Refresh</button>
      </div>

      <section className="stack">
        <h3 className="srv-h">Your skills</h3>
        {own.error && <div className="callout error" role="alert"><span className="grow">{own.error}</span><button type="button" className="btn sm" onClick={own.reload}>Try again</button></div>}
        {own.loading && !own.data && <Loading />}
        {own.data?.skills.length === 0 && <Empty icon="sparkles" title="No skills of your own yet">A skill is a set of instructions the agent loads when a task needs it, e.g. how you deploy, review or write.</Empty>}
        <div className="list-cards">{ownShown.map(s => <div className="list-card" key={s.name}>
          <div className="grow" style={{ minWidth: 0 }}>
            <div className="list-card-title">{s.name}{s.files > 0 && <span className="spec-chip" title="Files besides SKILL.md (references, scripts)">+{s.files} file{s.files === 1 ? '' : 's'}</span>}</div>
            <div className="list-card-sub">{s.description || <span className="muted">No description</span>}</div>
          </div>
          <div className="list-card-actions">
            <button type="button" className="btn sm" aria-label={`Copy ${invoke(s.name)}`} onClick={() => copy(invoke(s.name))}><Icon name="copy" size={14} />{invoke(s.name)}</button>
            <button type="button" className="btn ghost icon-only" aria-label={`Edit skill: ${s.name}`} onClick={() => open(s)}><Icon name="edit" size={15} /></button>
            <button type="button" className="btn ghost icon-only" aria-label={`Remove skill: ${s.name}`} onClick={() => setDel(s)}><Icon name="trash" size={15} /></button>
          </div>
        </div>)}</div>
        {own.data && <p className="muted xsmall">Saved in <code>{own.data.dir}</code></p>}
      </section>

      <section className="stack">
        <h3 className="srv-h">Also available</h3>
        <p className="muted small">From plugins, projects, your claude.ai account and the agent itself. These are read-only here.</p>
        {cli.loading && !cli.data && <Loading label={`Asking ${providerName(provider)}…`} />}
        {cli.error && <div className="callout error" role="alert">{cli.error}</div>}
        {cli.data?.error && <div className="callout warn">{cli.data.error}</div>}
        {cli.data && others.length === 0 && !cli.data.error && <p className="muted small">Nothing else.</p>}
        <div className="list-cards">{others.map(s => <div className="list-card" key={s.name}>
          <div className="grow" style={{ minWidth: 0 }}>
            <div className="list-card-title">{s.name.slice(s.name.indexOf(':') + 1)}{origin(s) && <span className="spec-chip">{origin(s)}</span>}</div>
            <div className="list-card-sub">{s.description || <span className="muted">No description</span>}</div>
          </div>
          <div className="list-card-actions">
            <button type="button" className="btn sm" aria-label={`Copy ${s.invoke}`} onClick={() => copy(s.invoke)}><Icon name="copy" size={14} />Copy</button>
          </div>
        </div>)}</div>
      </section>
    </div></div>
    {edit && <SkillEditor draft={edit} scope={scope} invokeChar={provider === 'codex' ? '$' : '/'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
    {del && <Confirm title={`Remove “${del.name}”?`} confirmLabel="Remove" message={deleting ? 'Removing…' : 'The agent stops using it. Its folder moves to .pocket-ai-trash next to the skills folder, so you can bring it back.'}
      onCancel={() => { if (!deleting) setDel(null) }} onConfirm={remove} />}
  </div>
}

function origin(s: Skill): string {
  if (s.scope) return s.scope === 'repo' ? 'Project' : s.scope[0].toUpperCase() + s.scope.slice(1)
  const i = s.name.indexOf(':')
  return i > 0 ? s.name.slice(0, i) : ''
}

function SkillEditor({ draft, scope, invokeChar, onClose, onSaved }: { draft: Draft; scope: string; invokeChar: string; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(draft)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = /^[a-z0-9][a-z0-9-]{0,63}$/.test(value.name) && !!value.description.trim() && !!value.body.trim()
  const save = async () => {
    if (!valid || busy) return
    setBusy(true); setError('')
    try {
      const body = { name: value.name, description: value.description, body: value.body }
      if (value.creating) await api.post(`${V1}/skills/own${scope}`, body)
      else await api.put(`${V1}/skills/own/${value.name}${scope}`, body)
      onSaved()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <Modal size="lg" title={value.creating ? 'New skill' : `Edit ${value.name}`} onClose={() => { if (!busy) onClose() }} footer={<>
    <button type="button" className="btn" disabled={busy} onClick={onClose}>Cancel</button>
    <button type="button" className="btn primary" disabled={!valid || busy} onClick={save}>{busy && <Spinner />}Save skill</button>
  </>}>
    <div className="stack">
      {value.creating && <Field label="Name" help={<>Lowercase letters, digits and dashes. You run it with <code>{invokeChar}{value.name || 'name'}</code>.</>}>
        <input autoFocus aria-label="Skill name" disabled={busy} value={value.name} placeholder="deploy-app" onChange={e => setValue({ ...value, name: slug(e.target.value) })} />
      </Field>}
      <Field label="When to use it" help="The agent reads this to decide when the skill applies. Say what it does and when to use it.">
        <textarea {...noGrammarly} autoFocus={!value.creating} aria-label="Skill description" rows={3} disabled={busy} value={value.description} onChange={e => setValue({ ...value, description: e.target.value })} />
      </Field>
      <Field label="Instructions" help={`Markdown. Loaded when the skill is used.${value.files === 1 ? " The skill's other file stays as it is." : value.files > 1 ? ` The skill's other ${value.files} files stay as they are.` : ''}`}>
        <textarea {...noGrammarly} className="mono" aria-label="Skill instructions" rows={16} disabled={busy} value={value.body} placeholder={'# Deploying\n\n1. …'} onChange={e => setValue({ ...value, body: e.target.value })} />
      </Field>
      {error && <p className="callout error" role="alert">{error}</p>}
    </div>
  </Modal>
}
