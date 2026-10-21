import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Anywhere', keys: [[['⌘', 'K'], 'Search & commands'], [['⌘', 'N'], 'New thread'], [['c'], 'New thread'], [['?'], 'This sheet']] },
  { title: 'In a thread', keys: [[['↵'], 'Send (desktop; set in Settings)'], [['⇧', '↵'], 'New line'], [['⌘', '↵'], 'Always sends'], [['⌘', '.'], 'Stop the agent'], [['/'], 'Recipes at the start of a message']] },
  { title: 'Images', keys: [[['⌘', 'V'], 'Paste a screenshot'], [['drop'], 'Drag images onto the page']] },
  { title: 'Dialogs', keys: [[['esc'], 'Close']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
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
