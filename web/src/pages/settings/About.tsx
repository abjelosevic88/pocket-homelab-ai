import type React from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { SystemInfo } from '../../lib/types'
import { Icon } from '../../components/icons'
import { Loading, useAsync } from '../../components/ui'
import { fmtBytes, Section, StatusDot } from './common'
import { useProjectsLink } from '../../lib/projects'

/** Anything else the server reports (binaries, roots, live processes), shown as-is. */
function extraRows(d: SystemInfo): [string, React.ReactNode][] {
  const known = new Set(['version', 'uptime', 'go', 'auth_mode', 'timezone', 'base_url', 'db_size', 'users', 'core', 'counts'])
  const out: [string, React.ReactNode][] = []
  for (const [k, v] of Object.entries(d)) {
    if (known.has(k) || v === null || v === undefined || v === '') continue
    const label = k.replace(/_/g, ' ').replace(/^./, x => x.toUpperCase())
    out.push([label, typeof v === 'object' ? <code key={k} className="small" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(v, null, 1)}</code> : String(v)])
  }
  return out
}

export function AboutTab() {
  const { user } = useApp()
  const navigate = useNavigate()
  const info = useAsync(() => api.get<SystemInfo>(`${V1}/settings/system`), [])
  const projects = useProjectsLink()
  const d = info.data
  if (info.error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{info.error}</span></div>
  if (!d) return <Loading />
  const c = d.counts || {}
  const counts: [string, number | undefined, string][] = [
    ['Threads', c.threads, 'message'], ['Recipes', c.recipes, 'zap'], ['Schedules', c.schedules, 'clock'], ['Folders', c.workspaces, 'folder'],
  ]
  const core = d.core || { configured: false, url: '', registered: false, error: '' }
  const rows: [string, React.ReactNode][] = [
    ['Version', <code key="v">{d.version}</code>],
    ['Uptime', d.uptime],
    ['Go', d.go],
    ['Sign-in', d.auth_mode],
    ['Box time zone', d.timezone],
    ['Public address', d.base_url || <span className="muted">not set</span>],
    ...extraRows(d),
  ]
  if (user?.role === 'admin') {
    if (d.db_size !== undefined) rows.push(['Database', fmtBytes(d.db_size)])
    if (d.users !== undefined) rows.push(['People', String(d.users)])
  }
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket AI</h3>
          <p className="muted small">Claude Code and Codex on your server, from any browser — part of the Pocket apps.</p>
        </div>
      </div>
      <div className="set-stats">
        {counts.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{n ?? 0}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>
      <Section title="Pocket Core">
        <div className="row">
          <StatusDot status={!core.configured ? 'off' : core.registered ? 'ok' : 'error'} />
          <span>{!core.configured ? 'Not configured on this box' : core.registered ? 'Connected' : 'Not reachable'}</span>
          {core.url && <code className="muted small">{core.url}</code>}
        </div>
        {core.error && <div className="callout danger mt-s"><Icon name="alert" size={15} /><span>{core.error}</span></div>}
      </Section>
      {core.configured && <Section title="Connected apps" sub="What Pocket AI reads from your other Pocket apps, through Pocket Core links. Switch them in Core.">
        <div className="row wrap">
          <StatusDot status={!projects ? 'off' : projects.on ? 'ok' : 'off'} />
          <span className="grow"><b>Pocket Projects</b> · see your issues <span className="muted small">— groups the conversations your issues started, by issue, in the sidebar and the tab bar</span></span>
          <span className="muted small">{!projects ? 'Checking…' : projects.on ? 'On' : 'Off'}</span>
          {projects?.core_links_url && <a className="btn sm" href={projects.core_links_url} target="_blank" rel="noreferrer"><Icon name="external" size={13} />Manage in Core</a>}
        </div>
        {projects && !projects.on && projects.reason && <p className="muted small mt-s">{projects.reason}</p>}
      </Section>}
      <Section title="This server">
        <dl className="set-dl">
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn" onClick={() => navigate('/', { state: { shortcuts: true } })}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-ai" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub</a>
      </div>
    </div>
  )
}
