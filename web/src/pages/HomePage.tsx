import { Select } from '../components/Select'
// Start a thread: pick the agent and folder, type (or paste a screenshot), go. Recipes one tap away.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { shortPath } from '../lib/format'
import type { ProviderId, Thread } from '../lib/types'
import { RunningList } from '../components/RunningList'
import { accountOptions, AgentPicker, type AgentValue, defaultsFor, ProviderToggle } from '../components/AgentPicker'
import { Composer, type ComposerHandle } from '../components/Composer'
import { BranchButton } from '../components/BranchPicker'
import { FolderButton } from '../components/FolderPicker'
import { Icon } from '../components/icons'
import { AskButton } from '../components/AskButton'
import { useRunRecipe } from '../components/RecipeRunner'
import { ThreadRow } from '../components/Sidebar'
import { errMsg, Loading, useContextMenu, useToast } from '../components/ui'
import { embedded } from '../lib/embed'
import { useThreadMenu } from '../components/ThreadMenu'

const LAST = 'pai-new-thread'

type Choice = AgentValue & { cwd: string }

/** A conversation another Pocket app prepared (?draft=<id>): the person reads it and presses send. */
interface LinkDraft {
  id: string; provider: ProviderId | ''; account_id: number | null; model: string; effort: string; mode: string; cwd: string
  title: string; text: string; system: string; source: string; ref: string; ref_url: string; ref_label: string; thread_id: string | null
}

function greeting() {
  const h = new Date().getHours()
  return h < 5 ? 'Up late?' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

export default function HomePage({ onMenu }: { onMenu?: () => void }) {
  const { status, user, settings, providers, providersReady, accounts, workspaces, recipes, threads, upsertThread, running } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const ctx = useContextMenu()
  const tm = useThreadMenu()
  const runRecipe = useRunRecipe()
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  const [choice, setChoice] = useState<Choice | null>(null)
  const inited = useRef(false)
  const [params, setParams] = useSearchParams()
  const draftId = params.get('draft')
  const [draft, setDraft] = useState<LinkDraft | null>(null)
  const draftApplied = useRef('')
  const serverComposer = useRef<ComposerHandle>(null)
  const [picked, setMode] = useState<'apps' | 'server'>('apps')
  // Ask my apps reads the person's Pocket apps through Pocket Core: without a Core it stays visible but greyed out
  const coreOn = !!status?.core_configured
  const mode = coreOn ? picked : 'server'
  const [askModel, setAskModel] = useState('sonnet')
  const [askAccount, setAskAccount] = useState<number | null>(null) // null = my default Claude account
  const askAccounts = accountOptions(accounts, 'claude')
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
    try { saved = JSON.parse(localStorage.getItem(LAST) || '{}') } catch { /* ignore */ }
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
    })
  }, [settings, providers, providersReady])
  // a prepared conversation never becomes the person's "last choice"
  useEffect(() => { if (choice && !draftId) localStorage.setItem(LAST, JSON.stringify(choice)) }, [choice, draftId])

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
      setDraft(d)
    }).catch(e => { if (live) { toast(errMsg(e), 'err'); setParams({}, { replace: true }) } })
    return () => { live = false }
  }, [draftId]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!draft || !settings || !providersReady || draftApplied.current === draft.id) return
    draftApplied.current = draft.id
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
    })
  }, [draft, settings, providers, providersReady]) // eslint-disable-line react-hooks/exhaustive-deps
  const dropDraft = () => { if (draft) sessionStorage.removeItem('pai-draft-link-' + draft.id); setParams({}, { replace: true }) }

  const pinned = useMemo(() => recipes.filter(r => r.pinned).sort((a, b) => a.sort_order - b.sort_order || b.runs - a.runs).slice(0, 8), [recipes])
  const recent = (threads || []).slice(0, 6)
  const needYou = (threads || []).filter(t => t.status === 'waiting' || t.pending > 0)

  const send = async (text: string, uploadIds: string[]) => {
    if (!choice) return false
    const ws = workspaces.find(w => w.path === choice.cwd)
    try {
      const t = await api.post<Thread>(draft ? `${V1}/drafts/${draft.id}/start` : `${V1}/threads`, {
        provider: choice.provider, model: choice.model || undefined, effort: choice.effort || undefined, mode: choice.mode || undefined,
        speed: choice.speed ?? undefined,
        account_id: accounts.some(a => a.provider === choice.provider && a.id === choice.account_id && a.signed_in) ? choice.account_id : undefined,
        cwd: choice.cwd || undefined, workspace_id: ws?.id, text, upload_ids: uploadIds,
      })
      upsertThread(t)
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
        <Composer ref={serverComposer} big autoFocus draftKey={`link-${draft.id}`} onSend={send}
          placeholder={`Ask ${choice.provider === 'codex' ? 'Codex' : 'Claude'} about ${draft.ref || 'this'}…`}
          footer={<AgentPicker compact showProvider={false} value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} />} />
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
            <button type="button" aria-pressed={mode === 'apps'} aria-disabled={!coreOn || undefined} title={coreOn ? undefined : 'Needs Pocket Core'} onClick={() => coreOn && setMode('apps')}><Icon name="sparkles" size={15} />Ask my apps</button>
            <button type="button" aria-pressed={mode === 'server'} onClick={() => setMode('server')}><Icon name="terminal" size={15} />Work on the server</button>
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
                  <BranchButton cwd={choice.cwd} />
                  <FolderButton value={choice.cwd} onChange={cwd => setChoice(c => c && { ...c, cwd })} className="btn sm" />
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
              <Composer ref={serverComposer} big autoFocus draftKey={draft ? `link-${draft.id}` : 'new'} onSend={send}
                placeholder={`Ask ${choice.provider === 'codex' ? 'Codex' : 'Claude'} anything${choice.cwd ? ` in ${shortPath(choice.cwd)}` : ''}… paste a screenshot, type / for recipes`}
                footer={<AgentPicker compact showProvider={false} value={choice} onChange={patch => setChoice(c => c && { ...c, ...patch })} />} />
              {providers.length > 0 && !providers.some(p => p.available) && (
                <div className="callout danger mt-s"><Icon name="alert" size={15} /><span>Neither Claude Code nor Codex is available on the server. {providers.map(p => p.error).filter(Boolean).join(' ')}</span></div>
              )}
            </>
          )}

          <RunningList />

          {canRun && mode === 'server' && (
            <section className="home-sec">
              <div className="home-sec-h"><h2>Recipes</h2><button type="button" className="link-btn small" onClick={() => navigate('/recipes')}>{pinned.length ? 'All recipes' : 'Add some'}</button></div>
              {pinned.length === 0 ? (
                <button type="button" className="recipe-empty" onClick={() => navigate('/recipes')}>
                  <Icon name="zap" size={18} /><span><b>One-tap prompts</b><br /><span className="muted small">“Check the server”, “Why is &lt;app&gt; down?”, “Disk space report” — save them once, run them from here.</span></span>
                </button>
              ) : (
                <div className="recipe-grid">
                  {pinned.map(r => (
                    <button key={r.id} type="button" className="recipe-card" onClick={() => runRecipe(r)} title={r.prompt}>
                      <span className="recipe-ico">{r.icon || '✦'}</span>
                      <span className="recipe-name">{r.name}</span>
                      <span className="recipe-sub">{r.prompt}</span>
                    </button>
                  ))}
                </div>
              )}
            </section>
          )}

          {recent.length > 0 && (
            <section className="home-sec">
              <div className="home-sec-h"><h2>Recent</h2></div>
              <div className="home-recent">
                {recent.map(t => <ThreadRow key={t.id} t={t} active={false} onOpen={() => navigate(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
                {ctx.menu}{tm.dialogs}
              </div>
            </section>
          )}
        </div>
      </div>
    </div>
  )
}
