import { Select } from '../components/Select'
// Start a thread: pick the agent and folder, type (or paste a screenshot), go. Recipes one tap away.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, ApiError, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { shortPath } from '../lib/format'
import type { ProviderId, Thread, WorktreeRepoInfo, WorktreeSpec } from '../lib/types'
import { accountOptions, AgentPicker, type AgentValue, defaultsFor, ProviderToggle } from '../components/AgentPicker'
import { Composer, type ComposerHandle } from '../components/Composer'
import { BranchButton } from '../components/BranchPicker'
import { FolderButton } from '../components/FolderPicker'
import { HostPicker } from '../components/HostPicker'
import { WorktreeChooser } from '../components/Worktree'
import { Icon } from '../components/icons'
import { AskButton } from '../components/AskButton'
import { errMsg, Loading, useToast } from '../components/ui'
import { embedded } from '../lib/embed'
import { folderHistory, restoredFolder } from '../lib/folder-history'

const LAST = 'pai-new-thread'

type Choice = AgentValue & { cwd: string; host_id?: number; worktree?: WorktreeSpec | null }


/** A conversation another Pocket app prepared (?draft=<id>): the person reads it and presses send. */
interface LinkDraft {
  id: string; provider: ProviderId | ''; account_id: number | null; model: string; effort: string; mode: string; cwd: string
  title: string; text: string; system: string; source: string; ref: string; ref_url: string; ref_label: string; thread_id: string | null
  /** files the app attached (the issue's screenshots, documents): they go with the first message */
  uploads?: DraftFile[]
  /** the app asks for a git worktree of its own on this branch (a ticket's) */
  worktree?: { name: string; base?: string } | null
  /** the machine it runs on (Settings → Servers), 0 = this server */
  host_id?: number
}
interface DraftFile { id: string; name: string; mime: string; size: number; url: string }

/** The worktree a prepared conversation asks for, when the app's panel has no worktree
 * chooser: its branch, and the branch a new one starts from (pickable). */
function DraftWorktree({ spec, cwd, host, onChange, onRename }: { spec: WorktreeSpec; cwd: string; host: number; onChange: (v: WorktreeSpec | null) => void; onRename: (from: string, to: string) => void }) {
  const [info, setInfo] = useState<WorktreeRepoInfo | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  // spaces become dashes, like the server does; git's own rules are checked on send
  const clean = (v: string) => v.trim().replace(/\s+/g, '-').replace(/^\/+|\/+$/g, '')
  const bad = (v: string) => !v || v.startsWith('-') || /[~^:?*[\\]|\.\.|\.lock$/.test(v)
  const commit = () => {
    if (editing === null) return
    const to = clean(editing)
    setEditing(null)
    if (!bad(to) && to !== spec.name) { onRename(spec.name || '', to); onChange({ ...spec, name: to }) }
  }
  useEffect(() => {
    let live = true
    api.get<WorktreeRepoInfo>(`${V1}/worktrees/config${qs({ path: cwd, host: host || undefined })}`).then(d => { if (live) setInfo(d) }).catch(() => {})
    return () => { live = false }
  }, [cwd, host])
  // same rule as the server: the repo's base (Worktrees → Setup…), else origin's default branch
  const def = info ? info.config.effective.base || info.default || info.base : ''
  useEffect(() => { if (def && !spec.base) onChange({ ...spec, base: def }) }, [def]) // eslint-disable-line react-hooks/exhaustive-deps
  const mine = info?.branches.filter(b => !b.startsWith('origin/')) || []
  const opts = info ? [...mine, ...info.branches.filter(b => b.startsWith('origin/') && !mine.includes(b.slice(7)))].map(b => ({ value: b, label: b })) : []
  return (
    <div className="draft-files" aria-label="Worktree">
      <div className="draft-files-list">
        <span className="draft-file draft-wt" title="Works in a git worktree of its own on this branch (an existing one for this branch is reused, and then it keeps its own base)">
          <Icon name="fork" size={14} />
          {editing !== null
            ? <input className="draft-wt-name mono" autoFocus value={editing} aria-label="Branch name" aria-invalid={bad(clean(editing)) || undefined} size={Math.max(16, Math.min(editing.length + 1, 60))}
                onChange={e => setEditing(e.target.value)} onBlur={commit}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); commit() } else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setEditing(null) } }} />
            : <button type="button" className="draft-wt-rename" title="Rename the branch" onClick={() => setEditing(spec.name || '')}>
                <span className="ellipsis">Worktree <span className="mono">{spec.name}</span></span><Icon name="edit" size={12} /></button>}
          {opts.length > 0 && <><span className="muted">from</span>
            <Select className="ghost inline" size="sm" label="Start from branch" searchable value={spec.base || def} options={opts} width={280} onChange={b => onChange({ ...spec, base: b })} /></>}
          <button type="button" className="draft-file-x" aria-label="Work in the folder itself" title="Work in the folder itself" onClick={() => onChange(null)}><Icon name="x" size={12} /></button>
        </span>
      </div>
    </div>
  )
}

/** The files a prepared conversation brings along, each removable before sending. */
function DraftFiles({ files, source, onRemove }: { files: DraftFile[]; source: string; onRemove: (id: string) => void }) {
  if (!files.length) return null
  return (
    <div className="draft-files" aria-label="Attached files">
      <span className="muted xsmall draft-files-h"><Icon name="paperclip" size={12} />{files.length === 1 ? 'This file goes' : `These ${files.length} files go`} with your message{source ? ` (from ${source})` : ''}</span>
      <div className="draft-files-list">
        {files.map(f => (
          <span key={f.id} className="draft-file" title={f.name}>
            {f.mime.startsWith('image/') ? <img src={f.url} alt="" /> : <Icon name="file" size={14} />}
            <span className="ellipsis">{f.name}</span>
            <button type="button" className="draft-file-x" aria-label={`Don't send ${f.name}`} title="Don’t send this one" onClick={() => onRemove(f.id)}><Icon name="x" size={12} /></button>
          </span>
        ))}
      </div>
    </div>
  )
}

function greeting() {
  const h = new Date().getHours()
  return h < 5 ? 'Up late?' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

export default function HomePage({ onMenu }: { onMenu?: () => void }) {
  const { status, user, settings, providers, providersReady, accounts, hosts, worktrees, workspaces, threads, upsertThread, running } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  const [choice, setChoice] = useState<Choice | null>(null)
  const inited = useRef(false)
  const folderRevision = useRef(0)
  const lastChoiceKey = `${LAST}:${user?.id ?? 'anonymous'}`
  const [params, setParams] = useSearchParams()
  const draftId = params.get('draft')
  const [draft, setDraft] = useState<LinkDraft | null>(null)
  const [draftFiles, setDraftFiles] = useState<DraftFile[]>([])
  const draftApplied = useRef('')
  const serverComposer = useRef<ComposerHandle>(null)
  const [picked, setMode] = useState<'apps' | 'server'>('server')
  // Ask my apps reads the person's Pocket apps through Pocket Core: without a Core it stays visible but greyed out
  const coreOn = !!status?.core_configured
  const mode = coreOn ? picked : 'server'
  const [askModel, setAskModel] = useState('sonnet')
  const [askAccount, setAskAccount] = useState<number | null>(null) // null = my default Claude account
  const askAccounts = accountOptions(accounts, 'claude', askAccount ?? settings?.claude?.account_id ?? 0)
  const [noMemory, setNoMemory] = useState(false)
  const [connected, setConnected] = useState<boolean | null>(null)
  const appComposer = useRef<ComposerHandle>(null)
  const checkAsk = useCallback(() => { api.get<{ connected: boolean }>(`${V1}/assistant/status`).then(v => setConnected(v.connected)).catch(() => setConnected(false)) }, [])
  useEffect(() => { checkAsk() }, [checkAsk])
  // connecting happens in the Ask panel (Pocket Core): look again when it closes or the tab comes back
  useEffect(() => {
    if (connected !== false) return
    const t = setInterval(checkAsk, 5000)
    window.addEventListener('focus', checkAsk)
    return () => { clearInterval(t); window.removeEventListener('focus', checkAsk) }
  }, [connected, checkAsk])
  const askApps = async (text: string, uploadIds: string[]) => {
    try { const t = await api.post<Thread>(`${V1}/ask/threads`, { text, model: askModel, no_memory: noMemory, upload_ids: uploadIds, account_id: askAccount ?? undefined }); upsertThread(t); navigate(`/t/${t.id}`); return true }
    catch (e) { toast(errMsg(e), 'err'); return false }
  }

  // last choice wins, else the person's defaults
  useEffect(() => {
    if (inited.current || !settings || !providersReady) return
    inited.current = true
    let saved: Partial<Choice> = {}
    try { saved = JSON.parse(localStorage.getItem(lastChoiceKey) || '{}') } catch { /* ignore */ }
    const pid = ((saved.provider && providers.find(p => p.id === saved.provider)?.available) ? saved.provider : (providers.find(p => p.id === settings.default_provider)?.available ? settings.default_provider : providers.find(p => p.available)?.id || settings.default_provider)) as ProviderId
    const p = providers.find(x => x.id === pid)
    const d = defaultsFor(p, settings)
    const same = saved.provider === pid
    setChoice({
      provider: pid,
      model: same && saved.model && p?.models?.some(m => m.id === saved.model) ? saved.model : d.model,
      effort: same && saved.effort ? saved.effort : d.effort,
      mode: same && saved.mode && p?.modes?.some(m => m.id === saved.mode) ? saved.mode : d.mode,
      speed: same && saved.speed !== undefined && (saved.speed === '' || p?.speeds?.some(s => s.id === saved.speed && s.available)) ? saved.speed : d.speed,
      cwd: saved.cwd || settings.default_cwd || '',
      host_id: saved.host_id || 0,
      worktree: saved.worktree?.mode === 'new' ? { mode: 'new' } : null,
    })
    const host = saved.host_id && hosts.some(h => h.id === saved.host_id) ? saved.host_id : 0
    const revision = folderRevision.current
    if (!draftId && !params.get('worktree')) {
      folderHistory(host).then(h => {
        if (folderRevision.current !== revision) return
        setChoice(c => c && { ...c, host_id: host, cwd: restoredFolder(h, host ? '' : settings.default_cwd || '') })
      }).catch(() => {}) // Older/offline servers retain the current choice.
    }
  }, [settings, providers, providersReady])
  // a prepared conversation never becomes the person's "last choice"
  useEffect(() => {
    if (!choice || draftId) return
    // a worktree choice is kept as "on" only: bases and branches belong to one repo
    localStorage.setItem(lastChoiceKey, JSON.stringify({ ...choice, worktree: choice.worktree ? { mode: 'new' } : null }))
  }, [choice, draftId])
  // a saved machine that was removed: back to this server
  const hostId = choice?.host_id && hosts.some(h => h.id === choice.host_id) ? choice.host_id : 0
  // the start page's repository (for the worktree switch): null = still looking, 'no' = not a git repo
  const [repo, setRepo] = useState<WorktreeRepoInfo | null | 'no'>(null)
  // embedded drafts have no chooser to look at the folder: the server checks it on send
  const wtSpec = choice?.worktree && (repo !== 'no' || choice.worktree.mode === 'existing' || (embedded && !!draft)) ? choice.worktree : undefined
  const wtWhere = !wtSpec ? '' : wtSpec.mode === 'existing' ? ` in worktree ${wtSpec.branch}` : wtSpec.mode === 'branch' ? ` in a worktree on ${wtSpec.branch}` : wtSpec.mode === 'pr' ? ` in a worktree of PR #${wtSpec.pr}` : wtSpec.name ? ` in worktree ${wtSpec.name}` : ` in a new worktree from ${wtSpec.base || (repo && repo !== 'no' ? repo.base : 'the default branch')}`
  const pickHost = (id: number) => {
    const revision = ++folderRevision.current
    const fallback = id ? '' : settings?.default_cwd || ''
    setChoice(c => c && { ...c, host_id: id, host_home: '', worktree: c.worktree?.mode === 'new' ? { mode: 'new' } : null, cwd: fallback })
    folderHistory(id).then(h => {
      if (folderRevision.current !== revision) return
      setChoice(c => c && c.host_id === id ? { ...c, cwd: restoredFolder(h, fallback) } : c)
    }).catch(e => toast(`Could not load remembered folders: ${errMsg(e)}`, 'err'))
  }
  // ?worktree=<id>: a new thread in one of my worktrees ("New thread here")
  const wtParam = Number(params.get('worktree')) || 0
  useEffect(() => {
    if (!wtParam || !choice) return
    const w = worktrees.find(x => x.id === wtParam && x.status === 'active')
    if (!w) return
    folderRevision.current++
    setMode('server')
    setChoice(c => c && { ...c, host_id: w.host_id, host_home: c.host_id === w.host_id ? c.host_home : '', cwd: w.path, worktree: { mode: 'existing', id: w.id, branch: w.branch } })
    setParams(p => { p.delete('worktree'); return p }, { replace: true })
  }, [wtParam, !!choice, worktrees]) // eslint-disable-line react-hooks/exhaustive-deps

  // ?draft=<id>: load what the other app prepared (already sent → open that thread)
  useEffect(() => {
    setDraft(null)
    draftApplied.current = ''
    if (!draftId) return
    let live = true
    api.get<LinkDraft>(`${V1}/drafts/${encodeURIComponent(draftId)}`).then(d => {
      if (!live) return
      if (d.thread_id) { navigate(`/t/${d.thread_id}`, { replace: true }); return }
      // the composer reads its slot when its draftKey switches to this draft (an edit in progress survives a reload)
      if (!sessionStorage.getItem('pai-draft-link-' + d.id)) sessionStorage.setItem('pai-draft-link-' + d.id, d.text)
      setDraftFiles(d.uploads || [])
      setDraft(d)
    }).catch(e => { if (live) { toast(errMsg(e), 'err'); setParams({}, { replace: true }) } })
    return () => { live = false }
  }, [draftId]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!draft || !settings || !providersReady || draftApplied.current === draft.id) return
    draftApplied.current = draft.id
    folderRevision.current++
    const pid = (draft.provider && providers.some(p => p.id === draft.provider) ? draft.provider : (choice?.provider || settings.default_provider)) as ProviderId
    const p = providers.find(x => x.id === pid)
    const d = defaultsFor(p, settings)
    setMode('server')
    setChoice({
      provider: pid,
      model: draft.model && p?.models?.some(m => m.id === draft.model) ? draft.model : d.model,
      effort: draft.effort || d.effort,
      mode: draft.mode && p?.modes?.some(m => m.id === draft.mode) ? draft.mode : d.mode,
      speed: d.speed,
      account_id: draft.account_id ?? d.account_id,
      cwd: draft.cwd,
      host_id: draft.host_id || 0,
      worktree: draft.worktree?.name ? { mode: 'new', name: draft.worktree.name, base: draft.worktree.base || undefined } : null,
    })
  }, [draft, settings, providers, providersReady]) // eslint-disable-line react-hooks/exhaustive-deps
  const dropDraft = () => { if (draft) sessionStorage.removeItem('pai-draft-link-' + draft.id); setParams({}, { replace: true }) }

  const needYou = (threads || []).filter(t => t.status === 'waiting' || t.pending > 0)

  // Remote Control for the next conversation: remembered on this device
  const [rcNext, setRcNext] = useState(() => localStorage.getItem('pai-rc-next') === '1')
  const remoteSwitch = { on: rcNext, onToggle: () => setRcNext(v => { localStorage.setItem('pai-rc-next', v ? '0' : '1'); return !v }) }

  const send = async (text: string, uploadIds: string[]) => {
    if (!choice) return false
    if (wtSpec && repo === null && wtSpec.mode !== 'existing' && !(embedded && draft)) { toast('Still checking the folder for the worktree — try again in a second', 'err'); return false }
    const ws = workspaces.find(w => w.path === choice.cwd)
    try {
      const t = await api.post<Thread>(draft ? `${V1}/drafts/${draft.id}/start` : `${V1}/threads`, {
        provider: choice.provider, model: choice.model || undefined, effort: choice.effort || undefined, mode: choice.mode || undefined,
        speed: choice.speed ?? undefined,
        account_id: !hostId && accounts.some(a => a.provider === choice.provider && a.id === choice.account_id && a.signed_in) ? choice.account_id : undefined,
        cwd: choice.cwd || undefined, workspace_id: hostId ? undefined : ws?.id, host_id: hostId || undefined, host_home: hostId ? choice.host_home || undefined : undefined, text, upload_ids: uploadIds,
        worktree: wtSpec,
        draft_uploads: draft ? draftFiles.map(f => f.id) : undefined,
      })
      upsertThread(t)
      if (rcNext && t.provider === 'claude') {
        // the thread page shows how it goes (chip + switch); only failures need a word here
        api.post<Thread>(`${V1}/threads/${t.id}/remote-control`, { on: true }).then(upsertThread, e => toast('Remote Control: ' + errMsg(e), 'err'))
      }
      navigate(`/t/${t.id}`, { replace: !!draft && embedded })
      return true
    } catch (e) {
      if (draft && e instanceof ApiError && e.status === 409 && e.data.thread_id) { navigate(`/t/${e.data.thread_id}`); return true }
      toast(errMsg(e), 'err'); return false
    }
  }

  // Embedded with &send=1 (the other app's "Send right away"): send the prepared prompt once
  const autoSent = useRef('')
  useEffect(() => {
    if (!embedded || params.get('send') !== '1' || !draft || !choice || draftApplied.current !== draft.id || choice.cwd !== draft.cwd || autoSent.current === draft.id) return
    autoSent.current = draft.id
    send(draft.text, []).then(ok => { if (ok) sessionStorage.removeItem('pai-draft-link-' + draft.id); else { autoSent.current = 'failed'; setParams(p => { p.delete('send'); return p }, { replace: true }) } })
  }, [draft, choice]) // eslint-disable-line react-hooks/exhaustive-deps

  const pickProvider = (pid: ProviderId | '') => {
    const p = providers.find(x => x.id === pid)
    setChoice(c => c && pid ? { ...c, provider: pid, ...defaultsFor(p, settings) } : c)
  }

  // Embedded in the app that prepared it: folder, agent and context are chosen there,
  // so only the prompt (filled in, ready to add to and send) and the composer's own chips
  if (embedded && draftId) return (
    <div className="home embed-draft">
      {!draft || !choice || (params.get('send') === '1' && autoSent.current !== 'failed') ? <div className="embed-starting"><Loading label={params.get('send') === '1' ? 'Starting the conversation…' : 'Loading…'} /></div> : (
        <>
        {choice.worktree && <DraftWorktree spec={choice.worktree} cwd={choice.cwd} host={hostId} onChange={wt => setChoice(c => c && { ...c, worktree: wt })} onRename={(from, to) => {
          // the prompt names the branch: it follows the new name
          const text = sessionStorage.getItem('pai-draft-link-' + draft.id) ?? draft.text
          if (from && text.includes(from)) serverComposer.current?.setText(text.split(from).join(to))
        }} />}
        <DraftFiles files={draftFiles} source={draft.ref} onRemove={id => setDraftFiles(f => f.filter(x => x.id !== id))} />
        <Composer ref={serverComposer} big autoFocus draftKey={`link-${draft.id}`} onSend={send} skills={choice?.provider ? { provider: choice.provider, cwd: choice.cwd, account_id: choice.account_id, host_id: hostId } : undefined}
          placeholder={`Ask ${choice.provider === 'codex' ? 'Codex' : 'Claude'} about ${draft.ref || 'this'}${hostId ? ` on ${hosts.find(h => h.id === hostId)?.name}` : ''}…`}
          footer={<AgentPicker compact showProvider={false} host={hostId} value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} remote={remoteSwitch} />} />
        </>
      )}
    </div>
  )

  return (
    <div className="home">
      <header className="home-top">
        {onMenu && <button type="button" className="btn ghost icon-only menu-btn" aria-label="Threads" onClick={onMenu}><Icon name="menu" size={19} />{needYou.length > 0 ? <span className="menu-badge">{needYou.length}</span> : running.length > 0 && <span className="menu-run-dot" />}</button>}
        <span className="grow" />
        <AskButton />
      </header>
      <div className="home-scroll">
        <div className="home-inner">
          <div className="home-hero">
            <img src="/favicon.svg" alt="" width={46} height={46} />
            <h1>{greeting()}{user?.name ? `, ${user.name.split(' ')[0]}` : ''}</h1>
            <p className="muted">{mode === 'apps' ? 'What would you like to know or get done in your apps?' : 'What should we check, fix or build on the server?'}</p>
          </div>

          {needYou.length > 0 && (
            <div className="needyou-bar">
              <Icon name="help-circle" size={16} />
              <span className="grow">{needYou.length === 1 ? `“${needYou[0].title || 'A thread'}” is waiting for your answer` : `${needYou.length} threads are waiting for your answer`}</span>
              <button type="button" className="btn sm" onClick={() => navigate(`/t/${needYou[0].id}`)}>Open</button>
            </div>
          )}

          <div className="home-mode" role="group" aria-label="Assistant mode">
            <button type="button" aria-pressed={mode === 'server'} onClick={() => setMode('server')}><Icon name="terminal" size={15} />Work on the server</button>
            <button type="button" aria-pressed={mode === 'apps'} aria-disabled={!coreOn || undefined} title={coreOn ? undefined : 'Needs Pocket Core'} onClick={() => coreOn && setMode('apps')}><Icon name="sparkles" size={15} />Ask my apps</button>
          </div>
          {!coreOn && <p className="core-hint">Ask my apps needs Pocket Core. Pocket Core connects your Pocket apps to each other and to your services: one sign-in for everyone, each person's own accounts, nothing to copy or paste. Set CORE_URL to connect this app. <a href="https://github.com/abjelosevic88/pocket-homelab-core" target="_blank" rel="noreferrer">Learn more</a></p>}
          {mode === 'apps' ? <>
            {connected === false && <div className="callout info ask-connect"><Icon name="sparkles" size={15} />
              <span className="grow">Ask reads your Pocket apps as you, so connect it once: sign in to Pocket Core in the panel and approve.</span>
              <button type="button" className="btn primary sm" onClick={() => window.dispatchEvent(new CustomEvent('pocket-ask'))}>Connect Ask</button></div>}
            <Composer ref={appComposer} big draftKey="new-apps" onSend={askApps} placeholder="Ask about your money, notes, tasks, documents… or attach a receipt" footer={<div className="app-composer-options">
              <Select className="ghost inline" size="sm" label="App assistant model" value={askModel} onChange={setAskModel} options={[{ value: 'haiku', label: 'Haiku' }, { value: 'sonnet', label: 'Sonnet' }, { value: 'opus', label: 'Opus' }]} />
              {askAccounts.length > 0 && <Select className="ghost inline" size="sm" label="Claude account" value={askAccount ?? settings?.claude?.account_id ?? 0} onChange={setAskAccount} options={askAccounts} width={280}
                renderValue={o => <span className="ellipsis"><Icon name="user" size={12} className="inline-ico" />{o?.label ?? 'Account'}</span>} />}
              <button type="button" className="memory-choice" aria-label="Use saved memory" aria-pressed={!noMemory} title={noMemory ? 'Saved memory is off for this conversation' : 'Use saved preferences in this conversation'} onClick={() => setNoMemory(v => !v)}><Icon name="brain" size={14} /><span>Memory {noMemory ? 'off' : 'on'}</span></button>
            </div>} />
            <div className="app-suggestions" role="group" aria-label="Suggested questions">
              {[
                { icon: 'today', label: 'Today’s priorities', prompt: 'What needs my attention today?' },
                { icon: 'receipt', label: 'Monthly spending', prompt: 'How much did I spend this month?' },
                { icon: 'wrench', label: 'Car expenses', prompt: 'Find my car expenses without counting duplicates' },
                { icon: 'edit', label: 'Make a spending note', prompt: 'Create a spending note in Quick Notes' },
              ].map(({ icon, label, prompt }) => <button type="button" key={prompt} title={prompt} onClick={() => { appComposer.current?.setText(prompt); appComposer.current?.focus() }}><Icon name={icon} size={15} /><span>{label}</span></button>)}
            </div>
            <nav className="home-shortcuts" aria-label="Assistant pages">
              <button type="button" onClick={() => navigate('/briefing')}><Icon name="today" size={14} />My briefing</button>
              <button type="button" onClick={() => navigate('/workflows')}><Icon name="repeat" size={14} />Workflows</button>
              <button type="button" onClick={() => navigate('/memory')}><Icon name="brain" size={14} />Memory</button>
            </nav>
          </> : !canRun ? (
            <div className="callout info"><Icon name="lock" size={15} /><span>Agents run with the server’s own account, so an admin has to allow you first (Settings → Users → “May run agents”). You can still look at usage and set things up.</span></div>
          ) : !choice ? null : (
            <>
              <div className="home-agent">
                <ProviderToggle value={choice.provider} onChange={pickProvider} />
                <div className="home-where">
                  {!draft && <HostPicker value={hostId} onChange={pickHost} />}
                  <WorktreeChooser cwd={choice.cwd} host={hostId} value={choice.worktree || null} onChange={wt => setChoice(c => c && { ...c, worktree: wt })} onRepo={setRepo} />
                  {choice.worktree?.mode !== 'existing' && <BranchButton cwd={choice.cwd} host={hostId} />}
                  <FolderButton value={choice.cwd} host={hostId} onChange={cwd => { folderRevision.current++; setChoice(c => c && { ...c, cwd, worktree: c.worktree?.mode === 'existing' ? null : c.worktree }) }} className="btn sm" />
                </div>
              </div>
              {draft && (
                <div className="callout info">
                  <Icon name="link" size={15} />
                  <span className="grow">From Pocket {draft.source.charAt(0).toUpperCase() + draft.source.slice(1)}
                    {draft.ref && <> · {draft.ref_url ? <a href={draft.ref_url} target="_blank" rel="noreferrer">{draft.ref_label || draft.ref}</a> : (draft.ref_label || draft.ref)}</>}
                    {' '}— review the prompt and press send.{draft.system ? ' Its team instructions and memory go along as hidden context.' : ''}</span>
                  <button type="button" className="btn ghost sm" onClick={dropDraft}>Discard</button>
                </div>
              )}
              {draft && <DraftFiles files={draftFiles} source={draft.ref} onRemove={id => setDraftFiles(f => f.filter(x => x.id !== id))} />}
              <Composer ref={serverComposer} big autoFocus draftKey={draft ? `link-${draft.id}` : 'new'} onSend={send} skills={choice?.provider ? { provider: choice.provider, cwd: choice.cwd, account_id: choice.account_id, host_id: hostId } : undefined}
                placeholder={`Ask ${choice.provider === 'codex' ? 'Codex' : 'Claude'} anything${hostId ? ` on ${hosts.find(h => h.id === hostId)?.name}` : ''}${choice.cwd && !wtSpec ? ` in ${shortPath(choice.cwd)}` : ''}${wtWhere}… paste a screenshot, type / for recipes`}
                footer={<AgentPicker compact showProvider={false} host={hostId} value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} remote={remoteSwitch} />} />
              {providers.length > 0 && !providers.some(p => p.available) && (
                <div className="callout danger mt-s"><Icon name="alert" size={15} /><span>Neither Claude Code nor Codex is available on the server. {providers.map(p => p.error).filter(Boolean).join(' ')}</span></div>
              )}
            </>
          )}

        </div>
      </div>
    </div>
  )
}
