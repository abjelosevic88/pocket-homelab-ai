// Threads, grouped by time, with search (titles here, all messages on Enter), folder filter, favorites and archive.
import type React from 'react'
import { RunDots, RunningList, elapsed, useNow } from './RunningList'
import { idsOf, useTerminals } from '../lib/terminals'
import { UsageWidget } from './UsageWidget'
import { Fragment, useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { sortThreads, useApp } from '../lib/app-context'
import { baseName, stamp, threadTitle } from '../lib/format'
import { safeSnippet } from '../lib/markdown'
import type { SearchHit, Thread } from '../lib/types'
import { BgMark, backgroundSince, backgroundTitle, hasBackground } from './Background'
import { ProviderBadge, ThreadStatus } from './bits'
import { Icon } from './icons'
import { type Option, Select } from './Select'
import { Avatar, Kbd, Spinner, useContextMenu } from './ui'
import { useThreadMenu } from './ThreadMenu'
import { PrivacyChip } from './ShareDialog'
import { SHOW_ARCHIVE, SHOW_FAVORITES, sidebarLayout, sidebarSetting } from '../lib/sidebar'
import { projectsPaused, projectsRef, refHost, refKey, useProjectsLink } from '../lib/projects'

function groupOf(t: Thread): string {
  if (t.pinned) return 'Favorites'
  const d = new Date(t.activity_at)
  const now = new Date()
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((day(now) - day(d)) / 86400e3)
  if (diff <= 0) return 'Today'
  if (diff === 1) return 'Yesterday'
  if (diff < 7) return 'This week'
  if (diff < 31) return 'This month'
  return 'Older'
}

// The live run time in a thread row (its own component, so only rows that
// are working tick every second).
function RowTimer({ since }: { since?: string }) {
  const now = useNow(1000)
  return <span className="th-time run-time tnum">{elapsed(since, now)}</span>
}

export function ThreadRow({ t, active, onOpen, onContextMenu }: { t: Thread; active: boolean; onOpen: () => void; onContextMenu?: (e: React.MouseEvent) => void }) {
  const { running, user, hosts } = useApp()
  const host = t.host_id ? hosts.find(h => h.id === t.host_id)?.name || 'Removed server' : ''
  // working in Pocket AI, or in Paseo / a terminal on the same session
  const run = running.find(r => r.thread_id === t.id || (!!t.provider_session_id && r.session_id === t.provider_session_id))
  const waiting = t.status === 'waiting' || t.pending > 0 || run?.status === 'waiting'
  const working = waiting || t.status === 'running' || !!run
  // the turn is over but commands, monitors or sub-agents still run
  const bg = !working && hasBackground(t)
  const paused = projectsPaused(t)
  return (
    <button type="button" className={`th-row ${active ? 'active' : ''} ${t.unread ? 'unread' : ''} ${working ? 'is-working' : ''} ${bg ? 'is-background' : ''} ${paused ? 'is-paused' : ''}`} onClick={onOpen} onContextMenu={onContextMenu} title={paused || (host ? `${host}: ${t.cwd}` : t.cwd)}>
      <span className="th-main">
        <span className="th-title ellipsis">{t.pinned && <Icon name="star" size={10} fill className="fav-star" />}{threadTitle(t)}</span>
        <span className="th-meta">
          <ProviderBadge provider={t.provider} size="xs" />
          {t.archived && <Icon name="archive" size={11} />}
          {t.source === 'schedule' && <Icon name="clock" size={11} />}
          {t.source === 'quick-chat' && <span className="quick-chat-chip xs" title="Started from Quick Chat in Pocket Launcher"><Icon name="sparkles" size={10} />Quick Chat</span>}
          <PrivacyChip sharedWith={t.shares?.map(s => s.name)} iconOnly />
          <span className="ellipsis">{t.owner_name && user && t.owner_id !== user.id ? `${t.owner_name} · ` : ''}{host ? `${host}: ` : ''}{baseName(t.cwd)}</span>
        </span>
      </span>
      <span className="th-side">
        {working ? <RowTimer since={run?.prompt_at || t.prompt_at || t.activity_at} />
          : bg ? <RowTimer since={backgroundSince(t) || t.activity_at} />
          : <span className="th-time">{stamp(t.activity_at)}</span>}
        {bg ? <span className="tstat bg" title={backgroundTitle(t)} aria-label="Still running in the background"><BgMark size={12} /></span>
          : <ThreadStatus t={working ? { ...t, status: waiting ? 'waiting' : 'running' } : t} />}
      </span>
    </button>
  )
}

export function Sidebar({ onNavigate, onPalette }: { onNavigate: () => void; onPalette: () => void }) {
  const { threads, workspaces, worktrees, user, settings, saveSettings, live, status, updateReady, terms, away, setAway } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [ws, setWs] = useState<number>(() => Number(localStorage.getItem('pai-ws') || 0))
  // Archive / Favorites switch the thread list ('' = the usual list)
  const [view, setView] = useState<'' | 'archive' | 'favorites'>('')
  const [special, setSpecial] = useState<Thread[] | null>(null)
  const showArchive = view === 'archive'
  const showFav = view === 'favorites'
  const toggleView = (v: 'archive' | 'favorites') => { setView(x => x === v ? '' : v); setSpecial(null); setHits(null) }
  const ctx = useContextMenu()
  const tm = useThreadMenu()
  useEffect(() => { localStorage.setItem('pai-ws', String(ws)) }, [ws])
  useEffect(() => { if (!q.trim()) setHits(null) }, [q])
  useEffect(() => {
    const arch = () => { setView('archive'); setSpecial(null); setHits(null) }
    const fav = () => { setView('favorites'); setSpecial(null); setHits(null) }
    window.addEventListener(SHOW_ARCHIVE, arch)
    window.addEventListener(SHOW_FAVORITES, fav)
    return () => { window.removeEventListener(SHOW_ARCHIVE, arch); window.removeEventListener(SHOW_FAVORITES, fav) }
  }, [])
  useEffect(() => {
    if (!view) return
    const keep = (t: Thread) => view === 'archive' ? t.archived : t.pinned
    api.get<Thread[]>(`${V1}/threads${qs(view === 'archive' ? { archived: 1 } : { favorites: 1 })}`).then(l => setSpecial(sortThreads((l || []).filter(keep)))).catch(() => setSpecial([]))
  }, [view, threads])

  const go = (path: string) => { navigate(path); onNavigate() }
  // Away from keyboard: agents carry on without you; back = decide what they saved
  const saved = (away?.items.length || 0) + (away?.on ? away.waiting.length : 0)
  const toggleAway = async () => {
    try {
      const a = await setAway(!away?.on)
      if (!a.on && a.items.length + a.waiting.length > 0) go('/away')
    } catch { /* the button shows the old state */ }
  }
  // your terminal tabs, listed under Terminal
  const term = useTerminals()
  const termTabs = term.groups
  const activeId = loc.pathname.startsWith('/t/') ? loc.pathname.split('/')[2] : ''
  const mine = (t: Thread) => !user || !t.owner_id || t.owner_id === user.id
  const base = view ? special : threads && threads.filter(mine)
  const shared = view || !threads ? [] : threads.filter(t => !mine(t))
  const shown = useMemo(() => {
    let list = base || []
    if (ws) list = list.filter(t => t.workspace_id === ws)
    const n = q.trim().toLowerCase()
    if (n) list = list.filter(t => `${t.title} ${t.preview} ${t.cwd}`.toLowerCase().includes(n))
    return list
  }, [base, ws, q])
  // Projects conversations, by issue — only while the Core link to Pocket Projects is on
  const projRefs = useMemo(() => (base || []).map(projectsRef).filter(Boolean), [base])
  const proj = useProjectsLink(projRefs)
  const projIssues = useMemo(() => {
    if (!proj?.on || view) return []
    const out: { ref: string; items: Thread[] }[] = []
    for (const t of shown) {
      const ref = projectsRef(t)
      if (!ref) continue
      const g = out.find(x => x.ref === ref)
      if (g) g.items.push(t); else out.push({ ref, items: [t] })
    }
    return out
  }, [proj?.on, proj?.threads, view, shown])
  const [projOpen, setProjOpen] = useState<string[]>(() => { try { return JSON.parse(localStorage.getItem('pai-sb-projects') || '[]') } catch { return [] } })
  useEffect(() => { localStorage.setItem('pai-sb-projects', JSON.stringify(projOpen)) }, [projOpen])
  // Favorites / Today / … / Shared with me fold (header toggle, remembered); older ones start folded
  const [folded, setFolded] = useState<string[]>(() => { try { return JSON.parse(localStorage.getItem('pai-sb-folded') || 'null') || ['This week', 'This month', 'Older'] } catch { return [] } })
  useEffect(() => { localStorage.setItem('pai-sb-folded', JSON.stringify(folded)) }, [folded])
  const fold = (name: string) => setFolded(f => f.includes(name) ? f.filter(x => x !== name) : [...f, name])
  const foldHead = (name: string, n: number, isFolded: boolean) => (
    <div className="sb-h">
      <button type="button" className={`sb-h-fold ${isFolded ? '' : 'open'}`} aria-expanded={!isFolded} onClick={() => fold(name)} title={isFolded ? `Show ${name}` : `Fold ${name}`}>
        <Icon name="chevron-right" size={11} className="sb-h-caret" />{name === 'Favorites' && <Icon name="star" size={11} fill className="fav-star" />}<span>{name}</span>{isFolded && <span className="sb-issue-n tnum">{n}</span>}
      </button>
    </div>
  )
  // a folded group still shows the open thread
  const foldRows = (name: string, items: Thread[]) => !view && folded.includes(name) ? items.filter(t => t.id === activeId) : items
  const [projFolded, setProjFolded] = useState(() => localStorage.getItem('pai-sb-projects-folded') === '1')
  useEffect(() => { localStorage.setItem('pai-sb-projects-folded', projFolded ? '1' : '0') }, [projFolded])
  const groups = useMemo(() => {
    const out: { name: string; items: Thread[] }[] = []
    for (const t of shown) {
      if (projIssues.length && projectsRef(t)) continue // listed under Projects
      const g = showArchive ? 'Archived' : showFav ? 'Favorites' : groupOf(t)
      const last = out[out.length - 1]
      if (last && last.name === g) last.items.push(t); else out.push({ name: g, items: [t] })
    }
    return out
  }, [shown, showArchive, showFav, projIssues])
  const sharedN = (threads || []).filter(t => !mine(t) || !!t.shares?.length).length
  const activeWt = worktrees.filter(w => w.status === 'active').length
  const needYou = (base || []).filter(t => t.status === 'waiting' || t.pending > 0).length

  const searchAll = async () => {
    const n = q.trim()
    if (n.length < 2) return
    setSearching(true)
    try { setHits((await api.get<SearchHit[]>(`${V1}/search${qs({ q: n })}`)) || []) } catch { setHits([]) } finally { setSearching(false) }
  }
  const wsOpts: Option<number>[] = [{ value: 0, label: 'All folders' }, ...workspaces.map(w => ({ value: w.id, label: w.name, color: w.color || 'var(--accent)', description: w.path }))]
  const nav = (path: string, icon: string, label: string, extra?: React.ReactNode, onContextMenu?: (e: React.MouseEvent) => void) => (
    <button type="button" className={`sb-item ${loc.pathname.startsWith(path) ? 'active' : ''}`} onClick={() => go(path)} onContextMenu={onContextMenu}><Icon name={icon} size={16} /><span className="grow">{label}</span>{extra}</button>
  )
  // the menu items this person keeps, in their order (Settings → General → Sidebar)
  const layout = useMemo(() => sidebarLayout(settings?.sidebar, user), [settings?.sidebar, user])
  const menu = layout.filter(e => e.on)
  const removeItem = (id: string) => { void saveSettings({ sidebar: sidebarSetting(layout.map(e => e.item.id === id ? { ...e, on: false } : e)) }).catch(() => {}) }

  return (
    <aside className="sidebar" aria-label="Threads">
      <div className="sb-brand">
        <button type="button" className="sb-home grow" onClick={() => go('/')} title="New thread" aria-label="New thread">
          <img src="/favicon.svg" alt="" className="sb-logo" />
          <span className="sb-title"><b>{settings?.title && settings.title !== 'Pocket AI' ? settings.title : <>Pocket <span className="accent-text">AI</span></>}</b></span>
        </button>
        {updateReady ? <button type="button" className="sb-version update" onClick={() => location.reload()} title={`Pocket AI was updated to ${status?.version} — reload to use it`}>Reload</button>
        : status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
        {!live && <span className="live-off" title="Live updates dropped — reconnecting; lists may be out of date"><span className="live-dot" />Reconnecting…</span>}
        <button type="button" className="btn ghost icon-only sm" aria-label="Search and commands" title="Search and commands (⌘K)" onClick={onPalette}><Icon name="search" size={16} /></button>
      </div>
      <div className="sb-actions">
        <button type="button" className="btn primary create-btn" onClick={() => go('/')}><Icon name="plus" size={16} />New thread<Kbd>⌘ N</Kbd></button>
      </div>
      <nav className="sb-nav">
        {menu.map(({ item }) => {
          const extra = item.id === 'worktrees' ? (activeWt > 0 ? <span className="badge">{activeWt}</span> : undefined)
            : item.id === 'shared' ? (sharedN > 0 ? <span className="badge">{sharedN}</span> : undefined)
            : item.id === 'terminal' && terms.open > 0 ? (
              <span className="sb-term" title={terms.busy ? `${terms.open} open · ${terms.busy} running a command` : `${terms.open} open`}>
                {terms.busy > 0 && <RunDots size={11} />}
                <span className={`badge ${terms.busy ? 'accent-soft' : ''}`}>{terms.open}</span>
              </span>) : undefined
          const onMenu = (e: React.MouseEvent) => ctx.open(e, () => [
            { label: 'Remove from sidebar', icon: 'eye-off', onSelect: () => removeItem(item.id) },
            { label: 'Customize sidebar…', icon: 'settings', onSelect: () => go('/settings/general#sidebar') },
          ])
          if (item.id === 'archive' || item.id === 'favorites') return <button key={item.id} type="button" className={`sb-item ${view === item.id ? 'active' : ''}`} onClick={() => toggleView(item.id as 'archive' | 'favorites')} onContextMenu={onMenu}><Icon name={item.icon} size={16} /><span className="grow">{item.label}</span></button>
          return (
            <Fragment key={item.id}>
              {nav(item.path!, item.icon, item.label, extra, onMenu)}
              {item.id === 'terminal' && termTabs.length > 0 && (
                <div className="sb-terms">
                  {termTabs.map(g => {
                    const ts = idsOf(g.root).map(id => term.byId.get(id)).filter(t => !!t)
                    const lead = term.byId.get(g.focus) || ts[0]
                    if (!lead) return null
                    const busy = ts.some(t => !t.ended && terms.busy_ids.includes(t.id))
                    return (
                      <button key={g.id} type="button" className={`sb-term-row ${loc.pathname === `/term/${g.id}` ? 'active' : ''} ${ts.every(t => t.ended) ? 'ended' : ''}`}
                        onClick={() => go(`/term/${g.id}`)} title={`${lead.title} · ${lead.host_name}${lead.cwd ? ` · ${lead.cwd}` : ''}${ts.length > 1 ? ` · ${ts.length} panes` : ''}`}>
                        <span className="sb-term-ico">{busy ? <RunDots size={10} /> : <span className="term-dot" />}</span>
                        <span className="grow ellipsis">{lead.title}</span>
                        {ts.length > 1 && <span className="sb-term-n">{ts.length}</span>}
                      </button>
                    )
                  })}
                </div>
              )}
            </Fragment>
          )
        })}
      </nav>

      <div className="sb-sec">
        <div className="sb-search">
          <Icon name="search" size={14} />
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Filter threads…" aria-label="Filter threads"
            onKeyDown={e => { if (e.key === 'Enter') searchAll(); if (e.key === 'Escape') setQ('') }} />
          {q && <button type="button" className="btn ghost icon-only xs" aria-label="Clear" onClick={() => setQ('')}><Icon name="x" size={12} /></button>}
        </div>
        {q.trim().length >= 2 && !hits && (
          <button type="button" className="sb-searchall" onClick={searchAll} disabled={searching}>{searching ? <Spinner size={12} /> : <Icon name="message" size={13} />}Search all messages for “{q.trim()}”<kbd>↵</kbd></button>
        )}
        {workspaces.length > 0 && !hits && (
          <div className="sb-filter"><Select size="sm" className="ghost" value={ws} options={wsOpts} onChange={setWs} label="Folder" width={260} /></div>
        )}
      </div>

      {!hits && !view && <RunningList compact onOpened={onNavigate} />}
      {hits ? (
        <div className="sb-threads">
          <div className="sb-h"><span>Messages matching “{q.trim()}”</span><button type="button" className="btn ghost xs" aria-label="Close search" onClick={() => setHits(null)}><Icon name="x" size={12} /></button></div>
          {hits.length === 0 && <div className="sb-empty">Nothing found.</div>}
          {hits.map(h => (
            <button key={h.thread_id + h.item_key} type="button" className={`th-row hit ${activeId === h.thread_id ? 'active' : ''}`} onClick={() => go(`/t/${h.thread_id}${h.item_key ? `#item-${h.item_key}` : ''}`)}>
              <span className="th-main">
                <span className="th-title ellipsis">{h.title || 'Thread'}</span>
                <span className="th-snippet" dangerouslySetInnerHTML={{ __html: safeSnippet(h.snippet) }} />
              </span>
              <span className="th-side"><span className="th-time">{stamp(h.activity_at)}</span></span>
            </button>
          ))}
        </div>
      ) : (
        <div className="sb-threads">
          {view && <button type="button" className="sb-away" onClick={() => setView('')}><Icon name="arrow-left" size={13} /><span className="grow">Back to threads</span></button>}
          {(away?.on || saved > 0) && !view && (
            <button type="button" className={`sb-away ${away?.on ? 'on' : ''}`} onClick={() => go('/away')}>
              <Icon name="moon" size={13} /><span className="grow ellipsis">{away?.on ? 'Away — agents carry on without you' : 'Saved while you were away'}</span>
              {saved > 0 && <span className="badge">{saved}</span>}
            </button>
          )}
          {needYou > 0 && !view && !away?.on && <div className="sb-needyou"><Icon name="help-circle" size={13} />{needYou === 1 ? '1 thread needs you' : `${needYou} threads need you`}</div>}
          {base === null && <div className="sb-empty"><Spinner /></div>}
          {base && shown.length === 0 && !(shared.length && !q && !ws) && <div className="sb-empty">{q || ws ? 'No threads match.' : showArchive ? 'Nothing archived.' : showFav ? 'No favorites yet — right-click a thread → Add to favorites.' : 'No threads yet — start one above.'}</div>}
          {projIssues.length > 0 && (
            <div className="sb-group">
              <div className="sb-h">
                <button type="button" className={`sb-h-fold ${projFolded ? '' : 'open'}`} aria-expanded={!projFolded} onClick={() => setProjFolded(f => !f)} title={projFolded ? 'Show Projects' : 'Fold Projects'}>
                  <Icon name="chevron-right" size={11} className="sb-h-caret" /><span>Projects</span>{projFolded && <span className="sb-issue-n tnum">{projIssues.length}</span>}
                </button>
                {proj?.projects_url && <a className="btn ghost icon-only xs" href={proj.projects_url} target="_blank" rel="noreferrer" title="Open Pocket Projects" aria-label="Open Pocket Projects"><Icon name="external" size={12} /></a>}</div>
              {!projFolded && projIssues.map(g => {
                const is = proj?.issues[g.ref]
                const open = projOpen.includes(g.ref) || g.items.some(t => t.id === activeId)
                const issueUrl = is?.url || (proj?.projects_url ? `${proj.projects_url}/issue/${g.ref}` : '')
                return (
                  <div key={g.ref} className="sb-issue-wrap">
                    <div className="sb-issue-row" onContextMenu={e => { if (issueUrl) ctx.open(e, () => [{ label: `Open ${refKey(g.ref)} in Pocket Projects`, icon: 'external', onSelect: () => window.open(issueUrl, '_blank', 'noopener') }]) }}>
                      <button type="button" className={`sb-issue grow ${open ? 'open' : ''} ${is?.state_type === 'completed' || is?.state_type === 'canceled' ? 'closed' : ''}`} aria-expanded={open}
                        title={[refKey(g.ref), refHost(g.ref), is?.title].filter(Boolean).join(' · ')}
                        onClick={() => setProjOpen(o => o.includes(g.ref) ? o.filter(x => x !== g.ref) : [...o, g.ref])}>
                        <Icon name="chevron-right" size={12} className="sb-issue-caret" />
                        <span className="sb-issue-ref tnum">{refKey(g.ref)}</span>
                        <span className="sb-issue-title ellipsis">{is?.title || ''}</span>
                        <span className="sb-issue-n tnum">{g.items.length}</span>
                      </button>
                      {issueUrl && <a className="sb-issue-open" href={issueUrl} target="_blank" rel="noreferrer" title={`Open ${refKey(g.ref)} in Pocket Projects`} aria-label={`Open ${refKey(g.ref)} in Pocket Projects`}><Icon name="external" size={13} /></a>}
                    </div>
                    {open && g.items.map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
                  </div>
                )
              })}
            </div>
          )}
          {groups.map(g => (
            <div key={g.name} className="sb-group">
              {view ? <div className="sb-h"><span>{g.name}</span></div> : foldHead(g.name, g.items.length, folded.includes(g.name))}
              {foldRows(g.name, g.items).map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
            </div>
          ))}
          {shared.length > 0 && !q && !ws && (
            <div className="sb-group">
              {foldHead('Shared with me', shared.length, folded.includes('Shared with me'))}
              {foldRows('Shared with me', shared).map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
            </div>
          )}
        </div>
      )}

      <div className="sb-bottom">
      {!view && <UsageWidget onNavigate={onNavigate} />}
      <div className="sb-foot">
        <button type="button" className={`sb-foot-link away-toggle ${away?.on ? 'on' : ''}`} aria-pressed={!!away?.on} onClick={toggleAway}
          title={away?.on ? "I'm back: show what the agents saved for me" : 'Away from keyboard: agents keep working on what they can and save approvals and questions for when you are back'}>
          <Icon name="moon" size={14} />{away?.on ? 'Away' : 'Away mode'}
        </button>
        <span className="spacer" />
        <button type="button" className={`sb-foot-link ${loc.pathname.startsWith('/settings') ? 'active' : ''}`} onClick={() => go('/settings')} title="Settings">
          {user && <Avatar name={user.name} email={user.email} size={20} />}<Icon name="settings" size={14} />
        </button>
      </div>
      </div>
      {ctx.menu}{tm.dialogs}
    </aside>
  )
}
