// Inventory's always-searchable picker pattern, with a portal to avoid clipping
// inside composers and dialogs, and visual-viewport positioning for keyboards.
import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './icons'
import { type Anchor, rectOf } from './ui'

export interface Option<T> {
  value: T
  label: string
  /** Extra words that match the search, e.g. a country code. */
  keywords?: string
  hint?: React.ReactNode
  description?: React.ReactNode
  color?: string
  icon?: React.ReactNode
  group?: string
  disabled?: boolean
}

interface Props<T> {
  value: T
  options: Option<T>[]
  onChange: (v: T) => void
  placeholder?: string
  searchable?: boolean
  className?: string
  label?: string
  disabled?: boolean
  /** Render the trigger's content; default: dot/icon + label. */
  renderValue?: (o: Option<T> | undefined) => React.ReactNode
  width?: number | 'trigger'
  size?: '' | 'sm'
  id?: string
  /** groups become tabs above the list (one group shown at a time; filtering searches all) */
  tabs?: boolean
  /** an icon in front of a group's name (tabs and headings) */
  groupIcon?: (group: string) => React.ReactNode
}

export function Select<T extends string | number>({ value, options, onChange, placeholder = 'Choose…', searchable, className = '', label, disabled, renderValue, width = 'trigger', size = '', id, tabs, groupIcon }: Props<T>) {
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const lid = useId()
  const current = options.find(o => o.value === value)
  const search = searchable ?? true
  const open = () => { if (!disabled && btn.current) setAnchor(rectOf(btn.current)) }
  return (
    <>
      <button id={id} ref={btn} type="button" className={`select-trigger ${size} ${className}`} disabled={disabled} aria-haspopup="listbox" aria-expanded={!!anchor} aria-controls={anchor ? lid : undefined} aria-label={label}
        onClick={() => (anchor ? setAnchor(null) : open())}
        onKeyDown={e => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); open() } }}>
        <span className="select-value">
          {renderValue ? renderValue(current) : current ? <>{current.color && <span className="dot" style={{ background: current.color }} />}{current.icon}<span className="ellipsis">{current.label}</span></> : <span className="placeholder">{placeholder}</span>}
        </span>
        <Icon name="chevron-down" size={14} className="select-caret" />
      </button>
      {anchor && <OptionList trigger={btn} lid={lid} label={label} options={options} value={value} search={search} tabs={tabs} groupIcon={groupIcon} width={width === 'trigger' ? Math.max(anchor.w, 200) : width}
        onPick={v => { onChange(v); setAnchor(null); btn.current?.focus({ preventScroll: true }) }} onClose={restore => { setAnchor(null); if (restore) btn.current?.focus({ preventScroll: true }) }} />}
    </>
  )
}

export function OptionList<T extends string | number>({ trigger, lid, label, options, value, search, width, onPick, onClose, emptyText = 'No matches', footer, tabs, groupIcon }: { trigger: React.RefObject<HTMLButtonElement | null>; lid: string; label?: string; options: Option<T>[]; value?: T; search: boolean; width: number; onPick: (v: T) => void; onClose: (restore?: boolean) => void; emptyText?: string; footer?: React.ReactNode; tabs?: boolean; groupIcon?: (group: string) => React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const [q, setQ] = useState('')
  const groups = useMemo(() => tabs ? [...new Set(options.map(o => o.group).filter((g): g is string => !!g))] : [], [tabs, options])
  const tabbed = groups.length > 1
  const [tab, setTab] = useState(() => options.find(o => o.value === value)?.group || groups[0])
  // a tab shows its own group; typing searches every group
  const inTab = useMemo(() => tabbed ? options.filter(o => o.group === tab) : options, [tabbed, tab, options])
  const [hi, setHi] = useState(() => Math.max(0, inTab.findIndex(o => o.value === value && !o.disabled)))
  const [pos, setPos] = useState<{ left: number; top: number; maxH: number; width: number } | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    input.current?.focus({ preventScroll: true })
    const down = (e: PointerEvent) => {
      const target = e.target as Node
      if (!ref.current?.contains(target) && !trigger.current?.contains(target)) close.current()
    }
    document.addEventListener('pointerdown', down, true)
    return () => document.removeEventListener('pointerdown', down, true)
  }, [trigger])
  const shown = useMemo(() => {
    const n = q.trim().toLowerCase()
    if (!n) return inTab
    const words = n.split(/\s+/)
    return options.filter(o => { const hay = `${o.label} ${o.keywords || ''} ${o.group || ''} ${typeof o.hint === 'string' ? o.hint : ''} ${typeof o.description === 'string' ? o.description : ''}`.toLowerCase(); return words.every(w => hay.includes(w)) })
  }, [q, options, inTab])
  useEffect(() => { const i = shown.findIndex(o => o.value === value); setHi(i >= 0 && !q ? i : Math.max(0, shown.findIndex(o => !o.disabled))) }, [q, tab]) // eslint-disable-line react-hooks/exhaustive-deps
  const tabsH = tabbed ? 41 : 0
  useLayoutEffect(() => {
    let raf = 0
    const place = () => {
      if (!ref.current || !trigger.current || !list.current) return
      const a = trigger.current.getBoundingClientRect(), vv = window.visualViewport
      const x = vv?.offsetLeft ?? 0, y = vv?.offsetTop ?? 0
      const vw = vv?.width ?? innerWidth, vh = vv?.height ?? innerHeight
      const w = Math.min(width, vw - 16)
      const below = Math.max(0, y + vh - a.bottom - 12), above = Math.max(0, a.top - y - 12)
      const desired = Math.min(360, list.current.scrollHeight + (search ? 44 : 0) + tabsH)
      const down = desired <= below || below >= above
      const maxH = Math.max(44, Math.min(360, vh - 16, down ? below : above))
      const top = down ? a.bottom + 4 : a.top - Math.min(desired, maxH) - 4
      setPos({ left: Math.max(x + 8, Math.min(x + vw - w - 8, a.left)), top: Math.max(y + 8, Math.min(y + vh - Math.min(desired, maxH) - 8, top)), width: w, maxH })
    }
    const schedule = (e?: Event) => {
      if (e?.target instanceof Node && ref.current?.contains(e.target)) return
      cancelAnimationFrame(raf); raf = requestAnimationFrame(place)
    }
    place()
    const observer = new ResizeObserver(() => schedule())
    if (trigger.current) observer.observe(trigger.current)
    window.addEventListener('resize', schedule)
    window.addEventListener('scroll', schedule, true)
    window.visualViewport?.addEventListener('resize', schedule)
    window.visualViewport?.addEventListener('scroll', schedule)
    return () => {
      cancelAnimationFrame(raf); observer.disconnect()
      window.removeEventListener('resize', schedule)
      window.removeEventListener('scroll', schedule, true)
      window.visualViewport?.removeEventListener('resize', schedule)
      window.visualViewport?.removeEventListener('scroll', schedule)
    }
  }, [trigger, width, search, shown, tabsH])
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-i="${hi}"]`), parent = list.current
    if (!el || !parent) return
    // Scroll only the options, not the page or Safari's viewport.
    const row = el.getBoundingClientRect(), view = parent.getBoundingClientRect()
    if (row.top < view.top) parent.scrollTop -= view.top - row.top
    else if (row.bottom > view.bottom) parent.scrollTop += row.bottom - view.bottom
  }, [hi, pos])
  useEffect(() => { if (!search) ref.current?.focus({ preventScroll: true }) }, [search])
  const key = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const step = e.key === 'ArrowDown' ? 1 : -1
      let next = hi + step
      while (next >= 0 && next < shown.length && shown[next].disabled) next += step
      if (next >= 0 && next < shown.length) setHi(next)
    }
    else if (e.key === 'Enter') { e.preventDefault(); const o = shown[hi]; if (o && !o.disabled) onPick(o.value) }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(true) }
    else if (e.key === 'Tab') onClose(true)
    else if (tabbed && !q && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
      e.preventDefault()
      const i = groups.indexOf(tab || '') + (e.key === 'ArrowRight' ? 1 : -1)
      if (i >= 0 && i < groups.length) setTab(groups[i])
    }
  }
  let lastGroup: string | undefined
  return createPortal(
    <div ref={ref} className="select-pop" tabIndex={-1} style={{ width: pos?.width ?? width, maxHeight: pos?.maxH ?? 360, left: pos?.left ?? -9999, top: pos?.top ?? -9999 }} onKeyDown={key} data-keep-open>
      {search && <div className="select-search"><Icon name="search" size={14} /><input ref={input} value={q} onChange={e => setQ(e.target.value)} placeholder="Type to filter…" aria-label={label ? `Filter ${label}` : 'Filter options'} autoComplete="off" role="combobox" aria-autocomplete="list" aria-expanded="true" aria-activedescendant={shown[hi] ? `${lid}-${hi}` : undefined} aria-controls={lid} /></div>}
      {tabbed && <div className={`select-tabs ${q.trim() ? 'searching' : ''}`} role="tablist" aria-label={label}>
        {groups.map(g => <button key={g} type="button" role="tab" aria-selected={g === tab} className={g === tab ? 'active' : ''} onMouseDown={e => e.preventDefault()} onClick={() => { setTab(g); setQ('') }}>{groupIcon?.(g)}{g}</button>)}
      </div>}
      <div ref={list} className="select-list" role="listbox" aria-label={label || 'Options'} id={lid} style={{ maxHeight: Math.max(0, (pos?.maxH ?? 360) - (search ? 44 : 0) - tabsH) }}>
        {shown.length === 0 && <div className="select-empty" role="status">{emptyText}</div>}
        {shown.map((o, i) => {
          // tabs name the group already, except while a search mixes them
          const head = o.group && o.group !== lastGroup && (!tabbed || q.trim()) ? <div className="select-group" key={'g' + o.group}>{groupIcon?.(o.group)}{o.group}</div> : null
          lastGroup = o.group
          return (
            <React.Fragment key={String(o.value)}>
              {head}
              <div id={`${lid}-${i}`} data-i={i} role="option" aria-selected={o.value === value} aria-disabled={o.disabled} className={`select-option ${i === hi ? 'hi' : ''} ${o.value === value ? 'sel' : ''} ${o.disabled ? 'disabled' : ''}`}
                onMouseEnter={() => !o.disabled && setHi(i)} onMouseDown={e => e.preventDefault()} onClick={() => !o.disabled && onPick(o.value)}>
                {o.color && <span className="dot" style={{ background: o.color }} />}
                {o.icon}
                <span className="grow"><span className="opt-label">{o.label}</span>{o.description && <span className="opt-desc">{o.description}</span>}</span>
                {o.hint && <span className="opt-hint">{o.hint}</span>}
                {o.value === value && <Icon name="check" size={14} className="opt-check" />}
              </div>
            </React.Fragment>
          )
        })}
      </div>
      {footer}
    </div>, document.body)
}
