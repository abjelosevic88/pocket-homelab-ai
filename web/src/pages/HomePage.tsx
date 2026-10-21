// Start a thread: pick the agent and folder, type (or paste a screenshot), go. Recipes one tap away.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { shortPath } from '../lib/format'
import type { ProviderId, Thread } from '../lib/types'
import { RunningList } from '../components/RunningList'
import { AgentPicker, type AgentValue, defaultsFor, ProviderToggle } from '../components/AgentPicker'
import { Composer } from '../components/Composer'
import { FolderButton } from '../components/FolderPicker'
import { Icon } from '../components/icons'
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
  const { user, settings, providers, providersReady, workspaces, recipes, threads, upsertThread, running } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const runRecipe = useRunRecipe()
  const canRun = !!user && (user.role === 'admin' || user.can_run)
  const [choice, setChoice] = useState<Choice | null>(null)
  const inited = useRef(false)

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
        <button type="button" className="btn ghost icon-only" aria-label="Settings" onClick={() => navigate('/settings')}><Icon name="settings" size={17} /></button>
      </header>
      <div className="home-scroll">
        <div className="home-inner">
          <div className="home-hero">
            <img src="/favicon.svg" alt="" width={46} height={46} />
            <h1>{greeting()}{user?.name ? `, ${user.name.split(' ')[0]}` : ''}</h1>
            <p className="muted">What should we check, fix or build on the server?</p>
          </div>

          {needYou.length > 0 && (
            <div className="needyou-bar">
              <Icon name="help-circle" size={16} />
              <span className="grow">{needYou.length === 1 ? `“${needYou[0].title || 'A thread'}” is waiting for your answer` : `${needYou.length} threads are waiting for your answer`}</span>
              <button type="button" className="btn sm" onClick={() => navigate(`/t/${needYou[0].id}`)}>Open</button>
            </div>
          )}

          {!canRun ? (
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

          {canRun && (
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
