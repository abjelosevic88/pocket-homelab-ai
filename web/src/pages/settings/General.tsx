import { useEffect, useState } from 'react'
import { useApp } from '../../lib/app-context'
import { Icon } from '../../components/icons'
import { Loading, Segmented, SettingRow, Switch } from '../../components/ui'
import { Select } from '../../components/Select'
import { Section, useSaveSettings } from './common'

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
