// Threads, grouped by time, with search (titles here, all messages on Enter), folder filter and archive.
import type React from 'react'
import { RunningList, elapsed, useNow } from './RunningList'
import { UsageWidget } from './UsageWidget'
import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { sortThreads, useApp } from '../lib/app-context'
import { baseName, stamp, threadTitle } from '../lib/format'
import { safeSnippet } from '../lib/markdown'
import type { SearchHit, Thread } from '../lib/types'
import { ProviderBadge, ThreadStatus } from './bits'
import { Icon } from './icons'
import { type Option, Select } from './Select'
import { Avatar, Kbd, Spinner, useContextMenu } from './ui'
import { useThreadMenu } from './ThreadMenu'
import { PrivacyChip } from './ShareDialog'
import { projectsRef, useProjectsLink } from '../lib/projects'

function groupOf(t: Thread): string {
  if (t.pinned) return 'Pinned'
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
  return (
    <button type="button" className={`th-row ${active ? 'active' : ''} ${t.unread ? 'unread' : ''} ${working ? 'is-working' : ''}`} onClick={onOpen} onContextMenu={onContextMenu} title={host ? `${host}: ${t.cwd}` : t.cwd}>
      <span className="th-main">
        <span className="th-title ellipsis">{threadTitle(t)}</span>
        <span className="th-meta">
          <ProviderBadge provider={t.provider} size="xs" />
          {t.source === 'schedule' && <Icon name="clock" size={11} />}
          <PrivacyChip sharedWith={t.shares?.map(s => s.name)} iconOnly />
          <span className="ellipsis">{t.owner_name && user && t.owner_id !== user.id ? `${t.owner_name} · ` : ''}{host ? `${host}: ` : ''}{baseName(t.cwd)}</span>
        </span>
      </span>
      <span className="th-side">
        {working ? <RowTimer since={run?.prompt_at || t.prompt_at || t.activity_at} /> : <span className="th-time">{stamp(t.activity_at)}</span>}
        <ThreadStatus t={working ? { ...t, status: waiting ? 'waiting' : 'running' } : t} />
      </span>
    </button>
  )
}

export function Sidebar({ onNavigate, onPalette }: { onNavigate: () => void; onPalette: () => void }) {
  const { threads, workspaces, worktrees, user, settings, live, status, updateReady } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [ws, setWs] = useState<number>(() => Number(localStorage.getItem('pai-ws') || 0))
  const [archived, setArchived] = useState<Thread[] | null>(null)
  const [showArchive, setShowArchive] = useState(false)
  const ctx = useContextMenu()
  const tm = useThreadMenu()
  useEffect(() => { localStorage.setItem('pai-ws', String(ws)) }, [ws])
  useEffect(() => { if (!q.trim()) setHits(null) }, [q])
  useEffect(() => {
    if (!showArchive) return
    api.get<Thread[]>(`${V1}/threads${qs({ archived: 1 })}`).then(l => setArchived(sortThreads((l || []).filter(t => t.archived)))).catch(() => setArchived([]))
  }, [showArchive, threads])

  const go = (path: string) => { navigate(path); onNavigate() }
  const activeId = loc.pathname.startsWith('/t/') ? loc.pathname.split('/')[2] : ''
  const mine = (t: Thread) => !user || !t.owner_id || t.owner_id === user.id
  const base = showArchive ? archived : threads && threads.filter(mine)
  const shared = showArchive || !threads ? [] : threads.filter(t => !mine(t))
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
    if (!proj?.on || showArchive) return []
    const out: { ref: string; items: Thread[] }[] = []
    for (const t of shown) {
      const ref = projectsRef(t)
      if (!ref) continue
      const g = out.find(x => x.ref === ref)
      if (g) g.items.push(t); else out.push({ ref, items: [t] })
    }
    return out
  }, [proj?.on, proj?.threads, showArchive, shown])
  const [projOpen, setProjOpen] = useState<string[]>(() => { try { return JSON.parse(localStorage.getItem('pai-sb-projects') || '[]') } catch { return [] } })
  useEffect(() => { localStorage.setItem('pai-sb-projects', JSON.stringify(projOpen)) }, [projOpen])
  const groups = useMemo(() => {
    const out: { name: string; items: Thread[] }[] = []
    for (const t of shown) {
      if (projIssues.length && projectsRef(t)) continue // listed under Projects
      const g = showArchive ? 'Archived' : groupOf(t)
      const last = out[out.length - 1]
      if (last && last.name === g) last.items.push(t); else out.push({ name: g, items: [t] })
    }
    return out
  }, [shown, showArchive, projIssues])
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
  const nav = (path: string, icon: string, label: string, extra?: React.ReactNode) => (
    <button type="button" className={`sb-item ${loc.pathname.startsWith(path) ? 'active' : ''}`} onClick={() => go(path)}><Icon name={icon} size={16} /><span className="grow">{label}</span>{extra}</button>
  )

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
        {nav('/briefing', 'sun', 'Briefing')}
        {nav('/workflows', 'zap', 'Workflows')}
        {nav('/memory', 'bookmark', 'Memory')}
        {nav('/actions', 'list-checks', 'Action history')}
        {(user?.role === 'admin' || user?.can_run) && nav('/today', 'today', 'Today')}
        {nav('/recipes', 'zap', 'Recipes')}
        {nav('/schedules', 'clock', 'Schedules')}
        {(user?.role === 'admin' || user?.can_run) && nav('/history', 'history', 'On this server')}
        {(user?.role === 'admin' || user?.can_run) && nav('/worktrees', 'fork', 'Worktrees', activeWt > 0 ? <span className="badge">{activeWt}</span> : undefined)}
        {nav('/shared', 'users', 'Shared', sharedN > 0 ? <span className="badge">{sharedN}</span> : undefined)}
        {nav('/usage', 'chart', 'Usage')}
        {nav('/snippets', 'bookmark', 'Snippets')}
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

      {!hits && !showArchive && <RunningList compact onOpened={onNavigate} />}
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
          {needYou > 0 && !showArchive && <div className="sb-needyou"><Icon name="help-circle" size={13} />{needYou === 1 ? '1 thread needs you' : `${needYou} threads need you`}</div>}
          {base === null && <div className="sb-empty"><Spinner /></div>}
          {base && shown.length === 0 && !(shared.length && !q && !ws) && <div className="sb-empty">{q || ws ? 'No threads match.' : showArchive ? 'Nothing archived.' : 'No threads yet — start one above.'}</div>}
          {projIssues.length > 0 && (
            <div className="sb-group">
              <div className="sb-h"><span>Projects</span>{proj?.projects_url && <a className="btn ghost icon-only xs" href={proj.projects_url} target="_blank" rel="noreferrer" title="Open Pocket Projects" aria-label="Open Pocket Projects"><Icon name="external" size={12} /></a>}</div>
              {projIssues.map(g => {
                const is = proj?.issues[g.ref]
                const open = projOpen.includes(g.ref) || g.items.some(t => t.id === activeId)
                const issueUrl = is?.url || (proj?.projects_url ? `${proj.projects_url}/issue/${g.ref}` : '')
                return (
                  <div key={g.ref} className="sb-issue-wrap">
                    <div className="sb-issue-row" onContextMenu={e => { if (issueUrl) ctx.open(e, () => [{ label: `Open ${g.ref} in Pocket Projects`, icon: 'external', onSelect: () => window.open(issueUrl, '_blank', 'noopener') }]) }}>
                      <button type="button" className={`sb-issue grow ${open ? 'open' : ''} ${is?.state_type === 'completed' || is?.state_type === 'canceled' ? 'closed' : ''}`} aria-expanded={open}
                        title={is ? `${g.ref} · ${is.title}` : g.ref}
                        onClick={() => setProjOpen(o => o.includes(g.ref) ? o.filter(x => x !== g.ref) : [...o, g.ref])}>
                        <Icon name="chevron-right" size={12} className="sb-issue-caret" />
                        <span className="sb-issue-ref tnum">{g.ref}</span>
                        <span className="sb-issue-title ellipsis">{is?.title || ''}</span>
                        <span className="sb-issue-n tnum">{g.items.length}</span>
                      </button>
                      {issueUrl && <a className="sb-issue-open" href={issueUrl} target="_blank" rel="noreferrer" title={`Open ${g.ref} in Pocket Projects`} aria-label={`Open ${g.ref} in Pocket Projects`}><Icon name="external" size={13} /></a>}
                    </div>
                    {open && g.items.map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
                  </div>
                )
              })}
            </div>
          )}
          {groups.map(g => (
            <div key={g.name} className="sb-group">
              <div className="sb-h"><span>{g.name}</span></div>
              {g.items.map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
            </div>
          ))}
          {shared.length > 0 && !q && !ws && (
            <div className="sb-group">
              <div className="sb-h"><span>Shared with me</span></div>
              {shared.map(t => <ThreadRow key={t.id} t={t} active={t.id === activeId} onOpen={() => go(`/t/${t.id}`)} onContextMenu={e => ctx.open(e, () => tm.items(t))} />)}
            </div>
          )}
        </div>
      )}

      <div className="sb-bottom">
      {!showArchive && <UsageWidget onNavigate={onNavigate} />}
      <div className="sb-foot">
        <button type="button" className={`sb-foot-link ${showArchive ? 'active' : ''}`} onClick={() => { setShowArchive(a => !a); setHits(null) }}><Icon name="archive" size={14} />{showArchive ? 'Back to threads' : 'Archive'}</button>
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
