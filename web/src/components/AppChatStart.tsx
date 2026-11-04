// An app's own chat (?embed=1&ask=<slug>&app_url=…&app_name=…): the floating chat
// button of a Pocket app opens this. General questions about that app and how it
// connects with the others; "@" asks the framing page for the page it shows.
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import { useApp } from '../lib/app-context'
import type { Thread } from '../lib/types'
import { Composer, type ComposerHandle } from './Composer'
import { Icon } from './icons'
import { Select } from './Select'
import { errMsg, useToast } from './ui'

const MODEL = 'pai-app-chat-model'

export function AppChatStart({ app, appURL, appName, history = false }: { app: string; appURL: string; appName: string; history?: boolean }) {
  const { threads, upsertThread } = useApp()
  const navigate = useNavigate()
  const { toast } = useToast()
  const composer = useRef<ComposerHandle>(null)
  const [model, setModel] = useState(() => localStorage.getItem(MODEL) || 'sonnet')
  const [noMemory, setNoMemory] = useState(false)
  const [connected, setConnected] = useState<boolean | null>(null)
  useEffect(() => { api.get<{ connected: boolean }>(`${V1}/assistant/status`).then(v => setConnected(v.connected)).catch(() => setConnected(false)) }, [])
  // every earlier chat about this app (the app's history button opens them all)
  const [all, setAll] = useState(history)
  const [q, setQ] = useState('')
  const mine = (threads || []).filter(t => t.source === 'ask' && t.source_ref === appURL)
  const words = q.trim().toLowerCase()
  const earlier = all ? mine.filter(t => !words || (t.title || '').toLowerCase().includes(words)) : mine.slice(0, 5)

  const send = async (text: string, uploadIds: string[]) => {
    try {
      const t = await api.post<Thread>(`${V1}/ask/threads`, { text, model, no_memory: noMemory, upload_ids: uploadIds, app_chat: true, source: { app, url: appURL, title: appName } })
      upsertThread(t)
      navigate(`/t/${t.id}`)
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }
  const pickModel = (m: string) => { setModel(m); localStorage.setItem(MODEL, m) }

  return (
    <div className="home app-chat-start">
      <div className="app-chat-intro">
        <Icon name="sparkles" size={22} />
        <h1>Ask about {appName}</h1>
        <p className="muted">Anything about {appName}, or how it ties in with your other apps. Type <kbd>@</kbd> to add the page you are looking at.</p>
      </div>
      {connected === false && <div className="callout info ask-connect"><Icon name="sparkles" size={15} />
        <span className="grow">Ask reads your Pocket apps as you, so connect it once in Pocket AI.</span>
        <a className="btn primary sm" href={`${location.origin}/`} target="_blank" rel="noreferrer">Open Pocket AI</a></div>}
      {(mine.length > 0 || all) && (
        <div className={`app-chat-earlier ${all ? 'all' : ''}`}>
          <div className="app-chat-earlier-head">{all ? `All chats about ${appName} (${mine.length})` : 'Earlier chats'}</div>
          {all && mine.length > 5 && <input className="app-chat-earlier-q" type="search" placeholder="Find a chat…" value={q} onChange={e => setQ(e.target.value)} autoFocus />}
          <div className="app-chat-earlier-list">
            {earlier.map(t => (
              <button key={t.id} type="button" onClick={() => navigate(`/t/${t.id}`)}>
                <Icon name="message" size={14} /><span className="ellipsis grow">{t.title || 'Chat'}</span><span className="muted small">{ago(t.updated_at)}</span>
              </button>
            ))}
            {all && earlier.length === 0 && <div className="muted small app-chat-earlier-none">{mine.length ? 'No chat matches.' : `No chats about ${appName} yet.`}</div>}
          </div>
          {!all && mine.length > 5 && <button type="button" className="app-chat-earlier-more" onClick={() => setAll(true)}><Icon name="chevron-down" size={14} /><span>All {mine.length} chats</span></button>}
        </div>
      )}
      <Composer ref={composer} big autoFocus draftKey={`app-chat-${app}`} onSend={send}
        placeholder={`Ask about ${appName}… (@ adds this page)`}
        footer={<div className="app-composer-options">
          <Select className="ghost inline" size="sm" label="Model" value={model} onChange={pickModel} options={[{ value: 'haiku', label: 'Haiku' }, { value: 'sonnet', label: 'Sonnet' }, { value: 'opus', label: 'Opus' }]} />
          <button type="button" className="memory-choice" aria-label="Use saved memory" aria-pressed={!noMemory} title={noMemory ? 'Saved memory is off for this conversation' : 'Use saved preferences in this conversation'} onClick={() => setNoMemory(v => !v)}><Icon name="brain" size={14} /><span>Memory {noMemory ? 'off' : 'on'}</span></button>
        </div>} />
    </div>
  )
}

/** The app chat's parameters, when this frame is one (only inside another app's page). */
export function appChatParams(params: URLSearchParams): { app: string; appURL: string; appName: string; history: boolean } | null {
  const app = (params.get('ask') || '').toLowerCase()
  const appURL = params.get('app_url') || ''
  if (!/^[a-z][a-z0-9-]{0,30}$/.test(app)) return null
  try { const u = new URL(appURL); if (u.protocol !== 'https:' && u.protocol !== 'http:') return null } catch { return null }
  return { app, appURL, appName: (params.get('app_name') || '').slice(0, 80) || `Pocket ${app[0].toUpperCase()}${app.slice(1)}`, history: params.get('history') === '1' }
}
