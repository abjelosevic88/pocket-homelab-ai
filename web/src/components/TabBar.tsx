// Threads as tabs along the top, like Paseo or a browser: every thread you
// open gets one. Click to switch, drag to reorder, middle-click or ✕ to
// close. The list is kept per browser (localStorage).
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { threadTitle } from '../lib/format'
import type { Thread } from '../lib/types'
import { Icon } from './icons'
import { BgMark, backgroundTitle, hasBackground } from './Background'
import { type MenuItem, MenuButton, useContextMenu } from './ui'
import { useThreadMenu } from './ThreadMenu'
import { keyCaps, useShortcuts } from '../lib/shortcuts'
import { projectsRef, useProjectsLink } from '../lib/projects'
import { idsOf, isTermTab, TERM_TAB, tabPath, useMachineItems, useTerminals } from '../lib/terminals'
import { RunDots } from './RunningList'

const KEY = 'pai-tabs'
const load = (key = KEY): string[] => { try { const v = JSON.parse(localStorage.getItem(key) || '[]'); return Array.isArray(v) ? v.filter(x => typeof x === 'string') : [] } catch { return [] } }
/** Closed tabs, most recent first, for "Reopen closed tab". */
const CLOSED = 'pai-tabs-closed'

/** Named, coloured tab groups (like Chrome's): a label in the bar with its
 * tabs right after it; click the label to fold them away. Per browser. */
export interface TabGroup { id: string; name: string; color: string; collapsed?: boolean; tabs: string[]; /** made by the app: "projects:AS-7" */ auto?: string }
const GROUPS = 'pai-tab-groups'
/** Tabs already offered to an automatic group (taking one out keeps it out). */
const AUTO = 'pai-tab-auto2' // 2: forks of Projects conversations count too
export const GROUP_COLORS = ['blue', 'red', 'yellow', 'green', 'pink', 'purple', 'cyan', 'orange', 'grey']
const loadGroups = (): TabGroup[] => {
  try { const v = JSON.parse(localStorage.getItem(GROUPS) || '[]'); return Array.isArray(v) ? v.filter(g => g && typeof g.id === 'string' && Array.isArray(g.tabs)) : [] } catch { return [] }
}

/** The open tabs; opening a thread adds its tab. */
export function useTabs(enabled: boolean) {
  const [tabs, setTabs] = useState<string[]>(() => load())
  const [closed, setClosed] = useState<string[]>(() => load(CLOSED))
  const [groups, setGroups] = useState<TabGroup[]>(loadGroups)
  useEffect(() => { localStorage.setItem(GROUPS, JSON.stringify(groups)) }, [groups])
  const { threads } = useApp()
  const loc = useLocation()
  const navigate = useNavigate()
  const term = useTerminals()
  // a conversation's id, or term:<tab> for a terminal tab
  const active = /^\/t\/([^/]+)/.exec(loc.pathname)?.[1] || ((g => g ? TERM_TAB + g : '')(/^\/term\/([^/]+)/.exec(loc.pathname)?.[1]))
  useEffect(() => { localStorage.setItem(KEY, JSON.stringify(tabs)) }, [tabs])
  useEffect(() => { localStorage.setItem(CLOSED, JSON.stringify(closed)) }, [closed])
  useEffect(() => { if (enabled && active) { setTabs(t => t.includes(active) ? t : [...t, active]); setClosed(c => c.includes(active) ? c.filter(x => x !== active) : c) } }, [enabled, active])

  const close = useCallback((ids: string[], remember = true) => {
    const next = tabs.filter(x => !ids.includes(x))
    setTabs(next)
    // closing a terminal tab ends its shells (it cannot be reopened)
    const termGroups = ids.filter(isTermTab).map(id => id.slice(TERM_TAB.length))
    if (remember && termGroups.length) term.closeGroups(termGroups)
    if (remember) setClosed(c => [...ids.filter(id => tabs.includes(id) && !isTermTab(id)).reverse(), ...c.filter(x => !ids.includes(x))].slice(0, 20))
    if (active && ids.includes(active)) { // closing the open tab: go to its neighbour
      const to = next[Math.min(tabs.indexOf(active), next.length - 1)]
      navigate(to ? tabPath(to) : '/')
    }
  }, [tabs, active, navigate, term])
  const move = useCallback((id: string, before: string) => setTabs(t => {
    if (id === before) return t
    const rest = t.filter(x => x !== id)
    const i = before ? rest.indexOf(before) : rest.length
    rest.splice(i < 0 ? rest.length : i, 0, id)
    return rest
  }), [])
  // threads that are gone (deleted or archived) cannot come back
  const reopenable = threads ? closed.filter(id => threads.some(t => t.id === id)) : closed
  const reopen = useCallback((id = reopenable[0]) => { if (id) navigate(`/t/${id}`) }, [reopenable, navigate])
  /** Switch to the tab `by` places away (wraps around). */
  const step = useCallback((by: number) => {
    if (!tabs.length) return
    const i = tabs.indexOf(active)
    const to = tabs[i < 0 ? (by > 0 ? 0 : tabs.length - 1) : (i + by + tabs.length) % tabs.length]
    if (to !== active) navigate(tabPath(to))
  }, [tabs, active, navigate])
  // groups: members always sit together in `tabs`, the group where its first member is
  const groupOf = useCallback((id: string) => groups.find(g => g.tabs.includes(id)), [groups])
  useEffect(() => { // closed tabs leave their group; empty groups go
    setGroups(gs => {
      const next = gs.map(g => ({ ...g, tabs: g.tabs.filter(id => tabs.includes(id)) })).filter(g => g.tabs.length)
      return JSON.stringify(next) === JSON.stringify(gs) ? gs : next
    })
  }, [tabs])
  const place = useCallback((id: string, members: string[]) => setTabs(t => {
    // after the group's last member (or where `id` is, for a new group)
    const rest = t.filter(x => x !== id)
    const last = Math.max(-1, ...members.filter(m => m !== id).map(m => rest.indexOf(m)))
    if (last < 0) return t
    rest.splice(last + 1, 0, id)
    return rest
  }), [])
  const joinGroup = useCallback((id: string, gid: string) => {
    const g = groups.find(x => x.id === gid)
    if (!g || g.tabs.includes(id)) return
    setGroups(gs => gs.map(x => x.id === gid ? { ...x, collapsed: false, tabs: [...x.tabs, id] } : { ...x, tabs: x.tabs.filter(y => y !== id) }))
    place(id, g.tabs)
  }, [groups, place])
  const newGroup = useCallback((ids: string[], name = '', auto?: string) => {
    const id = 'g' + Date.now().toString(36)
    const used = new Set(groups.map(g => g.color))
    const color = GROUP_COLORS.find(c => !used.has(c)) || GROUP_COLORS[groups.length % GROUP_COLORS.length]
    setGroups(gs => [...gs.map(g => ({ ...g, tabs: g.tabs.filter(x => !ids.includes(x)) })), { id, name, color, tabs: ids, ...(auto ? { auto } : {}) }])
    ids.slice(1).forEach(x => place(x, ids))
    return id
  }, [groups, place])
  const leaveGroup = useCallback((id: string) => {
    const g = groups.find(x => x.tabs.includes(id))
    if (!g) return
    setGroups(gs => gs.map(x => x.id === g.id ? { ...x, tabs: x.tabs.filter(y => y !== id) } : x))
    // out to the right of the group, so it stays together
    const others = g.tabs.filter(x => x !== id)
    if (others.length) place(id, others)
  }, [groups, place])
  // a whole group, dragged: its tabs move together, next to `target` (after it
  // when the pointer is on its right half) but never into another group
  const moveGroup = useCallback((gid: string, target: string, after: boolean) => setTabs(t => {
    const g = groups.find(x => x.id === gid)
    if (!g) return t
    const members = t.filter(x => g.tabs.includes(x))
    if (!members.length || members.includes(target)) return t
    const rest = t.filter(x => !members.includes(x))
    let i = rest.indexOf(target)
    if (i < 0) return t
    const other = groups.find(x => x.id !== gid && x.tabs.includes(target))
    if (other) {
      const at = other.tabs.map(x => rest.indexOf(x)).filter(n => n >= 0)
      i = after ? Math.max(...at) + 1 : Math.min(...at)
    } else if (after) i++
    rest.splice(i, 0, ...members)
    return rest.join() === t.join() ? t : rest
  }), [groups])
  const editGroup = useCallback((gid: string, p: Partial<Omit<TabGroup, 'id' | 'tabs'>>) => setGroups(gs => gs.map(g => g.id === gid ? { ...g, ...p } : g)), [])
  const ungroup = useCallback((gid: string) => setGroups(gs => gs.filter(g => g.id !== gid)), [])
  // A conversation a Projects issue started joins that issue's group (made on
  // first need) — while the Core link to Pocket Projects is on. Once per tab.
  const proj = useProjectsLink(useMemo(() => (threads || []).filter(t => tabs.includes(t.id)).map(projectsRef).filter(Boolean), [threads, tabs]))
  const [autoDone, setAutoDone] = useState<string[]>(() => load(AUTO))
  useEffect(() => { localStorage.setItem(AUTO, JSON.stringify(autoDone.slice(-300))) }, [autoDone])
  useEffect(() => {
    if (!enabled || !threads || !proj?.on) return
    const id = tabs.find(x => !autoDone.includes(x))
    if (!id) return
    setAutoDone(d => [...d, id]) // one per pass: the next runs with fresh groups
    const ref = projectsRef(threads.find(t => t.id === id))
    if (!ref || groupOf(id)) return
    const g = groups.find(x => x.auto === 'projects:' + ref)
    if (g) joinGroup(id, g.id); else newGroup([id], ref, 'projects:' + ref)
  }, [enabled, threads, proj?.on, proj?.threads, tabs, autoDone, groups, groupOf, joinGroup, newGroup])
  return { tabs, active, close, move, moveGroup, closed: reopenable, reopen, step, groups, issues: proj?.on ? proj.issues : {}, groupOf, joinGroup, newGroup, leaveGroup, editGroup, ungroup }
}

export function TabBar({ tabs, active, close, move, moveGroup, closed, reopen, issues, groups, groupOf, joinGroup, newGroup, leaveGroup, editGroup, ungroup }: ReturnType<typeof useTabs>) {
  const [naming, setNaming] = useState('') // group whose label is being renamed
  const { threads, running, user, terms } = useApp()
  const term = useTerminals()
  const machines = useMachineItems(async h => { const g = await term.open(h); if (g) navigate(`/term/${g}`) })
  const termGroup = (id: string) => isTermTab(id) ? term.groups.find(g => g.id === id.slice(TERM_TAB.length)) : undefined
  const keys = useShortcuts()
  const navigate = useNavigate()
  const [drag, setDrag] = useState('')
  const [dragGroup, setDragGroup] = useState('') // a group label being dragged
  const ctx = useContextMenu()
  const tm = useThreadMenu()
  const byId = new Map((threads || []).map(t => [t.id, t] as [string, Thread]))
  // tabs of threads that are gone (deleted or archived) drop out — except the
  // one on screen (an archived thread you opened keeps its tab)
  const gone = tabs.filter(id => id !== active && (isTermTab(id) ? term.list !== null && !termGroup(id) : !!threads && !byId.has(id)))
  const shown = tabs.filter(id => !gone.includes(id))
  useEffect(() => { if (gone.length) close(gone, false) }, [gone.join()]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!shown.length) return null
  const working = (t?: Thread) => !!t && (t.status === 'running' || t.status === 'waiting' || t.pending > 0 || running.some(r => r.thread_id === t.id || (!!t.provider_session_id && r.session_id === t.provider_session_id)))
  const i = shown.indexOf(active)
  const tabMenu = (id: string, t?: Thread): MenuItem[] => {
    const at = shown.indexOf(id)
    return [
      ...(t ? [...tm.items(t), 'sep' as const] : []),
      { label: 'Add to new group', icon: 'plus', onSelect: () => setNaming(newGroup([id])) },
      ...groups.filter(g => !g.tabs.includes(id)).map(g => ({ label: <span className="row"><span className={`tg-dot tg-${g.color}`} />Add to “{g.name || 'Unnamed group'}”</span>, onSelect: () => joinGroup(id, g.id) })),
      ...(groupOf(id) ? [{ label: 'Remove from group', onSelect: () => leaveGroup(id) }] : []),
      'sep' as const,
      { label: 'Close tab', icon: 'x', onSelect: () => close([id]) },
      { label: 'Close other tabs', disabled: shown.length < 2, onSelect: () => close(shown.filter(x => x !== id)) },
      { label: 'Close tabs to the right', disabled: at === shown.length - 1, onSelect: () => close(shown.slice(at + 1)) },
    ]
  }

  const groupMenu = (g: TabGroup): MenuItem[] => [
    { label: 'Rename group', icon: 'edit', onSelect: () => setNaming(g.id) },
    { label: g.collapsed ? 'Expand group' : 'Collapse group', onSelect: () => editGroup(g.id, { collapsed: !g.collapsed }) },
    { heading: 'Colour' },
    ...GROUP_COLORS.map(c => ({ label: <span className="row"><span className={`tg-dot tg-${c}`} />{c[0].toUpperCase() + c.slice(1)}</span>, checked: g.color === c, onSelect: () => editGroup(g.id, { color: c }) })),
    'sep',
    { label: 'Ungroup', onSelect: () => ungroup(g.id) },
    { label: 'Close group', icon: 'x', danger: true, onSelect: () => close(g.tabs) },
  ]
  // the bar: a group's label where its first tab is, its tabs right after
  const seen = new Set<string>()
  const row: ({ group: TabGroup } | { id: string; group?: TabGroup })[] = []
  for (const id of shown) {
    if (seen.has(id)) continue
    const g = groupOf(id)
    if (!g) { row.push({ id }); continue }
    row.push({ group: g })
    for (const m of shown.filter(x => g.tabs.includes(x))) { seen.add(m); if (!g.collapsed || m === active) row.push({ id: m, group: g }) }
  }

  return (
    <div className="tabbar" role="tablist" aria-label="Open threads"
      // a drag always lands somewhere: the bar already moved things live, so
      // the browser must not animate the dragged picture back to its start
      onDragOver={e => { if (drag || dragGroup) { e.preventDefault(); e.dataTransfer.dropEffect = 'move' } }}
      onDrop={e => { if (drag || dragGroup) { e.preventDefault(); setDrag(''); setDragGroup('') } }}>
      <div className="tabbar-scroll">
        {row.map(r => {
          if (!('id' in r)) {
            const g = r.group
            const n = g.tabs.filter(x => shown.includes(x)).length
            return naming === g.id
              ? <input key={'g-' + g.id} autoFocus className={`tg-label tg-${g.color} editing`} defaultValue={g.name} placeholder="Name this group" aria-label="Group name" maxLength={40}
                  onFocus={e => e.currentTarget.select()}
                  onBlur={e => { editGroup(g.id, { name: e.currentTarget.value.trim() }); setNaming('') }}
                  onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur(); if (e.key === 'Escape') { e.currentTarget.value = g.name; e.currentTarget.blur() } }} />
              : <button key={'g-' + g.id} type="button" draggable className={`tg-label tg-${g.color} ${g.collapsed ? 'collapsed' : ''} ${g.name ? '' : 'empty'} ${dragGroup === g.id ? 'dragging' : ''}`}
                  onDragStart={e => { setDragGroup(g.id); e.dataTransfer.effectAllowed = 'move' }}
                  onDragEnd={() => setDragGroup('')}
                  title={`${g.name || 'Unnamed group'}${g.auto?.startsWith('projects:') && issues[g.auto.slice(9)] ? ` · ${issues[g.auto.slice(9)].title}` : ''} · ${n} ${n === 1 ? 'tab' : 'tabs'} — click to ${g.collapsed ? 'expand' : 'collapse'}, double-click to rename`}
                  aria-expanded={!g.collapsed}
                  onClick={() => editGroup(g.id, { collapsed: !g.collapsed })}
                  onDoubleClick={() => setNaming(g.id)}
                  onContextMenu={e => ctx.open(e, () => groupMenu(g))}
                  onDragOver={e => {
                    if (drag) { e.preventDefault(); joinGroup(drag, g.id) }
                    else if (dragGroup && dragGroup !== g.id) { e.preventDefault(); const r = e.currentTarget.getBoundingClientRect(); moveGroup(dragGroup, shown.find(x => g.tabs.includes(x)) || '', e.clientX > r.left + r.width / 2) }
                  }}>
                  {g.name || <span className="tg-dot" />}{g.collapsed && <span className="tg-count tnum">{n}</span>}
                </button>
          }
          const id = r.id
          const t = byId.get(id)
          const on = id === active
          const busy = working(t)
          const tg = termGroup(id)
          const tts = tg ? idsOf(tg.root).map(x => term.byId.get(x)).filter(x => !!x) : []
          const tLead = tg ? term.byId.get(tg.focus) || tts[0] : undefined
          const tBusy = tts.some(x => !x.ended && terms.busy_ids.includes(x.id))
          return (
            <div key={id} role="tab" aria-selected={on} tabIndex={0} draggable
              className={`tab ${on ? 'on' : ''} ${drag === id ? 'dragging' : ''} ${t?.unread && !on ? 'unread' : ''} ${r.group ? `grouped tg-${r.group.color}` : ''}`}
              title={t ? threadTitle(t) : tLead ? `${tLead.title} · ${tLead.host_name}${tts.length > 1 ? ` · ${tts.length} panes` : ''}` : ''}
              onClick={() => navigate(tabPath(id))}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); navigate(tabPath(id)) } }}
              onContextMenu={e => ctx.open(e, () => tabMenu(id, byId.get(id)))}
              onAuxClick={e => { if (e.button === 1) { e.preventDefault(); close([id]) } }}
              onMouseDown={e => { if (e.button === 1) e.preventDefault() }}
              onDragStart={e => { setDrag(id); e.dataTransfer.effectAllowed = 'move' }}
              onDragEnd={() => setDrag('')}
              onDragOver={e => {
                if (dragGroup) { e.preventDefault(); const b = e.currentTarget.getBoundingClientRect(); if (r.group?.id !== dragGroup) moveGroup(dragGroup, id, e.clientX > b.left + b.width / 2); return }
                if (!drag) return
                e.preventDefault()
                if (drag === id) return
                const into = r.group?.id || '', from = groupOf(drag)?.id || ''
                if (into !== from) { if (into) joinGroup(drag, into); else leaveGroup(drag) }
                move(drag, id)
              }}>
              {isTermTab(id) ? <span className="tab-ico term">
                {tBusy ? <span className="tab-term-busy" role="img" aria-label="Running a command"><RunDots size={11} /></span> : <Icon name="terminal" size={13} />}
              </span> : <span className={`tab-ico ${t?.provider || ''}`}>
                {busy ? <span className={`tab-busy ${t?.status === 'waiting' || (t?.pending || 0) > 0 ? 'waiting' : ''}`} role="img" aria-label="Working" />
                  : t && hasBackground(t) ? <span className="tab-bg" role="img" aria-label="Still running in the background" title={backgroundTitle(t)}><BgMark size={11} /></span>
                  : t?.unread && !on ? <span className="tab-done" role="img" aria-label="Done, new reply" title="Done — new reply" />
                  : <Icon name={t?.provider === 'codex' ? 'codex' : 'claude'} size={13} />}
              </span>}
              <span className="tab-title">{t ? threadTitle(t) : tLead ? tLead.title : '…'}</span>
              {tts.length > 1 && <span className="tab-panes" title={`${tts.length} panes`}><Icon name="split-right" size={10} />{tts.length}</span>}
              {t && <span className={`tab-privacy ${t.shares?.length ? 'shared' : ''}`} title={t.shares?.length ? (t.owner_name && t.owner_id !== user?.id ? `Shared by ${t.owner_name}` : 'Shared') : 'Private'}>
                <Icon name={t.shares?.length ? 'users' : 'lock'} size={10} /></span>}
              <button type="button" className="tab-x" aria-label="Close tab" title="Close tab (middle-click)" onClick={e => { e.stopPropagation(); close([id]) }}><Icon name="x" size={12} /></button>
            </div>
          )
        })}
      </div>
      <button type="button" className="tab-new" onClick={() => navigate('/')} aria-label="New thread" title="New thread"><Icon name="plus" size={16} /></button>
      {(user?.role === 'admin' || user?.can_run) && <MenuButton label="New terminal" className="tab-new" width={240} items={machines}><Icon name="terminal" size={15} /></MenuButton>}
      <MenuButton label="Tab options" className="tab-more" align="right" width={250} items={[
        { label: 'Close this tab', icon: 'x', disabled: i < 0, onSelect: () => close([active]) },
        { label: 'Close other tabs', disabled: i < 0 || shown.length < 2, onSelect: () => close(shown.filter(x => x !== active)) },
        { label: 'Close tabs to the right', disabled: i < 0 || i === shown.length - 1, onSelect: () => close(shown.slice(i + 1)) },
        'sep',
        { label: 'Reopen closed tab', icon: 'history', hint: keyCaps(keys.reopen_tab).join(''), disabled: !closed.length, onSelect: () => reopen() },
        ...(closed.length > 1 ? [{ heading: 'Recently closed' }, ...closed.slice(0, 8).map(id => {
          const t = byId.get(id)
          return { label: <span className="ellipsis">{t ? threadTitle(t) : '…'}</span>, icon: t?.provider === 'codex' ? 'codex' : 'claude', onSelect: () => reopen(id) }
        }), 'sep' as const] : ['sep' as const]),
        { label: 'Close all tabs', danger: true, onSelect: () => close(shown) },
      ]}><Icon name="more" size={16} /></MenuButton>
      {ctx.menu}{tm.dialogs}
    </div>
  )
}
