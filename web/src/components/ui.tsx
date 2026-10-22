import React, { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './icons'

// ---------- toasts (with an optional action, e.g. Undo) ----------

type ToastItem = { id: number; msg: React.ReactNode; kind: 'ok' | 'err'; action?: { label: string; run: () => void } }
type ToastFn = (msg: React.ReactNode, kind?: 'ok' | 'err', action?: { label: string; run: () => void }) => void
const ToastCtx = createContext<{ toast: ToastFn }>({ toast: () => {} })
export function useToast() { return useContext(ToastCtx) }
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const drop = (id: number) => setItems(t => t.filter(x => x.id !== id))
  const toast = useCallback<ToastFn>((msg, kind = 'ok', action) => {
    const id = Date.now() + Math.random()
    setItems(t => [...t.slice(-2), { id, msg, kind, action }])
    setTimeout(() => drop(id), action ? 8000 : kind === 'err' ? 6000 : 3000)
  }, [])
  return (
    <ToastCtx.Provider value={{ toast }}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map(t => (
          <div key={t.id} className={`toast ${t.kind}`}>
            {t.kind === 'err' && <Icon name="alert" size={15} />}
            <span className="grow">{t.msg}</span>
            {t.action && <button type="button" className="toast-action" onClick={() => { t.action!.run(); drop(t.id) }}>{t.action.label}</button>}
            <button type="button" className="toast-x" aria-label="Dismiss" onClick={() => drop(t.id)}><Icon name="x" size={14} /></button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

// ---------- hooks ----------

export function useMediaQuery(q: string) {
  const [m, setM] = useState(() => window.matchMedia(q).matches)
  useEffect(() => {
    const mq = window.matchMedia(q)
    const h = () => setM(mq.matches)
    mq.addEventListener('change', h)
    h()
    return () => mq.removeEventListener('change', h)
  }, [q])
  return m
}
export const useIsMobile = () => useMediaQuery('(max-width: 760px)')

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const run = useCallback(() => {
    setLoading(true)
    return fn().then(d => { setData(d); setError('') }).catch(e => setError((e as Error).message)).finally(() => setLoading(false))
  }, deps) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { run() }, [run])
  return { data, setData, error, loading, reload: run }
}

export function useDebounce<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value)
  useEffect(() => { const t = setTimeout(() => setV(value), ms); return () => clearTimeout(t) }, [value, ms])
  return v
}

/** Close on a click outside `ref` or Escape. */
export function useDismiss(ref: React.RefObject<HTMLElement | null>, onClose: () => void, active = true) {
  const cb = useRef(onClose)
  cb.current = onClose
  useEffect(() => {
    if (!active) return
    const down = (e: PointerEvent) => {
      const t = e.target as Element
      if (ref.current && !ref.current.contains(t) && !t.closest?.('[data-keep-open]')) cb.current()
    }
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape' && !(e.target as Element)?.closest?.('.select-pop')) { e.stopPropagation(); cb.current() } }
    const id = setTimeout(() => document.addEventListener('pointerdown', down, true), 0)
    document.addEventListener('keydown', key, true)
    return () => { clearTimeout(id); document.removeEventListener('pointerdown', down, true); document.removeEventListener('keydown', key, true) }
  }, [ref, active])
}

// ---------- layout helpers ----------

export function Field({ label, help, children, className = '', group = false }: { label: React.ReactNode; help?: React.ReactNode; children: React.ReactNode; className?: string; group?: boolean }) {
  const Tag = group ? 'div' : 'label'
  return <Tag className={`field ${className}`}><span className="label">{label}</span>{children}{help && <span className="help">{help}</span>}</Tag>
}

export function Card({ title, sub, actions, children, flush, className = '' }: { title?: React.ReactNode; sub?: React.ReactNode; actions?: React.ReactNode; children: React.ReactNode; flush?: boolean; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || actions) && <div className="card-head"><div className="grow"><h2>{title}</h2>{sub && <p className="muted small">{sub}</p>}</div>{actions && <div className="row">{actions}</div>}</div>}
      <div className={`card-body ${flush ? 'flush' : ''}`}>{children}</div>
    </section>
  )
}

export function Switch({ checked, onChange, label, disabled, size = '' }: { checked: boolean; onChange: (v: boolean) => void; label?: string; disabled?: boolean; size?: '' | 'sm' }) {
  return (
    <button type="button" role="switch" aria-checked={checked} aria-label={label} disabled={disabled} className={`switch ${size} ${checked ? 'on' : ''}`} onClick={e => { e.stopPropagation(); onChange(!checked) }}>
      <span className="knob" />
    </button>
  )
}

/** A row with a title, explanation and a control on the right. */
export function SettingRow({ title, help, children, stack }: { title: React.ReactNode; help?: React.ReactNode; children: React.ReactNode; stack?: boolean }) {
  return (
    <div className={`setting-row ${stack ? 'stack' : ''}`}>
      <div className="setting-text"><div className="setting-title">{title}</div>{help && <div className="setting-help">{help}</div>}</div>
      <div className="setting-control">{children}</div>
    </div>
  )
}

export function Segmented<T extends string | number>({ options, value, onChange, size = '', className = '', label }: { options: { value: T; label: React.ReactNode; title?: string }[]; value: T | null; onChange: (v: T) => void; size?: '' | 'sm'; className?: string; label?: string }) {
  return (
    <div className={`segmented ${size} ${className}`} role="radiogroup" aria-label={label}>
      {options.map(o => <button key={String(o.value)} type="button" role="radio" aria-checked={o.value === value} title={o.title} className={o.value === value ? 'active' : ''} onClick={() => onChange(o.value)}>{o.label}</button>)}
    </div>
  )
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string; icon?: string }[]; value: T; onChange: (t: T) => void }) {
  return (
    <nav className="tabs" role="tablist">
      {tabs.map(t => <button key={t.id} role="tab" aria-selected={t.id === value} type="button" className={t.id === value ? 'active' : ''} onClick={() => onChange(t.id)}>{t.icon && <Icon name={t.icon} size={15} />}{t.label}</button>)}
    </nav>
  )
}

export function Loading({ label = 'Loading…' }: { label?: string }) { return <div className="loading"><span className="spinner" /> {label}</div> }
export function Spinner({ size = 14 }: { size?: number }) { return <span className="spinner" style={{ width: size, height: size }} /> }

export function Empty({ title, children, icon }: { title: string; children?: React.ReactNode; icon?: string }) {
  return <div className="empty">{icon && <div className="empty-ico"><Icon name={icon} size={22} /></div>}<h3>{title}</h3>{children && <div className="muted">{children}</div>}</div>
}

export function Avatar({ name, email, size = 24, color }: { name?: string; email?: string; size?: number; color?: string }) {
  const label = (name || email || '?').trim()
  const initials = label.split(/[\s@.]+/).filter(Boolean).slice(0, 2).map(s => s[0]!.toUpperCase()).join('')
  const hue = [...(email || label)].reduce((a, c) => (a * 31 + c.charCodeAt(0)) % 360, 7)
  return <span className="avatar" style={{ width: size, height: size, fontSize: size * 0.4, background: color || `hsl(${hue} 55% 52%)` }} aria-hidden="true">{initials}</span>
}

export function Kbd({ children }: { children: React.ReactNode }) { return <kbd>{children}</kbd> }

// ---------- modal / sheet ----------

/** True for ~400 ms after mount: a tap that opened something also sends a
 *  compatibility mousedown/click at the same spot, which must not close it. */
function useJustOpened() {
  const t = useRef(Date.now())
  return () => Date.now() - t.current < 400
}

let openModals = 0
/** Is a dialog open? Then page shortcuts stay quiet. */
export function modalOpen(): boolean { return openModals > 0 || !!document.querySelector('.modal-backdrop, .palette-backdrop') }

export function Modal({ title, onClose, children, size = '', footer, className = '', bare = false, labelledBy }: { title?: React.ReactNode; onClose: () => void; children: React.ReactNode; size?: '' | 'sm' | 'lg' | 'xl'; footer?: React.ReactNode; className?: string; bare?: boolean; labelledBy?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const close = useRef(onClose)
  close.current = onClose
  const fresh = useJustOpened()
  useEffect(() => {
    openModals++
    const prev = document.activeElement as HTMLElement | null
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        // only the top-most dialog closes
        const all = document.querySelectorAll('.modal-backdrop')
        if (all[all.length - 1] === ref.current?.parentElement) { e.preventDefault(); close.current() }
      }
    }
    window.addEventListener('keydown', h)
    if (!ref.current?.contains(document.activeElement)) {
      // On touch screens focusing a text field opens the keyboard over the
      // dialog: only fields marked autofocus get focus there.
      const touch = window.matchMedia('(pointer: coarse)').matches
      const first = ref.current?.querySelector<HTMLElement>(touch ? '[autofocus]' : '[autofocus], input:not([type=hidden]), textarea, button.primary')
      first?.focus({ preventScroll: true })
    }
    return () => { openModals--; window.removeEventListener('keydown', h); prev?.focus?.() }
  }, [])
  return createPortal(
    <div className="modal-backdrop" onMouseDown={e => { if (e.target === e.currentTarget && !fresh()) onClose() }}>
      <div ref={ref} className={`modal ${size} ${className}`} role="dialog" aria-modal="true" aria-labelledby={labelledBy}>
        {!bare && <div className="modal-head"><h2>{title}</h2><button type="button" className="btn ghost icon-only" onClick={onClose} aria-label="Close"><Icon name="x" size={18} /></button></div>}
        <div className={bare ? '' : 'modal-body'}>{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>, document.body)
}

export function Confirm({ title, message, onConfirm, onCancel, danger = true, confirmLabel = 'Delete' }: { title: string; message: React.ReactNode; onConfirm: () => void; onCancel: () => void; danger?: boolean; confirmLabel?: string }) {
  return (
    <Modal title={title} onClose={onCancel} size="sm" footer={<>
      <button type="button" className="btn" onClick={onCancel}>Cancel</button>
      <button type="button" className={`btn ${danger ? 'danger' : 'primary'}`} onClick={onConfirm} autoFocus>{confirmLabel}</button>
    </>}>
      <div className="confirm-msg">{message}</div>
    </Modal>
  )
}

// ---------- popovers anchored to a rect (bottom sheet on phones) ----------

export type Anchor = { x: number; y: number; w: number; h: number }
export const rectOf = (el: Element): Anchor => { const r = el.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height } }

export function Popover({ anchor, onClose, children, prefer = 'side', width = 360, className = '', sheetOnMobile = true, label }: { anchor: Anchor; onClose: () => void; children: React.ReactNode; prefer?: 'side' | 'below'; width?: number; className?: string; sheetOnMobile?: boolean; label?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const mobile = useIsMobile()
  const sheet = mobile && sheetOnMobile
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  const fresh = useJustOpened()
  useDismiss(ref, () => { if (!fresh()) onClose() })
  useLayoutEffect(() => {
    if (sheet || !ref.current) return
    const el = ref.current
    const ph = el.offsetHeight, pw = el.offsetWidth
    const vw = window.innerWidth, vh = window.innerHeight, m = 10
    let left: number, top: number
    if (prefer === 'side') {
      const right = anchor.x + anchor.w + 8
      const leftSide = anchor.x - pw - 8
      if (right + pw <= vw - m) left = right
      else if (leftSide >= m) left = leftSide
      else left = Math.max(m, Math.min(vw - pw - m, anchor.x + anchor.w / 2 - pw / 2))
      top = Math.max(m, Math.min(vh - ph - m, anchor.y + Math.min(anchor.h, 40) / 2 - 40))
    } else {
      left = Math.max(m, Math.min(vw - pw - m, anchor.x))
      top = anchor.y + anchor.h + 6
      if (top + ph > vh - m) top = Math.max(m, anchor.y - ph - 6)
    }
    setPos({ left, top })
  }, [anchor.x, anchor.y, anchor.w, anchor.h, prefer, sheet, children])
  if (sheet) {
    return createPortal(
      <div className="sheet-backdrop" onMouseDown={e => { if (e.target === e.currentTarget && !fresh()) onClose() }}>
        <div ref={ref} className={`sheet ${className}`} role="dialog" aria-label={label}>
          <div className="sheet-grip" />
          {children}
        </div>
      </div>, document.body)
  }
  return createPortal(
    <div ref={ref} className={`popover ${className}`} role="dialog" aria-label={label} style={{ width, left: pos?.left ?? 0, top: pos?.top ?? 0, opacity: pos ? undefined : 0 }}>
      {children}
    </div>, document.body)
}

// ---------- menus ----------

export type MenuItem = { label: React.ReactNode; icon?: string; onSelect?: () => void; danger?: boolean; disabled?: boolean; hint?: React.ReactNode; checked?: boolean } | 'sep' | { heading: string }

export function Menu({ items, onClose, anchor, align = 'left', width = 230 }: { items: MenuItem[]; onClose: () => void; anchor: Anchor; align?: 'left' | 'right'; width?: number }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  const [hi, setHi] = useState(-1)
  useDismiss(ref, onClose)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const vw = window.innerWidth, vh = window.innerHeight
    let left = align === 'right' ? anchor.x + anchor.w - el.offsetWidth : anchor.x
    left = Math.max(8, Math.min(vw - el.offsetWidth - 8, left))
    let top = anchor.y + anchor.h + 4
    if (top + el.offsetHeight > vh - 8) top = Math.max(8, anchor.y - el.offsetHeight - 4)
    setPos({ left, top })
    el.focus()
  }, [anchor.x, anchor.y, anchor.w, anchor.h, align])
  const actionable = items.map((it, i) => (typeof it === 'object' && 'label' in it && !it.disabled ? i : -1)).filter(i => i >= 0)
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const idx = actionable.indexOf(hi)
      const next = e.key === 'ArrowDown' ? actionable[(idx + 1) % actionable.length] : actionable[(idx - 1 + actionable.length) % actionable.length]
      setHi(next ?? -1)
    } else if (e.key === 'Enter' && hi >= 0) {
      e.preventDefault()
      const it = items[hi]
      if (typeof it === 'object' && 'label' in it) { onClose(); it.onSelect?.() }
    }
  }
  return createPortal(
    <div ref={ref} tabIndex={-1} className="menu" role="menu" style={{ minWidth: width, left: pos?.left ?? -9999, top: pos?.top ?? -9999 }} onKeyDown={onKey}>
      {items.map((it, i) => {
        if (it === 'sep') return <div key={i} className="menu-sep" />
        if ('heading' in it) return <div key={i} className="menu-heading">{it.heading}</div>
        return (
          <button key={i} type="button" role="menuitem" disabled={it.disabled} className={`menu-item ${it.danger ? 'danger' : ''} ${hi === i ? 'hi' : ''}`} onMouseEnter={() => setHi(i)}
            onClick={() => { onClose(); it.onSelect?.() }}>
            <span className="menu-ico">{it.checked !== undefined ? (it.checked ? <Icon name="check" size={15} /> : null) : it.icon ? <Icon name={it.icon} size={15} /> : null}</span>
            <span className="grow">{it.label}</span>
            {it.hint && <span className="menu-hint">{it.hint}</span>}
          </button>
        )
      })}
    </div>, document.body)
}

/** A button that opens a Menu under itself. */
export function MenuButton({ items, children, className = 'btn ghost icon-only', label, align = 'left', width }: { items: MenuItem[] | (() => MenuItem[]); children: React.ReactNode; className?: string; label: string; align?: 'left' | 'right'; width?: number }) {
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  return (
    <>
      <button type="button" className={className} aria-label={label} title={label} aria-haspopup="menu" aria-expanded={!!anchor} onClick={e => { e.stopPropagation(); setAnchor(anchor ? null : rectOf(e.currentTarget)) }}>{children}</button>
      {anchor && <Menu anchor={anchor} align={align} width={width} items={typeof items === 'function' ? items() : items} onClose={() => setAnchor(null)} />}
    </>
  )
}

// ---------- misc ----------

export async function copyText(text: string): Promise<boolean> {
  try { await navigator.clipboard.writeText(text); return true } catch {
    const ta = document.createElement('textarea')
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
    document.body.appendChild(ta); ta.select()
    let ok = false
    try { ok = document.execCommand('copy') } catch { ok = false }
    ta.remove()
    return ok
  }
}

export function CopyField({ value, label, secret }: { value: string; label?: string; secret?: boolean }) {
  const { toast } = useToast()
  const [shown, setShown] = useState(!secret)
  return (
    <div className="copy-field">
      <input readOnly value={shown ? value : value.replace(/[^/]{12,}(?=\.ics$)/, m => '•'.repeat(Math.min(m.length, 24)))} onFocus={e => e.target.select()} aria-label={label} />
      {secret && <button type="button" className="btn ghost sm" onClick={() => setShown(s => !s)}>{shown ? 'Hide' : 'Show'}</button>}
      <button type="button" className="btn sm" onClick={async () => toast(await copyText(value) ? 'Copied' : 'Could not copy', 'ok')}><Icon name="copy" size={14} />Copy</button>
    </div>
  )
}

export function download(url: string) {
  const a = document.createElement('a')
  a.href = url; a.download = ''
  document.body.appendChild(a); a.click(); a.remove()
}

export function errMsg(e: unknown): string { return (e as Error)?.message || String(e) }
