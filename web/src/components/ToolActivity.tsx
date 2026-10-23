import { useState } from 'react'
import type { Item, ToolInfo } from '../lib/types'
import { Icon } from './icons'
import { ToolRow } from './ToolRow'
import { Spinner } from './ui'

const RUN_ICON: Record<string, string> = { bash: 'terminal', edit: 'file-edit', write: 'file-edit', read: 'file', search: 'search', web: 'globe', task: 'bot', mcp: 'layers', other: 'wrench' }

/** "Ran 3 commands, read 2 files" for a run of tool calls. */
export function runSummary(tools: ToolInfo[]): string {
  const n: Record<string, number> = {}
  for (const t of tools) {
    const k = t.kind === 'write' ? 'edit' : t.kind
    n[k] = (n[k] || 0) + 1
  }
  const pl = (c: number, one: string, many: string) => `${c} ${c === 1 ? one : many}`
  const parts: string[] = []
  if (n.bash) parts.push(`ran ${pl(n.bash, 'command', 'commands')}`)
  if (n.read) parts.push(`read ${pl(n.read, 'file', 'files')}`)
  if (n.edit) parts.push(`edited ${pl(n.edit, 'file', 'files')}`)
  if (n.search) parts.push(`searched ${pl(n.search, 'time', 'times')}`)
  if (n.web) parts.push(pl(n.web, 'web lookup', 'web lookups'))
  if (n.task) parts.push(pl(n.task, 'sub-agent', 'sub-agents'))
  const rest = (n.mcp || 0) + (n.other || 0)
  if (rest) parts.push(pl(rest, 'tool call', 'tool calls'))
  const s = parts.join(', ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

// A run of consecutive tool calls. One call is one quiet line (like Paseo);
// several fold into one line — "Ran 3 commands, read 2 files" — that opens
// into the calls. While one is running the line says which.
export function ToolActivity({ items }: { items: Item[] }) {
  const [open, setOpen] = useState(false)
  if (items.length === 1) return <div className="tool-run"><div id={`item-${items[0].key}`}><ToolRow tool={items[0].tool!} /></div></div>
  const tools = items.map(i => i.tool!)
  const running = tools.find(t => t.status === 'running')
  const kinds = new Set(tools.map(t => (t.kind === 'write' ? 'edit' : t.kind)))
  const icon = kinds.size === 1 ? RUN_ICON[[...kinds][0]] || 'wrench' : 'layers'
  const last = running || tools[tools.length - 1]
  const lastText = last.kind === 'bash' ? String((last.input as Record<string, unknown> | undefined)?.command || last.title || '') : (last.title || last.name)
  return (
    <div className={`tool-run grouped ${open ? 'open' : ''}`}>
      <button type="button" className="tool-head tool-run-head" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <span className="tool-ico">{running ? <Spinner size={12} /> : <Icon name={icon} size={14} />}</span>
        <span className="tool-name">{runSummary(tools)}</span>
        {!open && <span className="tool-title ellipsis" title={lastText}>{running ? lastText : ''}</span>}
        <Icon name="chevron-down" size={14} className="tool-caret" />
      </button>
      {open && <div className="tool-run-body">
        {items.map(item => <div key={item.key} id={`item-${item.key}`}><ToolRow tool={item.tool!} /></div>)}
      </div>}
    </div>
  )
}
