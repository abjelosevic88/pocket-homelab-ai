// Skills: every skill the agents can use, which agent has each, and your own
// skills to add, edit, remove and copy between Claude Code and Codex, and to
// the other machines (a chat on a machine only sees that machine's skills).
import { useState, type ReactNode } from 'react'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { providerName } from '../lib/format'
import type { ProviderId, Skill } from '../lib/types'
import { accountOptions } from '../components/AgentPicker'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Select } from '../components/Select'
import { Confirm, copyText, Empty, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, noGrammarly, useAsync, useToast, type MenuItem } from '../components/ui'

type OwnSkill = { name: string; description: string; body?: string; files: number; path: string; sum: string }
type HostCopy = { home: string; sum: string }
type HostSkills = { id: number; name: string; error?: string; homes: Partial<Record<ProviderId, string[]>>; skills: Record<ProviderId, Record<string, HostCopy[]>> }
/** How a skill stands on one machine: per CLI it has here and there, all its login folders hold this copy, some do not, or none. */
type OnHost = { state: 'on' | 'differs' | 'missing' | 'none' | 'error'; todo: ProviderId[]; title: string }
type OwnList = { dir: string; skills: OwnSkill[]; error?: string }
type Row = { name: string; claude?: OwnSkill; codex?: OwnSkill }
type Draft = { name: string; description: string; body: string; files: number; provider?: ProviderId; to: Record<ProviderId, boolean> }
type Show = 'all' | 'both' | ProviderId

const PROVIDERS: ProviderId[] = ['claude', 'codex']
const other = (p: ProviderId): ProviderId => (p === 'claude' ? 'codex' : 'claude')
const invoke = (p: ProviderId, name: string) => (p === 'codex' ? '$' : '/') + name
const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+/, '').slice(0, 64)
const both = (v: boolean) => ({ claude: v, codex: v }) as Record<ProviderId, boolean>

export default function SkillsPage({ onMenu }: { onMenu?: () => void }) {
  const { accounts } = useApp()
  const { toast } = useToast()
  const [acct, setAcct] = useState({ claude: 0, codex: 0 } as Record<ProviderId, number>)
  const [show, setShow] = useState<Show>('all')
  const [q, setQ] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<{ name: string; provider: ProviderId } | null>(null)
  const [busy, setBusy] = useState(false)
  const scope = (p: ProviderId) => qs({ provider: p, account: acct[p] || undefined })
  const own = useAsync(async () => {
    const [claude, codex] = await Promise.all(PROVIDERS.map(p => api.get<OwnList>(`${V1}/skills/own${scope(p)}`)
      .catch((e: unknown): OwnList => ({ dir: '', skills: [], error: errMsg(e) }))))
    return { claude, codex } as Record<ProviderId, OwnList>
  }, [acct.claude, acct.codex, refresh])
  const cli = useAsync(() => Promise.all(PROVIDERS.map(p => api.get<{ skills: Skill[]; error?: string }>(`${V1}/skills${qs({ provider: p, account: acct[p] || undefined, refresh: refresh ? 1 : undefined })}`)
    .then(r => ({ provider: p, skills: r.skills || [], error: r.error })).catch((e: unknown) => ({ provider: p, skills: [] as Skill[], error: errMsg(e) })))), [acct.claude, acct.codex, refresh])

  const machines = useAsync(() => api.get<{ hosts: HostSkills[] }>(`${V1}/skills/hosts`).then(r => r.hosts || []), [refresh])
  const [copying, setCopying] = useState('')

  const rows: Row[] = []
  for (const p of PROVIDERS) for (const s of own.data?.[p].skills || []) {
    let r = rows.find(x => x.name === s.name)
    if (!r) rows.push(r = { name: s.name })
    r[p] = s
  }
  rows.sort((a, b) => a.name.localeCompare(b.name))
  const match = (name: string, desc = '') => !q.trim() || `${name} ${desc}`.toLowerCase().includes(q.trim().toLowerCase())
  const inShow = (r: Row) => show === 'all' || (show === 'both' ? !!(r.claude && r.codex) : !!r[show])
  const shownRows = rows.filter(r => inShow(r) && match(r.name, (r.claude || r.codex)?.description))
  const mine = new Set(rows.map(r => r.name))
  const others = (cli.data || []).filter(l => show === 'all' || show === 'both' || l.provider === show)
    .flatMap(l => l.skills.filter(s => !mine.has(s.name) && match(s.name, s.description)).map(s => ({ ...s, provider: l.provider })))

  const reload = () => setRefresh(n => n + 1)
  const copy = async (text: string) => { if (await copyText(text)) toast(`Copied ${text}`); else toast('Could not copy', 'err') }
  const open = async (name: string, p: ProviderId) => {
    try {
      const full = await api.get<OwnSkill>(`${V1}/skills/own/${name}${scope(p)}`)
      setEdit({ name: full.name, description: full.description, body: full.body || '', files: full.files, provider: p, to: both(false) })
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const send = async (name: string, from: ProviderId, opts: { move?: boolean; replace?: boolean }) => {
    if (busy) return
    const to = other(from)
    setBusy(true)
    try {
      await api.post(`${V1}/skills/own/${name}/copy${scope(from)}`, { to, to_account: acct[to] || 0, ...opts })
      toast(`${opts.move ? 'Moved' : 'Copied'} ${name} to ${providerName(to)}`)
      reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const onHost = (r: Row, h: HostSkills): OnHost => {
    if (h.error) return { state: 'error', todo: [], title: `${h.name}: ${h.error}` }
    const todo: ProviderId[] = []
    const lines: string[] = []
    let have = 0, wanted = 0
    for (const p of PROVIDERS) {
      const mine = r[p], homes = h.homes[p] || []
      if (!mine || homes.length === 0) continue
      wanted++
      const copies = h.skills[p]?.[r.name] || []
      const same = copies.filter(c => c.sum === mine.sum)
      if (copies.length) have++
      if (same.length < homes.length) todo.push(p)
      for (const home of homes) {
        const c = copies.find(x => x.home === home)
        lines.push(`${providerName(p)} ${home}: ${!c ? 'not there' : c.sum === mine.sum ? 'same as here' : 'a different version'}`)
      }
    }
    if (wanted === 0) return { state: 'none', todo, title: `${h.name} has neither of these agents` }
    const title = lines.join('\n')
    if (todo.length === 0) return { state: 'on', todo, title }
    return { state: have ? 'differs' : 'missing', todo, title }
  }
  const toHost = async (r: Row, h: HostSkills, todo: ProviderId[], update: boolean) => {
    if (copying) return
    setCopying(`${r.name}|${h.id}`)
    try {
      for (const p of todo) await api.post(`${V1}/skills/own/${r.name}/to-host${scope(p)}`, { host_id: h.id })
      toast(`${update ? 'Updated' : 'Copied'} ${r.name} on ${h.name}`)
      reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setCopying('') }
  }
  const remove = async () => {
    if (!del || busy) return
    setBusy(true)
    try {
      await api.del(`${V1}/skills/own/${del.name}${scope(del.provider)}`)
      toast(`Removed ${del.name} from ${providerName(del.provider)} (kept in the trash folder)`)
      setDel(null); reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const menu = (r: Row): MenuItem[] => {
    const items: MenuItem[] = []
    for (const p of PROVIDERS) if (r[p]) items.push({ label: `Edit in ${providerName(p)}`, icon: 'edit', onSelect: () => open(r.name, p) })
    items.push('sep')
    for (const p of PROVIDERS) if (r[p]) items.push({ label: `Copy ${invoke(p, r.name)}`, icon: 'copy', onSelect: () => copy(invoke(p, r.name)) })
    items.push('sep')
    for (const p of PROVIDERS) {
      if (!r[p]) continue
      const to = other(p)
      if (!r[to]) {
        items.push({ label: `Copy to ${providerName(to)}`, icon: 'copy', onSelect: () => send(r.name, p, {}) })
        items.push({ label: `Move to ${providerName(to)}`, icon: 'move', onSelect: () => send(r.name, p, { move: true }) })
      } else {
        items.push({ label: `Replace ${providerName(to)}'s copy with ${providerName(p)}'s`, icon: 'refresh', onSelect: () => send(r.name, p, { replace: true }) })
      }
    }
    const hostItems: MenuItem[] = []
    for (const h of machines.data || []) {
      const o = onHost(r, h)
      if (o.state === 'missing') hostItems.push({ label: `Copy to ${h.name}`, icon: 'server', onSelect: () => toHost(r, h, o.todo, false) })
      if (o.state === 'differs') hostItems.push({ label: `Update on ${h.name}`, icon: 'refresh', onSelect: () => toHost(r, h, o.todo, true) })
    }
    if (hostItems.length) items.push('sep', ...hostItems)
    items.push('sep')
    for (const p of PROVIDERS) if (r[p]) items.push({ label: `Remove from ${providerName(p)}`, icon: 'trash', danger: true, onSelect: () => setDel({ name: r.name, provider: p }) })
    return items
  }
  const logins = (p: ProviderId) => accountOptions(accounts, p, acct[p])

  const hosts = machines.data || []
  const where = (p: ProviderId) => own.data?.[p].dir
  const legend = <span className="skills-legend muted xsmall">
    <span><Icon name="check" size={12} /> there</span><span><Icon name="plus" size={12} /> copy there</span><span className="warn"><Icon name="refresh" size={12} /> different copy, click to update</span>
  </span>

  return <div className="page skills-page">
    <PageHead title="Skills" icon="sparkles" onMenu={onMenu} sub={<>Type <code>/name</code> in a message to use one, or let the agent pick.</>}
      actions={<button type="button" className="btn primary" onClick={() => setEdit({ name: '', description: '', body: '', files: 0, to: both(true) })}><Icon name="plus" size={14} />New skill</button>} />
    <div className="page-scroll"><div className="page-inner stack">
      <div className="skills-bar">
        <div className="sb-search">
          <Icon name="search" size={14} />
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Find a skill" aria-label="Find a skill" onKeyDown={e => { if (e.key === 'Escape') setQ('') }} />
        </div>
        <Segmented label="Agent" size="sm" value={show} onChange={setShow} options={[
          { value: 'all', label: 'All' }, { value: 'claude', label: 'Claude Code' }, { value: 'codex', label: 'Codex' }]} />
        {PROVIDERS.filter(p => logins(p).length > 1).map(p => <Select key={p} size="sm" className="ghost" value={acct[p]} onChange={v => setAcct({ ...acct, [p]: v })} label={`${providerName(p)} login`} width={210}
          options={logins(p).map(o => ({ ...o, label: `${providerName(p)}: ${o.label}` }))} />)}
        <span className="grow" />
        <button type="button" className="btn ghost icon-only" aria-label="Refresh" title="Check again" onClick={reload} disabled={cli.loading || own.loading || machines.loading}>
          {machines.loading || own.loading ? <Spinner /> : <Icon name="refresh" size={15} />}</button>
      </div>

      {PROVIDERS.map(p => own.data?.[p].error && <div key={p} className="callout warn" role="alert">{providerName(p)}: {own.data[p].error}</div>)}
      {own.loading && !own.data && <Loading />}
      {own.data && rows.length === 0 && <Empty icon="sparkles" title="No skills of your own yet">A skill is a set of instructions the agent loads when a task needs it, e.g. how you deploy, review or write.</Empty>}
      {own.data && rows.length > 0 && <>
        <div className="skills-table-wrap">
          <table className="skills-table">
            <thead><tr>
              <th className="sk-name-h">Your skills <span className="muted">{rows.length}</span></th>
              <th className="sk-agents-h">Agents</th>
              <th className="sk-host-h" title={PROVIDERS.map(p => where(p) && `${providerName(p)}: ${where(p)}`).filter(Boolean).join('\n')}>This server</th>
              {hosts.map(h => <th key={h.id} className={`sk-host-h ${h.error ? 'off' : ''}`} title={h.error ? `${h.name}: ${h.error}` : h.name}>
                <span className="sk-host-name">{h.error && <Icon name="wifi-off" size={12} />}{h.name}</span></th>)}
              {machines.loading && !machines.data && <th className="sk-host-h"><Spinner /></th>}
              <th aria-label="Actions" />
            </tr></thead>
            <tbody>
              {shownRows.length === 0 && <tr><td colSpan={4 + hosts.length} className="muted small sk-none">No skill matches.</td></tr>}
              {shownRows.map(r => {
                const lead = (r.claude || r.codex)!
                return <tr key={r.name}>
                  <td className="sk-name">
                    <button type="button" className="sk-title" onClick={() => open(r.name, r.claude ? 'claude' : 'codex')} title="Edit">{r.name}</button>
                    <div className="sk-desc" title={lead.description}>{lead.description || <span className="muted">No description</span>}</div>
                  </td>
                  <td className="sk-agents">{PROVIDERS.map(p => r[p]
                    ? <span key={p} className={`sk-agent on prov-glyph ${p}`} title={`In ${providerName(p)}\n${r[p]!.path}`}><Icon name={p} size={15} /></span>
                    : <button key={p} type="button" className="sk-agent" disabled={busy} title={`Not in ${providerName(p)}. Click to copy it there.`} aria-label={`Copy ${r.name} to ${providerName(p)}`} onClick={() => send(r.name, other(p), {})}><Icon name={p} size={15} /></button>)}
                  </td>
                  <td className="sk-cell"><span className="sk-mark on" title={PROVIDERS.filter(p => r[p]).map(p => r[p]!.path).join('\n')}><Icon name="check" size={14} /></span></td>
                  {hosts.map(h => {
                    const o = onHost(r, h)
                    const label = `${r.name} on ${h.name}`
                    let mark: ReactNode
                    if (copying === `${r.name}|${h.id}`) mark = <span className="sk-mark"><Spinner /></span>
                    else if (o.state === 'on') mark = <span className="sk-mark on" title={o.title} aria-label={`${label}: there`}><Icon name="check" size={14} /></span>
                    else if (o.state === 'none' || o.state === 'error') mark = <span className="sk-mark none" title={o.title}>–</span>
                    else {
                      const update = o.state === 'differs'
                      mark = <button type="button" className={`sk-mark btn-mark ${update ? 'differs' : 'add'}`} disabled={!!copying} aria-label={update ? `Update ${label}` : `Copy ${r.name} to ${h.name}`}
                        title={`${o.title}\n\n${update ? `Click to replace it with this server's copy` : `Click to copy it to ${h.name}`}`} onClick={() => toHost(r, h, o.todo, update)}>
                        <Icon name={update ? 'refresh' : 'plus'} size={14} /></button>
                    }
                    return <td key={h.id} className="sk-cell">{mark}</td>
                  })}
                  {machines.loading && !machines.data && <td />}
                  <td className="sk-more"><MenuButton label={`More for ${r.name}`} align="right" width={290} items={() => menu(r)}><Icon name="more" size={16} /></MenuButton></td>
                </tr>
              })}
            </tbody>
          </table>
        </div>
        {hosts.length > 0 && <span className="muted xsmall sk-scroll-hint">Swipe the table sideways to see every machine.</span>}
        {legend}
      </>}

      <details className="skills-more">
        <summary><Icon name="chevron-right" size={14} />Also available{cli.data && <span className="muted"> · {others.length}</span>}<span className="muted xsmall sk-more-from">from plugins, projects and claude.ai</span></summary>
        {cli.loading && !cli.data && <Loading label="Asking Claude Code and Codex…" />}
        {cli.data?.map(l => l.error && <div key={l.provider} className="callout warn">{providerName(l.provider)}: {l.error}</div>)}
        {cli.data && others.length === 0 && <p className="muted small">Nothing else.</p>}
        <ul className="skills-more-list">{others.map(s => <li key={s.provider + s.name}>
          <span className={`prov-glyph ${s.provider}`} title={providerName(s.provider)}><Icon name={s.provider} size={14} /></span>
          <span className="sk-title static">{s.name.slice(s.name.indexOf(':') + 1)}</span>
          <span className="sk-desc grow" title={s.description}>{s.description}</span>
          {origin(s) && <span className="spec-chip">{origin(s)}</span>}
          <button type="button" className="btn ghost icon-only sm" aria-label={`Copy ${s.invoke}`} title={`Copy ${s.invoke}`} onClick={() => copy(s.invoke)}><Icon name="copy" size={14} /></button>
        </li>)}</ul>
      </details>
    </div></div>
    {edit && <SkillEditor draft={edit} scope={scope} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
    {del && <Confirm title={`Remove “${del.name}” from ${providerName(del.provider)}?`} confirmLabel="Remove" message={busy ? 'Removing…' : `${providerName(del.provider)} stops using it. Its folder moves to .pocket-ai-trash next to the skills folder, so you can bring it back.`}
      onCancel={() => { if (!busy) setDel(null) }} onConfirm={remove} />}
  </div>
}

function origin(s: Skill): string {
  if (s.scope) return s.scope === 'repo' ? 'Project' : s.scope[0].toUpperCase() + s.scope.slice(1)
  const i = s.name.indexOf(':')
  return i > 0 ? s.name.slice(0, i) : ''
}

function SkillEditor({ draft, scope, onClose, onSaved }: { draft: Draft; scope: (p: ProviderId) => string; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(draft)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const creating = !value.provider
  const targets = PROVIDERS.filter(p => value.to[p])
  const valid = /^[a-z0-9][a-z0-9-]{0,63}$/.test(value.name) && !!value.description.trim() && !!value.body.trim() && (!creating || targets.length > 0)
  const save = async () => {
    if (!valid || busy) return
    setBusy(true); setError('')
    const body = { name: value.name, description: value.description, body: value.body }
    try {
      if (value.provider) await api.put(`${V1}/skills/own/${value.name}${scope(value.provider)}`, body)
      else for (const p of targets) await api.post(`${V1}/skills/own${scope(p)}`, body)
      onSaved()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <Modal size="lg" title={creating ? 'New skill' : `Edit ${value.name} in ${providerName(value.provider!)}`} onClose={() => { if (!busy) onClose() }} footer={<>
    <button type="button" className="btn" disabled={busy} onClick={onClose}>Cancel</button>
    <button type="button" className="btn primary" disabled={!valid || busy} onClick={save}>{busy && <Spinner />}Save skill</button>
  </>}>
    <div className="stack">
      {creating && <>
        <Field label="Name" help={<>Lowercase letters, digits and dashes. Run it with <code>/{value.name || 'name'}</code> in Claude Code or <code>${value.name || 'name'}</code> in Codex.</>}>
          <input autoFocus aria-label="Skill name" disabled={busy} value={value.name} placeholder="deploy-app" onChange={e => setValue({ ...value, name: slug(e.target.value) })} />
        </Field>
        <Field label="Add to">
          <div className="row wrap" style={{ gap: 16 }}>{PROVIDERS.map(p => <span key={p} className="row" style={{ gap: 8 }}>
            <Switch size="sm" label={providerName(p)} checked={value.to[p]} disabled={busy} onChange={v => setValue({ ...value, to: { ...value.to, [p]: v } })} />{providerName(p)}</span>)}</div>
        </Field>
      </>}
      <Field label="When to use it" help="The agent reads this to decide when the skill applies. Say what it does and when to use it.">
        <textarea {...noGrammarly} autoFocus={!creating} aria-label="Skill description" rows={3} disabled={busy} value={value.description} onChange={e => setValue({ ...value, description: e.target.value })} />
      </Field>
      <Field label="Instructions" help={`Markdown. Loaded when the skill is used.${value.files === 1 ? " The skill's other file stays as it is." : value.files > 1 ? ` The skill's other ${value.files} files stay as they are.` : ''}${creating ? '' : ` Saves to ${providerName(value.provider!)} only; use “Replace … copy” in the ⋯ menu to sync the other agent.`}`}>
        <textarea {...noGrammarly} className="mono" aria-label="Skill instructions" rows={16} disabled={busy} value={value.body} placeholder={'# Deploying\n\n1. …'} onChange={e => setValue({ ...value, body: e.target.value })} />
      </Field>
      {error && <p className="callout error" role="alert">{error}</p>}
    </div>
  </Modal>
}
