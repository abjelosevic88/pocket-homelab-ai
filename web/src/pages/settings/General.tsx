import { useEffect, useState } from 'react'
import { useApp } from '../../lib/app-context'
import { Icon } from '../../components/icons'
import { Loading, Segmented, SettingRow, Switch } from '../../components/ui'
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
      </Section>

      <Section title="Composer">
        <SettingRow title="Enter sends" help="On a computer: Enter sends, Shift+Enter makes a new line. Off: ⌘/Ctrl+Enter sends. Phones always use the send button.">
          <Switch label="Enter sends" checked={s.enter_sends !== false} onChange={v => save({ enter_sends: v })} />
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
