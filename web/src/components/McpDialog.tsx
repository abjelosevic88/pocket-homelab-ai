// /mcp: the conversation's MCP servers and claude.ai connectors — status, reconnect, turn off/on, sign in or out.
import React, { useCallback, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { errMsg, Modal, Spinner, useToast } from './ui'

export interface McpServer {
  name: string
  status: 'connected' | 'pending' | 'needs-auth' | 'failed' | 'disabled' | string
  error?: string
  source?: string
  tools?: number
  can_toggle?: boolean
  can_login?: boolean
  can_logout?: boolean
}
interface McpLogin { auth_url: string; callback_expected: boolean }

const statusLabel: Record<string, [string, string]> = {
  connected: ['Connected', 'ok'],
  pending: ['Connecting…', ''],
  'needs-auth': ['Needs sign-in', 'warn'],
  failed: ['Failed', 'danger'],
  disabled: ['Off', ''],
}

export function McpDialog({ threadId, onClose, onRestart }: { threadId: string; onClose: () => void; onRestart?: () => void }) {
  const { toast } = useToast()
  const [servers, setServers] = useState<McpServer[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('') // "<action>:<server>"
  const [login, setLogin] = useState<{ server: string; callback: boolean } | null>(null)
  const [pasted, setPasted] = useState('')

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ servers: McpServer[] }>(`${V1}/threads/${threadId}/mcp`)
      setServers(r.servers || []); setError('')
    } catch (e) { setError(errMsg(e)) }
  }, [threadId])
  useEffect(() => { void load() }, [load])
  // while something connects, and when coming back from a sign-in tab
  const settling = !!servers?.some(s => s.status === 'pending')
  useEffect(() => {
    if (!settling && !login) return
    const t = setInterval(load, 2500)
    return () => clearInterval(t)
  }, [settling, login, load])
  useEffect(() => {
    const f = () => { if (document.visibilityState === 'visible') void load() }
    document.addEventListener('visibilitychange', f)
    return () => document.removeEventListener('visibilitychange', f)
  }, [load])

  const act = async (action: string, server = '', url = '') => {
    // a tab opened in the click itself is not blocked; the sign-in address comes after
    const tab = action === 'login' ? window.open('', '_blank') : null
    setBusy(`${action}:${server}`)
    try {
      const r = await api.post<{ servers?: McpServer[]; login?: McpLogin }>(`${V1}/threads/${threadId}/mcp`, { action, server, url })
      if (r.servers) setServers(r.servers)
      if (r.login) {
        if (tab) tab.location.href = r.login.auth_url
        else window.open(r.login.auth_url, '_blank', 'noopener')
        setLogin({ server, callback: r.login.callback_expected }); setPasted('')
      }
      if (action === 'callback') { setLogin(null); toast(`${server} is signed in`, 'ok') }
      if (action === 'reconnect') toast(server ? `Reconnected ${server}` : 'Reconnected all servers', 'ok')
      if (action === 'logout') toast(`Signed out of ${server} — sign in again with the account you want`, 'ok')
    } catch (e) {
      tab?.close()
      toast(errMsg(e), 'err')
    } finally { setBusy('') }
  }
  const working = (a: string, s = '') => busy === `${a}:${s}`

  return (
    <Modal title="MCP servers" size="lg" onClose={onClose} footer={<>
      {onRestart && <button type="button" className="btn ghost" title="A new process on the same conversation: everything connects from scratch" onClick={() => { onRestart(); onClose() }}><Icon name="repeat" size={14} />Restart session</button>}
      <span className="grow" />
      <button type="button" className="btn" disabled={!!busy || !servers?.length} onClick={() => act('reconnect')}>{working('reconnect') ? <Spinner size={14} /> : <Icon name="repeat" size={14} />}Reconnect all</button>
      <button type="button" className="btn primary" onClick={onClose}>Done</button>
    </>}>
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!servers && !error && <div className="loading"><Spinner size={16} /> Starting the agent and reading its servers…</div>}
      {servers && servers.length === 0 && <p className="muted">No MCP servers or connectors in this conversation.</p>}
      {login && (
        <div className="callout info mcp-login">
          <Icon name="link" size={15} />
          <div className="grow">
            {login.callback ? <>
              <div>Sign in to <b>{login.server}</b> in the tab that opened. It ends on a <code>localhost</code> page that won’t load — copy that page’s full address and paste it here.</div>
              <form className="mcp-callback" onSubmit={e => { e.preventDefault(); if (pasted.trim()) void act('callback', login.server, pasted.trim()) }}>
                <input value={pasted} onChange={e => setPasted(e.target.value)} placeholder="http://localhost:…/callback?code=…" aria-label="Address the sign-in ended on" autoFocus />
                <button type="submit" className="btn primary sm" disabled={!pasted.trim() || !!busy}>{working('callback', login.server) ? <Spinner size={13} /> : null}Finish</button>
              </form>
            </> : <div>Sign in to <b>{login.server}</b> in the tab that opened, then come back — it connects by itself (or tap Reconnect).</div>}
          </div>
          <button type="button" className="btn ghost sm icon-only" aria-label="Close" onClick={() => setLogin(null)}><Icon name="x" size={13} /></button>
        </div>
      )}
      {servers && servers.length > 0 && (
        <ul className="mcp-list">
          {servers.map(s => {
            const [label, tone] = statusLabel[s.status] || [s.status, '']
            const off = s.status === 'disabled'
            return (
              <li key={s.name} className="mcp-row">
                <div className="mcp-main">
                  <div className="mcp-name"><span className="ellipsis">{s.name}</span><span className={`badge ${tone}`}>{s.status === 'pending' && <Spinner size={10} />}{label}</span></div>
                  <div className="mcp-sub muted">{[s.source, s.tools ? `${s.tools} tool${s.tools === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')}</div>
                  {s.error && s.status !== 'connected' && <div className="mcp-err">{s.error}</div>}
                </div>
                <div className="mcp-actions">
                  {s.status === 'needs-auth' && s.can_login && <button type="button" className="btn primary sm" disabled={!!busy} onClick={() => act('login', s.name)}>{working('login', s.name) && <Spinner size={13} />}Sign in</button>}
                  {!off && <button type="button" className="btn sm" disabled={!!busy} onClick={() => act('reconnect', s.name)}>{working('reconnect', s.name) && <Spinner size={13} />}Reconnect</button>}
                  {s.status === 'connected' && s.can_logout && <button type="button" className="btn ghost sm" disabled={!!busy} title="Forget this server's sign-in, then sign in with another account" onClick={() => act('logout', s.name)}>{working('logout', s.name) && <Spinner size={13} />}Sign out</button>}
                  {s.status !== 'needs-auth' && s.status !== 'connected' && s.can_login && !off && <button type="button" className="btn ghost sm" disabled={!!busy} onClick={() => act('login', s.name)}>Sign in</button>}
                  {s.can_toggle && <button type="button" className="btn ghost sm" disabled={!!busy} onClick={() => act(off ? 'enable' : 'disable', s.name)}>{working(off ? 'enable' : 'disable', s.name) && <Spinner size={13} />}{off ? 'Turn on' : 'Turn off'}</button>}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </Modal>
  )
}

export default McpDialog
