// Skills: one library for every agent on every machine. This page lists it,
// shows how the machines stand, and settles the skills that differ somewhere.
// One skill is edited on its own page (SkillPage.tsx).
import { useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError, qs, upload, V1 } from '../lib/api'
import { ago, fmtBytes, providerName } from '../lib/format'
import { LIB, SYNC, quietRetry, skillSlug, standing, syncProgress, useSkillSync, validSkillName, type HostExtra, type Library, type LibSkill, type SyncHost, type SyncStatus } from '../lib/skills'
import type { CliId, Skill } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Confirm, copyText, Empty, errMsg, Field, Loading, Modal, Segmented, Spinner, Switch, Tabs, noGrammarly, useAsync, useToast } from '../components/ui'

const CLIS: CliId[] = ['claude', 'codex']
type Look = { host: SyncHost; name: string; state: 'differs' | 'failed'; where?: string; agent?: string; error?: string }

export default function SkillsPage({ onMenu }: { onMenu?: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [creating, setCreating] = useState(false)
  const [machines, setMachines] = useState(false)
  const [show, setShow] = useState<'library' | 'others'>('library')
  const [busy, setBusy] = useState('')
  const [replace, setReplace] = useState<{ file: File; names: string[] } | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const lib = useAsync(() => api.get<Library>(LIB), [refresh])
  const cli = useAsync(() => show !== 'others' ? Promise.resolve(null) : Promise.all(CLIS.map(p => api.get<{ skills: Skill[]; error?: string }>(`${V1}/skills${qs({ provider: p, refresh: refresh ? 1 : undefined })}`)
    .then(r => ({ provider: p, skills: r.skills || [], error: r.error })).catch((e: unknown) => ({ provider: p, skills: [] as Skill[], error: errMsg(e) })))), [refresh, show])
  const { sync, setSync, reload: reloadSync, soon } = useSkillSync()

  const skills = lib.data?.skills || []
  const match = (name: string, desc = '') => !q.trim() || `${name} ${desc}`.toLowerCase().includes(q.trim().toLowerCase())
  const shown = skills.filter(s => match(s.name, s.description))
  const mine = new Set(skills.map(s => s.name))
  const others = (cli.data || []).flatMap(l => l.skills.filter(s => !mine.has(s.name) && match(s.name, s.description)).map(s => ({ ...s, provider: l.provider })))
  const hosts = sync?.hosts || []
  const on = hosts.filter(h => h.on)
  const looks: Look[] = on.flatMap(h => Object.entries(h.skills).filter(([name, st]) => mine.has(name) && (st.state === 'differs' || st.state === 'failed'))
    .map(([name, st]) => ({ host: h, name, state: st.state as Look['state'], where: st.where, agent: st.agent, error: st.error })))
    .sort((a, b) => a.name.localeCompare(b.name) || a.host.name.localeCompare(b.host.name))
  const extras = new Map<string, { extra: HostExtra; hosts: SyncHost[] }>()
  for (const h of on) for (const e of h.extra) {
    const cur = extras.get(e.name)
    if (cur) cur.hosts.push(h); else extras.set(e.name, { extra: e, hosts: [h] })
  }
  const extraAll = [...extras.values()].sort((a, b) => a.extra.name.localeCompare(b.extra.name))

  const reload = () => { setRefresh(n => n + 1); soon() }
  const copy = async (text: string) => { if (await copyText(text)) toast(`Copied ${text}`); else toast('Could not copy', 'err') }
  const settle = async (l: Look, use: 'library' | 'machine') => {
    if (busy) return
    setBusy(`${l.name}|${l.host.id}`)
    try {
      setSync(await api.post<SyncStatus>(`${SYNC}/resolve`, { host_id: l.host.id, name: l.name, use }))
      toast(use === 'library' ? `${l.host.name} now has this server's ${l.name}` : `${l.name} from ${l.host.name} is now in your library`)
      setRefresh(n => n + 1)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const adopt = async (name: string, h: SyncHost) => {
    if (busy) return
    setBusy(`${name}|${h.id}`)
    try {
      setSync(await api.post<SyncStatus>(`${SYNC}/adopt`, { host_id: h.id, name }))
      toast(`Added ${name} to your library`)
      setRefresh(n => n + 1)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const importFile = async (file: File, force = false) => {
    setBusy('import')
    try {
      const r = await upload<{ added: string[] }>(`${LIB}/import${force ? '?replace=1' : ''}`, 'file', file)
      toast(r.added.length === 1 ? `Added ${r.added[0]}` : `Added ${r.added.length} skills`)
      setReplace(null); reload()
      if (r.added.length === 1) navigate(`/skills/${r.added[0]}`)
    } catch (e) {
      const exists = e instanceof ApiError && e.status === 409 ? (e.data.exists as string[] | undefined) : undefined
      if (exists?.length && !force) setReplace({ file, names: exists }); else toast(errMsg(e), 'err')
    } finally { setBusy('') }
  }

  return <div className="page skills-page">
    <PageHead title="Skills" icon="sparkles" onMenu={onMenu} sub={<>One library for all your agents and machines. Type <code>/name</code> in a message to use a skill.</>}
      actions={<>
        <input ref={fileInput} type="file" accept=".zip,.skill,.md" hidden onChange={e => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void importFile(f) }} />
        <button type="button" className="btn" disabled={busy === 'import'} title="Add skills from a .zip, .skill or SKILL.md file" onClick={() => fileInput.current?.click()}>
          {busy === 'import' ? <Spinner /> : <Icon name="upload" size={14} />}<span className="sk-hide-sm">Import</span></button>
        <button type="button" className="btn primary" onClick={() => setCreating(true)}><Icon name="plus" size={14} />New skill</button>
      </>} />
    <div className="page-scroll"><div className="page-inner stack">
      {lib.error && <div className="callout danger" role="alert">{lib.error}</div>}
      {sync && lib.data && <SyncCard lib={lib.data} sync={sync} looks={looks.length} onMachines={() => setMachines(true)} onSync={() => { void reloadSync(true); setRefresh(n => n + 1) }} />}
      {sync && !sync.asked && hosts.length > 0 && <FirstTime hosts={hosts} onDone={s => { setSync(s); soon() }} />}

      {looks.length > 0 && <section className="sk-looks" aria-label="Skills that need a look">
        <h2 className="sk-h"><Icon name="alert" size={15} />Needs a look <span className="muted">{looks.length}</span></h2>
        <ul>{looks.map(l => {
          const key = `${l.name}|${l.host.id}`
          return <li key={key}>
            <div className="grow">
              <div><button type="button" className="sk-link" onClick={() => navigate(`/skills/${l.name}`)}>{l.name}</button>{' '}
                {l.state === 'failed' ? <>could not be sent to <b>{l.host.name}</b></> : <>is different on <b>{l.host.name}</b></>}</div>
              <div className="muted small">{l.state === 'failed' ? l.error
                : l.where && l.where !== 'lib' ? `${providerName(l.agent || 'claude')} there has its own copy (in ~/${l.where.slice(l.where.lastIndexOf('/') + 1)}/skills). Which one should every machine use?`
                  : 'It was changed here and there since the last sync. Which one should every machine use? The other one is kept in the trash folder.'}</div>
            </div>
            {busy === key ? <Spinner /> : <div className="row wrap sk-look-actions">
              <button type="button" className="btn sm" disabled={!!busy} onClick={() => settle(l, 'library')}>{l.state === 'failed' ? 'Send again' : 'Use this server’s'}</button>
              {l.state === 'differs' && <button type="button" className="btn sm" disabled={!!busy} onClick={() => settle(l, 'machine')}>Use {l.host.name}’s</button>}
            </div>}
          </li>
        })}</ul>
      </section>}

      {lib.loading && !lib.data && <Loading />}
      {lib.data && <>
        <div className="skills-bar">
          <div className="sb-search">
            <Icon name="search" size={14} />
            <input value={q} onChange={e => setQ(e.target.value)} placeholder="Find a skill" aria-label="Find a skill" onKeyDown={e => { if (e.key === 'Escape') setQ('') }} />
          </div>
          {show === 'library' && skills.length > 0 && <span className="muted small">{shown.length === skills.length ? `${skills.length} in your library` : `${shown.length} of ${skills.length}`}</span>}
          <span className="grow" />
          <Segmented label="Which skills" size="sm" value={show} onChange={setShow} options={[{ value: 'library', label: 'Your library' }, { value: 'others', label: 'From plugins' }]} />
        </div>
        {show === 'library' && <>
          {skills.length === 0 && <Empty icon="sparkles" title="No skills yet">A skill is a set of instructions an agent loads when a task needs it, e.g. how you deploy, review or write. Make one here and every agent on every machine gets it.</Empty>}
          {skills.length > 0 && shown.length === 0 && <p className="muted small">No skill matches.</p>}
          <div className="sk-grid">{shown.map(s => <SkillCard key={s.name} skill={s} sync={sync} onOpen={() => navigate(`/skills/${s.name}`)} onCopy={() => copy(`/${s.name}`)} />)}</div>
        </>}
        {show === 'others' && <>
          <p className="muted small sk-others-note">Skills that come with plugins, projects and claude.ai. The agents load them by themselves; they are not part of your library and are not copied to your machines.</p>
          {cli.loading && !cli.data && <Loading label="Asking Claude Code and Codex…" />}
          {cli.data?.map(l => l.error && <div key={l.provider} className="callout warn">{providerName(l.provider)}: {l.error}</div>)}
          {cli.data && others.length === 0 && <p className="muted small">{q.trim() ? 'No skill matches.' : 'Nothing else.'}</p>}
          {others.length > 0 && <ul className="skills-more-list">{others.map(s => <li key={s.provider + s.name}>
            <span className={`prov-glyph ${s.provider}`} title={providerName(s.provider)}><Icon name={s.provider} size={14} /></span>
            <span className="sk-title static">{s.name.slice(s.name.indexOf(':') + 1)}</span>
            <span className="sk-desc grow" title={s.description}>{s.description}</span>
            {origin(s) && <span className="spec-chip">{origin(s)}</span>}
            <button type="button" className="btn ghost icon-only sm" aria-label={`Copy ${s.invoke}`} title={`Copy ${s.invoke}`} onClick={() => copy(s.invoke)}><Icon name="copy" size={14} /></button>
          </li>)}</ul>}
        </>}
      </>}
    </div></div>
    {creating && <NewSkill onClose={() => setCreating(false)} onSaved={name => { setCreating(false); soon(); navigate(`/skills/${name}`) }} />}
    {machines && sync && <MachinesDialog sync={sync} extras={extraAll} busy={busy} onAdopt={adopt} onClose={() => setMachines(false)} onChange={s => { setSync(s); soon() }} />}
    {replace && <Confirm title={replace.names.length === 1 ? `Replace “${replace.names[0]}”?` : `Replace ${replace.names.length} skills?`} confirmLabel="Replace"
      message={`${replace.names.join(', ')} ${replace.names.length === 1 ? 'is' : 'are'} already in your library. The current ${replace.names.length === 1 ? 'one moves' : 'ones move'} to the trash folder.`}
      onCancel={() => setReplace(null)} onConfirm={() => importFile(replace.file, true)} />}
  </div>
}

function origin(s: Skill): string {
  if (s.scope) return s.scope === 'repo' ? 'Project' : s.scope[0].toUpperCase() + s.scope.slice(1)
  const i = s.name.indexOf(':')
  return i > 0 ? s.name.slice(0, i) : ''
}

function SkillCard({ skill, sync, onOpen, onCopy }: { skill: LibSkill; sync: SyncStatus | null; onOpen: () => void; onCopy: () => void }) {
  const st = standing(skill.name, sync)
  return <article className="sk-card">
    <button type="button" className="sk-card-main" onClick={onOpen} aria-label={`Open ${skill.name}`}>
      <span className="sk-card-name">{skill.name}</span>
      <span className="sk-card-desc">{skill.description || 'No description'}</span>
    </button>
    <footer>
      <span className={`sk-standing ${st.kind}`} title={st.title}>
        {st.kind === 'busy' ? <Spinner size={11} /> : <Icon name={st.kind === 'look' ? 'alert' : st.kind === 'local' ? 'server' : 'check'} size={12} />}{st.label}</span>
      <span className="grow" />
      <span className="muted xsmall" title={skill.path}>{skill.file_count > 0 && `${skill.file_count + 1} files · `}{fmtBytes(skill.size)} · {ago(skill.modified)}</span>
      <button type="button" className="btn ghost icon-only sm" aria-label={`Copy /${skill.name}`} title={`Copy /${skill.name}`} onClick={onCopy}><Icon name="copy" size={13} /></button>
    </footer>
  </article>
}

/** The strip on top: which agents use the library, and how the machines stand. */
function SyncCard({ lib, sync, looks, onMachines, onSync }: { lib: Library; sync: SyncStatus; looks: number; onMachines: () => void; onSync: () => void }) {
  const on = sync.hosts.filter(h => h.on)
  const down = on.filter(h => h.error)
  const fine = on.length - down.length
  const prog = syncProgress(sync)
  const names = (l: SyncHost[]) => (l.length <= 2 ? l.map(h => h.name).join(' and ') : `${l.length} machines`)
  let kind = 'ok', text = 'Your library is on this server'
  let sub: ReactNode = sync.hosts.length ? 'No other machine follows it yet.' : 'Add a machine in Settings → Machines to share it.'
  const checked = sync.at ? ` Checked ${ago(sync.at)}.` : ''
  if (prog) {
    kind = 'busy'
    text = prog.done > 0 || prog.now.length > 0 ? `Syncing: ${prog.done} of ${prog.total} ${prog.total === 1 ? 'machine' : 'machines'} done` : 'Starting to sync…'
    sub = prog.now.length ? cap(prog.now.slice(0, 3).join(', ')) + (prog.now.length > 3 ? ` and ${prog.now.length - 3} more` : '') : 'Looking at what each machine has.'
  } else if (looks) {
    kind = 'look'; text = looks === 1 ? '1 skill needs a look' : `${looks} skills need a look`
    sub = `Pick which version to keep below.${checked}`
  } else if (down.length && fine === 0) {
    kind = 'look'; text = `No answer from ${names(down)}`
    sub = `${down.length === 1 ? 'It gets' : 'They get'} your skills when ${down.length === 1 ? 'it is' : 'they are'} back. Nothing else to do.${checked}`
  } else if (down.length) {
    kind = 'part'; text = `Up to date on ${fine === 1 ? '1 machine' : `${fine} machines`}. No answer from ${names(down)}.`
    sub = `${down.length === 1 ? 'It gets' : 'They get'} your skills when ${down.length === 1 ? 'it is' : 'they are'} back. Nothing else to do.${checked}`
  } else if (on.length) {
    text = on.length === 1 ? `This server and ${on[0].name} have the same skills` : `This server and all ${on.length} machines have the same skills`
    sub = <><span className="sk-hide-sm">A change here goes to the machines, a change there comes back.</span>{checked}</>
  }
  return <section className="sk-sync">
    <div className="sk-sync-top">
      <span className={`sk-sync-ico ${kind}`}>{kind === 'busy' ? <Spinner size={16} /> : <Icon name={kind === 'look' ? 'alert' : 'check'} size={17} />}</span>
      <div className="grow">
        <div className="sk-sync-title" role="status">{text}</div>
        <div className="muted small">{sub}</div>
      </div>
      {sync.hosts.length > 0 && <button type="button" className="btn sm" onClick={onMachines}><Icon name="server" size={13} />Machines</button>}
      {on.length > 0 && <button type="button" className="btn ghost icon-only sm" aria-label="Sync now" title="Check every machine now" disabled={sync.running} onClick={onSync}><Icon name="refresh" size={14} /></button>}
    </div>
    {prog && <div className="sk-progress" role="progressbar" aria-valuemin={0} aria-valuemax={prog.total} aria-valuenow={prog.done} aria-label="Machines done"><i style={{ width: `${Math.max(4, Math.round((prog.done / prog.total) * 100))}%` }} /></div>}
    <div className="sk-sync-rows">
      <div className="sk-sync-row">
        <span className="sk-sync-label">Agents</span>
        <div className="sk-chips">{lib.agents.map(a => <span key={a.id} className="sk-chip" title={`${a.name}: ${a.how}`}><span className={`prov-glyph ${a.id}`}><Icon name={a.id} size={13} /></span>{a.name}</span>)}</div>
      </div>
      {sync.hosts.length > 0 && <div className="sk-sync-row">
        <span className="sk-sync-label">Machines</span>
        <div className="sk-chips">
          <span className="sk-chip" title={lib.dir}><i className="sk-dot ok" />This server</span>
          {sync.hosts.map(h => {
            const bad = Object.values(h.skills).filter(s => s.state === 'differs' || s.state === 'failed').length
            const state = !h.on ? 'off' : h.error ? 'down' : bad ? 'look' : h.at ? 'ok' : 'wait'
            const title = !h.on ? `${h.name} does not follow the library. Click to change.` : h.busy ? (quietRetry(h) ? 'Did not answer last time. Trying again…' : `${cap(h.busy)}…`)
              : h.error ? `${h.name}: ${h.error}` : bad ? `${bad} to look at` : h.at ? `Same as the library. Answered ${ago(h.at)}.` : 'Not checked yet'
            return <button type="button" key={h.id} className={`sk-chip ${state}`} title={title} onClick={onMachines}>{h.on && h.busy ? <Spinner size={9} /> : <i className={`sk-dot ${state}`} />}{h.name}
              {state === 'off' && <span className="muted"> · off</span>}{state === 'down' && <span className="muted"> · no answer</span>}</button>
          })}
        </div>
      </div>}
    </div>
  </section>
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/** Asked once: which machines should follow the library. Nothing is sent before. */
function FirstTime({ hosts, onDone }: { hosts: SyncHost[]; onDone: (s: SyncStatus) => void }) {
  const { toast } = useToast()
  const [pick, setPick] = useState<Record<number, boolean>>(() => Object.fromEntries(hosts.map(h => [h.id, true])))
  const [busy, setBusy] = useState(false)
  const save = async (ids: number[]) => {
    setBusy(true)
    try { onDone(await api.put<SyncStatus>(`${SYNC}/machines`, { on: ids })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const ids = hosts.filter(h => pick[h.id]).map(h => h.id)
  return <section className="sk-first">
    <h2>Use these skills on your other machines too?</h2>
    <p className="muted small">Pocket AI copies the library to the machines you pick and keeps them the same from then on. Skills a machine already has stay; one that differs from yours waits for your choice.</p>
    <div className="sk-first-list">{hosts.map(h => <label key={h.id} className="sk-first-item">
      <input type="checkbox" checked={!!pick[h.id]} disabled={busy} onChange={e => setPick({ ...pick, [h.id]: e.target.checked })} /><Icon name="server" size={14} />{h.name}</label>)}</div>
    <div className="row">
      <button type="button" className="btn primary" disabled={busy || ids.length === 0} onClick={() => save(ids)}>{busy && <Spinner />}{ids.length === hosts.length ? 'Sync all of them' : ids.length === 1 ? 'Sync 1 machine' : `Sync ${ids.length} machines`}</button>
      <button type="button" className="btn ghost" disabled={busy} onClick={() => save([])}>Not now</button>
    </div>
  </section>
}

type Extra = { extra: HostExtra; hosts: SyncHost[] }

/** Everything about the machines: which follow the library, the skills only one of them has, and what was synced lately. */
function MachinesDialog({ sync, extras, busy: adopting, onAdopt, onClose, onChange }: { sync: SyncStatus; extras: Extra[]; busy: string; onAdopt: (name: string, h: SyncHost) => void; onClose: () => void; onChange: (s: SyncStatus) => void }) {
  const { toast } = useToast()
  const [tab, setTab] = useState<'machines' | 'only' | 'changes'>('machines')
  const [busy, setBusy] = useState(0)
  const toggle = async (h: SyncHost, v: boolean) => {
    setBusy(h.id)
    try {
      const ids = sync.hosts.filter(x => (x.id === h.id ? v : x.on)).map(x => x.id)
      onChange(await api.put<SyncStatus>(`${SYNC}/machines`, { on: ids }))
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(0) }
  }
  return <Modal size="lg" title="Machines" onClose={onClose} footer={<button type="button" className="btn primary" onClick={onClose}>Done</button>}>
    <Tabs value={tab} onChange={setTab} tabs={[
      { id: 'machines', label: 'Follow the library' },
      { id: 'only', label: extras.length ? `Only on a machine · ${extras.length}` : 'Only on a machine' },
      { id: 'changes', label: 'Recent changes' }]} />
    <div className="sk-dialog-body">
      {tab === 'machines' && <>
        <p className="muted small">A machine that is on gets every skill and its changes come back here. Switching one off leaves the skills it has.</p>
        <ul className="sk-machines">{sync.hosts.map(h => {
          const bad = Object.values(h.skills).filter(s => s.state === 'differs' || s.state === 'failed').length
          return <li key={h.id}>
            <Icon name="server" size={15} />
            <div className="grow">
              <div className="sk-machine-name">{h.name}</div>
              <div className={`small ${h.on && h.error && !h.busy ? 'danger-text' : 'muted'}`}>{!h.on ? 'Not following' : h.busy ? (quietRetry(h) ? 'Did not answer last time. Trying again…' : `${cap(h.busy)}…`) : h.error ? `Did not answer: ${h.error}` : bad ? `${bad} to look at` : h.at ? `Same as the library · answered ${ago(h.at)}` : 'Checking…'}
                {h.on && h.extra.length > 0 && <> · <button type="button" className="sk-inline" onClick={() => setTab('only')}>{h.extra.length === 1 ? '1 skill only here' : `${h.extra.length} skills only here`}</button></>}</div>
            </div>
            {busy === h.id ? <Spinner /> : <Switch label={`Sync skills to ${h.name}`} checked={h.on} disabled={!!busy} onChange={v => toggle(h, v)} />}
          </li>
        })}</ul>
      </>}
      {tab === 'only' && <>
        <p className="muted small">Skills one of your machines has that are not in your library. Add one and every machine gets it.</p>
        {extras.length === 0 && <p className="muted small">None: every skill on your machines is in the library.</p>}
        {extras.length > 0 && <ul className="skills-more-list">{extras.map(x => {
          const h = x.hosts[0], key = `${x.extra.name}|${h.id}`
          return <li key={x.extra.name}>
            <div className="grow sk-only">
              <span className="sk-title static">{x.extra.name}</span>
              <span className="sk-desc" title={x.extra.description}>{x.extra.description}</span>
            </div>
            <span className="spec-chip" title={x.hosts.map(m => m.name).join(', ')}>{x.hosts.length === 1 ? h.name : `${x.hosts.length} machines`}</span>
            <button type="button" className="btn sm" disabled={!!adopting} title={`Copy it from ${h.name} into your library. Every machine gets it then.`} onClick={() => onAdopt(x.extra.name, h)}>{adopting === key ? <Spinner /> : <Icon name="plus" size={13} />}Add</button>
          </li>
        })}</ul>}
      </>}
      {tab === 'changes' && <>
        {sync.log.length === 0 && <p className="muted small">Nothing was synced yet.</p>}
        {sync.log.length > 0 && <ul className="skills-more-list sk-log">{sync.log.map((e, i) => <li key={i}>
          <span className="prov-glyph"><Icon name={e.what === 'taken' ? 'download' : e.what === 'removed' ? 'trash' : 'upload'} size={14} /></span>
          <span className="grow">{e.what === 'taken' ? <><b>{e.skill}</b> changed on {e.host}: taken into your library</>
            : e.what === 'removed' ? <><b>{e.skill}</b> removed from {e.host}</>
              : e.what === 'added' ? <><b>{e.skill}</b> put on {e.host}</> : <><b>{e.skill}</b> updated on {e.host}</>}</span>
          <span className="muted xsmall">{ago(e.at)}</span>
        </li>)}</ul>}
      </>}
    </div>
  </Modal>
}

function NewSkill({ onClose, onSaved }: { onClose: () => void; onSaved: (name: string) => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = validSkillName(name) && !!description.trim() && !!body.trim()
  const save = async () => {
    if (!valid || busy) return
    setBusy(true); setError('')
    try { await api.post(LIB, { name, description, body }); onSaved(name) } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <Modal size="lg" title="New skill" onClose={() => { if (!busy) onClose() }} footer={<>
    <button type="button" className="btn" disabled={busy} onClick={onClose}>Cancel</button>
    <button type="button" className="btn primary" disabled={!valid || busy} onClick={save}>{busy && <Spinner />}Create skill</button>
  </>}>
    <div className="stack">
      <Field label="Name" help={<>Lowercase letters, digits and dashes. You run it with <code>/{name || 'name'}</code>.</>}>
        <input autoFocus aria-label="Skill name" disabled={busy} value={name} placeholder="deploy-app" onChange={e => setName(skillSlug(e.target.value))} />
      </Field>
      <Field label="When to use it" help="The agent reads this to decide when the skill applies. Say what it does and when to use it.">
        <textarea {...noGrammarly} aria-label="Skill description" rows={3} disabled={busy} value={description} onChange={e => setDescription(e.target.value)} />
      </Field>
      <Field label="Instructions" help="Markdown. Loaded when the skill is used. You can add more files to the skill afterwards.">
        <textarea {...noGrammarly} className="mono" aria-label="Skill instructions" rows={12} disabled={busy} value={body} placeholder={'# Deploying\n\n1. …'} onChange={e => setBody(e.target.value)} />
      </Field>
      {error && <p className="callout danger" role="alert">{error}</p>}
    </div>
  </Modal>
}
