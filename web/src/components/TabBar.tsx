// Threads as tabs along the top, like Paseo or a browser: every thread you
// open gets one. Click to switch, drag to reorder, middle-click or ✕ to
// close. The list is kept per browser (localStorage).
import { useCallback, useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { threadTitle } from '../lib/format'
import type { Thread } from '../lib/types'
import { Icon } from './icons'
import { type MenuItem, MenuButton, useContextMenu } from './ui'
import { useThreadMenu } from './ThreadMenu'

const KEY = 'pai-tabs'
const load = (): string[] => { try { const v = JSON.parse(localStorage.getItem(KEY) || '[]'); return Array.isArray(v) ? v.filter(x => typeof x === 'string') : [] } catch { return [] } }

/** The open tabs; opening a thread adds its tab. */
export function useTabs(enabled: boolean) {
  const [tabs, setTabs] = useState<string[]>(load)
  const loc = useLocation()
  const navigate = useNavigate()
  const active = /^\/t\/([^/]+)/.exec(loc.pathname)?.[1] || ''
  useEffect(() => { localStorage.setItem(KEY, JSON.stringify(tabs)) }, [tabs])
  useEffect(() => { if (enabled && active) setTabs(t => t.includes(active) ? t : [...t, active]) }, [enabled, active])

  const close = useCallback((ids: string[]) => {
    const next = tabs.filter(x => !ids.includes(x))
    setTabs(next)
    if (active && ids.includes(active)) { // closing the open tab: go to its neighbour
      const to = next[Math.min(tabs.indexOf(active), next.length - 1)]
      navigate(to ? `/t/${to}` : '/')
    }
  }, [tabs, active, navigate])
  const move = useCallback((id: string, before: string) => setTabs(t => {
    if (id === before) return t
    const rest = t.filter(x => x !== id)
    const i = before ? rest.indexOf(before) : rest.length
    rest.splice(i < 0 ? rest.length : i, 0, id)
    return rest
  }), [])
  return { tabs, active, close, move }
}

export function TabBar({ tabs, active, close, move }: ReturnType<typeof useTabs>) {
  const { threads, running, user } = useApp()
  const navigate = useNavigate()
  const [drag, setDrag] = useState('')
  const ctx = useContextMenu()
  const tm = useThreadMenu()
  const byId = new Map((threads || []).map(t => [t.id, t] as [string, Thread]))
  // tabs of threads that are gone (deleted or archived) drop out — except the
  // one on screen (an archived thread you opened keeps its tab)
  const gone = threads ? tabs.filter(id => !byId.has(id) && id !== active) : []
  const shown = tabs.filter(id => !gone.includes(id))
  useEffect(() => { if (gone.length) close(gone) }, [gone.join()]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!shown.length) return null
  const working = (t?: Thread) => !!t && (t.status === 'running' || t.status === 'waiting' || t.pending > 0 || running.some(r => r.thread_id === t.id || (!!t.provider_session_id && r.session_id === t.provider_session_id)))
  const i = shown.indexOf(active)
  const tabMenu = (id: string, t?: Thread): MenuItem[] => {
    const at = shown.indexOf(id)
    return [
      ...(t ? [...tm.items(t), 'sep' as const] : []),
      { label: 'Close tab', icon: 'x', onSelect: () => close([id]) },
      { label: 'Close other tabs', disabled: shown.length < 2, onSelect: () => close(shown.filter(x => x !== id)) },
      { label: 'Close tabs to the right', disabled: at === shown.length - 1, onSelect: () => close(shown.slice(at + 1)) },
    ]
  }

  return (
    <div className="tabbar" role="tablist" aria-label="Open threads">
      <div className="tabbar-scroll">
        {shown.map(id => {
          const t = byId.get(id)
          const on = id === active
          const busy = working(t)
          return (
            <div key={id} role="tab" aria-selected={on} tabIndex={0} draggable
              className={`tab ${on ? 'on' : ''} ${drag === id ? 'dragging' : ''} ${t?.unread && !on ? 'unread' : ''}`}
              title={t ? threadTitle(t) : ''}
              onClick={() => navigate(`/t/${id}`)}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); navigate(`/t/${id}`) } }}
              onContextMenu={e => ctx.open(e, () => tabMenu(id, byId.get(id)))}
              onAuxClick={e => { if (e.button === 1) { e.preventDefault(); close([id]) } }}
              onMouseDown={e => { if (e.button === 1) e.preventDefault() }}
              onDragStart={e => { setDrag(id); e.dataTransfer.effectAllowed = 'move' }}
              onDragEnd={() => setDrag('')}
              onDragOver={e => { if (drag) { e.preventDefault(); if (drag !== id) move(drag, id) } }}>
              <span className={`tab-ico ${t?.provider || ''}`}>
                {busy ? <span className={`tab-busy ${t?.status === 'waiting' || (t?.pending || 0) > 0 ? 'waiting' : ''}`} role="img" aria-label="Working" />
                  : t?.unread && !on ? <span className="tab-done" role="img" aria-label="Done, new reply" title="Done — new reply" />
                  : <Icon name={t?.provider === 'codex' ? 'codex' : 'claude'} size={13} />}
              </span>
              <span className="tab-title">{t ? threadTitle(t) : '…'}</span>
              {t && <span className={`tab-privacy ${t.shares?.length ? 'shared' : ''}`} title={t.shares?.length ? (t.owner_name && t.owner_id !== user?.id ? `Shared by ${t.owner_name}` : 'Shared') : 'Private'}>
                <Icon name={t.shares?.length ? 'users' : 'lock'} size={10} /></span>}
              <button type="button" className="tab-x" aria-label="Close tab" title="Close tab (middle-click)" onClick={e => { e.stopPropagation(); close([id]) }}><Icon name="x" size={12} /></button>
            </div>
          )
        })}
      </div>
      <button type="button" className="tab-new" onClick={() => navigate('/')} aria-label="New thread" title="New thread"><Icon name="plus" size={16} /></button>
      <MenuButton label="Tab options" className="tab-more" align="right" width={210} items={[
        { label: 'Close this tab', icon: 'x', disabled: i < 0, onSelect: () => close([active]) },
        { label: 'Close other tabs', disabled: i < 0 || shown.length < 2, onSelect: () => close(shown.filter(x => x !== active)) },
        { label: 'Close tabs to the right', disabled: i < 0 || i === shown.length - 1, onSelect: () => close(shown.slice(i + 1)) },
        'sep',
        { label: 'Close all tabs', danger: true, onSelect: () => close(shown) },
      ]}><Icon name="more" size={16} /></MenuButton>
      {ctx.menu}{tm.dialogs}
    </div>
  )
}
