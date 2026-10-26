// Every worktree in one place: what each holds (ahead, uncommitted, merged,
// size on disk), its threads, and tidying up — archive what is merged,
// restore or delete archived ones, adopt or remove worktrees made elsewhere
// (by hand, by Paseo).
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, shortPath } from '../lib/format'
import type { Worktree } from '../lib/types'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { WorktreePanel } from '../components/Worktree'
import { type Anchor, Confirm, Empty, errMsg, Loading, Menu, type MenuItem, MenuButton, Spinner, useToast } from '../components/ui'

interface Group { id: number; name: string; position: number; collapsed: boolean; items: number[] }
// what is being dragged: a worktree card or a group's heading
type Drag = { kind: 'wt'; id: number } | { kind: 'group'; id: number }

interface Other { host_id: number; host_name?: string; repo: string; path: string; branch: string; head: string; missing: boolean; paseo: boolean }

const size = (kb?: number) => !kb ? '' : kb < 1024 ? `${kb} KB` : kb < 1024 * 1024 ? `${(kb / 1024).toFixed(0)} MB` : `${(kb / 1024 / 1024).toFixed(1)} GB`

export default function WorktreesPage({ onMenu }: { onMenu?: () => void }) {
  const { worktrees } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [list, setList] = useState<Worktree[] | null>(null)
  const [others, setOthers] = useState<Other[]>([])
  const [n, setN] = useState(0)
  const [params, setParams] = useSearchParams()
  // ?open=<id>: a link from another app (Pocket Projects' issue) opens that worktree
  const [open, setOpen] = useState<number | null>(() => Number(params.get('open')) || null)
  const [busy, setBusy] = useState('')
  const [showArchived, setShowArchived] = useState(false)
  const [removing, setRemoving] = useState<Other | null>(null)
  // your own groups (drag worktrees into them); the rest shows by repository
  const [wgroups, setWGroups] = useState<Group[]>([])
  const [drag, setDrag] = useState<Drag | null>(null)
  const [over, setOver] = useState('')
  const [editing, setEditing] = useState<number | null>(null)
  const [cardMenu, setCardMenu] = useState<{ w: Worktree; at: Anchor } | null>(null)

  // The list shows at once; git state and folder sizes (ssh to other
  // machines, du) fill in as they arrive. The cheap list from the app
  // context keeps it current between loads.
  useEffect(() => {
    let live = true
    let full = false
    let sizes: Record<number, number> | null = null
    const withSizes = (d: Worktree[]) => sizes ? d.map(w => w.id in sizes! ? { ...w, state: { ...w.state, size_kb: sizes![w.id] } as Worktree['state'] } : w) : d
    api.get<Worktree[]>(`${V1}/worktrees`).then(d => { if (live && !full) setList(withSizes(d)) }).catch(() => {})
    api.get<Worktree[]>(`${V1}/worktrees?state=1`).then(d => { if (live) { full = true; setList(withSizes(d)) } }).catch(e => { if (live) { toast(errMsg(e), 'err'); setList(l => l || []) } })
    api.get<Worktree[]>(`${V1}/worktrees?sizes=1`).then(d => {
      if (!live) return
      sizes = Object.fromEntries(d.filter(w => w.state?.size_kb).map(w => [w.id, w.state!.size_kb!]))
      setList(l => l && withSizes(l))
    }).catch(() => {})
    api.get<Other[]>(`${V1}/worktrees/others`).then(d => { if (live) setOthers(d) }).catch(() => {})
    api.get<Group[]>(`${V1}/worktree-groups`).then(d => { if (live) setWGroups(d) }).catch(() => {})
    return () => { live = false }
  }, [n, worktrees.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const reload = () => setN(x => x + 1)

  const grouped = useMemo(() => new Set(wgroups.flatMap(g => g.items)), [wgroups])
  const byId = useMemo(() => new Map((list || []).map(w => [w.id, w])), [list])
  const groups = useMemo(() => {
    const m = new Map<string, { repo: string; host: number; host_name?: string; items: Worktree[] }>()
    for (const w of (list || []).filter(x => x.status === 'active' && !grouped.has(x.id))) {
      const k = `${w.host_id}:${w.repo}`
      if (!m.has(k)) m.set(k, { repo: w.repo, host: w.host_id, host_name: w.host_name, items: [] })
      m.get(k)!.items.push(w)
    }
    return [...m.values()]
  }, [list, grouped])
  const activeCount = (list || []).filter(w => w.status === 'active').length

  // ---------- your groups: every change shows at once, then is saved ----------
  const saveFailed = (e: unknown) => { toast(errMsg(e), 'err'); reload() }
  const newGroup = async (withWorktree?: number) => {
    try {
      const g = await api.post<Group>(`${V1}/worktree-groups`, { name: 'New group' })
      setWGroups(gs => [...gs.map(x => withWorktree ? { ...x, items: x.items.filter(i => i !== withWorktree) } : x), { ...g, items: withWorktree ? [withWorktree] : [] }])
      setEditing(g.id)
      if (withWorktree) await api.put(`${V1}/worktrees/${withWorktree}/group`, { group_id: g.id })
    } catch (e) { saveFailed(e) }
  }
  /** Puts a worktree in a group (0: back to its repository), before another one or at the end. */
  const place = (wid: number, gid: number, before?: number) => {
    let order: number[] = []
    const next = wgroups.map(g => {
      const items = g.items.filter(i => i !== wid)
      if (g.id !== gid) return { ...g, items }
      const at = before !== undefined ? items.indexOf(before) : -1
      items.splice(at < 0 ? items.length : at, 0, wid)
      order = items
      return { ...g, items }
    })
    setWGroups(next)
    api.put(`${V1}/worktrees/${wid}/group`, { group_id: gid, order: gid ? order : [] }).catch(saveFailed)
  }
  const moveGroup = (id: number, before: number) => {
    if (id === before) return
    const g = wgroups.find(x => x.id === id)
    if (!g) return
    const rest = wgroups.filter(x => x.id !== id)
    const at = rest.findIndex(x => x.id === before)
    rest.splice(at < 0 ? rest.length : at, 0, g)
    setWGroups(rest)
    api.put(`${V1}/worktree-groups/order`, { ids: rest.map(x => x.id) }).catch(saveFailed)
  }
  const patchGroup = (g: Group, p: Partial<Pick<Group, 'name' | 'collapsed'>>) => {
    setWGroups(gs => gs.map(x => x.id === g.id ? { ...x, ...p } : x))
    api.patch(`${V1}/worktree-groups/${g.id}`, p).catch(saveFailed)
  }
  const deleteGroup = (g: Group) => {
    setWGroups(gs => gs.filter(x => x.id !== g.id))
    api.del(`${V1}/worktree-groups/${g.id}`).catch(saveFailed)
  }
  // drag and drop (HTML5): a card onto a group, a card, a repository or "new group"; a heading onto a heading
  const dragData = (e: React.DragEvent, d: Drag) => { e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', `${d.kind}:${d.id}`); setDrag(d) }
  const endDrag = () => { setDrag(null); setOver('') }
  const target = (key: string, ok: boolean, drop: () => void) => ok ? {
    onDragOver: (e: React.DragEvent) => { e.preventDefault(); e.dataTransfer.dropEffect = 'move'; if (over !== key) setOver(key) },
    onDragLeave: (e: React.DragEvent) => { if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setOver(o => o === key ? '' : o) },
    onDrop: (e: React.DragEvent) => { e.preventDefault(); e.stopPropagation(); drop(); endDrag() },
  } : {}
  const cardItems = (w: Worktree): MenuItem[] => {
    const inGroup = wgroups.find(g => g.items.includes(w.id))
    return [
      { heading: 'Move to' },
      ...wgroups.map(g => ({ label: g.name, icon: 'folder', checked: inGroup?.id === g.id, onSelect: () => place(w.id, g.id) })),
      { label: 'New group…', icon: 'plus', onSelect: () => { void newGroup(w.id) } },
      ...(inGroup ? ['sep' as const, { label: `Out of “${inGroup.name}”`, icon: 'x', onSelect: () => place(w.id, 0) }] : []),
    ]
  }
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
      <div key={w.id} role="button" tabIndex={0} className={`wt-card ${drag?.kind === 'wt' && drag.id === w.id ? 'dragging' : ''} ${over === `card:${w.id}` ? 'drop-before' : ''}`}
        onClick={() => setOpen(w.id)} onKeyDown={e => { if (e.key === 'Enter') setOpen(w.id) }}
        draggable onDragStart={e => dragData(e, { kind: 'wt', id: w.id })} onDragEnd={endDrag}
        onContextMenu={e => { e.preventDefault(); setCardMenu({ w, at: { x: e.clientX, y: e.clientY, w: 0, h: 0 } }) }}
        {...target(`card:${w.id}`, drag?.kind === 'wt' && drag.id !== w.id && wgroups.some(g => g.items.includes(w.id)), () => {
          const g = wgroups.find(x => x.items.includes(w.id))
          if (g && drag?.kind === 'wt') place(drag.id, g.id, w.id)
        })}>
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
        actions={<>{activeCount > 0 && <button type="button" className="btn sm ghost" onClick={() => { void newGroup() }} title="A group of your own: drag worktrees into it"><Icon name="plus" size={13} />New group</button>}{merged.length > 0 && <button type="button" className="btn sm" disabled={!!busy} onClick={cleanup} title="Archive every merged worktree without uncommitted work, and delete its branch">{busy === 'cleanup' ? <Spinner /> : <Icon name="archive" size={13} />}Clean up {merged.length} merged</button>}</>} />
      <div className="page-scroll">
        <div className="page-inner srv">
          {list === null ? <Loading label="Looking at the worktrees…" /> : !groups.length && !wgroups.length && !archived.length && !others.length ? (
            <Empty title="No worktrees yet" icon="fork">On the start page, pick a folder inside a git repository and switch on <b>Worktree</b>: the agent gets its own copy and branch, and you merge (or drop) its work from here or from the thread.</Empty>
          ) : <>
            {total > 0 && <p className="muted xsmall" style={{ margin: '0 0 4px' }}>{size(total)} on disk in {activeCount} worktrees.</p>}
            {wgroups.map(g => {
              const items = g.items.map(i => byId.get(i)).filter((w): w is Worktree => !!w && w.status === 'active')
              return (
                <section key={`g${g.id}`} className={`srv-group wt-group ${over === `group:${g.id}` ? 'drop-on' : ''} ${drag?.kind === 'group' && drag.id === g.id ? 'dragging' : ''}`}
                  {...target(`group:${g.id}`, !!drag && !(drag.kind === 'group' && drag.id === g.id), () => {
                    if (drag?.kind === 'wt') place(drag.id, g.id)
                    else if (drag?.kind === 'group') moveGroup(drag.id, g.id)
                  })}>
                  <h3 className="srv-h wt-group-h" draggable={editing !== g.id} onDragStart={e => dragData(e, { kind: 'group', id: g.id })} onDragEnd={endDrag} title="Drag to reorder the groups">
                    <button type="button" className="link-btn" onClick={() => patchGroup(g, { collapsed: !g.collapsed })} aria-label={g.collapsed ? 'Unfold' : 'Fold'}><Icon name={g.collapsed ? 'chevron-right' : 'chevron-down'} size={13} /></button>
                    <Icon name="folder" size={12} />
                    {editing === g.id
                      ? <input className="wt-group-name" autoFocus defaultValue={g.name} aria-label="Group name" onFocus={e => e.currentTarget.select()}
                          onBlur={e => { const v = e.currentTarget.value.trim(); setEditing(null); if (v && v !== g.name) patchGroup(g, { name: v }) }}
                          onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur(); if (e.key === 'Escape') { e.currentTarget.value = g.name; e.currentTarget.blur() } }} />
                      : <span className="ellipsis" onDoubleClick={() => setEditing(g.id)} title="Double-click to rename">{g.name}</span>}
                    <span className="muted">{items.length}</span>
                    <span className="grow" />
                    <MenuButton label={`${g.name} menu`} className="btn ghost icon-only xs" align="right" width={200} items={[
                      { label: 'Rename', icon: 'edit', onSelect: () => setEditing(g.id) },
                      { label: g.collapsed ? 'Unfold' : 'Fold', icon: g.collapsed ? 'chevron-down' : 'chevron-right', onSelect: () => patchGroup(g, { collapsed: !g.collapsed }) },
                      'sep',
                      { label: 'Delete the group', icon: 'trash', danger: true, hint: 'worktrees stay', onSelect: () => deleteGroup(g) },
                    ]}><Icon name="more" size={15} /></MenuButton>
                  </h3>
                  {!g.collapsed && (items.length ? <div className="wt-cards">{items.map(card)}</div>
                    : <div className="wt-group-empty muted xsmall">Drag worktrees here (or right-click one → Move to)</div>)}
                </section>
              )
            })}
            {drag?.kind === 'wt' && (
              <div className={`wt-new-drop ${over === 'new' ? 'drop-on' : ''}`} {...target('new', true, () => { void newGroup(drag.id) })}>
                <Icon name="plus" size={14} />Drop here for a new group
              </div>
            )}
            {groups.map(g => (
              <section key={`${g.host}:${g.repo}`} className={`srv-group ${over === `repo:${g.host}:${g.repo}` ? 'drop-on' : ''}`}
                {...target(`repo:${g.host}:${g.repo}`, drag?.kind === 'wt' && grouped.has(drag.id), () => { if (drag?.kind === 'wt') place(drag.id, 0) })}>
                <h3 className="srv-h"><span className="ellipsis">{shortPath(g.repo)}</span>{g.host_name && <span className="badge"><Icon name="server" size={10} />{g.host_name}</span>}<span className="muted">{g.items.length}</span>
                  </h3>
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
      {cardMenu && <Menu items={cardItems(cardMenu.w)} anchor={cardMenu.at} onClose={() => setCardMenu(null)} width={230} />}
      {open !== null && <WorktreePanel id={open} onClose={() => { setOpen(null); if (params.has('open')) setParams(p => { p.delete('open'); return p }, { replace: true }); reload() }} />}
      {removing && <Confirm title={`Remove ${removing.branch || 'this worktree'}?`} confirmLabel="Remove"
        message={<>The folder <code>{removing.path}</code> is removed. Anything uncommitted in it is saved under <code>refs/pocket-ai/removed/…</code> in the repository first; the branch stays.</>}
        onCancel={() => setRemoving(null)} onConfirm={() => { const o = removing; setRemoving(null); removeOther(o) }} />}
    </div>
  )
}
