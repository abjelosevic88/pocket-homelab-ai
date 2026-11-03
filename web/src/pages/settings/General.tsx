import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { Icon } from '../../components/icons'
import { Loading, Segmented, SettingRow, Switch } from '../../components/ui'
import { Select } from '../../components/Select'
import { Section, useSaveSettings } from './common'
import { useDisplay } from '../../lib/display'
import { useIsMobile } from '../../components/ui'
import { type SidebarEntry, sidebarLayout, sidebarSetting } from '../../lib/sidebar'
import { comboOf, isMac, keyCaps, SHORTCUTS, type ShortcutId, useShortcuts } from '../../lib/shortcuts'

export function GeneralTab() {
  const { settings, user, themeMode, setThemeMode } = useApp()
  const save = useSaveSettings()
  const [display, setDisplay] = useDisplay()
  const mobile = useIsMobile()
  const [title, setTitle] = useState(settings?.title || '')
  useEffect(() => { setTitle(settings?.title || '') }, [settings?.title])
  if (!settings) return <Loading />
  const s = settings

  return (
    <div className="stack loose">
      <Section title="Appearance">
        <SettingRow title="Theme" help="System follows your device’s light or dark setting.">
          <Segmented label="Theme" value={themeMode} onChange={setThemeMode} options={[
            { value: 'system', label: <span className="row"><Icon name="monitor" size={14} />System</span> },
            { value: 'light', label: <span className="row"><Icon name="sun" size={14} />Light</span> },
            { value: 'dark', label: <span className="row"><Icon name="moon" size={14} />Dark</span> },
          ]} />
        </SettingRow>
        <SettingRow title="Show the agent’s thinking" help="Expanded by default. Off: a small “Thinking” line you can open.">
          <Switch label="Show thinking" checked={!!s.show_thinking} onChange={v => save({ show_thinking: v })} />
        </SettingRow>
        <SettingRow title="Threads as tabs" help="Every thread you open gets a tab along the top, like a browser — switch with a click, drag to reorder, middle-click or ✕ to close. On computers and tablets; phones use the menu.">
          <Switch label="Threads as tabs" checked={s.thread_tabs !== false} onChange={v => save({ thread_tabs: v })} />
        </SettingRow>
        <SettingRow title="Plan limits always in view" help="Claude’s and Codex’s limits at the bottom of the sidebar (in the menu on phones). Pick which ones with the pins on the Usage page.">
          <Switch label="Plan limits always in view" checked={s.usage_widget !== false} onChange={v => save({ usage_widget: v })} />
        </SettingRow>
      </Section>

      <SidebarSection />

      <Section title="On this device" sub="How conversations look in this browser. Your phone and your computer each keep their own.">
        <SettingRow title="Text size" help="Messages, code, and the tool and thinking rows.">
          <Segmented label="Text size" value={display.text} onChange={text => setDisplay({ text })} options={[
            { value: 's', label: 'Small' }, { value: 'm', label: 'Default' }, { value: 'l', label: 'Large' }, { value: 'xl', label: 'Larger' },
          ]} />
        </SettingRow>
        <SettingRow title="Spacing" help="Room between messages and around the “Ran a command” / “Thinking” rows.">
          <Segmented label="Spacing" value={display.spacing} onChange={spacing => setDisplay({ spacing })} options={[
            { value: 'compact', label: 'Compact' }, { value: 'default', label: 'Default' }, { value: 'roomy', label: 'Roomy' },
          ]} />
        </SettingRow>
        {!mobile && <SettingRow title="Conversation width" help="How wide messages run on a big screen.">
          <Segmented label="Conversation width" value={display.width} onChange={width => setDisplay({ width })} options={[
            { value: 'narrow', label: 'Narrow' }, { value: 'default', label: 'Default' }, { value: 'wide', label: 'Wide' }, { value: 'full', label: 'Full' },
          ]} />
        </SettingRow>}
        <div className="display-preview" aria-hidden>
          <div className="timeline">
            <div className="tl-item"><div className="msg user"><div className="bubble"><div className="user-text">How much space is left on the NAS?</div></div></div></div>
            <div className="tl-item"><div className="thinking"><span className="thinking-h">Thinking</span></div></div>
            <div className="tl-item"><div className="msg assistant"><div className="md"><p>The <code>tank</code> pool has <b>3.1 TB</b> free of 10.8 TB — about 29%.</p></div></div></div>
          </div>
        </div>
      </Section>

      <Section title="Composer">
        <SettingRow title="Enter sends" help="On a computer: Enter sends, Shift+Enter makes a new line. Off: Enter makes a new line; ⌘/Ctrl+Enter uses the alternate send action. Phones always use the send button.">
          <Switch label="Enter sends" checked={s.enter_sends !== false} onChange={v => save({ enter_sends: v })} />
        </SettingRow>
        <SettingRow title="Send behavior" help={`While working, ${s.enter_sends === false ? 'the send button' : 'Enter and the send button'} ${s.enter_send_now ? 'send immediately; ⌘/Ctrl+Enter queues' : 'queue; ⌘/Ctrl+Enter sends immediately'}. Sending immediately hands the message to the running turn after its current step, like typing in the terminal; Stop interrupts.`}>
          <Select label="Send behavior" value={s.enter_send_now ? 'now' : 'queue'} onChange={v => save({ enter_send_now: v === 'now' })} options={[
            { value: 'queue', label: 'Queue message', description: '⌘/Ctrl+Enter sends immediately'  },
            { value: 'now', label: 'Send immediately', description: '⌘/Ctrl+Enter queues'  },
          ]} />
        </SettingRow>
      </Section>

      <ShortcutsSection />

      {user?.role === 'admin' && (
        <Section title="This box" sub="Only admins see this.">
          <SettingRow title="App title" help="Shown in the browser tab and on the sign-in page.">
            <input value={title} onChange={e => setTitle(e.target.value)} onBlur={() => { if (title.trim() && title !== s.title) save({ title: title.trim() }, 'Title saved') }}
              onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} style={{ width: 240 }} aria-label="App title" />
          </SettingRow>
        </Section>
      )}
    </div>
  )
}

/** Every changeable shortcut: click its keys, press new ones. Only changes are saved. */
function ShortcutsSection() {
  const { settings } = useApp()
  const save = useSaveSettings()
  const keys = useShortcuts()
  const [rec, setRec] = useState<ShortcutId | null>(null)
  const [warn, setWarn] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const loc = useLocation()
  useEffect(() => { if (loc.hash === '#shortcuts') ref.current?.scrollIntoView({ block: 'start', behavior: 'smooth' }) }, [loc.hash])
  const mine = settings?.shortcuts || {}

  const set = (id: ShortcutId, combo: string) => {
    const next = { ...mine }
    if (combo === SHORTCUTS.find(s => s.id === id)!.keys) delete next[id]; else next[id] = combo
    void save({ shortcuts: next })
  }
  useEffect(() => {
    if (!rec) return
    const h = (e: KeyboardEvent) => {
      e.preventDefault(); e.stopPropagation()
      if (e.key === 'Escape') { setRec(null); setWarn(''); return }
      if ((e.key === 'Backspace' || e.key === 'Delete') && !e.metaKey && !e.ctrlKey && !e.altKey) { set(rec, ''); setRec(null); setWarn(''); return }
      const c = comboOf(e)
      if (!c) return
      if (!c.startsWith('mod+') && !c.startsWith('alt+')) { setWarn(`Hold ${isMac ? '⌘ or ⌥' : 'Ctrl or Alt'} with the key, so typing doesn’t trigger it.`); return }
      const other = SHORTCUTS.find(s => s.id !== rec && keys[s.id] === c)
      if (other) { setWarn(`${keyCaps(c).join(' ')} is already “${other.label}”. Change that one first.`); return }
      set(rec, c); setRec(null); setWarn('')
    }
    window.addEventListener('keydown', h, true)
    return () => window.removeEventListener('keydown', h, true)
  }, [rec, keys, mine]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div ref={ref} id="shortcuts" className="scroll-anchor">
      <Section title="Keyboard shortcuts" sub={<>Click the keys of a shortcut, then press the new ones. Backspace turns it off, Esc cancels. In a browser tab the browser keeps {isMac ? '⌘T, ⌘⇧T, ⌘N and ⌘W' : 'Ctrl+T, Ctrl+Shift+T, Ctrl+N and Ctrl+W'} for itself; they work in the installed app window.</>}
        actions={Object.keys(mine).length > 0 && <button type="button" className="btn sm" onClick={() => { setRec(null); void save({ shortcuts: {} }, 'Shortcuts reset') }}>Reset all</button>}>
        {SHORTCUTS.map(s => {
          const on = rec === s.id
          const changed = s.id in mine
          return (
            <SettingRow key={s.id} title={s.label} help={on && warn ? <span className="danger-text">{warn}</span> : s.group}>
              <span className="row" style={{ gap: 6 }}>
                {changed && !on && <button type="button" className="btn ghost icon-only sm" aria-label={`Reset ${s.label}`} title={`Back to ${s.keys ? keyCaps(s.keys).join(' ') : 'off'}`} onClick={() => set(s.id, s.keys)}><Icon name="undo" size={14} /></button>}
                <button type="button" className={`btn sm sc-record ${on ? 'recording' : ''}`} aria-label={`${s.label}: ${keys[s.id] ? keyCaps(keys[s.id]).join(' ') : 'off'}. Click to change`}
                  onClick={() => { setWarn(''); setRec(on ? null : s.id) }}>
                  {on ? 'Press keys…' : keys[s.id] ? <span className="sc-keys">{keyCaps(keys[s.id]).map((k, i) => <kbd key={i}>{k}</kbd>)}</span> : <span className="muted">Off</span>}
                </button>
              </span>
            </SettingRow>
          )
        })}
      </Section>
    </div>
  )
}

/** Which pages the sidebar lists and in what order: drag the grips, ✕ removes, + adds back. */
function SidebarSection() {
  const { settings, user } = useApp()
  const save = useSaveSettings()
  const ref = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const loc = useLocation()
  useEffect(() => { if (loc.hash === '#sidebar') ref.current?.scrollIntoView({ block: 'start', behavior: 'smooth' }) }, [loc.hash])
  const saved = useMemo(() => sidebarLayout(settings?.sidebar, user), [settings?.sidebar, user])
  // the order while a row is being dragged (saved when it is let go)
  const [draft, setDraft] = useState<SidebarEntry[] | null>(null)
  const [dragId, setDragId] = useState('')
  const layout = draft || saved
  const shown = layout.filter(e => e.on)
  const removed = layout.filter(e => !e.on)

  const store = (next: SidebarEntry[]) => { void save({ sidebar: sidebarSetting(next) }) }
  const toggle = (id: string, on: boolean) => {
    // added back items go to the end of the menu
    const rest = layout.filter(e => e.item.id !== id)
    const e = layout.find(x => x.item.id === id)!
    store(on ? [...rest.filter(x => x.on), { ...e, on }, ...rest.filter(x => !x.on)] : layout.map(x => x.item.id === id ? { ...x, on } : x))
  }
  // put the shown item `id` at position `to` among the shown ones
  const moved = (list: SidebarEntry[], id: string, to: number) => {
    const on = list.filter(e => e.on && e.item.id !== id)
    on.splice(Math.max(0, Math.min(to, on.length)), 0, list.find(e => e.item.id === id)!)
    return [...on, ...list.filter(e => !e.on)]
  }

  const down = (e: React.PointerEvent, id: string) => {
    if (e.button !== 0) return
    e.preventDefault()
    const el = e.currentTarget as HTMLElement
    el.setPointerCapture(e.pointerId)
    setDragId(id)
    let cur = saved
    setDraft(cur)
    const move = (ev: PointerEvent) => {
      const rows = [...(listRef.current?.querySelectorAll<HTMLElement>('[data-sb-id]') || [])].filter(r => r.dataset.sbId !== id)
      const to = rows.filter(r => { const b = r.getBoundingClientRect(); return b.top + b.height / 2 < ev.clientY }).length
      const next = moved(cur, id, to)
      if (next.map(x => x.item.id).join() !== cur.map(x => x.item.id).join()) { cur = next; setDraft(next) }
    }
    const up = () => {
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', up)
      el.removeEventListener('pointercancel', up)
      setDragId('')
      if (cur !== saved) store(cur)
      setDraft(null)
    }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }
  const key = (e: React.KeyboardEvent, id: string, i: number) => {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
    e.preventDefault()
    const to = i + (e.key === 'ArrowUp' ? -1 : 1)
    if (to < 0 || to >= shown.length) return
    store(moved(layout, id, to))
    requestAnimationFrame(() => listRef.current?.querySelector<HTMLElement>(`[data-sb-id="${id}"] .sbset-grip`)?.focus())
  }

  return (
    <div ref={ref} id="sidebar" className="scroll-anchor">
      <Section title="Sidebar" sub="What the sidebar lists and in what order. Drag the handles to reorder, ✕ removes. Everything stays in ⌘K. Right-click a sidebar item to remove it from there."
        actions={(settings?.sidebar?.length || 0) > 0 && <button type="button" className="btn sm" onClick={() => void save({ sidebar: [] }, 'Sidebar reset')}>Reset</button>}>
        <div className="sbset-list" ref={listRef} role="list" aria-label="In the sidebar">
          {shown.map(({ item }, i) => (
            <div key={item.id} data-sb-id={item.id} role="listitem" className={`sbset-row ${dragId === item.id ? 'dragging' : ''}`}>
              <button type="button" className="sbset-grip" aria-label={`Move ${item.label} (arrow keys)`} title="Drag to reorder"
                onPointerDown={e => down(e, item.id)} onKeyDown={e => key(e, item.id, i)}><Icon name="grip" size={14} strokeWidth={3} /></button>
              <Icon name={item.icon} size={16} />
              <span className="grow">{item.label}</span>
              <button type="button" className="btn ghost icon-only sm" aria-label={`Remove ${item.label} from the sidebar`} title="Remove from the sidebar"
                disabled={shown.length <= 1} onClick={() => toggle(item.id, false)}><Icon name="x" size={14} /></button>
            </div>
          ))}
        </div>
        {removed.length > 0 && <>
          <div className="sbset-h">Not in the sidebar</div>
          <div className="sbset-list" role="list" aria-label="Not in the sidebar">
            {removed.map(({ item }) => (
              <div key={item.id} role="listitem" className="sbset-row off">
                <span className="sbset-grip" aria-hidden />
                <Icon name={item.icon} size={16} />
                <span className="grow">{item.label}</span>
                <button type="button" className="btn ghost sm" aria-label={`Add ${item.label} to the sidebar`} onClick={() => toggle(item.id, true)}><Icon name="plus" size={14} />Add</button>
              </div>
            ))}
          </div>
        </>}
      </Section>
    </div>
  )
}
