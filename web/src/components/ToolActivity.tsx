import { useState } from 'react'
import type { Item, ToolInfo } from '../lib/types'
import { Icon } from './icons'
import { ToolBody } from './ToolRow'
import { Spinner } from './ui'

const RUN_ICON: Record<string, string> = { bash: 'terminal', edit: 'file-edit', write: 'file-edit', read: 'file', search: 'search', web: 'globe', task: 'bot', mcp: 'layers', other: 'wrench' }

/** "Ran 3 commands, read 2 files" for a run of tool calls. */
export function runSummary(tools: ToolInfo[]): string {
  const n: Record<string, number> = {}
  for (const t of tools) {
    const k = t.kind === 'write' ? 'edit' : t.kind
    n[k] = (n[k] || 0) + 1
  }
  const pl = (c: number, one: string, many: string) => c === 1 ? `${/^[aeiou]/.test(one) ? 'an' : 'a'} ${one}` : `${c} ${many}`
  const parts: string[] = []
  if (n.bash) parts.push(`ran ${pl(n.bash, 'command', 'commands')}`)
  if (n.read) parts.push(`read ${pl(n.read, 'file', 'files')}`)
  if (n.edit) parts.push(`edited ${pl(n.edit, 'file', 'files')}`)
  if (n.search) parts.push(n.search === 1 ? 'searched' : `searched ${n.search} times`)
  if (n.web) parts.push(pl(n.web, 'web lookup', 'web lookups'))
  if (n.task) parts.push(pl(n.task, 'sub-agent', 'sub-agents'))
  const rest = (n.mcp || 0) + (n.other || 0)
  if (rest) parts.push(pl(rest, 'tool call', 'tool calls'))
  const s = parts.join(', ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

// A run of consecutive tool calls — one or many — is one quiet line that says
// what kind of work it was ("Ran a command", "Ran 3 commands, read 2 files"),
// never the commands themselves; it opens into the calls with their details.
export function ToolActivity({ items }: { items: Item[] }) {
  const [open, setOpen] = useState(false)
  const tools = items.map(i => i.tool!)
  const running = tools.find(t => t.status === 'running')
  const kinds = new Set(tools.map(t => (t.kind === 'write' ? 'edit' : t.kind)))
  const icon = kinds.size === 1 ? RUN_ICON[[...kinds][0]] || 'wrench' : 'layers'
  return (
    <div className={`tool-run grouped ${open ? 'open' : ''}`} id={items.length === 1 ? `item-${items[0].key}` : undefined}>
      <button type="button" className="tool-head tool-run-head" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <span className="tool-ico">{running ? <Spinner size={12} /> : <Icon name={icon} size={14} />}</span>
        <span className="tool-name">{runSummary(tools)}</span>
        <Icon name="chevron-down" size={14} className="tool-caret" />
      </button>
      {/* one layer: the commands and their output, straight away */}
      {open && <div className="tool-run-body">
        {items.map(item => <div key={item.key} id={items.length > 1 ? `item-${item.key}` : undefined} className="tool-run-call"><ToolBody tool={item.tool!} className="tool-run-detail" /></div>)}
      </div>}
    </div>
  )
}
