import { useId, useState } from 'react'
import type { Item } from '../lib/types'
import { toolLabel } from '../lib/tool-activity'
import { Icon } from './icons'
import { ToolRow } from './ToolRow'

export function ToolActivity({ items }: { items: Item[] }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  if (items.length === 1) return <ToolRow tool={items[0].tool!} />
  const labels = [...new Set(items.map(item => toolLabel(item.tool!)))]
  const active = items.filter(item => item.tool?.status === 'running').length
  const title = labels.length === 1 ? labels[0] : 'Tool activity'
  return <section className={`tool-activity ${open ? 'open' : ''} ${active ? 'running' : ''}`}>
    <button className="tool-activity-head" type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(value => !value)}>
      <Icon name="layers" size={14} />
      <span className="tool-activity-title ellipsis" title={labels.join(' · ')}>{title}</span>
      <span className="tool-activity-count tnum">{items.length} calls</span>
      {active > 0 && <span className="tool-activity-active">{active} active</span>}
      <Icon name="chevron-down" size={14} className="tool-caret" />
    </button>
    {open && <div className="tool-activity-body" id={id} role="region" aria-label="Tool call details" tabIndex={0}>
      {items.map(item => <div key={item.key} id={`item-${item.key}`}><ToolRow tool={item.tool!} /></div>)}
    </div>}
  </section>
}
