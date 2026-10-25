// Keyboard shortcuts you can change (Settings → General). Keys are written
// "mod+shift+t": mod is ⌘ on a Mac and Ctrl elsewhere. Only the ones you
// change are saved (settings.shortcuts); "" turns one off.
import { useApp } from './app-context'

export type ShortcutId = 'palette' | 'new_thread' | 'new_tab' | 'reopen_tab' | 'close_tab' | 'prev_tab' | 'next_tab' | 'stop'

export const SHORTCUTS: { id: ShortcutId; label: string; group: 'Anywhere' | 'Tabs' | 'In a thread'; keys: string }[] = [
  { id: 'palette', label: 'Search & commands', group: 'Anywhere', keys: 'mod+k' },
  { id: 'new_thread', label: 'New thread', group: 'Anywhere', keys: 'mod+n' },
  { id: 'new_tab', label: 'New tab', group: 'Tabs', keys: 'mod+t' },
  { id: 'reopen_tab', label: 'Reopen closed tab', group: 'Tabs', keys: 'mod+shift+t' },
  { id: 'close_tab', label: 'Close tab', group: 'Tabs', keys: '' },
  { id: 'prev_tab', label: 'Previous tab', group: 'Tabs', keys: 'mod+shift+[' },
  { id: 'next_tab', label: 'Next tab', group: 'Tabs', keys: 'mod+shift+]' },
  { id: 'stop', label: 'Stop the agent', group: 'In a thread', keys: 'mod+.' },
]

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)

/** The key itself, the same on every keyboard layout for letters and digits
 * (Option+T on a Mac types "†", Shift+[ types "{"). */
function keyName(e: KeyboardEvent): string {
  const letter = /^Key([A-Z])$/.exec(e.code)
  if (letter) return letter[1].toLowerCase()
  const digit = /^Digit(\d)$/.exec(e.code)
  if (digit) return digit[1]
  const code: Record<string, string> = { BracketLeft: '[', BracketRight: ']', Period: '.', Comma: ',', Slash: '/', Semicolon: ';', Quote: "'", Backquote: '`', Minus: '-', Equal: '=', Backslash: '\\' }
  if (code[e.code]) return code[e.code]
  return e.key.toLowerCase()
}

/** "mod+shift+t" for a key press, or '' for a modifier on its own. */
export function comboOf(e: KeyboardEvent): string {
  if (['Meta', 'Control', 'Shift', 'Alt'].includes(e.key)) return ''
  const parts: string[] = []
  if (e.metaKey || e.ctrlKey) parts.push('mod')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')
  parts.push(keyName(e))
  return parts.join('+')
}

const symbols: Record<string, string> = { mod: isMac ? '⌘' : 'Ctrl', alt: isMac ? '⌥' : 'Alt', shift: '⇧', enter: '↵', escape: 'esc', arrowup: '↑', arrowdown: '↓', arrowleft: '←', arrowright: '→', backspace: '⌫', ' ': 'Space' }
/** "mod+shift+t" → ["⌘", "⇧", "T"] */
export const keyCaps = (keys: string) => keys ? keys.split('+').map(k => symbols[k] || k.toUpperCase()) : []

/** The keys in use for each action: yours, or the default. */
export function useShortcuts(): Record<ShortcutId, string> {
  const { settings } = useApp()
  const mine = settings?.shortcuts || {}
  return Object.fromEntries(SHORTCUTS.map(s => [s.id, s.id in mine ? mine[s.id] : s.keys])) as Record<ShortcutId, string>
}

/** The action a key press is bound to, if any. */
export function actionFor(e: KeyboardEvent, keys: Record<ShortcutId, string>): ShortcutId | undefined {
  const c = comboOf(e)
  if (!c) return undefined
  return (Object.keys(keys) as ShortcutId[]).find(id => keys[id] === c)
}
