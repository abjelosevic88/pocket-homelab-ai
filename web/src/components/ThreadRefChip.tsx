// Where a thread came from when another Pocket app prepared it ("AS-2 · Pocket Projects"), linking back.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'

interface ThreadContext { source: string; ref: string; ref_url: string; ref_label: string }

const appName = (source: string) => 'Pocket ' + source.charAt(0).toUpperCase() + source.slice(1)

export function ThreadRefChip({ threadId, source }: { threadId: string; source?: string }) {
  const [c, setC] = useState<ThreadContext | null>(null)
  const linked = !!source?.startsWith('link:')
  useEffect(() => {
    setC(null)
    if (!linked) return
    let live = true
    api.get<ThreadContext>(`${V1}/threads/${threadId}/context`).then(v => { if (live) setC(v) }).catch(() => {})
    return () => { live = false }
  }, [threadId, linked])
  if (!c || !c.ref) return null
  const label = `${c.ref} · ${appName(c.source)}`
  const title = `${c.ref_label || c.ref} — prepared in ${appName(c.source)}`
  return c.ref_url
    ? <a className="privacy-badge" href={c.ref_url} target="_blank" rel="noreferrer" title={title}><Icon name="link" size={11} />{label}</a>
    : <span className="privacy-badge" title={title}><Icon name="link" size={11} />{label}</span>
}
