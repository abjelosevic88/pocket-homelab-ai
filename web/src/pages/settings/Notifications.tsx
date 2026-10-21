import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Settings } from '../../lib/types'
import { Icon } from '../../components/icons'
import { errMsg, Field, Loading, Segmented, SettingRow, Spinner, Switch, useToast } from '../../components/ui'
import { Section, useSaveSettings } from './common'

const KINDS: { value: Settings['notify_kind']; label: string; help: string; placeholder: string; token: string }[] = [
  { value: 'apprise', label: 'Apprise', help: 'An Apprise API route — it fans out to ntfy, Pushover, Telegram, email and 100+ others.', placeholder: 'https://apprise.example.com/notify/ai', token: 'Usually not needed' },
  { value: 'ntfy', label: 'ntfy', help: 'An ntfy topic URL. Install the ntfy app and subscribe to the same topic.', placeholder: 'https://ntfy.sh/my-agents', token: 'Access token for a protected topic (optional)' },
  { value: 'webhook', label: 'Webhook', help: 'A JSON POST with title, body and link to any URL you choose.', placeholder: 'https://example.com/hooks/ai', token: 'Sent as a Bearer token (optional)' },
]

function BrowserNotifications() {
  const supported = typeof Notification !== 'undefined'
  const [perm, setPerm] = useState(supported ? Notification.permission : 'denied')
  if (!supported) return <p className="muted small" style={{ margin: 0 }}>This browser has no notifications here. On iPhone, add Pocket AI to the Home Screen first (Share → Add to Home Screen), then open it from there.</p>
  if (perm === 'granted') return <div className="row"><Icon name="check-circle" size={15} className="ok-text" /><span>On for this browser.</span></div>
  if (perm === 'denied') return <p className="muted small" style={{ margin: 0 }}>Blocked for this site — allow notifications in the browser’s site settings.</p>
  return <button type="button" className="btn" onClick={async () => setPerm(await Notification.requestPermission())}><Icon name="bell" size={14} />Turn on for this browser</button>
}

export function NotificationsTab() {
  const { sr, settings } = useApp()
  const save = useSaveSettings()
  const { toast } = useToast()
  const [url, setUrl] = useState(settings?.notify_url || '')
  const [token, setToken] = useState('')
  const [testing, setTesting] = useState(false)
  useEffect(() => { setUrl(settings?.notify_url || '') }, [settings?.notify_url])
  if (!sr || !settings) return <Loading />
  const s = settings
  const kind = KINDS.find(k => k.value === s.notify_kind) || KINDS[0]
  const dirty = url !== (s.notify_url || '') || token !== ''

  const saveRoute = async () => {
    const patch: Partial<Settings> & { notify_token?: string } = { notify_url: url.trim() }
    if (token) patch.notify_token = token
    if (await save(patch, 'Notification address saved')) setToken('')
  }
  const test = async () => {
    setTesting(true)
    try { await api.post(`${V1}/notify/test`); toast('Test sent — check your phone') } catch (e) { toast(errMsg(e), 'err') } finally { setTesting(false) }
  }

  return (
    <div className="stack loose">
      <Section title="Tell me when" sub="Pushed from the server, so it arrives with the phone locked and no tab open.">
        <SettingRow title="An agent finished" help="With the first lines of its answer.">
          <Switch label="Finished" checked={!!s.notify_finished} onChange={v => save({ notify_finished: v })} />
        </SettingRow>
        <SettingRow title="An agent needs my answer" help="A permission, a question or a plan to approve — it waits until you answer.">
          <Switch label="Needs input" checked={!!s.notify_input} onChange={v => save({ notify_input: v })} />
        </SettingRow>
        <SettingRow title="Something failed" help="The CLI crashed or the turn ended with an error.">
          <Switch label="Failed" checked={!!s.notify_failed} onChange={v => save({ notify_failed: v })} />
        </SettingRow>
        <SettingRow title="Only when I’m not looking" help="Skip the push while the thread is open in a browser.">
          <Switch label="Only when away" checked={!!s.notify_only_away} onChange={v => save({ notify_only_away: v })} />
        </SettingRow>
      </Section>

      <Section title="Where to send">
        {sr.notify_box_route && <div className="callout info mb"><Icon name="info" size={15} /><span>This box already has a notification route set by the admin. It is used for you until you set your own address here.</span></div>}
        <div className="stack">
          <Segmented label="Service" value={s.notify_kind || 'apprise'} onChange={v => save({ notify_kind: v })} options={KINDS.map(k => ({ value: k.value, label: k.label }))} />
          <p className="muted small" style={{ margin: 0 }}>{kind.help}</p>
          <Field label="Address"><input value={url} onChange={e => setUrl(e.target.value)} placeholder={kind.placeholder} inputMode="url" autoComplete="off" spellCheck={false} /></Field>
          <Field label="Token" help={kind.token}><input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder={sr.notify_has_token ? '•••• saved — type to replace' : 'Optional'} autoComplete="new-password" /></Field>
          <div className="row wrap">
            <button type="button" className="btn primary" disabled={!dirty} onClick={saveRoute}>Save</button>
            <button type="button" className="btn" disabled={testing || (!s.notify_url && !sr.notify_box_route)} onClick={test}>{testing ? <Spinner /> : <Icon name="bell" size={14} />}Send a test</button>
            {sr.notify_has_token && <button type="button" className="btn ghost sm" onClick={() => save({ notify_token: '' }, 'Token removed')}>Remove token</button>}
          </div>
        </div>
      </Section>

      <Section title="In this browser" sub="While Pocket AI is open (or installed on the Home Screen), a notification when a thread you are not looking at finishes or needs you.">
        <BrowserNotifications />
      </Section>
    </div>
  )
}
