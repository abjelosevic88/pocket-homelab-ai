// What the person on screen may do with a thread: it is theirs, or someone
// shared it with them to chat in or only to read.
import type { Thread, User } from './types'

export type Access = 'owner' | 'chat' | 'view'

export function accessOf(t: Thread, user: User | null): Access {
  if (!user || !t.owner_id || t.owner_id === user.id) return 'owner'
  const role = t.shares?.find(s => s.user_id === user.id)?.role
  // Ask chats run on each person's own apps, so chatting needs no run permission
  return role === 'chat' && (user.role === 'admin' || user.can_run || t.source === 'ask') ? 'chat' : 'view'
}

/** "Alice", "Alice and Bob", "Alice and 2 others". */
export function sharedNames(t: Thread): string {
  const n = (t.shares || []).map(s => s.name)
  if (n.length <= 2) return n.join(' and ')
  return `${n[0]} and ${n.length - 1} others`
}
