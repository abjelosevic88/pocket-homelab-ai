import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from './api'
import { openSSE } from './sse'
import type { AuthStatus, Limits, Provider, Recipe, Settings, SettingsResponse, Thread, User, Workspace } from './types'

export type ThemeMode = 'system' | 'light' | 'dark'

interface AppState {
  status: AuthStatus | null
  user: User | null
  sr: SettingsResponse | null
  settings: Settings | null
  providers: Provider[]
  /** GET /providers answered (or failed) at least once. */
  providersReady: boolean
  threads: Thread[] | null
  workspaces: Workspace[]
  recipes: Recipe[]
  limits: Limits
  /** The thread open on screen (no push/browser notification for it). */
  viewing: string | null
  setViewing: (id: string | null) => void
  live: boolean
  themeMode: ThemeMode
  theme: 'light' | 'dark'
  setThemeMode: (m: ThemeMode) => void
  refresh: () => Promise<void>
  reloadThreads: () => Promise<void>
  reloadWorkspaces: () => Promise<void>
  reloadRecipes: () => Promise<void>
  reloadProviders: () => Promise<void>
  upsertThread: (t: Thread) => void
  dropThread: (id: string) => void
  saveSettings: (patch: Partial<Settings> & { notify_token?: string }) => Promise<SettingsResponse>
  setUser: (u: User | null) => void
}

const Ctx = createContext<AppState>(null!)
export function useApp() { return useContext(Ctx) }

const THEME_KEY = 'pai-theme'

export const DEFAULT_SETTINGS: Settings = {
  title: 'Pocket AI', default_provider: 'claude', default_cwd: '',
  claude: { model: '', effort: '', mode: '' }, codex: { model: '', effort: '', mode: '' },
  notify_kind: 'apprise', notify_url: '', notify_finished: true, notify_input: true, notify_failed: true, notify_only_away: true,
  enter_sends: true, show_thinking: false, system_prompt: '',
}

/** GET /settings may answer the bare object or `{settings, …}`; fill defaults either way. */
export function normalizeSettings(raw: unknown): SettingsResponse {
  const r = (raw || {}) as Record<string, unknown>
  const wrapped = r.settings && typeof r.settings === 'object'
  const s = (wrapped ? r.settings : r) as Partial<Settings>
  const settings: Settings = {
    ...DEFAULT_SETTINGS, ...s,
    claude: { ...DEFAULT_SETTINGS.claude, ...(s.claude || {}) },
    codex: { ...DEFAULT_SETTINGS.codex, ...(s.codex || {}) },
  }
  return wrapped ? { ...(r as unknown as SettingsResponse), settings } : { settings, notify_has_token: !!(r as { notify_has_token?: boolean }).notify_has_token }
}

export function sortThreads(list: Thread[]): Thread[] {
  return [...list].sort((a, b) => (Number(b.pinned) - Number(a.pinned)) || (b.activity_at || '').localeCompare(a.activity_at || ''))
}

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [sr, setSr] = useState<SettingsResponse | null>(null)
  const [providers, setProviders] = useState<Provider[]>([])
  const [providersReady, setProvidersReady] = useState(false)
  const [threads, setThreads] = useState<Thread[] | null>(null)
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [recipes, setRecipes] = useState<Recipe[]>([])
  const [limits, setLimits] = useState<Limits>({})
  const [viewing, setViewingRaw] = useState<string | null>(null)
  const viewingRef = useRef<string | null>(null)
  const setViewing = useCallback((id: string | null) => { viewingRef.current = id; setViewingRaw(id) }, [])
  const [live, setLive] = useState(false)
  const [themeMode, setThemeModeRaw] = useState<ThemeMode>(() => (localStorage.getItem(THEME_KEY) as ThemeMode) || 'system')
  const [systemDark, setSystemDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches)

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const h = () => setSystemDark(mq.matches)
    mq.addEventListener('change', h)
    return () => mq.removeEventListener('change', h)
  }, [])
  const theme: 'light' | 'dark' = themeMode === 'system' ? (systemDark ? 'dark' : 'light') : themeMode
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    document.querySelectorAll('meta[name="theme-color"]').forEach(m => m.setAttribute('content', theme === 'dark' ? '#111113' : '#ffffff'))
  }, [theme])
  const setThemeMode = useCallback((m: ThemeMode) => { localStorage.setItem(THEME_KEY, m); setThemeModeRaw(m) }, [])

  const settings = sr?.settings ?? null

  const reloadThreads = useCallback(async () => { setThreads(sortThreads((await api.get<Thread[]>(`${V1}/threads`)) || [])) }, [])
  const reloadWorkspaces = useCallback(async () => { setWorkspaces((await api.get<Workspace[]>(`${V1}/workspaces`).catch(() => [])) || []) }, [])
  const reloadRecipes = useCallback(async () => { setRecipes((await api.get<Recipe[]>(`${V1}/recipes`).catch(() => [])) || []) }, [])
  const reloadProviders = useCallback(async () => { setProviders((await api.get<Provider[]>(`${V1}/providers`).catch(() => [])) || []); setProvidersReady(true) }, [])

  const refresh = useCallback(async () => {
    const st = await api.get<AuthStatus>(`${V1}/auth/status`)
    const norm: AuthStatus = { ...st, needs_setup: !!(st.needs_setup ?? st.setup_needed) }
    let u = st.user
    if (!u && st.authenticated !== false && !norm.needs_setup) u = await api.get<User>(`${V1}/auth/me`).catch(() => null)
    norm.user = u
    norm.authenticated = !!u
    setStatus(norm)
    setUser(u)
    if (u) {
      const [s] = await Promise.all([
        api.get<unknown>(`${V1}/settings`).then(normalizeSettings),
        reloadThreads().catch(() => setThreads([])),
        reloadWorkspaces(),
        reloadRecipes(),
        reloadProviders(),
      ])
      setSr(s)
    }
  }, [reloadThreads, reloadWorkspaces, reloadRecipes, reloadProviders])

  useEffect(() => {
    refresh().catch(() => setStatus({ needs_setup: false, auth_mode: 'local', authenticated: false, user: null, version: '', title: 'Pocket AI' }))
  }, [refresh])
  useEffect(() => {
    const h = () => { setUser(null); setStatus(s => s ? { ...s, authenticated: false, user: null } : s) }
    window.addEventListener('pai:unauthorized', h)
    return () => window.removeEventListener('pai:unauthorized', h)
  }, [])
  useEffect(() => { if (settings?.title) document.title = settings.title }, [settings?.title])

  const upsertThread = useCallback((t: Thread) => {
    setThreads(list => {
      if (!list) return list
      if (t.archived) return list.filter(x => x.id !== t.id)
      const i = list.findIndex(x => x.id === t.id)
      const next = i >= 0 ? list.map(x => x.id === t.id ? t : x) : [t, ...list]
      return sortThreads(next)
    })
  }, [])
  const dropThread = useCallback((id: string) => setThreads(list => list ? list.filter(x => x.id !== id) : list), [])

  // Global live updates: the list, unread/waiting badges and browser notifications.
  const prevStatus = useRef<Map<string, string>>(new Map())
  useEffect(() => { for (const t of threads || []) if (!prevStatus.current.has(t.id)) prevStatus.current.set(t.id, t.status) }, [threads])
  const settingsRef = useRef(settings)
  settingsRef.current = settings
  useEffect(() => {
    if (!user) return
    const close = openSSE(`${V1}/events`, {
      thread: d => {
        const t = d as Thread
        if (!t?.id) return
        const before = prevStatus.current.get(t.id)
        prevStatus.current.set(t.id, t.status)
        upsertThread(t)
        if (before && before !== t.status) notifyBrowser(t, before, viewingRef.current, settingsRef.current)
      },
      thread_removed: () => { reloadThreads().catch(() => {}) },
      limits: d => setLimits((d || {}) as Limits),
      ping: () => {},
    }, { onOpen: () => { setLive(true); reloadThreads().catch(() => {}) }, onDown: () => setLive(false) })
    return () => { close(); setLive(false) }
  }, [user, upsertThread, reloadThreads])

  // App badge (installed PWA) = threads that need you.
  useEffect(() => {
    const n = (threads || []).filter(t => t.status === 'waiting' || t.unread).length
    const nav = navigator as Navigator & { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> }
    if (n) nav.setAppBadge?.(n).catch(() => {}); else nav.clearAppBadge?.().catch(() => {})
  }, [threads])

  const saveSettings = useCallback(async (patch: Partial<Settings> & { notify_token?: string }) => {
    setSr(s => s ? { ...s, settings: { ...s.settings, ...patch } } : s)
    try {
      const r = normalizeSettings(await api.put<unknown>(`${V1}/settings`, patch))
      setSr(prev => ({ ...prev, ...r }))
      return r
    } catch (e) {
      setSr(normalizeSettings(await api.get<unknown>(`${V1}/settings`)))
      throw e
    }
  }, [])

  const value = useMemo<AppState>(() => ({
    status, user, sr, settings, providers, providersReady, threads, workspaces, recipes, limits, viewing, setViewing, live, themeMode, theme, setThemeMode, refresh,
    reloadThreads, reloadWorkspaces, reloadRecipes, reloadProviders, upsertThread, dropThread, saveSettings, setUser,
  }), [status, user, sr, settings, providers, providersReady, threads, workspaces, recipes, limits, viewing, setViewing, live, themeMode, theme, setThemeMode, refresh, reloadThreads, reloadWorkspaces, reloadRecipes, reloadProviders, upsertThread, dropThread, saveSettings])

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

/** A browser notification when a thread you are not looking at finishes or needs you. */
function notifyBrowser(t: Thread, before: string, viewing: string | null, s: Settings | null) {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return
  if (viewing === t.id && !document.hidden) return
  let body = ''
  if (t.status === 'waiting') { if (s && !s.notify_input) return; body = 'Needs your answer' }
  else if (t.status === 'error') { if (s && !s.notify_failed) return; body = t.error || 'Stopped with an error' }
  else if (t.status === 'idle' && before === 'running') { if (s && !s.notify_finished) return; body = t.preview || 'Finished' }
  else return
  try {
    const n = new Notification(t.title || 'Pocket AI', { body: body.slice(0, 180), tag: `pai-${t.id}`, icon: '/icon-192.png' })
    n.onclick = () => { window.focus(); window.location.assign(`/t/${t.id}`); n.close() }
  } catch { /* iOS without a home-screen install has no Notification constructor */ }
}
