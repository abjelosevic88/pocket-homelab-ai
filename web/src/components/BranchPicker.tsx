// The git branch of a working folder, with a switcher (only shown inside a repo).
import { useEffect, useRef, useState } from 'react'
import { api, qs, V1 } from '../lib/api'
import { ago, shortPath } from '../lib/format'
import type { GitBranch, GitInfo } from '../lib/types'
import { Icon } from './icons'
import { errMsg, Popover, rectOf, Spinner, useToast, type Anchor } from './ui'

const VALID_NAME = /^(?!-)(?!.*\.\.)(?!.*\/\/)(?!.*@\{)[^\s~^:?*[\\]+(?<!\/|\.lock|\.)$/

export function BranchButton({ cwd, className = 'btn sm', lockedReason, refreshKey, onSwitched }: {
  cwd: string; className?: string
  /** set while switching would pull the floor out from under a running agent */
  lockedReason?: string
  /** bump to re-read (e.g. after a turn: the agent may have switched itself) */
  refreshKey?: unknown
  onSwitched?: (info: GitInfo) => void
}) {
  const [info, setInfo] = useState<GitInfo | null>(null)
  const [anchor, setAnchor] = useState<Anchor | null>(null)

  useEffect(() => {
    if (!cwd) { setInfo(null); return }
    let live = true
    api.get<GitInfo>(`${V1}/fs/git${qs({ path: cwd })}`).then(d => { if (live) setInfo(d) }).catch(() => { if (live) setInfo(null) })
    return () => { live = false }
  }, [cwd, refreshKey])

  if (!info?.repo) return null
  const label = info.detached ? info.head || 'detached' : info.branch || 'no commits'
  const title = `${info.detached ? 'Detached at ' + info.head : 'Branch ' + info.branch}${info.dirty ? ` · ${info.dirty} uncommitted change${info.dirty === 1 ? '' : 's'}` : ''}${info.ahead ? ` · ${info.ahead} ahead` : ''}${info.behind ? ` · ${info.behind} behind` : ''}`
  return (
    <>
      <button type="button" className={`${className} branch-btn`} title={title} aria-haspopup="dialog" aria-expanded={!!anchor}
        onClick={e => setAnchor(anchor ? null : rectOf(e.currentTarget))}>
        <Icon name="git-branch" size={13} /><span className="ellipsis">{label}</span>
        {info.dirty > 0 && <span className="branch-dirty" aria-label="uncommitted changes" />}
      </button>
      {anchor && <BranchPopover anchor={anchor} cwd={cwd} info={info} lockedReason={lockedReason} onClose={() => setAnchor(null)}
        onInfo={next => { setInfo(next); if (next.branch !== info.branch || next.head !== info.head) onSwitched?.(next) }} />}
    </>
  )
}

function BranchPopover({ anchor, cwd, info: initial, lockedReason, onClose, onInfo }: {
  anchor: Anchor; cwd: string; info: GitInfo; lockedReason?: string; onClose: () => void; onInfo: (i: GitInfo) => void
}) {
  const { toast } = useToast()
  const [info, setInfo] = useState(initial)
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const input = useRef<HTMLInputElement>(null)

  // re-read on open: the list in the button may be minutes old
  useEffect(() => {
    api.get<GitInfo>(`${V1}/fs/git${qs({ path: cwd })}`).then(d => { setInfo(d); onInfo(d) }).catch(() => {})
    input.current?.focus()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const needle = q.trim().toLowerCase()
  const branches = (info.branches || []).filter(b => !needle || b.name.toLowerCase().includes(needle))
  const local = branches.filter(b => !b.remote)
  const remote = branches.filter(b => b.remote)
  const exact = (info.branches || []).some(b => b.name === q.trim() || (b.remote && b.name.slice(b.name.indexOf('/') + 1) === q.trim()))
  const canCreate = !!q.trim() && !exact && VALID_NAME.test(q.trim())

  const go = async (body: { branch: string; create?: boolean; remote?: boolean }) => {
    if (lockedReason || busy) return
    setBusy(body.branch); setErr('')
    try {
      const next = await api.post<GitInfo>(`${V1}/fs/git/switch`, { path: cwd, ...body })
      setInfo(next); onInfo(next); onClose()
      toast(body.create ? `Created and switched to ${next.branch}` : `Switched to ${next.branch}`)
    } catch (e) { setErr(errMsg(e)) }
    finally { setBusy('') }
  }
  const pick = (b: GitBranch) => { if (!b.current) void go({ branch: b.name, remote: b.remote }) }
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key !== 'Enter') return
    e.preventDefault()
    const first = branches.find(b => !b.current)
    if (canCreate && !branches.some(b => b.name.toLowerCase() === needle)) void go({ branch: q.trim(), create: true })
    else if (first) pick(first)
  }

  const row = (b: GitBranch) => (
    <button key={(b.remote ? 'r:' : '') + b.name} type="button" className={`branch-row ${b.current ? 'current' : ''}`} disabled={!!lockedReason && !b.current} onClick={() => pick(b)} title={b.subject}>
      <span className="branch-ico">{busy === b.name ? <Spinner size={13} /> : b.current ? <Icon name="check" size={14} /> : <Icon name={b.remote ? 'globe' : 'git-branch'} size={13} />}</span>
      <span className="grow ellipsis mono">{b.name}</span>
      {b.time ? <span className="faint xsmall nowrap">{ago(new Date(b.time * 1000).toISOString())}</span> : null}
    </button>
  )

  return (
    <Popover anchor={anchor} onClose={onClose} prefer="below" width={340} className="branch-pop" label="Switch branch">
      <div className="branch-pop-head">
        <input ref={input} value={q} onChange={e => setQ(e.target.value)} onKeyDown={onKey} placeholder="Find or create a branch…" spellCheck={false} autoCapitalize="off" autoCorrect="off" aria-label="Branch name" className="mono" />
      </div>
      {lockedReason ? <div className="branch-note warn"><Icon name="lock" size={13} /><span>{lockedReason}</span></div>
        : info.dirty > 0 && <div className="branch-note"><Icon name="info" size={13} /><span>{info.dirty} uncommitted change{info.dirty === 1 ? '' : 's'} — git carries {info.dirty === 1 ? 'it' : 'them'} to the other branch, or refuses if they would clash.</span></div>}
      {err && <div className="branch-note danger"><Icon name="alert" size={13} /><span className="pre-wrap">{err}</span></div>}
      <div className="branch-list">
        {canCreate && (
          <button type="button" className="branch-row create" disabled={!!lockedReason} onClick={() => go({ branch: q.trim(), create: true })}>
            <span className="branch-ico">{busy === q.trim() ? <Spinner size={13} /> : <Icon name="plus" size={14} />}</span>
            <span className="grow ellipsis">Create <b className="mono">{q.trim()}</b> from {info.detached ? info.head : info.branch}</span>
          </button>
        )}
        {local.map(row)}
        {remote.length > 0 && <div className="menu-heading">Remote only</div>}
        {remote.map(row)}
        {branches.length === 0 && !canCreate && <div className="muted small" style={{ padding: '10px 12px' }}>{needle ? 'No branch matches.' : 'No branches yet.'}</div>}
      </div>
      <div className="branch-foot faint xsmall ellipsis" title={info.root}>
        {info.ahead > 0 && <span>↑{info.ahead}</span>}{info.behind > 0 && <span>↓{info.behind}</span>}
        <span className="ellipsis mono">{shortPath(info.root)}</span>
      </div>
    </Popover>
  )
}
