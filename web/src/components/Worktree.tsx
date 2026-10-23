// Git worktrees in the UI: choosing one when a thread starts (WorktreeChooser),
// the branch chip + panel in a thread (WorktreeChip / WorktreePanel: status,
// changes, commit, update, merge, push / pull request, archive) and the
// per-repository setup (WorktreeSettingsDialog). The page with all of them is
// pages/WorktreesPage.tsx.
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError, qs, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, shortPath } from '../lib/format'
import type { Worktree, WorktreeRepoInfo, WorktreeSetupConfig, WorktreeSpec } from '../lib/types'
import { Select } from './Select'
import { Icon } from './icons'
import { Diff } from './ToolRow'
import { Confirm, CopyField, errMsg, Field, Loading, Modal, Popover, rectOf, Segmented, Spinner, Switch, useToast, type Anchor } from './ui'

// ---------- changes ----------

export interface DiffFile { path: string; from?: string; status: 'added' | 'deleted' | 'modified' | 'renamed' | 'binary'; add: number; del: number; body: string }

/** Splits a unified diff (git diff + --no-index new files) into files. */
export function parseDiff(text: string): DiffFile[] {
  const out: DiffFile[] = []
  for (const chunk of text.split(/^(?=diff --git )/m)) {
    if (!chunk.startsWith('diff --git ')) continue
    const lines = chunk.split('\n')
    let path = '', from = '', status: DiffFile['status'] = 'modified', add = 0, del = 0
    const m = lines[0].match(/^diff --git a\/(.*) b\/(.*)$/)
    if (m) { from = m[1]; path = m[2] }
    for (const l of lines.slice(1)) {
      if (l.startsWith('new file mode') || l === '--- /dev/null') status = 'added'
      else if (l.startsWith('deleted file mode') || l === '+++ /dev/null') status = 'deleted'
      else if (l.startsWith('rename to ')) { status = 'renamed'; path = l.slice(10) }
      else if (l.startsWith('rename from ')) from = l.slice(12)
      else if (l.startsWith('Binary files')) status = 'binary'
      else if (l.startsWith('+++ b/')) path = l.slice(6)
      else if (l.startsWith('+') && !l.startsWith('+++')) add++
      else if (l.startsWith('-') && !l.startsWith('---')) del++
    }
    const at = lines.findIndex(l => l.startsWith('@@'))
    out.push({ path, from: status === 'renamed' ? from : undefined, status, add, del, body: at >= 0 ? lines.slice(at).join('\n').replace(/\n$/, '') : '' })
  }
  return out
}

export function ChangesView({ worktree, refreshKey }: { worktree: Worktree; refreshKey?: unknown }) {
  const [files, setFiles] = useState<DiffFile[] | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  useEffect(() => {
    let live = true
    setErr('')
    api.getText(`${V1}/worktrees/${worktree.id}/diff`).then(t => { if (live) setFiles(parseDiff(t)) }).catch(e => { if (live) { setErr(errMsg(e)); setFiles([]) } })
    return () => { live = false }
  }, [worktree.id, refreshKey])
  if (!files) return <Loading label="Reading the changes…" />
  if (err) return <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>
  if (!files.length) return <p className="muted small" style={{ margin: 0 }}>No changes against {worktree.base} yet.</p>
  const add = files.reduce((n, f) => n + f.add, 0), del = files.reduce((n, f) => n + f.del, 0)
  const many = files.length > 6
  return (
    <div className="wt-changes">
      <div className="wt-changes-h muted small">
        <span className="grow">{files.length} file{files.length === 1 ? '' : 's'} changed against <b>{worktree.base}</b> (committed and not)</span>
        <span className="wt-add tnum">+{add}</span><span className="wt-del tnum">−{del}</span>
      </div>
      {files.map(f => {
        const isOpen = open[f.path] ?? !many
        return (
          <div key={f.path} className="wt-file">
            <button type="button" className="wt-file-h" aria-expanded={isOpen} onClick={() => setOpen(o => ({ ...o, [f.path]: !isOpen }))}>
              <Icon name={isOpen ? 'chevron-down' : 'chevron-right'} size={13} />
              <span className={`wt-file-st ${f.status}`}>{f.status === 'added' ? 'A' : f.status === 'deleted' ? 'D' : f.status === 'renamed' ? 'R' : f.status === 'binary' ? 'B' : 'M'}</span>
              <span className="ellipsis mono grow" title={f.from ? `${f.from} → ${f.path}` : f.path}>{f.from ? `${f.from} → ` : ''}{f.path}</span>
              <span className="wt-add tnum">+{f.add}</span><span className="wt-del tnum">−{f.del}</span>
            </button>
            {isOpen && (f.body ? <Diff text={f.body} /> : <div className="muted xsmall wt-file-empty">{f.status === 'binary' ? 'Binary file' : 'No text changes'}</div>)}
          </div>
        )
      })}
    </div>
  )
}

// ---------- starting a thread in a worktree ----------

/** The "Worktree" switch next to the folder on the start page. */
export function WorktreeChooser({ cwd, host, value, onChange }: { cwd: string; host: number; value: WorktreeSpec | null; onChange: (v: WorktreeSpec | null) => void }) {
  const [info, setInfo] = useState<WorktreeRepoInfo | null>(null)
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  useEffect(() => {
    setInfo(null)
    if (!cwd) return
    let live = true
    api.get<WorktreeRepoInfo>(`${V1}/worktrees/config${qs({ path: cwd, host: host || undefined })}`).then(d => { if (live) setInfo(d) })
      .catch(() => { if (live && value && value.mode !== 'existing') onChange(null) }) // not a repository: no worktree
    return () => { live = false }
  }, [cwd, host]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!info) return null
  const on = !!value
  const label = !value ? 'Worktree' : value.mode === 'branch' || value.mode === 'existing' ? value.branch : value.mode === 'pr' ? `PR #${value.pr}` : `new from ${value.base || info.base}`
  return (
    <>
      <button type="button" className={`btn sm wt-toggle ${on ? 'on' : ''}`} aria-pressed={on} aria-haspopup="dialog"
        title={on ? 'Runs in its own git worktree' : 'Work in an isolated git worktree (its own folder and branch)'}
        onClick={e => setAnchor(anchor ? null : rectOf(e.currentTarget))}>
        <Icon name="fork" size={13} /><span className="ellipsis">{label}</span>
      </button>
      {anchor && <WorktreeSpecPopover anchor={anchor} info={info} host={host} value={value} onChange={onChange} onClose={() => setAnchor(null)} />}
    </>
  )
}

function WorktreeSpecPopover({ anchor, info, host, value, onChange, onClose }: { anchor: Anchor; info: WorktreeRepoInfo; host: number; value: WorktreeSpec | null; onChange: (v: WorktreeSpec | null) => void; onClose: () => void }) {
  const { worktrees } = useApp()
  const [mode, setMode] = useState<WorktreeSpec['mode'] | 'off'>(value?.mode || 'new')
  const [base, setBase] = useState(value?.base || info.base)
  const [branch, setBranch] = useState(value?.branch || '')
  const [pr, setPr] = useState(value?.pr ? String(value.pr) : '')
  const [name, setName] = useState(value?.name || '')
  const [settings, setSettings] = useState(false)
  const locals = info.branches.filter(b => !b.startsWith('origin/'))
  const baseOpts = useMemo(() => info.branches.map(b => ({ value: b, label: b })), [info.branches])
  const branchOpts = useMemo(() => {
    const seen = new Set(locals)
    return [...locals, ...info.branches.filter(b => b.startsWith('origin/') && !seen.has(b.slice(7)))].map(b => ({ value: b, label: b }))
  }, [info.branches]) // eslint-disable-line react-hooks/exhaustive-deps
  const open = worktrees.filter(w => w.status === 'active' && w.repo === info.repo && w.host_id === host)
  const cfg = info.config.effective
  const apply = () => {
    if (mode === 'off') onChange(null)
    else if (mode === 'branch') onChange(branch ? { mode, branch } : null)
    else if (mode === 'pr') onChange(Number(pr) > 0 ? { mode, pr: Number(pr) } : null)
    else onChange({ mode: 'new', base, name: name.trim() || undefined })
    onClose()
  }
  if (settings) return <WorktreeSettingsDialog info={info} host={host} onClose={() => setSettings(false)} />
  return (
    <Popover anchor={anchor} onClose={onClose} width={380} label="Worktree" prefer="below">
      <div className="wt-pop stack">
        <div>
          <b>Work in a worktree</b>
          <p className="muted small" style={{ margin: '2px 0 0' }}>Its own folder and branch, so the main checkout and other agents are untouched. Review, merge or throw away when done.</p>
        </div>
        <Segmented size="sm" value={mode} onChange={setMode} label="Worktree" options={[
          { value: 'off', label: 'Off' }, { value: 'new', label: 'New branch' }, { value: 'branch', label: 'Branch' }, { value: 'pr', label: 'PR' },
        ]} />
        {mode === 'new' && <>
          <Field label="Start from"><Select searchable value={base} onChange={setBase} options={baseOpts.length ? baseOpts : [{ value: base, label: base }]} label="Start from" /></Field>
          <Field label="Name" help="Folder and branch. Empty = from your prompt, e.g. fix-login-timeout.">
            <input value={name} onChange={e => setName(e.target.value)} placeholder="from the prompt" spellCheck={false} autoCapitalize="off" className="mono" />
          </Field>
        </>}
        {mode === 'branch' && <Field label="Check out" help="An existing branch (a remote one gets a local branch tracking it).">
          <Select searchable value={branch} onChange={setBranch} options={branchOpts} label="Branch" placeholder="Pick a branch…" />
        </Field>}
        {mode === 'pr' && <Field label="Pull request number" help="Fetched from origin into pr-<number>.">
          <input value={pr} onChange={e => setPr(e.target.value.replace(/\D/g, ''))} inputMode="numeric" placeholder="123" style={{ maxWidth: 140 }} />
        </Field>}
        {mode !== 'off' && (
          <div className="wt-pop-setup muted xsmall">
            <Icon name="terminal" size={12} />
            <span className="grow">{cfg.setup.length ? <>Setup: <code>{cfg.setup[0]}</code>{cfg.setup.length > 1 ? ` +${cfg.setup.length - 1}` : ''}</> : 'No setup commands'}
              {info.config.file && !info.config.saved ? ` (from ${info.config.file})` : ''} · copies {cfg.copy.length} file patterns</span>
            <button type="button" className="link-btn xsmall" onClick={() => setSettings(true)}>Edit</button>
          </div>
        )}
        {open.length > 0 && mode !== 'off' && (
          <div className="wt-pop-open">
            <div className="muted xsmall">Or continue in one of this repo’s worktrees</div>
            {open.slice(0, 5).map(w => (
              <button key={w.id} type="button" className={`wt-pop-wt ${value?.mode === 'existing' && value.id === w.id ? 'active' : ''}`} onClick={() => { onChange({ mode: 'existing', id: w.id, branch: w.branch }); onClose() }}>
                <Icon name="fork" size={12} /><span className="ellipsis mono">{w.branch}</span><span className="muted xsmall">{ago(w.created_at)}</span>
              </button>
            ))}
          </div>
        )}
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <button type="button" className="btn sm" onClick={onClose}>Cancel</button>
          <button type="button" className="btn primary sm" disabled={(mode === 'branch' && !branch) || (mode === 'pr' && !(Number(pr) > 0))} onClick={apply}>Use</button>
        </div>
      </div>
    </Popover>
  )
}

// ---------- per-repository setup ----------

const lines = (s: string) => s.split('\n').map(x => x.trim()).filter(Boolean)

export function WorktreeSettingsDialog({ info, host, onClose }: { info: WorktreeRepoInfo; host: number; onClose: () => void }) {
  const { toast } = useToast()
  const start = info.config.saved || info.config.file_config
  const [setup, setSetup] = useState((start.setup || []).join('\n'))
  const [teardown, setTeardown] = useState((start.teardown || []).join('\n'))
  const [copy, setCopy] = useState((info.config.saved?.copy || info.config.file_config.copy || []).join('\n'))
  const [base, setBase] = useState(start.base || '')
  const [busy, setBusy] = useState(false)
  const save = async (saved: WorktreeSetupConfig | null) => {
    setBusy(true)
    try {
      await api.put(`${V1}/worktrees/config`, { host, path: info.repo, saved })
      toast(saved ? 'Worktree setup saved' : `Back to ${info.config.file || 'no setup'}`)
      onClose()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={`Worktree setup · ${info.repo.split('/').pop()}`} onClose={onClose} size="lg" footer={<>
      {info.config.saved && <button type="button" className="btn ghost" disabled={busy} onClick={() => save(null)}>{info.config.file ? `Use ${info.config.file}` : 'Clear'}</button>}
      <span className="grow" />
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy} onClick={() => save({ setup: lines(setup), teardown: lines(teardown), copy: lines(copy), base: base.trim() || undefined })}>Save</button>
    </>}>
      <div className="stack">
        <p className="muted small" style={{ margin: 0 }}>
          Kept in Pocket AI for <code>{shortPath(info.repo)}</code>, so nothing has to be added to the repository.
          {info.config.file ? <> The repo’s <code>{info.config.file}</code> is used for anything left empty here{info.config.saved ? '' : ' (it is what runs now)'}.</> : null}
          {' '}Commands run in the new worktree with <code>sh</code>, one after another; <code>$POCKET_ROOT_PATH</code> is the main checkout, <code>$POCKET_WORKTREE_PATH</code> the worktree (Paseo’s <code>$PASEO_*</code> names work too).
        </p>
        <Field label="Setup commands" help="One per line; the first failure stops. The agent starts after they finish (you see the output in the thread).">
          <textarea value={setup} onChange={e => setSetup(e.target.value)} rows={4} className="mono" spellCheck={false} placeholder={info.config.file_config.setup?.join('\n') || 'pnpm install'} />
        </Field>
        <Field label="Files to copy from the main checkout" help={<>Git patterns, one per line, for files git does not track. Always copied: <code>{info.config.default_copy.join(' ')}</code></>}>
          <textarea value={copy} onChange={e => setCopy(e.target.value)} rows={2} className="mono" spellCheck={false} placeholder="config/local.yml" />
        </Field>
        <Field label="Teardown commands" help="Run before a worktree is archived (e.g. docker compose down). A failure does not stop the archive.">
          <textarea value={teardown} onChange={e => setTeardown(e.target.value)} rows={2} className="mono" spellCheck={false} placeholder={info.config.file_config.teardown?.join('\n') || 'docker compose down'} />
        </Field>
        <Field label="Default branch to start from" help={`Empty = the main checkout's branch (${info.main_branch || info.default || 'main'}).`}>
          <Select searchable value={base} onChange={setBase} label="Default base" options={[{ value: '', label: 'Main checkout’s branch' }, ...info.branches.filter(b => !b.startsWith('origin/')).map(b => ({ value: b, label: b }))]} />
        </Field>
        <p className="muted xsmall" style={{ margin: 0 }}>Worktrees of this repo go to <code>{info.dir}</code>.</p>
      </div>
    </Modal>
  )
}

// ---------- in a thread ----------

/** The worktree's branch in the thread header; opens the panel. */
export function WorktreeChip({ id, threadId, onAsk }: { id: number; threadId?: string; onAsk?: (text: string) => void }) {
  const { worktrees } = useApp()
  const [open, setOpen] = useState(false)
  const w = worktrees.find(x => x.id === id)
  if (!w) return null
  return (
    <>
      <button type="button" className={`btn ghost wt-chip ${w.status !== 'active' ? 'archived' : ''} ${w.setup === 'failed' ? 'failed' : ''}`} title={`Worktree ${w.branch} (from ${w.base}) · ${w.path}`} onClick={() => setOpen(true)}>
        <Icon name="fork" size={12} /><span className="ellipsis">{w.branch}</span>
        {w.status !== 'active' && <span className="wt-chip-tag">archived</span>}
        {w.setup === 'running' && <Spinner size={10} />}
      </button>
      {open && <WorktreePanel id={id} threadId={threadId} onAsk={onAsk} onClose={() => setOpen(false)} />}
    </>
  )
}

function Stat({ n, label, tone }: { n: number | string; label: string; tone?: string }) {
  return <div className={`wt-stat ${tone || ''}`}><b className="tnum">{n}</b><span>{label}</span></div>
}

/** Everything about one worktree, with the git actions. onAsk sends a prompt to the agent. */
export function WorktreePanel({ id, threadId, onAsk, onClose }: { id: number; threadId?: string; onAsk?: (text: string) => void; onClose: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { reloadWorktrees } = useApp()
  const [w, setW] = useState<Worktree | null>(null)
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<'overview' | 'changes'>('overview')
  const [busy, setBusy] = useState('')
  const [n, setN] = useState(0)
  const [dialog, setDialog] = useState<'' | 'commit' | 'merge' | 'archive' | 'pr' | 'forget'>('')
  const [conflicts, setConflicts] = useState<string[]>([])

  useEffect(() => {
    let live = true
    api.get<Worktree>(`${V1}/worktrees/${id}`).then(d => { if (live) setW(d) }).catch(e => { if (live) setErr(errMsg(e)) })
    return () => { live = false }
  }, [id, n])
  const refresh = () => { setN(x => x + 1); reloadWorktrees().catch(() => {}) }
  const act = async <T,>(name: string, fn: () => Promise<T>, ok?: (r: T) => string | void) => {
    setBusy(name)
    try { const r = await fn(); const m = ok?.(r); if (m) toast(m); refresh(); return r } catch (e) {
      if (e instanceof ApiError && Array.isArray(e.data.conflicts)) setConflicts(e.data.conflicts as string[])
      toast(errMsg(e), 'err'); return undefined
    } finally { setBusy('') }
  }
  // the agent: in this thread, or the worktree's latest
  const agentThread = threadId || w?.threads[w.threads.length - 1]?.id
  const ask = (text: string) => {
    if (onAsk) { onAsk(text); onClose(); return }
    if (!agentThread) return
    api.post(`${V1}/threads/${agentThread}/messages`, { text }).then(() => { navigate(`/t/${agentThread}`); onClose() }).catch(e => toast(errMsg(e), 'err'))
  }

  if (!w) return <Modal title="Worktree" onClose={onClose} size="lg">{err ? <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div> : <Loading />}</Modal>
  const st = w.state
  const active = w.status === 'active'
  const canAsk = !!(onAsk || agentThread)
  return (
    <Modal title={<span className="row" style={{ gap: 8, minWidth: 0 }}><Icon name="fork" size={16} /><span className="ellipsis mono">{w.branch}</span></span>} onClose={onClose} size="lg" className="wt-panel" footer={<>
      {active ? <>
        <button type="button" className="btn ghost danger-text" disabled={!!busy} onClick={() => setDialog('archive')}><Icon name="archive" size={14} />Archive</button>
        <span className="grow" />
        <button type="button" className="btn" onClick={() => { navigate(`/?worktree=${w.id}`); onClose() }}><Icon name="plus" size={14} /><span className="hide-narrow">New thread here</span><span className="show-narrow">Thread</span></button>
      </> : <>
        <button type="button" className="btn ghost danger-text" disabled={!!busy} onClick={() => setDialog('forget')}><Icon name="trash" size={14} />Delete for good</button>
        <span className="grow" />
        <button type="button" className="btn primary" disabled={!!busy} onClick={() => act('restore', () => api.post(`${V1}/worktrees/${w.id}/restore`), () => 'Restored — its setup runs again')}>{busy === 'restore' ? <Spinner /> : <Icon name="undo" size={14} />}Restore</button>
      </>}
    </>}>
      <div className="stack">
        <div className="wt-head">
          <div className="muted small">
            {w.title && <div className="wt-title">{w.title}</div>}
            from <b>{w.base}</b>{w.host_name ? <> on <b>{w.host_name}</b></> : null} · {shortPath(w.repo)} · made {ago(w.created_at)}
            {w.pr_url && <> · <a href={w.pr_url} target="_blank" rel="noreferrer">pull request{st?.pr_state ? ` (${st.pr_state.toLowerCase()})` : ''} ↗</a></>}
          </div>
          {active && <CopyField value={w.path} label="Folder" />}
        </div>

        {!active ? (
          <div className="callout info"><Icon name="archive" size={15} /><span>Archived {ago(w.archived_at)}. {w.archive_ref ? <>Its commits and uncommitted work are kept (<code>{w.archive_ref}</code>); Restore puts the folder back as it was.</> : 'It had no changes to keep.'} Its threads continue once it is restored.</span></div>
        ) : !st ? <Loading label="Looking at git…" /> : st.error ? (
          <div className="callout danger"><Icon name="alert" size={15} /><span>{st.error}</span></div>
        ) : st.gone ? (
          <div className="callout danger"><Icon name="alert" size={15} /><span>The folder is gone (removed outside Pocket AI). Archive it here to tidy up; the branch stays.</span></div>
        ) : <>
          {w.setup === 'failed' && (
            <div className="callout warn"><Icon name="alert" size={15} /><span className="grow">The setup failed (output in the thread). Messages wait until it passes or you continue without it.</span>
              <button type="button" className="btn sm" disabled={!!busy} onClick={() => act('setup', () => api.post(`${V1}/worktrees/${w.id}/setup`, { thread_id: threadId }), () => 'Setup started')}>Run again</button>
              <button type="button" className="btn sm" disabled={!!busy} onClick={() => act('skip', () => api.post(`${V1}/worktrees/${w.id}/setup`, { skip: true }), () => 'Continuing without setup')}>Continue</button></div>
          )}
          {w.setup === 'running' && <div className="callout info"><Spinner /><span>Setting up (output in the thread)…</span></div>}
          <div className="wt-stats">
            <Stat n={st.ahead} label={`commit${st.ahead === 1 ? '' : 's'} ahead`} tone={st.ahead ? 'accent' : ''} />
            <Stat n={st.dirty} label="uncommitted" tone={st.dirty ? 'warn' : ''} />
            <Stat n={st.behind} label={`behind ${w.base}`} tone={st.behind ? 'warn' : ''} />
            {st.merged ? <Stat n="✓" label={st.pr_state === 'MERGED' ? 'PR merged' : 'merged'} tone="ok" />
              : st.upstream ? <Stat n={st.unpushed} label="not pushed" /> : <Stat n="–" label="not pushed yet" />}
          </div>
          {(st.merging || st.rebasing || st.conflicts.length > 0 || conflicts.length > 0) && (
            <div className="callout danger"><Icon name="alert" size={15} />
              <span className="grow">{st.merging || st.rebasing ? `A ${st.rebasing ? 'rebase' : 'merge'} is in progress` : 'Conflicts'}{(st.conflicts.length ? st.conflicts : conflicts).length ? ': ' + (st.conflicts.length ? st.conflicts : conflicts).join(', ') : ''}.</span>
              {canAsk && <button type="button" className="btn sm" onClick={() => ask(st.merging || st.rebasing ? `There are merge conflicts in this worktree (${st.conflicts.join(', ')}). Resolve them carefully, keeping the intent of both sides, run the checks, and finish the ${st.rebasing ? 'rebase' : 'merge'}.` : `Bring ${w.base} into this branch (git merge ${w.base}), resolve the conflicts in ${conflicts.join(', ')} keeping the intent of both sides, run the checks and commit.`)}>Ask the agent to resolve</button>}
              {(st.merging || st.rebasing) && <button type="button" className="btn sm" onClick={() => act('abort', () => api.post(`${V1}/worktrees/${w.id}/update`, { how: 'abort' }), () => 'Aborted')}>Abort</button>}
            </div>
          )}
          <div className="wt-actions">
            <button type="button" className="btn" disabled={!!busy || !st.dirty} onClick={() => setDialog('commit')}><Icon name="check" size={14} />Commit…</button>
            <button type="button" className="btn" disabled={!!busy || !st.behind || st.dirty > 0} title={st.dirty ? 'Commit first' : st.behind ? `Merge the ${st.behind} new commit(s) of ${w.base} into this branch` : `${w.base} has nothing new`}
              onClick={() => act('update', () => api.post<{ conflicts: string[] }>(`${V1}/worktrees/${w.id}/update`, { how: 'merge' }), r => { setConflicts(r.conflicts); return r.conflicts.length ? undefined : `Up to date with ${w.base}` })}>
              {busy === 'update' ? <Spinner /> : <Icon name="arrow-down" size={14} />}Update from {w.base}</button>
            <button type="button" className="btn" disabled={!!busy || (!st.ahead && !st.dirty)} onClick={() => setDialog('pr')}><Icon name="upload" size={14} />{w.pr_url ? 'Push' : 'Push / PR…'}</button>
            <button type="button" className="btn primary" disabled={!!busy || !st.ahead || st.merged} title={st.dirty ? 'Commit first' : undefined} onClick={() => setDialog('merge')}><Icon name="merge" size={14} />Merge into {w.base}…</button>
          </div>
          {st.dirty > 0 && canAsk && <p className="muted xsmall" style={{ margin: 0 }}>Uncommitted work: <button type="button" className="link-btn xsmall" onClick={() => ask('Review the uncommitted changes in this worktree, then commit them with a clear message (split into several commits if they are unrelated).')}>ask the agent to commit it</button> or commit it yourself.</p>}
          {st.merged && <div className="callout success"><Icon name="check-circle" size={15} /><span className="grow">Its work is in {w.base}. Archive it to free the folder — everything is kept and can be restored.</span>
            <button type="button" className="btn sm" onClick={() => setDialog('archive')}>Archive</button></div>}
        </>}

        {active && st && !st.error && !st.gone && <>
          <div className="wt-tabs" role="tablist">
            <button type="button" role="tab" aria-selected={tab === 'overview'} onClick={() => setTab('overview')}>Commits<span className="muted">{st.commits.length}</span></button>
            <button type="button" role="tab" aria-selected={tab === 'changes'} onClick={() => setTab('changes')}>Changes</button>
          </div>
          {tab === 'overview' ? (
            st.commits.length ? <div className="wt-commits">{st.commits.map(c => (
              <div key={c.sha} className="wt-commit"><code>{c.sha}</code><span className="grow ellipsis">{c.subject}</span><span className="muted xsmall">{ago(new Date(c.time * 1000).toISOString())}</span></div>
            ))}</div> : <p className="muted small" style={{ margin: 0 }}>No commits on {w.branch} yet{st.dirty ? ' — the work so far is uncommitted (see Changes)' : ''}.</p>
          ) : <ChangesView worktree={w} refreshKey={n} />}
        </>}

        {w.threads.length > 0 && (
          <div className="wt-threads">
            <div className="muted xsmall">Threads in this worktree</div>
            {w.threads.map(t => (
              <button key={t.id} type="button" className={`wt-thread ${t.id === threadId ? 'current' : ''}`} onClick={() => { navigate(`/t/${t.id}`); onClose() }}>
                <span className={`prov-dot prov-${t.provider}`} /><span className="ellipsis grow">{t.title || 'Untitled'}</span>
                <span className="muted xsmall">{t.status === 'running' ? 'working' : t.status === 'waiting' ? 'waiting' : ago(t.updated_at)}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {dialog === 'commit' && <CommitDialog w={w} onClose={() => setDialog('')} onDone={() => { setDialog(''); refresh() }} />}
      {dialog === 'merge' && st && <MergeDialog w={w} onClose={() => setDialog('')} onDone={archived => { setDialog(''); refresh(); if (archived) onClose() }} onConflicts={c => { setConflicts(c); setDialog('') }} />}
      {dialog === 'pr' && <PushDialog w={w} onClose={() => setDialog('')} onDone={() => { setDialog(''); refresh() }} />}
      {dialog === 'archive' && <ArchiveDialog w={w} onClose={() => setDialog('')} onDone={() => { setDialog(''); refresh() }} />}
      {dialog === 'forget' && <Confirm title={`Delete ${w.branch} for good?`} confirmLabel="Delete"
        message={<>The saved work (<code>{w.archive_ref || 'none'}</code>) and the branch <code>{w.branch}</code> are deleted from the repository. Its threads stay, but can’t continue.</>}
        onCancel={() => setDialog('')} onConfirm={() => { setDialog(''); act('forget', () => api.del(`${V1}/worktrees/${w.id}?delete_branch=1`), () => 'Deleted').then(() => onClose()) }} />}
    </Modal>
  )
}

function CommitDialog({ w, onClose, onDone }: { w: Worktree; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try { await api.post(`${V1}/worktrees/${w.id}/commit`, { message: msg }); toast('Committed'); onDone() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Commit everything" size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !msg.trim()} onClick={go}>{busy && <Spinner />}Commit</button>
    </>}>
      <Field label="Message" help={`All ${w.state?.dirty ?? ''} changed and new files in ${w.branch}.`}>
        <textarea value={msg} onChange={e => setMsg(e.target.value)} rows={3} autoFocus placeholder={w.title || 'What changed'} onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && msg.trim()) go() }} />
      </Field>
    </Modal>
  )
}

function MergeDialog({ w, onClose, onDone, onConflicts }: { w: Worktree; onClose: () => void; onDone: (archived: boolean) => void; onConflicts: (c: string[]) => void }) {
  const { toast } = useToast()
  const st = w.state!
  const [how, setHow] = useState<'merge' | 'squash'>(st.commits.length > 1 ? 'squash' : 'merge')
  const [msg, setMsg] = useState('')
  const [push, setPush] = useState(false)
  const [archive, setArchive] = useState(true)
  const [busy, setBusy] = useState(false)
  const ff = st.behind === 0 && how === 'merge'
  const go = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ how: string; pushed?: boolean; push_error?: string; archive_error?: string }>(`${V1}/worktrees/${w.id}/merge`, { how, message: msg, push_base: push, archive, delete_branch: archive })
      toast(`Merged into ${w.base} (${r.how})${r.pushed ? ' and pushed' : ''}${archive && !r.archive_error ? ' · archived' : ''}`)
      if (r.push_error) toast('Push failed: ' + r.push_error, 'err')
      if (r.archive_error) toast('Not archived: ' + r.archive_error, 'err')
      onDone(archive && !r.archive_error)
    } catch (e) {
      if (e instanceof ApiError && Array.isArray(e.data.conflicts)) { onConflicts(e.data.conflicts as string[]); toast(errMsg(e), 'err') } else toast(errMsg(e), 'err')
    } finally { setBusy(false) }
  }
  return (
    <Modal title={`Merge ${w.branch} into ${w.base}`} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || st.dirty > 0} onClick={go}>{busy && <Spinner />}Merge</button>
    </>}>
      <div className="stack">
        {st.dirty > 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>{st.dirty} file(s) are not committed; commit them first.</span></div>}
        <Segmented value={how} onChange={setHow} label="How" options={[{ value: 'merge', label: ff ? 'Fast-forward' : 'Merge commit' }, { value: 'squash', label: 'Squash into one commit' }]} />
        <p className="muted small" style={{ margin: 0 }}>
          {how === 'squash' ? `The ${st.commits.length} commit(s) become one on ${w.base}.` : ff ? `${w.base} simply moves forward to ${w.branch}.` : `A merge commit joins ${w.branch} into ${w.base}.`}
          {' '}The merge is worked out without touching any folder; where {w.base} is checked out (usually your main checkout) it then moves forward like <code>git merge --ff-only</code> — your uncommitted work there is never overwritten (git refuses instead).
        </p>
        {(how === 'squash' || !ff) && <Field label="Commit message"><textarea value={msg} onChange={e => setMsg(e.target.value)} rows={3} placeholder={how === 'squash' ? (w.title || w.branch) + '\n\n' + st.commits.slice().reverse().map(c => '- ' + c.subject).join('\n') : `Merge branch '${w.branch}'`} /></Field>}
        <label className="row wt-check"><Switch checked={push} onChange={setPush} size="sm" /><span>Push {w.base} to origin afterwards</span></label>
        <label className="row wt-check"><Switch checked={archive} onChange={setArchive} size="sm" /><span>Archive the worktree and delete the branch (its work is in {w.base}; the archive keeps a copy)</span></label>
      </div>
    </Modal>
  )
}

function PushDialog({ w, onClose, onDone }: { w: Worktree; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const st = w.state
  const [pr, setPr] = useState(!w.pr_url)
  const [title, setTitle] = useState(w.title || w.branch)
  const [body, setBody] = useState((st?.commits || []).slice().reverse().map(c => '- ' + c.subject).join('\n'))
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ pr_url?: string; compare_url?: string }>(`${V1}/worktrees/${w.id}/push`, { pr, title, body })
      if (r.pr_url) { toast('Pushed — pull request ready'); window.open(r.pr_url, '_blank', 'noopener') }
      else if (r.compare_url) { toast('Pushed — open the page to create the pull request'); window.open(r.compare_url, '_blank', 'noopener') }
      else toast('Pushed')
      onDone()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={`Push ${w.branch}`} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || (pr && !title.trim())} onClick={go}>{busy && <Spinner />}{pr ? 'Push and open PR' : 'Push'}</button>
    </>}>
      <div className="stack">
        {st && st.dirty > 0 && <div className="callout warn"><Icon name="alert" size={15} /><span>{st.dirty} file(s) are not committed and won’t be pushed.</span></div>}
        {w.pr_url ? <p className="small" style={{ margin: 0 }}>Its pull request: <a href={w.pr_url} target="_blank" rel="noreferrer">{w.pr_url}</a>. Pushing updates it.</p>
          : <label className="row wt-check"><Switch checked={pr} onChange={setPr} size="sm" /><span>Open a pull request into {w.base} (with <code>gh</code> where it is installed, else the forge’s page)</span></label>}
        {pr && !w.pr_url && <>
          <Field label="Title"><input value={title} onChange={e => setTitle(e.target.value)} /></Field>
          <Field label="Description"><textarea value={body} onChange={e => setBody(e.target.value)} rows={5} /></Field>
        </>}
      </div>
    </Modal>
  )
}

function ArchiveDialog({ w, onClose, onDone }: { w: Worktree; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const st = w.state
  const [del, setDel] = useState(!!st?.merged)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try { await api.post(`${V1}/worktrees/${w.id}/archive`, { delete_branch: del }); toast('Archived — restore it any time'); onDone() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={`Archive ${w.branch}?`} size="sm" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy} onClick={go}>{busy && <Spinner />}Archive</button>
    </>}>
      <div className="stack">
        <p className="small" style={{ margin: 0 }}>The folder is removed (with its installed dependencies). Commits and {st?.dirty ? <b>the {st.dirty} uncommitted file(s)</b> : 'any uncommitted work'} are saved in the repository first, so <b>Restore</b> brings it all back. Idle agents in it stop; the threads stay.</p>
        <label className="row wt-check"><Switch checked={del} onChange={setDel} size="sm" /><span>Also delete the branch {st?.merged ? `(its work is in ${w.base})` : '(not merged — the archive still keeps a copy)'}</span></label>
      </div>
    </Modal>
  )
}
