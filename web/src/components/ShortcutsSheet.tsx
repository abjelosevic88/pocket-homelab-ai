import { Link } from 'react-router-dom'
import { isMac, keyCaps, SHORTCUTS, useShortcuts } from '../lib/shortcuts'
import { Modal } from './ui'

const M = isMac ? '⌘' : 'Ctrl'
/** Fixed keys; the ones you can change come from lib/shortcuts.ts. */
const FIXED: Record<string, [string[], string][]> = {
  Anywhere: [[['c'], 'New thread'], [['?'], 'This sheet']],
  'In a thread': [[['↵'], 'Send (desktop; set in Settings)'], [['⇧', '↵'], 'New line'], [[M, '↵'], 'Always sends'], [['/'], 'Recipes and skills at the start of a message']],
  Images: [[[M, 'V'], 'Paste a screenshot'], [['drop'], 'Drag images onto the page']],
  Dialogs: [[['esc'], 'Close']],
}

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  const keys = useShortcuts()
  const groups = ['Anywhere', 'Tabs', 'In a thread', 'Images', 'Dialogs'].map(title => ({
    title,
    keys: [...SHORTCUTS.filter(s => s.group === title && keys[s.id]).map(s => [keyCaps(keys[s.id]), s.label] as [string[], string]), ...(FIXED[title] || [])],
  })).filter(g => g.keys.length)
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg" footer={<Link to="/settings/general#shortcuts" className="btn" onClick={onClose}>Change shortcuts</Link>}>
      <div className="shortcuts">
        {groups.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}
