// A searchable select (combobox) — the app has no native <select> elements.
import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './icons'
import { type Anchor, rectOf, useDismiss } from './ui'

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
}

export function Select<T extends string | number>({ value, options, onChange, placeholder = 'Choose…', searchable, className = '', label, disabled, renderValue, width = 'trigger', size = '', id }: Props<T>) {
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const current = options.find(o => o.value === value)
  const search = searchable ?? options.length > 9
  const open = () => { if (!disabled && btn.current) setAnchor(rectOf(btn.current)) }
  return (
    <>
      <button id={id} ref={btn} type="button" className={`select-trigger ${size} ${className}`} disabled={disabled} aria-haspopup="listbox" aria-expanded={!!anchor} aria-label={label}
        onClick={() => (anchor ? setAnchor(null) : open())}
        onKeyDown={e => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); open() } }}>
        <span className="select-value">
          {renderValue ? renderValue(current) : current ? <>{current.color && <span className="dot" style={{ background: current.color }} />}{current.icon}<span className="ellipsis">{current.label}</span></> : <span className="placeholder">{placeholder}</span>}
        </span>
        <Icon name="chevron-down" size={14} className="select-caret" />
      </button>
      {anchor && <OptionList anchor={anchor} options={options} value={value} search={search} width={width === 'trigger' ? Math.max(anchor.w, 200) : width}
        onPick={v => { onChange(v); setAnchor(null); btn.current?.focus() }} onClose={() => setAnchor(null)} />}
    </>
  )
}

export function OptionList<T extends string | number>({ anchor, options, value, search, width, onPick, onClose, emptyText = 'Nothing matches', footer }: { anchor: Anchor; options: Option<T>[]; value?: T; search: boolean; width: number; onPick: (v: T) => void; onClose: () => void; emptyText?: string; footer?: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const [pos, setPos] = useState<{ left: number; top: number; maxH: number } | null>(null)
  const lid = useId()
  useDismiss(ref, onClose)
  const shown = useMemo(() => {
    const n = q.trim().toLowerCase()
    if (!n) return options
    const words = n.split(/\s+/)
    return options.filter(o => { const hay = `${o.label} ${o.keywords || ''} ${o.group || ''}`.toLowerCase(); return words.every(w => hay.includes(w)) })
  }, [q, options])
  useEffect(() => { const i = shown.findIndex(o => o.value === value); setHi(i >= 0 && !q ? i : 0) }, [q]) // eslint-disable-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const vw = window.innerWidth, vh = window.innerHeight
    const below = vh - (anchor.y + anchor.h) - 12
    const above = anchor.y - 12
    const h = Math.min(el.scrollHeight, 360)
    const left = Math.max(8, Math.min(vw - width - 8, anchor.x))
    if (h <= below || below >= above) setPos({ left, top: anchor.y + anchor.h + 4, maxH: Math.min(360, below) })
    else setPos({ left, top: Math.max(8, anchor.y - Math.min(h, above) - 4), maxH: Math.min(360, above) })
  }, [anchor.x, anchor.y, anchor.w, anchor.h, width])
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-i="${hi}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [hi, pos])
  useEffect(() => { if (!search) ref.current?.focus() }, [search])
  const key = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => Math.min(shown.length - 1, h + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => Math.max(0, h - 1)) }
    else if (e.key === 'Enter') { e.preventDefault(); const o = shown[hi]; if (o && !o.disabled) onPick(o.value) }
    else if (e.key === 'Tab') onClose()
  }
  let lastGroup: string | undefined
  return createPortal(
    <div ref={ref} className="select-pop" tabIndex={-1} style={{ width, left: pos?.left ?? -9999, top: pos?.top ?? -9999 }} onKeyDown={key} data-keep-open>
      {search && <div className="select-search"><Icon name="search" size={14} /><input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Search…" aria-controls={lid} /></div>}
      <div ref={list} className="select-list" role="listbox" id={lid} style={{ maxHeight: (pos?.maxH ?? 360) - (search ? 44 : 0) }}>
        {shown.length === 0 && <div className="select-empty">{emptyText}</div>}
        {shown.map((o, i) => {
          const head = o.group && o.group !== lastGroup ? <div className="select-group" key={'g' + o.group}>{o.group}</div> : null
          lastGroup = o.group
          return (
            <React.Fragment key={String(o.value)}>
              {head}
              <div data-i={i} role="option" aria-selected={o.value === value} aria-disabled={o.disabled} className={`select-option ${i === hi ? 'hi' : ''} ${o.value === value ? 'sel' : ''} ${o.disabled ? 'disabled' : ''}`}
                onMouseEnter={() => setHi(i)} onMouseDown={e => e.preventDefault()} onClick={() => !o.disabled && onPick(o.value)}>
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
