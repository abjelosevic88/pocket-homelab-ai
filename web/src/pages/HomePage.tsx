import { Select } from '../components/Select'
// Start a thread: pick the agent and folder, type (or paste a screenshot), go. Recipes one tap away.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { shortPath } from '../lib/format'
import type { ProviderId, Thread } from '../lib/types'
import { RunningList } from '../components/RunningList'
import { accountOptions, AgentPicker, type AgentValue, defaultsFor, ProviderToggle } from '../components/AgentPicker'
import { Composer, type ComposerHandle } from '../components/Composer'
import { FolderButton } from '../components/FolderPicker'
import { Icon } from '../components/icons'
import { AskButton } from '../components/AskButton'
import { useRunRecipe } from '../components/RecipeRunner'
import { ThreadRow } from '../components/Sidebar'
import { errMsg, useToast } from '../components/ui'

const LAST = 'pai-new-thread'

type Choice = AgentValue & { cwd: string }

function greeting() {
  const h = new Date().getHours()
  return h < 5 ? 'Up late?' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

export default function HomePage({ onMenu }: { onMenu?: () => void }) {
  const { user, settings, providers, providersReady, accounts, workspaces, recipes, threads, upsertThread, running } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const runRecipe = useRunRecipe()
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  const [choice, setChoice] = useState<Choice | null>(null)
  const inited = useRef(false)
  const [mode, setMode] = useState<'apps' | 'server'>('apps')
  const [askModel, setAskModel] = useState('sonnet')
  const [askAccount, setAskAccount] = useState<number | null>(null) // null = my default Claude account
  const askAccounts = accountOptions(accounts, 'claude')
  const [noMemory, setNoMemory] = useState(false)
  const [connected, setConnected] = useState<boolean | null>(null)
  const appComposer = useRef<ComposerHandle>(null)
  useEffect(() => { api.get<{ connected: boolean }>(`${V1}/assistant/status`).then(v => setConnected(v.connected)).catch(() => setConnected(false)) }, [])
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
      model: same && saved.model && p?.models.some(m => m.id === saved.model) ? saved.model : d.model,
      effort: same && saved.effort ? saved.effort : d.effort,
      mode: same && saved.mode && p?.modes.some(m => m.id === saved.mode) ? saved.mode : d.mode,
      speed: same && saved.speed !== undefined && (saved.speed === '' || p?.speeds?.some(s => s.id === saved.speed && s.available)) ? saved.speed : d.speed,
      cwd: saved.cwd || settings.default_cwd || '',
    })
  }, [settings, providers, providersReady])
  useEffect(() => { if (choice) localStorage.setItem(LAST, JSON.stringify(choice)) }, [choice])

  const pinned = useMemo(() => recipes.filter(r => r.pinned).sort((a, b) => a.sort_order - b.sort_order || b.runs - a.runs).slice(0, 8), [recipes])
  const recent = (threads || []).slice(0, 6)
  const needYou = (threads || []).filter(t => t.status === 'waiting' || t.pending > 0)

  const send = async (text: string, uploadIds: string[]) => {
    if (!choice) return false
    const ws = workspaces.find(w => w.path === choice.cwd)
    try {
      const t = await api.post<Thread>(`${V1}/threads`, {
        provider: choice.provider, model: choice.model || undefined, effort: choice.effort || undefined, mode: choice.mode || undefined,
        speed: choice.speed ?? undefined,
        account_id: accounts.some(a => a.provider === choice.provider && a.id === choice.account_id && a.signed_in) ? choice.account_id : undefined,
        cwd: choice.cwd || undefined, workspace_id: ws?.id, text, upload_ids: uploadIds,
      })
      upsertThread(t)
      navigate(`/t/${t.id}`)
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }

  const pickProvider = (pid: ProviderId | '') => {
    const p = providers.find(x => x.id === pid)
    setChoice(c => c && pid ? { ...c, provider: pid, ...defaultsFor(p, settings) } : c)
  }

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
            <button type="button" aria-pressed={mode === 'apps'} onClick={() => setMode('apps')}><Icon name="sparkles" size={15} />Ask my apps</button>
            <button type="button" aria-pressed={mode === 'server'} onClick={() => setMode('server')}><Icon name="terminal" size={15} />Work on the server</button>
          </div>
          {mode === 'apps' ? <>
            {connected === false && <div className="callout info">Connect Ask from any Pocket app first using its Ask button, then return here.</div>}
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
                <FolderButton value={choice.cwd} onChange={cwd => setChoice(c => c && { ...c, cwd })} className="btn sm" />
              </div>
              <Composer big autoFocus draftKey="new" onSend={send}
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
                {recent.map(t => <ThreadRow key={t.id} t={t} active={false} onOpen={() => navigate(`/t/${t.id}`)} />)}
              </div>
            </section>
          )}
        </div>
      </div>
    </div>
  )
}
