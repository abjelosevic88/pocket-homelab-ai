// One skill of the library: its description and instructions, its other
// files, and where it is (which machines have it, which it is kept off).
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, qs } from '../lib/api'
import { ago, fmtBytes, providerName } from '../lib/format'
import { LIB, SYNC, skillSlug, useSkillSync, validSkillName, type LibSkill, type SyncHost, type SyncStatus } from '../lib/skills'
import { Icon } from '../components/icons'
import { Markdown } from '../components/Markdown'
import { Confirm, copyText, download, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, noGrammarly, useToast, type MenuItem } from '../components/ui'

const MAIN = 'SKILL.md'
type FileText = { path: string; size: number; text: boolean; content: string }
type Ask = { kind: 'file' | 'move' | 'rename' | 'duplicate'; value: string; from?: string }

export default function SkillPage({ onMenu }: { onMenu?: () => void }) {
  const name = useParams().name || ''
  const navigate = useNavigate()
  const { toast } = useToast()
  const [skill, setSkill] = useState<LibSkill | null>(null)
  const [error, setError] = useState('')
  const [sel, setSel] = useState(MAIN)
  const [desc, setDesc] = useState('')
  const [body, setBody] = useState('')
  const [file, setFile] = useState<FileText | null>(null)
  const [text, setText] = useState('')
  const [view, setView] = useState<'write' | 'preview'>('write')
  const [busy, setBusy] = useState(false)
  const [ask, setAsk] = useState<Ask | null>(null)
  const [leave, setLeave] = useState<(() => void) | null>(null)
  const [del, setDel] = useState<'skill' | string | null>(null)
  const { sync, setSync, soon } = useSkillSync()

  const take = useCallback((s: LibSkill) => { setSkill(s); setDesc(s.description); setBody(s.body || '') }, [])
  useEffect(() => {
    let live = true
    setSkill(null); setError(''); setSel(MAIN); setFile(null)
    api.get<LibSkill>(`${LIB}/${name}`).then(s => { if (live) take(s) }).catch(e => { if (live) setError(errMsg(e)) })
    return () => { live = false }
  }, [name, take])

  const dirty = !!skill && (sel === MAIN ? desc !== skill.description || body !== (skill.body || '') : !!file?.text && text !== file.content)
  const dirtyRef = useRef(dirty)
  dirtyRef.current = dirty
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => { if (dirtyRef.current) e.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [])
  /** Run something that drops what is typed, after asking when there is anything. */
  const guard = (fn: () => void) => { if (dirty) setLeave(() => fn); else fn() }

  const open = (path: string) => guard(async () => {
    setSel(path); setFile(null); setView('write')
    if (path === MAIN) { if (skill) take(skill); return }
    try {
      const f = await api.get<FileText>(`${LIB}/${name}/file${qs({ path })}`)
      setFile(f); setText(f.content)
    } catch (e) { toast(errMsg(e), 'err'); setSel(MAIN) }
  })
  const valid = sel === MAIN ? !!desc.trim() && !!body.trim() : true
  const save = async () => {
    if (!skill || busy || !dirty || !valid) return
    setBusy(true)
    try {
      if (sel === MAIN) take(await api.put<LibSkill>(`${LIB}/${name}`, { description: desc, body }))
      else {
        const s = await api.put<LibSkill>(`${LIB}/${name}/file${qs({ path: sel })}`, { content: text })
        setSkill({ ...s, body: skill.body }); setFile(f => f && { ...f, content: text, size: new Blob([text]).size })
      }
      toast(onCount(sync) ? 'Saved. Your machines get it in a moment.' : 'Saved')
      soon()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const saveRef = useRef(save)
  saveRef.current = save
  useEffect(() => {
    const key = (e: KeyboardEvent) => { if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') { e.preventDefault(); void saveRef.current() } }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [])

  const submitAsk = async () => {
    if (!ask || !skill || busy) return
    const v = ask.value.trim()
    setBusy(true)
    try {
      if (ask.kind === 'file') {
        const s = await api.put<LibSkill>(`${LIB}/${name}/file${qs({ path: v })}`, { content: '', create: true })
        setSkill({ ...s, body: skill.body }); setAsk(null)
        setSel(v); setFile({ path: v, size: 0, text: true, content: '' }); setText(''); setView('write')
      } else if (ask.kind === 'move') {
        const s = await api.put<LibSkill>(`${LIB}/${name}/file${qs({ path: ask.from })}`, { rename: v })
        setSkill({ ...s, body: skill.body }); setAsk(null)
        if (sel === ask.from) { setSel(v); setFile(f => f && { ...f, path: v }) }
      } else if (ask.kind === 'rename') {
        await api.put<LibSkill>(`${LIB}/${name}`, { rename: v, description: skill.description, body: skill.body })
        setAsk(null); toast(`Renamed to ${v}`); navigate(`/skills/${v}`, { replace: true })
      } else {
        await api.post(`${LIB}/${name}/duplicate`, { name: v })
        setAsk(null); toast(`Copied to ${v}`); navigate(`/skills/${v}`)
      }
      soon()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const remove = async () => {
    if (!del || busy) return
    setBusy(true)
    try {
      if (del === 'skill') {
        await api.del(`${LIB}/${name}`)
        toast(`Removed ${name} (kept in the trash folder)`); navigate('/skills', { replace: true })
      } else {
        const s = await api.del<LibSkill>(`${LIB}/${name}/file${qs({ path: del })}`)
        setSkill({ ...s, body: skill?.body })
        if (sel === del) { setSel(MAIN); setFile(null) }
        setDel(null)
      }
      soon()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const keepOn = async (h: SyncHost, v: boolean) => {
    if (!skill) return
    const off = v ? skill.off.filter(id => id !== h.id) : [...skill.off, h.id]
    setSkill({ ...skill, off })
    try { await api.put(`${LIB}/${name}/machines`, { off }); soon() } catch (e) { toast(errMsg(e), 'err'); setSkill(s => s && { ...s, off: skill.off }) }
  }
  const settle = async (h: SyncHost, use: 'library' | 'machine') => {
    if (busy) return
    setBusy(true)
    try {
      setSync(await api.post<SyncStatus>(`${SYNC}/resolve`, { host_id: h.id, name, use }))
      if (use === 'machine') { const s = await api.get<LibSkill>(`${LIB}/${name}`); take(s); setSel(MAIN); setFile(null) }
      toast(use === 'library' ? `${h.name} now has this version` : `Now using the version from ${h.name}`)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }

  if (error) return <div className="page"><Head name={name} onMenu={onMenu} onBack={() => navigate('/skills')} /><div className="page-inner"><div className="callout danger" role="alert">{error === 'not found' ? `There is no skill called ${name} in your library.` : error}</div></div></div>
  if (!skill) return <div className="page"><Head name={name} onMenu={onMenu} onBack={() => navigate('/skills')} /><Loading /></div>

  const files = skill.files || []
  const menu: MenuItem[] = [
    { label: `Copy /${name}`, icon: 'copy', onSelect: async () => { if (await copyText(`/${name}`)) toast(`Copied /${name}`) } },
    { label: 'Copy its folder path', icon: 'folder', onSelect: async () => { if (await copyText(skill.path)) toast('Copied the path') } },
    'sep',
    { label: 'Rename', icon: 'edit', onSelect: () => guard(() => setAsk({ kind: 'rename', value: name })) },
    { label: 'Duplicate', icon: 'copy', onSelect: () => setAsk({ kind: 'duplicate', value: skillSlug(`${name}-copy`) }) },
    { label: 'Download as .zip', icon: 'download', onSelect: () => download(`${LIB}/${name}/download`) },
    'sep',
    { label: 'Delete skill', icon: 'trash', danger: true, onSelect: () => setDel('skill') },
  ]
  const fileMenu = (path: string): MenuItem[] => [
    { label: 'Rename or move', icon: 'edit', onSelect: () => guard(() => setAsk({ kind: 'move', value: path, from: path })) },
    { label: 'Download', icon: 'download', onSelect: () => download(`${LIB}/${name}/file${qs({ path, raw: 1 })}`) },
    'sep',
    { label: 'Delete file', icon: 'trash', danger: true, onSelect: () => setDel(path) },
  ]
  const hosts = sync?.hosts || []
  const askValid = !!ask && (ask.kind === 'rename' || ask.kind === 'duplicate' ? validSkillName(ask.value) && ask.value !== name : validFile(ask.value) && ask.value !== ask.from)

  return <div className="page skill-page">
    <Head name={name} onMenu={onMenu} onBack={() => guard(() => navigate('/skills'))} actions={<>
      {dirty && <span className="muted small sk-hide-sm">Not saved</span>}
      <button type="button" className="btn primary" disabled={!dirty || !valid || busy} onClick={save} title="Save (⌘S)">{busy ? <Spinner /> : <Icon name="check" size={14} />}Save</button>
      <MenuButton label={`More for ${name}`} align="right" width={220} items={menu}><Icon name="more" size={17} /></MenuButton>
    </>} />
    <div className="page-scroll"><div className="sk-detail">
      <aside className="sk-side">
        <section>
          <h2 className="sk-h">Files <span className="muted">{files.length + 1}</span>
            <button type="button" className="btn ghost icon-only sm" aria-label="Add a file" title="Add a file" onClick={() => guard(() => setAsk({ kind: 'file', value: 'references/' }))}><Icon name="plus" size={14} /></button></h2>
          <ul className="sk-files">
            <li className={sel === MAIN ? 'active' : ''}><button type="button" className="sk-file" onClick={() => open(MAIN)}><Icon name="sparkles" size={13} /><span className="grow">The skill itself<small>SKILL.md</small></span></button></li>
            {files.map(f => <li key={f.path} className={sel === f.path ? 'active' : ''}>
              <button type="button" className="sk-file" onClick={() => open(f.path)} title={`${f.path} · ${fmtBytes(f.size)}`}><Icon name="file" size={13} /><span className="grow">{base(f.path)}{dir(f.path) && <small>{dir(f.path)}</small>}</span></button>
              <MenuButton label={`More for ${f.path}`} className="btn ghost icon-only sm" align="right" width={190} items={() => fileMenu(f.path)}><Icon name="more" size={14} /></MenuButton>
            </li>)}
          </ul>
          <p className="muted xsmall sk-side-note">Extra files (references, scripts) are read by the agent only when the instructions point to them.</p>
        </section>
        <section>
          <h2 className="sk-h">Where it is</h2>
          <ul className="sk-where">
            <li><i className="sk-dot ok" /><div className="grow"><div>This server</div><div className="muted xsmall" title={skill.path}>The library · changed {ago(skill.modified)}</div></div></li>
            {hosts.map(h => {
              const st = h.skills[name], kept = !skill.off.includes(h.id)
              const state = !h.on ? 'off' : !kept ? 'off' : h.error ? 'down' : !st ? 'wait' : st.state === 'same' ? 'ok' : st.state === 'off' ? 'off' : 'look'
              const label = !h.on ? 'Does not follow the library' : !kept ? 'Kept off this machine' : h.error ? 'Did not answer' : !st ? (sync?.running ? 'Syncing…' : 'Not checked yet')
                : st.state === 'same' ? 'The same' : st.state === 'off' ? 'Kept off this machine' : st.state === 'failed' ? st.error || 'Could not be sent'
                  : st.where && st.where !== 'lib' ? `${providerName(st.agent || 'claude')} has its own copy` : 'A different version'
              return <li key={h.id}>
                <i className={`sk-dot ${state}`} />
                <div className="grow">
                  <div>{h.name}</div>
                  <div className={`xsmall ${state === 'look' ? 'sk-warn' : 'muted'}`} title={h.error}>{label}</div>
                  {h.on && kept && st?.state === 'differs' && <div className="row wrap sk-where-actions">
                    <button type="button" className="btn sm" disabled={busy} onClick={() => settle(h, 'library')}>Send this one</button>
                    <button type="button" className="btn sm" disabled={busy} onClick={() => guard(() => settle(h, 'machine'))}>Use theirs</button>
                  </div>}
                  {h.on && kept && st?.state === 'failed' && <div className="row wrap sk-where-actions"><button type="button" className="btn sm" disabled={busy} onClick={() => settle(h, 'library')}>Send again</button></div>}
                </div>
                {h.on && <Switch size="sm" label={`Keep ${name} on ${h.name}`} checked={kept} onChange={v => keepOn(h, v)} />}
              </li>
            })}
          </ul>
          {hosts.length === 0 && <p className="muted xsmall sk-side-note">Add machines in Settings → Machines to have it there too.</p>}
        </section>
      </aside>

      <main className="sk-edit">
        {sel === MAIN ? <>
          <Field label="When to use it" help="The agent reads this to decide when the skill applies. Say what it does and when to use it.">
            <textarea {...noGrammarly} aria-label="Skill description" rows={4} disabled={busy} value={desc} onChange={e => setDesc(e.target.value)} />
          </Field>
          <div className="sk-edit-head">
            <span className="sk-label">Instructions</span>
            <span className="grow" />
            <Segmented label="View" size="sm" value={view} onChange={setView} options={[{ value: 'write', label: 'Write' }, { value: 'preview', label: 'Preview' }]} />
          </div>
          {view === 'write'
            ? <textarea {...noGrammarly} className="mono sk-text" aria-label="Skill instructions" spellCheck={false} disabled={busy} value={body} onChange={e => setBody(e.target.value)} />
            : <div className="sk-preview"><Markdown text={body} /></div>}
        </> : !file ? <Loading /> : !file.text ? <div className="sk-binary">
          <Icon name="file" size={22} />
          <p><b>{base(sel)}</b> · {fmtBytes(file.size)}</p>
          <p className="muted small">This file is not text, or is too large to edit here. It is copied to your machines with the skill.</p>
          <button type="button" className="btn" onClick={() => download(`${LIB}/${name}/file${qs({ path: sel, raw: 1 })}`)}><Icon name="download" size={14} />Download</button>
        </div> : <>
          <div className="sk-edit-head">
            <span className="sk-label mono">{sel}</span>
            <span className="grow" />
            {/\.(md|markdown)$/i.test(sel) && <Segmented label="View" size="sm" value={view} onChange={setView} options={[{ value: 'write', label: 'Write' }, { value: 'preview', label: 'Preview' }]} />}
          </div>
          {view === 'preview' && /\.(md|markdown)$/i.test(sel)
            ? <div className="sk-preview"><Markdown text={text} /></div>
            : <textarea {...noGrammarly} className="mono sk-text tall" aria-label={`Contents of ${sel}`} spellCheck={false} disabled={busy} value={text} onChange={e => setText(e.target.value)} />}
        </>}
      </main>
    </div></div>

    {ask && <Modal title={ask.kind === 'file' ? 'Add a file' : ask.kind === 'move' ? 'Rename or move the file' : ask.kind === 'rename' ? 'Rename the skill' : 'Duplicate the skill'} onClose={() => { if (!busy) setAsk(null) }} footer={<>
      <button type="button" className="btn" disabled={busy} onClick={() => setAsk(null)}>Cancel</button>
      <button type="button" className="btn primary" disabled={!askValid || busy} onClick={submitAsk}>{busy && <Spinner />}{ask.kind === 'file' ? 'Add file' : ask.kind === 'duplicate' ? 'Duplicate' : 'Rename'}</button>
    </>}>
      <Field label={ask.kind === 'file' || ask.kind === 'move' ? 'File name' : 'Name'} help={ask.kind === 'file' || ask.kind === 'move' ? <>A folder in front keeps things tidy, e.g. <code>references/api.md</code> or <code>scripts/check.sh</code>.</>
        : ask.kind === 'rename' ? <>Lowercase letters, digits and dashes. You run it with <code>/{ask.value || 'name'}</code> afterwards; the machines follow.</> : 'Lowercase letters, digits and dashes.'}>
        <input autoFocus aria-label="Name" className={ask.kind === 'file' || ask.kind === 'move' ? 'mono' : ''} disabled={busy} value={ask.value}
          onChange={e => setAsk({ ...ask, value: ask.kind === 'rename' || ask.kind === 'duplicate' ? skillSlug(e.target.value) : e.target.value })}
          onKeyDown={e => { if (e.key === 'Enter' && askValid) void submitAsk() }} />
      </Field>
    </Modal>}
    {leave && <Confirm title="Leave without saving?" confirmLabel="Discard changes" message="What you typed since the last save is lost." onCancel={() => setLeave(null)} onConfirm={() => { const fn = leave; setLeave(null); fn() }} />}
    {del && <Confirm title={del === 'skill' ? `Delete “${name}”?` : `Delete ${del}?`} confirmLabel="Delete"
      message={busy ? 'Deleting…' : del === 'skill' ? `Every agent stops using it${onCount(sync) ? ', on your machines too' : ''}. Its folder moves to the trash folder (~/.agents/.pocket-ai-trash), so you can bring it back.` : 'The file is removed from the skill, on your machines too.'}
      onCancel={() => { if (!busy) setDel(null) }} onConfirm={remove} />}
  </div>
}

const base = (p: string) => p.slice(p.lastIndexOf('/') + 1)
const dir = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '')
const onCount = (s: SyncStatus | null) => (s?.hosts || []).filter(h => h.on).length
const validFile = (p: string) => !!p && !p.startsWith('/') && !p.endsWith('/') && !p.split('/').some(seg => !seg || seg === '.' || seg === '..' || seg === '.git') && p !== MAIN && p.length <= 240

function Head({ name, onMenu, onBack, actions }: { name: string; onMenu?: () => void; onBack: () => void; actions?: ReactNode }) {
  return <header className="page-head sk-head">
    {onMenu && <button type="button" className="btn ghost icon-only list-menu" aria-label="Menu" onClick={onMenu}><Icon name="menu" size={19} /></button>}
    <button type="button" className="btn ghost icon-only" aria-label="Back to Skills" title="All skills" onClick={onBack}><Icon name="arrow-left" size={17} /></button>
    <div className="grow">
      <h1>{name}</h1>
      <p className="page-sub">A skill in your library · run it with <code>/{name}</code></p>
    </div>
    <div className="row page-actions">{actions}</div>
  </header>
}
