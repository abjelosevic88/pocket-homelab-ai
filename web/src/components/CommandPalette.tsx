// ⌘K — jump to a thread, run a recipe, or go anywhere.
import React, { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useApp } from '../lib/app-context'
import { baseName, stamp, threadTitle } from '../lib/format'
import type { Recipe } from '../lib/types'
import { ProviderBadge, ThreadStatus } from './bits'
import { Icon } from './icons'

export interface PaletteCommand { id: string; label: string; icon: string; hint?: string; keywords?: string; run: () => void }

export function CommandPalette({ onClose, commands, openThread, runRecipe }: { onClose: () => void; commands: PaletteCommand[]; openThread: (id: string) => void; runRecipe: (r: Recipe) => void }) {
  const { threads, recipes } = useApp()
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const list = useRef<HTMLDivElement>(null)

  type Row = { key: string; group: string; icon: React.ReactNode; label: React.ReactNode; sub?: React.ReactNode; hint?: React.ReactNode; run: () => void }
  const rows = useMemo<Row[]>(() => {
    const out: Row[] = []
    const n = q.trim().toLowerCase()
    const match = (s: string) => !n || n.split(/\s+/).every(w => s.toLowerCase().includes(w))
    for (const c of commands) if (match(`${c.label} ${c.keywords || ''}`)) out.push({ key: c.id, group: 'Go to', icon: <Icon name={c.icon} size={16} />, label: c.label, hint: c.hint ? <kbd>{c.hint}</kbd> : undefined, run: c.run })
    for (const r of recipes) if (match(`${r.name} ${r.prompt}`)) out.push({ key: 'r' + r.id, group: 'Run a recipe', icon: <span className="pal-emoji">{r.icon || '✦'}</span>, label: r.name, sub: r.prompt.slice(0, 100), run: () => runRecipe(r) })
    let nThreads = 0
    for (const t of threads || []) {
      if (nThreads >= (n ? 20 : 8)) break
      if (!match(`${t.title} ${t.preview} ${t.cwd}`)) continue
      nThreads++
      out.push({ key: 't' + t.id, group: n ? 'Threads' : 'Recent threads', icon: (t.status !== 'idle' || t.unread || t.pending > 0) ? <ThreadStatus t={t} /> : <Icon name="message" size={16} />, label: threadTitle(t), sub: <span className="row" style={{ gap: 6 }}><ProviderBadge provider={t.provider} size="xs" />{baseName(t.cwd)} · {stamp(t.activity_at)}</span>, run: () => openThread(t.id) })
    }
    // recent threads first when nothing is typed
    if (!n) out.sort((a, b) => (a.group === 'Recent threads' ? 0 : 1) - (b.group === 'Recent threads' ? 0 : 1))
    return out
  }, [q, commands, recipes, threads, openThread, runRecipe])
  useEffect(() => setHi(0), [q])
  useEffect(() => { list.current?.querySelector(`[data-i="${hi}"]`)?.scrollIntoView({ block: 'nearest' }) }, [hi])

  const run = (r: Row | undefined) => { if (!r) return; onClose(); r.run() }
  let last = ''
  return createPortal(
    <div className="palette-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette-box" role="dialog" aria-label="Search and commands">
        <div className="pal-input">
          <Icon name="search" size={18} />
          <input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Find a thread, run a recipe, go somewhere…" aria-label="Search"
            onKeyDown={e => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => Math.min(rows.length - 1, h + 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => Math.max(0, h - 1)) }
              else if (e.key === 'Enter') { e.preventDefault(); run(rows[hi]) }
              else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose() }
            }} />
          <kbd>esc</kbd>
        </div>
        <div className="pal-list" ref={list} role="listbox">
          {rows.length === 0 && <div className="pal-empty">Nothing matches.</div>}
          {rows.map((r, i) => {
            const head = r.group !== last ? <div className="pal-group" key={'g' + r.group}>{r.group}</div> : null
            last = r.group
            return (
              <React.Fragment key={r.key}>
                {head}
                <div data-i={i} role="option" aria-selected={i === hi} className={`pal-row ${i === hi ? 'hi' : ''}`} onMouseEnter={() => setHi(i)} onClick={() => run(r)}>
                  <span className="pal-ico">{r.icon}</span>
                  <span className="grow" style={{ minWidth: 0 }}><span className="pal-label ellipsis">{r.label}</span>{r.sub && <span className="pal-sub">{r.sub}</span>}</span>
                  {r.hint}
                  {i === hi && <Icon name="chevron-right" size={14} className="muted" />}
                </div>
              </React.Fragment>
            )
          })}
        </div>
      </div>
    </div>, document.body)
}
