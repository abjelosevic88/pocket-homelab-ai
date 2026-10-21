// "Running now": every prompt an agent is working on — in Pocket AI, Paseo,
// a terminal or any other app — with how long it has been going.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { baseName } from '../lib/format'
import type { RunningItem, Thread } from '../lib/types'
import { ProviderBadge } from './bits'
import { Icon } from './icons'
import { errMsg, useToast } from './ui'

/** Four dots taking turns — the "working" mark used everywhere. */
export function RunDots({ waiting = false, size = 14 }: { waiting?: boolean; size?: number }) {
  return (
    <span className={`run-dots ${waiting ? 'waiting' : ''}`} style={{ width: size, height: size }} aria-hidden="true">
      <span /><span /><span /><span />
    </span>
  )
}

/** Re-renders every `ms` so elapsed times tick. */
export function useNow(ms = 1000) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), ms); return () => clearInterval(t) }, [ms])
  return now
}

/** 45s · 2m 40s · 1h 12m */
export function elapsed(fromIso: string | undefined, now: number) {
  if (!fromIso) return ''
  const s = Math.max(0, Math.floor((now - new Date(fromIso).getTime()) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
}

const ORIGIN: Record<string, string> = { 'pocket-ai': 'Pocket AI', paseo: 'Paseo', terminal: 'Terminal', app: 'Another app' }

export function useOpenRunning() {
  const navigate = useNavigate()
  const { upsertThread } = useApp()
  const { toast } = useToast()
  return async (r: RunningItem) => {
    if (r.thread_id) { navigate(`/t/${r.thread_id}`); return }
    try {
      const t = await api.post<Thread>(`${V1}/history/import`, { provider: r.provider, session_id: r.session_id })
      upsertThread(t)
      navigate(`/t/${t.id}`)
    } catch (e) { toast(errMsg(e), 'err') }
  }
}

export function RunningList({ compact = false, onOpened }: { compact?: boolean; onOpened?: () => void }) {
  const { running } = useApp()
  const now = useNow(1000)
  const open = useOpenRunning()
  if (!running.length) return null
  if (compact) {
    return (
      <div className="run-side">
        <div className="sb-h"><span>Running now</span><span className="run-count">{running.length}</span></div>
        {running.map(r => (
          <button key={r.provider + r.session_id + r.thread_id} type="button" className="th-row run-row" onClick={() => { open(r); onOpened?.() }} title={r.prompt}>
            <RunDots waiting={r.status === 'waiting'} size={12} />
            <span className="th-main">
              <span className="th-title ellipsis">{r.title || r.prompt || 'Session'}</span>
              <span className="th-meta"><span className="ellipsis">{ORIGIN[r.origin] || r.origin} · {baseName(r.cwd)}</span></span>
            </span>
            <span className="run-time tnum">{elapsed(r.prompt_at, now)}</span>
          </button>
        ))}
      </div>
    )
  }
  return (
    <section className="home-sec run-sec">
      <div className="home-sec-h"><h2><RunDots size={11} /> Running now</h2><span className="muted small">{running.length === 1 ? '1 prompt' : `${running.length} prompts`}</span></div>
      <div className="run-cards">
        {running.map((r, i) => (
          <button key={r.provider + r.session_id + r.thread_id} type="button" className={`run-card ${r.status}`} style={{ animationDelay: `${Math.min(i, 8) * 45}ms` }} onClick={() => open(r)}>
            <span className="run-card-top">
              <RunDots waiting={r.status === 'waiting'} />
              <ProviderBadge provider={r.provider} size="xs" />
              <span className={`origin-chip ${r.origin}`}>{ORIGIN[r.origin] || r.origin}</span>
              <span className="grow" />
              <span className="run-time tnum"><Icon name="clock" size={11} />{elapsed(r.prompt_at, now)}</span>
            </span>
            <span className="run-title">{r.title || 'Session'}</span>
            {r.prompt && r.prompt !== r.title && <span className="run-prompt">“{r.prompt}”</span>}
            <span className="run-foot muted xsmall"><Icon name="folder" size={11} />{baseName(r.cwd) || '~'}{r.status === 'waiting' && <b className="warn-text"> · needs you</b>}{!r.thread_id && <span> · tap to follow live</span>}</span>
          </button>
        ))}
      </div>
    </section>
  )
}
