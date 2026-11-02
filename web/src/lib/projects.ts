// The Core link to Pocket Projects: whether it is on for me, and the issues
// my Projects conversations belong to (title, state). One shared fetch for the
// sidebar, the tab bar and Settings; refreshed every two minutes.
import { useEffect, useMemo, useState } from 'react'
import { api, qs, V1 } from './api'
import type { Thread } from './types'

export interface ProjectsIssue { title: string; state_type: string; url: string }
export interface ProjectsLink {
  configured: boolean
  on: boolean
  reason?: string
  issues: Record<string, ProjectsIssue>
  /** thread id → issue key, forks and rewinds of Projects conversations included */
  threads?: Record<string, string>
  core_links_url?: string
  projects_url?: string
  /** the Projects workspace open to Pocket AI (only with several) */
  agent_workspace?: string
  /** thread id → why it is paused: its issue lives in another workspace */
  paused?: Record<string, string>
}

let state: ProjectsLink | null = null

/** A conversation that belongs to a Projects issue (started there or attached): its issue key (AS-7), else ''. */
export const projectsRef = (t?: Thread) => !t ? '' : state?.threads?.[t.id] || ((t.source as string) === 'link:projects' ? (t.source_ref || '').toUpperCase() : '')

/** An issue ref as it reads on screen: AS-7, and ATH-12 for a connected
 * instance's host/ATH-12 (the host goes in tooltips). */
export const refKey = (ref: string) => ref.slice(ref.lastIndexOf('/') + 1)
export const refHost = (ref: string) => ref.includes('/') ? ref.slice(0, ref.lastIndexOf('/')).toLowerCase() : ''

/** Why a conversation is paused (its Projects workspace isn't the one open), else ''. */
export const projectsPaused = (t?: Thread) => (t && state?.paused?.[t.id]) || ''

let asked = ''
let inflight: Promise<void> | null = null
let at = 0
const subs = new Set<() => void>()

function load(refs: string) {
  if (inflight && refs === asked) return
  asked = refs
  inflight = api.get<ProjectsLink>(`${V1}/projects-link${qs({ refs: refs || undefined })}`)
    .then(v => { state = v; at = Date.now() })
    .catch(() => { state = state || { configured: false, on: false, issues: {} }; at = Date.now() })
    .finally(() => { inflight = null; subs.forEach(f => f()) })
}

/** Ask again now (a conversation was attached to or detached from an issue). */
export function reloadProjectsLink() { inflight = null; load(asked) }

/** The link's state, with the issues of `refs` filled in. */
export function useProjectsLink(refs: string[] = []): ProjectsLink | null {
  const key = useMemo(() => [...new Set(refs)].sort().join(','), [refs])
  const [, tick] = useState(0)
  useEffect(() => {
    const f = () => tick(n => n + 1)
    subs.add(f)
    return () => { subs.delete(f) }
  }, [])
  useEffect(() => {
    // ask again when new issues show up (union, so other users of the hook keep theirs)
    const want = [...new Set([...asked.split(',').filter(Boolean), ...key.split(',').filter(Boolean)])].sort().join(',')
    if (!state || want !== asked || Date.now() - at > 120e3) load(want)
    const t = setInterval(() => load(asked), 120e3)
    // switching workspace happens in Projects, often in another tab: look again on coming back
    const vis = () => { if (document.visibilityState === 'visible' && Date.now() - at > 5e3) load(asked) }
    document.addEventListener('visibilitychange', vis)
    return () => { clearInterval(t); document.removeEventListener('visibilitychange', vis) }
  }, [key])
  return state
}
