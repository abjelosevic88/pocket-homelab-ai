import type { Item, ToolInfo } from './types'

const words = (text: string) => text.replace(/[_-]+/g, ' ').trim()
const capital = (text: string) => text ? text[0].toUpperCase() + text.slice(1) : text
const apps: Record<string, string> = { ai: 'AI', core: 'Core', money: 'Money', notes: 'Notes', tasks: 'Tasks', split: 'Split', habits: 'Habits' }

export function toolLabel(tool: Pick<ToolInfo, 'name'>): string {
  const parts = tool.name.startsWith('pocket.pocket_') ? ['mcp', 'pocket', tool.name.slice(7)] : tool.name.split('__')
  if (parts[0] !== 'mcp' || parts.length < 3) return tool.name
  const server = parts[1], name = parts.slice(2).join('__')
  if (server !== 'pocket' || !name.startsWith('pocket_')) return `${capital(words(server))} · ${capital(words(name))}`
  const [app, ...rest] = name.slice(7).split('_')
  if (app === 'list' && rest.join('_') === 'apps') return 'List Pocket apps'
  const operation = rest.join('_')
  const label = apps[app] || capital(words(app))
  const action = /^(create|update|delete|set|add|remove|record|mark|complete|archive|restore|move|rename|subscribe|unsubscribe|import|export|cancel)(_|$)/.exec(operation)
  if (action) return `${label} · ${capital(words(operation))}`
  return `${operation.startsWith('search') ? 'Search' : 'Read'} ${label} ${words(operation.replace(/^(get|list|search)_?/, ''))}`.trim()
}

export type ActivityEntry = { type: 'item'; key: string; item: Item } | { type: 'tools'; key: string; items: Item[] }

// Consecutive routine calls form one run of rows. Messages, permissions,
// image previews and denied calls keep their place. The first key stays stable as a run grows.
export function groupToolActivity(items: Item[]): ActivityEntry[] {
  const out: ActivityEntry[] = []
  for (const item of items) {
    const tool = item.tool
    const routine = item.kind === 'tool' && tool && tool.status !== 'denied' && !tool.images && !/^\[image\]$/m.test(tool.output || '')
    const last = out[out.length - 1]
    if (routine) {
      if (last?.type === 'tools') last.items.push(item)
      else out.push({ type: 'tools', key: `activity:${item.key}`, items: [item] })
    } else out.push({ type: 'item', key: item.key, item })
  }
  return out
}

const SHELL_SKIP = new Set(['sudo', 'set', 'env', 'export', 'cd', 'echo', 'time', 'nohup', 'then', 'do', 'done', 'fi', 'else', 'if', 'for', 'while', 'true', 'false', '!', '{', '}', '(', ')'])

/** A short line for a shell call: the agent's own description of it, else
 *  the programs it runs ("uname · modinfo · which"), never the whole command. */
export function shellLabel(input: unknown): string {
  const o = (input && typeof input === 'object' ? input : {}) as Record<string, unknown>
  const desc = typeof o.description === 'string' ? o.description.trim() : ''
  if (desc) return desc
  const cmd = String(o.command ?? o.cmd ?? '').replace(/\\\n/g, ' ')
  const progs: string[] = []
  for (const seg of cmd.split(/;|&&|\|\||\||\n|\$\(|`/)) {
    const words = seg.trim().replace(/^\(+/, '').split(/\s+/).filter(Boolean)
    for (const w of words) {
      if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(w)) continue // VAR=value before the program
      if (SHELL_SKIP.has(w) || w.startsWith('-')) { if (w === 'sudo' || w === 'env' || w === 'time' || w === 'nohup' || w === 'then' || w === 'do' || w === 'else' || w === '!') continue; break }
      const name = w.split('/').pop()!.replace(/[^A-Za-z0-9._+-]/g, '')
      if (name && !progs.includes(name)) progs.push(name)
      break
    }
    if (progs.length >= 4) break
  }
  return progs.length ? progs.slice(0, 3).join(' · ') + (progs.length > 3 ? ' …' : '') : 'Command'
}
