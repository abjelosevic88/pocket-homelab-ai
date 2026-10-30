// Where a thread came from when another Pocket app prepared it ("AS-2 · Pocket Projects"), linking back.
// A Projects conversation is pinned to its issue (its Projects changes touch only that issue); the person unpins it here.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { errMsg, useToast } from './ui'

interface ThreadContext { source: string; ref: string; ref_url: string; ref_label: string; unpinned?: boolean }

const appName = (source: string) => 'Pocket ' + source.charAt(0).toUpperCase() + source.slice(1)

export function ThreadRefChip({ threadId, source, owner = true }: { threadId: string; source?: string; owner?: boolean }) {
  const [c, setC] = useState<ThreadContext | null>(null)
  const { toast } = useToast()
  // forks and rewinds keep the context of the conversation they came from
  const linked = !!source?.startsWith('link:') || source === 'fork' || source === 'rewind'
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
  const chip = c.ref_url
    ? <a className="privacy-badge" href={c.ref_url} target="_blank" rel="noreferrer" title={title}><Icon name="link" size={11} />{label}</a>
    : <span className="privacy-badge" title={title}><Icon name="link" size={11} />{label}</span>
  if (c.source !== 'projects') return chip
  const pinned = !c.unpinned
  const toggle = () => api.post<ThreadContext>(`${V1}/threads/${threadId}/pin`, { pinned: !pinned })
    .then(v => { setC(v); toast(v.unpinned ? `Unpinned: this conversation may change other issues too` : `Pinned to ${c.ref}`) })
    .catch(e => toast(errMsg(e), 'err'))
  return <>
    {chip}
    <button type="button" className={`privacy-badge ${pinned ? '' : 'muted'}`} disabled={!owner} onClick={toggle}
      title={pinned ? `Pinned: its Pocket Projects changes may only touch ${c.ref}, new sub-issues of it and its team's memory. Click to let it change other issues too.` : `Not pinned: it may change any issue in the workspace open in Projects. Click to pin it to ${c.ref} again.`}>
      <Icon name="pushpin" size={11} />{pinned ? 'Pinned' : 'Unpinned'}
    </button>
  </>
}
