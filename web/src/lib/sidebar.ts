// The sidebar menu: which pages it lists and in what order. Each person
// arranges their own in Settings → General → Sidebar (settings.sidebar).
import type { User } from './types'

export interface SidebarItem {
  id: string
  label: string
  icon: string
  /** the page it opens; Favorites and Archive have none (they switch the thread list) */
  path?: string
  /** only for people who may run agents */
  runner?: boolean
}

/** Every menu item, in the default order. */
export const SIDEBAR_ITEMS: SidebarItem[] = [
  { id: 'favorites', label: 'Favorites', icon: 'star' },
  { id: 'briefing', label: 'Briefing', icon: 'sun', path: '/briefing' },
  { id: 'workflows', label: 'Workflows', icon: 'zap', path: '/workflows' },
  { id: 'memory', label: 'Memory', icon: 'bookmark', path: '/memory' },
  { id: 'actions', label: 'Action history', icon: 'list-checks', path: '/actions' },
  { id: 'today', label: 'Today', icon: 'today', path: '/today', runner: true },
  { id: 'recipes', label: 'Recipes', icon: 'zap', path: '/recipes' },
  { id: 'schedules', label: 'Schedules', icon: 'clock', path: '/schedules' },
  { id: 'skills', label: 'Skills', icon: 'sparkles', path: '/skills', runner: true },
  { id: 'history', label: 'On this server', icon: 'history', path: '/history', runner: true },
  { id: 'worktrees', label: 'Worktrees', icon: 'fork', path: '/worktrees', runner: true },
  { id: 'terminal', label: 'Terminal', icon: 'terminal', path: '/term', runner: true },
  { id: 'files', label: 'Files', icon: 'folder', path: '/files', runner: true },
  { id: 'shared', label: 'Shared', icon: 'users', path: '/shared' },
  { id: 'usage', label: 'Usage', icon: 'chart', path: '/usage' },
  { id: 'snippets', label: 'Snippets', icon: 'bookmark', path: '/snippets' },
  { id: 'archive', label: 'Archive', icon: 'archive' },
]

export interface SidebarEntry { item: SidebarItem; on: boolean }

/** The person's menu in their order, with what they removed (on: false).
 * Items they never arranged (new in a later version) keep their default spot. */
export function sidebarLayout(saved: string[] | undefined, user: User | null | undefined): SidebarEntry[] {
  const runner = !user || user.role === 'admin' || !!user.can_run
  const byId = new Map(SIDEBAR_ITEMS.map(i => [i.id, i]))
  const out: SidebarEntry[] = []
  for (const raw of saved || []) {
    const id = raw.replace(/^-/, '')
    const item = byId.get(id)
    if (item && !out.some(e => e.item.id === id)) out.push({ item, on: !raw.startsWith('-') })
  }
  SIDEBAR_ITEMS.forEach((item, i) => {
    if (out.some(e => e.item.id === item.id)) return
    // after the nearest item that comes before it by default
    let at = 0
    for (let j = i - 1; j >= 0; j--) {
      const k = out.findIndex(e => e.item.id === SIDEBAR_ITEMS[j].id)
      if (k >= 0) { at = k + 1; break }
    }
    out.splice(at, 0, { item, on: true })
  })
  return out.filter(e => runner || !e.item.runner)
}

/** What settings.sidebar stores for a layout ([] = the default menu). */
export function sidebarSetting(layout: SidebarEntry[]): string[] {
  const ids = layout.map(e => (e.on ? '' : '-') + e.item.id)
  const def = sidebarLayout([], null).filter(e => layout.some(x => x.item.id === e.item.id)).map(e => e.item.id)
  return ids.join(',') === def.join(',') ? [] : ids
}

/** The palette's Archive command asks the sidebar to show the archive. */
export const SHOW_ARCHIVE = 'pai:show-archive'
/** …and Favorites the favorite threads. */
export const SHOW_FAVORITES = 'pai:show-favorites'
