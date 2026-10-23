// Every worktree in one place: what each holds (ahead, uncommitted, merged,
// size on disk), its threads, and tidying up — archive what is merged,
// restore or delete archived ones, adopt or remove worktrees made elsewhere
// (by hand, by Paseo).
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, shortPath } from '../lib/format'
import type { Worktree, WorktreeRepoInfo } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { WorktreePanel, WorktreeSettingsDialog } from '../components/Worktree'
import { Confirm, Empty, errMsg, Loading, Spinner, useToast } from '../components/ui'

interface Other { host_id: number; host_name?: string; repo: string; path: string; branch: string; head: string; missing: boolean; paseo: boolean }

const size = (kb?: number) => !kb ? '' : kb < 1024 ? `${kb} KB` : kb < 1024 * 1024 ? `${(kb / 1024).toFixed(0)} MB` : `${(kb / 1024 / 1024).toFixed(1)} GB`

export default function WorktreesPage({ onMenu }: { onMenu?: () => void }) {
  const { worktrees } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [list, setList] = useState<Worktree[] | null>(null)
  const [others, setOthers] = useState<Other[]>([])
  const [n, setN] = useState(0)
  const [open, setOpen] = useState<number | null>(null)
  const [busy, setBusy] = useState('')
  const [showArchived, setShowArchived] = useState(false)
  const [settings, setSettings] = useState<{ info: WorktreeRepoInfo; host: number } | null>(null)
  const [removing, setRemoving] = useState<Other | null>(null)

  // live git state + sizes; the cheap list from the app context keeps it current between loads
  useEffect(() => {
    let live = true
    api.get<Worktree[]>(`${V1}/worktrees?state=1&sizes=1`).then(d => { if (live) setList(d) }).catch(e => { if (live) { toast(errMsg(e), 'err'); setList([]) } })
    api.get<Other[]>(`${V1}/worktrees/others`).then(d => { if (live) setOthers(d) }).catch(() => {})
    return () => { live = false }
  }, [n, worktrees.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const reload = () => setN(x => x + 1)

  const groups = useMemo(() => {
    const m = new Map<string, { repo: string; host: number; host_name?: string; items: Worktree[] }>()
    for (const w of (list || []).filter(x => x.status === 'active')) {
      const k = `${w.host_id}:${w.repo}`
      if (!m.has(k)) m.set(k, { repo: w.repo, host: w.host_id, host_name: w.host_name, items: [] })
      m.get(k)!.items.push(w)
    }
    return [...m.values()]
  }, [list])
  const archived = (list || []).filter(w => w.status === 'archived')
  const merged = (list || []).filter(w => w.status === 'active' && w.state?.merged)
  const total = (list || []).reduce((s, w) => s + (w.state?.size_kb || 0), 0)

  const cleanup = async () => {
    setBusy('cleanup')
    try {
      const r = await api.post<{ archived: string[]; skipped: string[] }>(`${V1}/worktrees/cleanup`, { delete_branches: true })
      toast(r.archived.length ? `Archived ${r.archived.length}: ${r.archived.join(', ')}${r.skipped.length ? ` · skipped ${r.skipped.join(', ')} (uncommitted work or an agent at work)` : ''}` : 'Nothing to clean up')
      reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const openSettings = async (repo: string, host: number) => {
    try { setSettings({ info: await api.get<WorktreeRepoInfo>(`${V1}/worktrees/config${qs({ path: repo, host: host || undefined })}`), host }) } catch (e) { toast(errMsg(e), 'err') }
  }
  const adopt = async (o: Other) => {
    setBusy(o.path)
    try { await api.post(`${V1}/worktrees/adopt`, { host: o.host_id, path: o.path }); toast(`${o.branch || o.path} is managed here now`); reload() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const removeOther = async (o: Other) => {
    setBusy(o.path)
    try {
      const r = await api.post<{ saved_ref?: string }>(`${V1}/worktrees/others/remove`, { host: o.host_id, repo: o.repo, path: o.path })
      toast(r.saved_ref ? `Removed — its work is saved as ${r.saved_ref}` : 'Removed')
      reload()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }

  const card = (w: Worktree) => {
    const st = w.state
    return (
      <div key={w.id} role="button" tabIndex={0} className="wt-card" onClick={() => setOpen(w.id)} onKeyDown={e => { if (e.key === 'Enter') setOpen(w.id) }}>
        <div className="wt-card-top">
          <Icon name="fork" size={14} />
          <span className="mono ellipsis wt-card-branch">{w.branch}</span>
          <span className="muted xsmall">from {w.base}</span>
          <span className="grow" />
          {w.setup === 'running' && <span className="badge"><Spinner size={9} />setup</span>}
          {w.setup === 'failed' && <span className="badge danger">setup failed</span>}
          {st?.error ? <span className="badge danger" title={st.error}>error</span> : st?.gone ? <span className="badge danger">folder gone</span> : <>
            {st?.merged && <span className="badge ok">merged</span>}
            {!!st?.ahead && !st.merged && <span className="badge accent">{st.ahead} ahead</span>}
            {!!st?.dirty && <span className="badge warn">{st.dirty} uncommitted</span>}
            {!!st?.behind && <span className="badge">{st.behind} behind</span>}
            {(st?.conflicts?.length || st?.merging) ? <span className="badge danger">conflicts</span> : null}
          </>}
          {w.pr_url && <a className="badge" href={w.pr_url} target="_blank" rel="noreferrer" onClick={e => e.stopPropagation()}>PR{st?.pr_state ? ` · ${st.pr_state.toLowerCase()}` : ''}</a>}
        </div>
        {w.title && w.title !== w.branch && <div className="wt-card-title ellipsis">{w.title}</div>}
        <div className="wt-card-meta muted xsmall">
          <span>{ago(w.created_at)}</span>
          {st?.size_kb ? <span>{size(st.size_kb)}</span> : null}
          {w.threads.length > 0 && <span className="wt-card-threads">{w.threads.map(t => (
            <button key={t.id} type="button" className="link-btn xsmall" onClick={e => { e.stopPropagation(); navigate(`/t/${t.id}`) }}>
              <span className={`prov-dot prov-${t.provider}`} />{(t.title || 'Thread').slice(0, 40)}{t.status === 'running' ? ' · working' : ''}
            </button>
          ))}</span>}
        </div>
      </div>
    )
  }

  return (
    <div className="page">
      <PageHead title="Worktrees" icon="fork" onMenu={onMenu}
        sub="Each task in its own folder and branch. Start one with the Worktree switch next to the folder on the start page."
        actions={merged.length > 0 && <button type="button" className="btn sm" disabled={!!busy} onClick={cleanup} title="Archive every merged worktree without uncommitted work, and delete its branch">{busy === 'cleanup' ? <Spinner /> : <Icon name="archive" size={13} />}Clean up {merged.length} merged</button>} />
      <div className="page-scroll">
        <div className="page-inner srv">
          {list === null ? <Loading label="Looking at the worktrees…" /> : !groups.length && !archived.length && !others.length ? (
            <Empty title="No worktrees yet" icon="fork">On the start page, pick a folder inside a git repository and switch on <b>Worktree</b>: the agent gets its own copy and branch, and you merge (or drop) its work from here or from the thread.</Empty>
          ) : <>
            {total > 0 && <p className="muted xsmall" style={{ margin: '0 0 4px' }}>{size(total)} on disk in {groups.reduce((s, g) => s + g.items.length, 0)} worktrees.</p>}
            {groups.map(g => (
              <section key={`${g.host}:${g.repo}`} className="srv-group">
                <h3 className="srv-h"><span className="ellipsis">{shortPath(g.repo)}</span>{g.host_name && <span className="badge"><Icon name="server" size={10} />{g.host_name}</span>}<span className="muted">{g.items.length}</span>
                  <span className="grow" /><button type="button" className="link-btn xsmall" onClick={() => openSettings(g.repo, g.host)}>Setup…</button></h3>
                <div className="wt-cards">{g.items.map(card)}</div>
              </section>
            ))}
            {others.length > 0 && (
              <section className="srv-group">
                <h3 className="srv-h">Other worktrees<span className="muted">{others.length}</span></h3>
                <p className="muted xsmall" style={{ margin: '0 0 8px' }}>Made by hand or by another tool{others.some(o => o.paseo) ? ' (Paseo)' : ''}. Adopt one to work in it from here, or remove it — its uncommitted work is saved in the repository first.</p>
                <div className="srv-list">{others.map(o => (
                  <div key={o.path} className="srv-row wt-other">
                    <Icon name="fork" size={14} />
                    <span className="srv-main">
                      <span className="srv-title mono">{o.branch || '(detached)'}</span>
                      <span className="srv-meta muted xsmall"><span className="ellipsis">{shortPath(o.path)}</span>{o.paseo && <span className="badge">Paseo</span>}{o.missing && <span className="badge danger">folder gone</span>}{o.host_name && <span className="badge">{o.host_name}</span>}</span>
                    </span>
                    <span className="srv-side row">
                      {busy === o.path ? <Spinner /> : <>
                        {!o.missing && o.branch && <button type="button" className="btn sm" onClick={() => adopt(o)}>Adopt</button>}
                        <button type="button" className="btn sm ghost" onClick={() => setRemoving(o)}>{o.missing ? 'Prune' : 'Remove…'}</button>
                      </>}
                    </span>
                  </div>
                ))}</div>
              </section>
            )}
            {archived.length > 0 && (
              <section className="srv-group">
                <h3 className="srv-h"><button type="button" className="link-btn" onClick={() => setShowArchived(v => !v)}><Icon name={showArchived ? 'chevron-down' : 'chevron-right'} size={13} />Archived</button><span className="muted">{archived.length}</span></h3>
                {showArchived && <div className="srv-list">{archived.map(w => (
                  <button key={w.id} type="button" className="srv-row" onClick={() => setOpen(w.id)}>
                    <Icon name="archive" size={14} />
                    <span className="srv-main"><span className="srv-title mono">{w.branch}</span><span className="srv-meta muted xsmall">{shortPath(w.repo)} · archived {ago(w.archived_at)}{w.title ? ` · ${w.title}` : ''}</span></span>
                    <span className="srv-side muted xsmall">{w.merged_at ? 'merged' : w.archive_ref ? 'work saved' : ''}</span>
                  </button>
                ))}</div>}
              </section>
            )}
          </>}
        </div>
      </div>
      {open !== null && <WorktreePanel id={open} onClose={() => { setOpen(null); reload() }} />}
      {settings && <WorktreeSettingsDialog info={settings.info} host={settings.host} onClose={() => setSettings(null)} />}
      {removing && <Confirm title={`Remove ${removing.branch || 'this worktree'}?`} confirmLabel="Remove"
        message={<>The folder <code>{removing.path}</code> is removed. Anything uncommitted in it is saved under <code>refs/pocket-ai/removed/…</code> in the repository first; the branch stays.</>}
        onCancel={() => setRemoving(null)} onConfirm={() => { const o = removing; setRemoving(null); removeOther(o) }} />}
    </div>
  )
}
