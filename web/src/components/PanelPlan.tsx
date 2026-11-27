// A Refine panel is the home of the plan it works on: every version of it,
// compared, edited and gone back to; another round on the current one; a
// word with the editor; and ways to take it to other conversations.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { diffCount, diffLines } from '../lib/diff'
import { ago, providerName } from '../lib/format'
import { VERSION_SOURCE, type Panel, type PanelChatMessage, type PanelVersion } from '../lib/panels'
import type { ProviderId, Thread } from '../lib/types'
import { offeredProviders } from './AgentPicker'
import { FolderButton, FolderPicker } from './FolderPicker'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { Select } from './Select'
import { Confirm, copyText, errMsg, MenuButton, Modal, Spinner, useToast, type MenuItem, noGrammarly } from './ui'

/** Two versions side by side in one column: what went, what came, what stayed. */
export function VersionDiff({ before, after }: { before: string; after: string }) {
  const rows = useMemo(() => diffLines(before, after), [before, after])
  if (!rows.some(r => r.kind === 'add' || r.kind === 'del')) return <div className="muted small pv-nodiff">The two are the same.</div>
  return (
    <div className="pv-diff" role="group" aria-label="What changed">
      {rows.map((r, i) => r.kind === 'skip'
        ? <div key={i} className="pv-skip">{r.count} unchanged {r.count === 1 ? 'line' : 'lines'}</div>
        : <div key={i} className={`pv-line ${r.kind}`}><span className="pv-sign" aria-hidden>{r.kind === 'add' ? '+' : r.kind === 'del' ? '−' : ''}</span>
          <span className="pv-text">{r.kind !== 'same' && r.parts ? r.parts.map((p, j) => p.hit ? <mark key={j}>{p.text}</mark> : p.text) : r.text || ' '}</span></div>)}
    </div>
  )
}

const vName = (v: PanelVersion) => v.source === 'restore' ? v.note || 'Went back' : VERSION_SOURCE[v.source] || v.source

/** The plan with its versions, and what can be done with the current one. */
export function PanelPlan({ p, onChange }: { p: Panel; onChange: (p: Panel) => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { providers, settings, upsertThread } = useApp()
  const versions = p.versions || []
  const cur = versions[versions.length - 1]
  const [at, setAt] = useState(0) // the version shown (0 = the current one)
  const [compare, setCompare] = useState(false)
  const [busy, setBusy] = useState('')
  const [edit, setEdit] = useState<{ text: string; note: string } | null>(null)
  const [round, setRound] = useState<string | null>(null)
  const [impl, setImpl] = useState<{ text: string; provider: ProviderId | ''; cwd: string } | null>(null)
  const [replace, setReplace] = useState<{ dir?: string } | null>(null)
  const [pickDir, setPickDir] = useState(false)
  const shown = versions.find(v => v.n === at) || cur
  const prev = versions.find(v => v.n === shown.n - 1)
  const running = p.status === 'running'
  const isCurrent = shown.n === cur.n
  useEffect(() => { setAt(0) }, [cur?.n]) // a new version: show it
  const changed = useMemo(() => prev ? diffCount(prev.text, shown.text) : null, [prev, shown])

  const act = async (what: string, fn: () => Promise<void>) => { setBusy(what); try { await fn() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') } }
  const post = (path: string, body?: unknown) => api.post<Panel>(`${V1}/panels/${p.id}${path}`, body).then(onChange)
  const restore = () => act('restore', async () => { await post(`/versions/${shown.n}/restore`); toast(`Version ${shown.n} is the current one again`) })
  const saveEdit = () => act('edit', async () => { await post('/versions', edit); setEdit(null); toast('Saved as a new version') })
  const startRound = () => act('round', async () => { await post('/rounds', { note: round || '' }); setRound(null) })
  const implement = () => act('impl', async () => {
    const t = await api.post<Thread>(`${V1}/panels/${p.id}/continue`, { text: impl?.text || '', provider: impl?.provider || undefined, cwd: impl?.cwd || undefined })
    upsertThread(t); setImpl(null); navigate(`/t/${t.id}`)
  })
  const saveFile = (dir?: string, force = false) => act('save', async () => {
    try {
      const r = await api.post<{ path: string }>(`${V1}/panels/${p.id}/save`, { dir, replace: force })
      toast(`Saved to ${r.path}`)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.data.exists) { setReplace({ dir }); return }
      if (e instanceof ApiError && /Pick the folder/.test(e.message)) { setPickDir(true); return }
      throw e
    }
  })
  const reference = `[${p.title.replace(/[[\]]/g, m => m === '[' ? '(' : ')')}](${p.url || `${location.origin}/panels/${p.id}`})`
  const more: MenuItem[] = [
    { label: 'Copy this version', icon: 'copy', onSelect: async () => { if (await copyText(shown.text)) toast('Copied') } },
    { label: 'Copy a link for another chat', icon: 'link', hint: 'always the current version', onSelect: async () => { if (await copyText(reference)) toast('Copied. Paste it into any conversation — or type @panels # there and pick it.', 'ok', undefined, 7000) } },
    'sep',
    { label: 'Save as a file in the project…', icon: 'folder', onSelect: () => void saveFile() },
    { label: 'Download', icon: 'download', onSelect: () => {
      const a = document.createElement('a')
      a.href = URL.createObjectURL(new Blob([shown.text + '\n'], { type: 'text/markdown' }))
      a.download = `${p.title.replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'plan'}-v${shown.n}.md`
      a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 2000)
    } },
  ]
  const provOpts = [{ value: '' as ProviderId | '', label: 'My default agent' }, ...offeredProviders(providers, settings?.default_provider).map(x => ({ value: x as ProviderId | '', label: providerName(x), disabled: !providers.find(q => q.id === x)?.available }))]

  return (
    <section className="pn-outcome pv">
      <header className="pv-head">
        <Icon name="sparkles" size={15} className="pv-ico" />
        <div className="pv-versions" role="tablist" aria-label="Versions">
          {versions.map(v => (
            <button key={v.n} type="button" role="tab" aria-selected={v.n === shown.n} className={`pv-v ${v.n === shown.n ? 'on' : ''} ${v.n === cur.n ? 'cur' : ''}`} onClick={() => { setAt(v.n); if (v.n === 1) setCompare(false) }}
              title={`Version ${v.n} · ${vName(v)} · ${ago(v.created_at)}${v.n === cur.n ? ' · the current one' : ''}`}>
              <b>v{v.n}</b><span>{v.n === cur.n ? 'Current' : vName(v)}</span>
            </button>
          ))}
          {running && <span className="pv-v pv-next"><Spinner size={10} /><span>v{cur.n + 1} being worked on</span></span>}
        </div>
        <MenuButton label="More" align="right" width={290} items={more}><Icon name="more" size={16} /></MenuButton>
      </header>
      <div className="pv-bar">
        <span className="pv-what"><b>Version {shown.n}</b><span className="muted"> · {vName(shown)} · {ago(shown.created_at)}</span>
          {changed && (changed.added > 0 || changed.removed > 0) && <span className="pv-delta" title={`Against version ${prev!.n}`}><span className="add">+{changed.added}</span> <span className="del">−{changed.removed}</span></span>}</span>
        <span className="grow" />
        {prev && <button type="button" className={`btn sm ${compare ? 'on' : ''}`} aria-pressed={compare} onClick={() => setCompare(c => !c)} title={`Show what changed against version ${prev.n}`}><Icon name="split-right" size={13} />Compare with v{prev.n}</button>}
        {!isCurrent && <button type="button" className="btn sm" disabled={running || busy === 'restore'} onClick={restore} title="Make this version the current one again; nothing is lost, it becomes a new version">{busy === 'restore' ? <Spinner size={11} /> : <Icon name="undo" size={13} />}Go back to this</button>}
        {isCurrent && <button type="button" className="btn sm" disabled={running} onClick={() => setEdit({ text: cur.text, note: '' })}><Icon name="edit" size={13} />Edit</button>}
      </div>
      {compare && prev ? <VersionDiff before={prev.text} after={shown.text} /> : <Markdown text={shown.text} />}
      {shown.note && shown.source !== 'restore' && <details className="pv-note"><summary>{shown.source === 'rewrite' ? 'What the editor changed, and what it did not take' : shown.source === 'chat' ? 'What you asked for' : 'Your note'}</summary>
        {shown.source === 'rewrite' ? <Markdown text={shown.note} /> : <p>{shown.note}</p>}</details>}
      {isCurrent && <div className="pn-outcome-foot">
        <button type="button" className="btn primary sm" disabled={running} onClick={() => setRound('')} title="The participants judge this version, and the editor rewrites it from their verdicts"><Icon name="refresh" size={13} />Another round</button>
        <button type="button" className="btn sm" onClick={() => setImpl({ text: 'Implement phase 1 of this plan.', provider: '', cwd: p.spec.host_id ? '' : p.spec.cwd })}><Icon name="play" size={12} />Implement…</button>
        <button type="button" className="btn sm" onClick={async () => { if (await copyText(reference)) toast('Link copied. Paste it into any conversation — or type @panels # there and pick it. It always stands for the current version.', 'ok', undefined, 8000) }}><Icon name="link" size={13} />Use in a chat</button>
        {p.from_thread_id && p.from_title && <button type="button" className="btn ghost sm" onClick={() => navigate(`/t/${p.from_thread_id}`)}><Icon name="message" size={13} />Back to the conversation</button>}
      </div>}

      {edit && <Modal title={`Edit version ${cur.n}`} onClose={() => setEdit(null)} size="xl" footer={<>
        <span className="muted small grow">It is saved as version {cur.n + 1}; version {cur.n} stays.</span>
        <button type="button" className="btn" onClick={() => setEdit(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={busy === 'edit' || !edit.text.trim() || edit.text.trim() === cur.text.trim()} onClick={saveEdit}>{busy === 'edit' && <Spinner />}Save as version {cur.n + 1}</button>
      </>}>
        <div className="stack tight">
          <textarea {...noGrammarly} className="pv-edit mono" autoFocus value={edit.text} onChange={e => setEdit({ ...edit, text: e.target.value })} aria-label="The text" />
          <input value={edit.note} maxLength={300} onChange={e => setEdit({ ...edit, note: e.target.value })} placeholder="What you changed (optional)" aria-label="What you changed" />
        </div>
      </Modal>}
      {round !== null && <Modal title="Another round" onClose={() => setRound(null)} footer={<>
        <button type="button" className="btn" onClick={() => setRound(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={busy === 'round'} onClick={startRound}>{busy === 'round' ? <Spinner /> : <Icon name="play" size={12} />}Start</button>
      </>}>
        <div className="stack">
          <p className="muted small" style={{ margin: 0 }}>The same models judge version {cur.n}, and the editor rewrites it from their verdicts into version {cur.n + 1}.</p>
          <textarea {...noGrammarly} autoFocus rows={4} value={round} onChange={e => setRound(e.target.value)} placeholder="Anything they should keep in mind? e.g. “leave phase 1 as it is, tighten phase 2” (optional)" />
        </div>
      </Modal>}
      {impl && <Modal title="Implement" onClose={() => setImpl(null)} footer={<>
        <button type="button" className="btn" onClick={() => setImpl(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={busy === 'impl' || !impl.text.trim()} onClick={implement}>{busy === 'impl' && <Spinner />}Start the conversation</button>
      </>}>
        <div className="stack">
          <p className="muted small" style={{ margin: 0 }}>A conversation starts with what you write and a link to this plan. The agent gets the current version with it — and the newest one again whenever you link the plan there later.</p>
          <textarea {...noGrammarly} autoFocus rows={3} value={impl.text} onChange={e => setImpl({ ...impl, text: e.target.value })} />
          <div className="row wrap">
            <Select label="Agent" value={impl.provider} options={provOpts} onChange={v => setImpl({ ...impl, provider: v })} searchable={false} width={240} />
            <FolderButton value={impl.cwd} onChange={cwd => setImpl({ ...impl, cwd })} className="btn" />
          </div>
        </div>
      </Modal>}
      {replace && <Confirm title="Replace the file?" message="A file with this name is already in that folder and says something else." confirmLabel="Replace" onCancel={() => setReplace(null)} onConfirm={() => { const d = replace.dir; setReplace(null); void saveFile(d, true) }} />}
      {pickDir && <FolderPicker value="" onPick={dir => { setPickDir(false); void saveFile(dir) }} onClose={() => setPickDir(false)} />}
    </section>
  )
}

/** The person's chat with the panel's editor about the current version. */
export function EditorChat({ p, onChange }: { p: Panel; onChange: (p: Panel) => void }) {
  const { toast } = useToast()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState<number | null>(null) // a proposal being looked at
  const chat = p.chat || []
  const versions = p.versions || []
  const cur = versions[versions.length - 1]
  const working = chat.some(m => m.role === 'editor' && m.state === 'working')
  const running = p.status === 'running'
  // new messages show at the bottom of the chat's own list; the page itself stays where it is
  const list = useRef<HTMLDivElement>(null)
  const count = chat.length + (p.chat_live?.length || 0)
  useEffect(() => { if (list.current) list.current.scrollTop = list.current.scrollHeight }, [count])
  const editor = p.seats.filter(s => s.chair && s.state !== 'failed').pop()
  const send = async () => {
    const t = text.trim()
    if (!t || busy || working || running) return
    setBusy(true)
    try { onChange(await api.post<Panel>(`${V1}/panels/${p.id}/chat`, { text: t })); setText('') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const take = async (m: PanelChatMessage) => {
    try { onChange(await api.post<Panel>(`${V1}/panels/${p.id}/chat/${m.id}/accept`)); setOpen(null); toast(`Saved as version ${cur.n + 1}`) } catch (e) { toast(errMsg(e), 'err') }
  }
  if (!cur) return null
  return (
    <section className="pc">
      <header className="pc-head">
        <Icon name="message" size={14} /><b>Talk to the editor</b>
        {editor && <span className="muted xsmall pc-who"><Icon name={editor.provider} size={12} className={`prov-glyph ${editor.provider}`} />{providerName(editor.provider)}{editor.model ? ` · ${editor.model}` : ''}</span>}
        <span className="muted xsmall pc-hint">It knows the text and every verdict. Say what to change; you decide whether to take what it proposes.</span>
      </header>
      {chat.length > 0 && <div className="pc-list" ref={list}>
        {chat.map(m => m.role === 'person'
          ? <div key={m.id} className="pc-msg me"><div className="pc-bubble">{m.text}</div></div>
          : <div key={m.id} className="pc-msg">
            {m.state === 'working'
              ? <div className="pc-bubble working">{p.chat_live ? <Markdown text={p.chat_live} /> : null}<span className="pc-typing"><Spinner size={11} />{p.chat_live ? 'Writing…' : 'Thinking…'}</span></div>
              : <div className={`pc-bubble ${m.state === 'failed' ? 'failed' : ''}`}>
                {m.text && (m.state === 'failed' ? <span>{m.text}</span> : <Markdown text={m.text} />)}
                {m.proposal && (
                  <div className={`pc-proposal ${m.version_n ? 'taken' : ''}`}>
                    <span className="pc-prop-h"><Icon name="sparkles" size={13} />{m.version_n ? `Taken as version ${m.version_n}` : 'It proposes a new version'}</span>
                    <span className="grow" />
                    <button type="button" className="btn sm" onClick={() => setOpen(m.id)}><Icon name="eye" size={13} />{m.version_n ? 'Look' : 'See what changes'}</button>
                    {!m.version_n && <button type="button" className="btn primary sm" disabled={running} onClick={() => void take(m)}>Take it as v{cur.n + 1}</button>}
                  </div>
                )}
              </div>}
          </div>)}
      </div>}
      {p.editor ? (
        <div className="pc-input">
          <textarea {...noGrammarly} rows={2} value={text} disabled={running} onChange={e => setText(e.target.value)} placeholder={running ? 'A round is running — the editor is busy with it' : `What should change in version ${cur.n}? Or ask it something about the plan.`}
            onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); void send() } }} aria-label="Message to the editor" />
          <button type="button" className="btn primary icon-only" disabled={!text.trim() || busy || working || running} onClick={send} aria-label="Send" title="Send (Enter)">{busy || working ? <Spinner size={13} /> : <Icon name="arrow-right" size={15} />}</button>
        </div>
      ) : <div className="muted small pc-none">This panel has no editor to talk to any more. “Another round” brings one in.</div>}
      {open !== null && (() => {
        const m = chat.find(x => x.id === open)
        if (!m?.proposal) return null
        // against what was current when it was proposed: the version before the one it became, else today's
        const base = m.version_n ? versions.find(v => v.n === m.version_n! - 1) || cur : cur
        return (
          <Modal title={m.version_n ? `Version ${m.version_n}, from the chat` : `Proposed: version ${cur.n + 1}`} onClose={() => setOpen(null)} size="xl" footer={<>
            <span className="muted small grow">Compared with version {base.n}</span>
            <button type="button" className="btn" onClick={() => setOpen(null)}>Close</button>
            {!m.version_n && <button type="button" className="btn primary" disabled={running} onClick={() => void take(m)}>Take it as version {cur.n + 1}</button>}
          </>}>
            <VersionDiff before={base.text} after={m.proposal} />
          </Modal>
        )
      })()}
    </section>
  )
}
