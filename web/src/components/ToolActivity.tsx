import type { Item } from '../lib/types'
import { ToolRow } from './ToolRow'

// A run of consecutive tool calls: one quiet line each, like Paseo.
export function ToolActivity({ items }: { items: Item[] }) {
  return <div className="tool-run">
    {items.map(item => <div key={item.key} id={`item-${item.key}`}><ToolRow tool={item.tool!} /></div>)}
  </div>
}
