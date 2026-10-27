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
  core_links_url?: string
  projects_url?: string
}

/** A conversation a Projects issue started: its issue key (AS-7), else ''. */
export const projectsRef = (t?: Thread) => t && (t.source as string) === 'link:projects' ? (t.source_ref || '').toUpperCase() : ''

let state: ProjectsLink | null = null
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
    return () => clearInterval(t)
  }, [key])
  return state
}
