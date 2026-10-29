// The app shell: thread sidebar (a drawer on phones) + the page; palette and shortcuts.
import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { api, V1 } from '../lib/api'
import type { Thread } from '../lib/types'
import { CommandPalette, type PaletteCommand } from '../components/CommandPalette'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { RecipeRunnerProvider, useRunRecipe } from '../components/RecipeRunner'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { Sidebar } from '../components/Sidebar'
import { TabBar, useTabs } from '../components/TabBar'
import { useBusyFavicon } from '../lib/favicon'
import { embedded, tellParent } from '../lib/embed'
import { Loading, modalOpen, useIsMobile, useMediaQuery } from '../components/ui'
import HomePage from './HomePage'
import ThreadPage from './ThreadPage'
import RecipesPage from './RecipesPage'
import SchedulesPage from './SchedulesPage'
import HistoryPage from './HistoryPage'
import SharedPage from './SharedPage'
import TodayPage from './TodayPage'
import UsagePage from './UsagePage'
import SnippetsPage from './SnippetsPage'
import WorktreesPage from './WorktreesPage'
// xterm is big: only loaded when a terminal is opened
const TerminalPage = lazy(() => import('./TerminalPage'))
import { BriefingPage, MemoryPage, ActionsPage, WorkflowsPage } from './AssistantPages'
import { actionFor, useShortcuts } from '../lib/shortcuts'

function typing(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  return !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)
}

function ShellInner() {
  const { setThemeMode, theme, settings, threads, running, user, upsertThread } = useApp()
  const navigate = useNavigate()
  // Installed app: a Pocket AI link opened from elsewhere (Projects' "Open in
  // Pocket AI") focuses this window (manifest launch_handler) and lands here,
  // so it opens as a tab instead of in a second window.
  useEffect(() => {
    const lq = (window as unknown as { launchQueue?: { setConsumer: (f: (p: { targetURL?: string }) => void) => void } }).launchQueue
    if (!lq || embedded) return
    lq.setConsumer(p => {
      if (!p.targetURL) return
      const u = new URL(p.targetURL)
      const path = u.pathname + u.search + u.hash
      if (u.origin === location.origin && path !== location.pathname + location.search + location.hash) navigate(path)
      window.focus()
    })
  }, [navigate])
  const loc = useLocation()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 980px)')
  const drawer = !embedded && (mobile || narrow)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const runRecipe = useRunRecipe()
  const tabsOn = settings?.thread_tabs !== false && !mobile && !embedded
  const tabs = useTabs(tabsOn)
  const mine = (threads || []).filter(t => !t.owner_id || t.owner_id === user?.id)
  useBusyFavicon(
    running.some(r => r.status === 'running') || mine.some(t => t.status === 'running'),
    running.some(r => r.status === 'waiting') || mine.some(t => t.status === 'waiting' || t.pending > 0))
  useEffect(() => setDrawerOpen(false), [drawer, loc.pathname])
  // embedded: the framing app hears the open thread's state as it changes
  const openId = loc.pathname.match(/^\/t\/([^/]+)/)?.[1]
  const open = openId ? (threads || []).find(t => t.id === openId) : undefined
  // background sub-agents or commands still at work after the turn: still working, for the app too
  const openState = !open ? '' : open.status === 'waiting' || open.pending > 0 ? 'waiting' : open.subagents?.length || open.background?.length ? 'running' : open.status
  useEffect(() => tellParent(loc.pathname, openState), [loc.pathname, openState])
  // the framing page (Pocket Projects' panel tab menu) renames the conversation shown here
  useEffect(() => {
    if (!embedded || !openId) return
    const h = (e: MessageEvent) => {
      if (e.source !== window.parent || e.data?.type !== 'pocket-ai-command' || typeof e.data.rename !== 'string') return
      const title = e.data.rename.trim().slice(0, 200)
      if (title) api.patch<Thread>(`${V1}/threads/${openId}`, { title }).then(upsertThread).catch(() => {})
    }
    window.addEventListener('message', h)
    return () => window.removeEventListener('message', h)
  }, [openId, upsertThread])
  useEffect(() => { if ((loc.state as { shortcuts?: boolean } | null)?.shortcuts) setShortcuts(true) }, [loc.state])

  const keys = useShortcuts()
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      const action = actionFor(e, keys)
      const tabAction = action === 'new_tab' || action === 'reopen_tab' || action === 'close_tab' || action === 'prev_tab' || action === 'next_tab'
      if (action && action !== 'stop' && !(tabAction && !tabsOn)) {
        e.preventDefault()
        if (action === 'palette') setPalette(p => !p)
        else if (action === 'new_thread' || action === 'new_tab') navigate('/')
        else if (action === 'reopen_tab') tabs.reopen()
        else if (action === 'close_tab') { if (tabs.active) tabs.close([tabs.active]) }
        else if (action === 'prev_tab') tabs.step(-1)
        else if (action === 'next_tab') tabs.step(1)
        return
      }
      if (mod || e.altKey || typing(e) || modalOpen()) return
      if (e.key === 'c') { e.preventDefault(); navigate('/') }
      else if (e.key === '?') { e.preventDefault(); setShortcuts(true) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate, keys, tabs, tabsOn])

  const commands = useMemo<PaletteCommand[]>(() => [
    { id: 'new', label: 'New thread', icon: 'plus', hint: 'C', run: () => navigate('/') },
    ...['briefing', 'memory', 'actions', 'workflows'].map(path => ({ id: path, label: path[0].toUpperCase() + path.slice(1), icon: 'sparkles', run: () => navigate('/' + path) })),
    { id: 'recipes', label: 'Recipes', icon: 'zap', keywords: 'prompts saved', run: () => navigate('/recipes') },
    { id: 'schedules', label: 'Schedules', icon: 'clock', keywords: 'cron timer automatic', run: () => navigate('/schedules') },
    { id: 'history', label: 'On this server — CLI sessions', icon: 'history', keywords: 'import resume history claude codex', run: () => navigate('/history') },
    { id: 'snippets', label: 'Snippets', icon: 'bookmark', keywords: 'saved text prompts', run: () => navigate('/snippets') },
    { id: 'terminal', label: 'Terminal', icon: 'terminal', keywords: 'shell ssh console bash zsh server machine', run: () => navigate('/terminal') },
    { id: 'usage', label: 'Usage & limits', icon: 'chart', keywords: 'cost tokens rate limit', run: () => navigate('/usage') },
    { id: 'settings', label: 'Settings', icon: 'settings', run: () => navigate('/settings') },
    { id: 'agents', label: 'Agent defaults', icon: 'bot', keywords: 'model effort settings', run: () => navigate('/settings/agents') },
    { id: 'theme', label: theme === 'dark' ? 'Light theme' : 'Dark theme', icon: theme === 'dark' ? 'sun' : 'moon', run: () => setThemeMode(theme === 'dark' ? 'light' : 'dark') },
    { id: 'keys', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
    { id: 'keys-edit', label: 'Change keyboard shortcuts', icon: 'keyboard', keywords: 'keys hotkeys bindings', run: () => navigate('/settings/general#shortcuts') },
  ], [navigate, theme, setThemeMode])

  const openMenu = useCallback(() => setDrawerOpen(true), [])
  const onMenu = drawer ? openMenu : undefined

  return (
    <div className={`app ${drawer ? 'drawer-mode' : ''} ${embedded ? 'embedded' : ''}`}>
      {!embedded && <div className={`sidebar-wrap ${drawerOpen ? 'open' : ''}`}>
        <Sidebar onNavigate={() => setDrawerOpen(false)} onPalette={() => { setDrawerOpen(false); setPalette(true) }} />
      </div>}
      {drawer && drawerOpen && <div className="drawer-backdrop" onClick={() => setDrawerOpen(false)} />}
      <main className="main">
        {tabsOn && <TabBar {...tabs} />}
        <ErrorBoundary where="page" key={loc.pathname.split('/')[1] || 'home'}>
          <Routes>
            <Route path="/" element={<HomePage onMenu={onMenu} />} />
            <Route path="/t/:id" element={<ThreadPage onMenu={onMenu} />} />
            <Route path="/recipes" element={<RecipesPage onMenu={onMenu} />} />
            <Route path="/schedules" element={<SchedulesPage onMenu={onMenu} />} />
            <Route path="/history" element={<HistoryPage onMenu={onMenu} />} />
            <Route path="/shared" element={<SharedPage onMenu={onMenu} />} />
            <Route path="/today" element={<TodayPage onMenu={onMenu} />} />
            <Route path="/briefing" element={<BriefingPage onMenu={onMenu} />} />
            <Route path="/memory" element={<MemoryPage onMenu={onMenu} />} />
            <Route path="/actions" element={<ActionsPage onMenu={onMenu} />} />
            <Route path="/workflows" element={<WorkflowsPage onMenu={onMenu} />} />
            <Route path="/usage" element={<UsagePage onMenu={onMenu} />} />
            <Route path="/snippets" element={<SnippetsPage onMenu={onMenu} />} />
            <Route path="/worktrees" element={<WorktreesPage onMenu={onMenu} />} />
            <Route path="/terminal" element={<Suspense fallback={<Loading />}><TerminalPage onMenu={onMenu} /></Suspense>} />
            <Route path="*" element={<HomePage onMenu={onMenu} />} />
          </Routes>
        </ErrorBoundary>
      </main>
      {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} openThread={id => navigate(`/t/${id}`)} runRecipe={runRecipe} />}
      {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
    </div>
  )
}

export default function Shell() {
  return <RecipeRunnerProvider><ShellInner /></RecipeRunnerProvider>
}
