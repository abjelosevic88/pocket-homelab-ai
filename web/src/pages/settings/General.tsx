import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { Icon } from '../../components/icons'
import { Loading, Segmented, SettingRow, Switch } from '../../components/ui'
import { Select } from '../../components/Select'
import { Section, useSaveSettings } from './common'
import { comboOf, isMac, keyCaps, SHORTCUTS, type ShortcutId, useShortcuts } from '../../lib/shortcuts'

export function GeneralTab() {
  const { settings, user, themeMode, setThemeMode } = useApp()
  const save = useSaveSettings()
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

      <Section title="Composer">
        <SettingRow title="Enter sends" help="On a computer: Enter sends, Shift+Enter makes a new line. Off: Enter makes a new line; ⌘/Ctrl+Enter uses the alternate send action. Phones always use the send button.">
          <Switch label="Enter sends" checked={s.enter_sends !== false} onChange={v => save({ enter_sends: v })} />
        </SettingRow>
        <SettingRow title="Send behavior" help={`While working, ${s.enter_sends === false ? 'the send button' : 'Enter and the send button'} ${s.enter_send_now ? 'send immediately; ⌘/Ctrl+Enter queues' : 'queue; ⌘/Ctrl+Enter sends immediately'}. Sending immediately interrupts the current turn.`}>
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
