// The app shell: thread sidebar (a drawer on phones) + the page; palette and shortcuts.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { CommandPalette, type PaletteCommand } from '../components/CommandPalette'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { RecipeRunnerProvider, useRunRecipe } from '../components/RecipeRunner'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { Sidebar } from '../components/Sidebar'
import { TabBar, useTabs } from '../components/TabBar'
import { useBusyFavicon } from '../lib/favicon'
import { embedded, tellParent } from '../lib/embed'
import { modalOpen, useIsMobile, useMediaQuery } from '../components/ui'
import HomePage from './HomePage'
import ThreadPage from './ThreadPage'
import RecipesPage from './RecipesPage'
import SchedulesPage from './SchedulesPage'
import HistoryPage from './HistoryPage'
import SharedPage from './SharedPage'
import TodayPage from './TodayPage'
import UsagePage from './UsagePage'
import WorktreesPage from './WorktreesPage'
import { BriefingPage, MemoryPage, ActionsPage, WorkflowsPage } from './AssistantPages'

function typing(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  return !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)
}

function ShellInner() {
  const { setThemeMode, theme, settings, threads, running, user } = useApp()
  const navigate = useNavigate()
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
  const openState = !open ? '' : open.status === 'waiting' || open.pending > 0 ? 'waiting' : open.status
  useEffect(() => tellParent(loc.pathname, openState), [loc.pathname, openState])
  useEffect(() => { if ((loc.state as { shortcuts?: boolean } | null)?.shortcuts) setShortcuts(true) }, [loc.state])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (mod && e.key.toLowerCase() === 'n' && !e.shiftKey) { e.preventDefault(); navigate('/'); return }
      if (mod || e.altKey || typing(e) || modalOpen()) return
      if (e.key === 'c') { e.preventDefault(); navigate('/') }
      else if (e.key === '?') { e.preventDefault(); setShortcuts(true) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])

  const commands = useMemo<PaletteCommand[]>(() => [
    { id: 'new', label: 'New thread', icon: 'plus', hint: 'C', run: () => navigate('/') },
    ...['briefing', 'memory', 'actions', 'workflows'].map(path => ({ id: path, label: path[0].toUpperCase() + path.slice(1), icon: 'sparkles', run: () => navigate('/' + path) })),
    { id: 'recipes', label: 'Recipes', icon: 'zap', keywords: 'prompts saved', run: () => navigate('/recipes') },
    { id: 'schedules', label: 'Schedules', icon: 'clock', keywords: 'cron timer automatic', run: () => navigate('/schedules') },
    { id: 'history', label: 'On this server — CLI sessions', icon: 'history', keywords: 'import resume history claude codex', run: () => navigate('/history') },
    { id: 'usage', label: 'Usage & limits', icon: 'chart', keywords: 'cost tokens rate limit', run: () => navigate('/usage') },
    { id: 'settings', label: 'Settings', icon: 'settings', run: () => navigate('/settings') },
    { id: 'agents', label: 'Agent defaults', icon: 'bot', keywords: 'model effort settings', run: () => navigate('/settings/agents') },
    { id: 'theme', label: theme === 'dark' ? 'Light theme' : 'Dark theme', icon: theme === 'dark' ? 'sun' : 'moon', run: () => setThemeMode(theme === 'dark' ? 'light' : 'dark') },
    { id: 'keys', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
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
            <Route path="/worktrees" element={<WorktreesPage onMenu={onMenu} />} />
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
