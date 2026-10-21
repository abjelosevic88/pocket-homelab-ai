import type { ProviderId, Thread } from './types'

/** "3 min ago", "yesterday", "Oct 12". */
export function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const t = Date.parse(iso)
  if (isNaN(t)) return iso
  const s = Math.round((Date.now() - t) / 1000)
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  if (s < 2 * 86400) return 'yesterday'
  if (s < 14 * 86400) return `${Math.round(s / 86400)} days ago`
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: new Date(t).getFullYear() === new Date().getFullYear() ? undefined : 'numeric' })
}

/** Short list stamp: 14:05 today, Mon, Oct 12. */
export function stamp(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  const now = new Date()
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  if (now.getTime() - d.getTime() < 6 * 86400e3) return d.toLocaleDateString(undefined, { weekday: 'short' })
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

/** Future time: "in 3 h", "tomorrow 08:00", "Mon 08:00". */
export function when(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const now = new Date()
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  const diff = d.getTime() - now.getTime()
  if (diff < 0) return ago(iso)
  if (diff < 3600e3) return `in ${Math.max(1, Math.round(diff / 60e3))} min`
  if (d.toDateString() === now.toDateString()) return `today ${time}`
  const tm = new Date(now); tm.setDate(tm.getDate() + 1)
  if (d.toDateString() === tm.toDateString()) return `tomorrow ${time}`
  if (diff < 6 * 86400e3) return `${d.toLocaleDateString(undefined, { weekday: 'short' })} ${time}`
  return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}`
}

export function fmtDuration(ms: number): string {
  if (!ms || ms < 0) return '0s'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function fmtCost(usd: number | undefined): string {
  if (!usd) return '$0'
  if (usd < 0.01) return '<$0.01'
  if (usd < 10) return `$${usd.toFixed(2)}`
  return `$${usd.toFixed(0)}`
}

export function fmtTokens(n: number | undefined): string {
  if (!n) return '0'
  if (n < 1000) return String(n)
  if (n < 1e6) return `${(n / 1000).toFixed(n < 10000 ? 1 : 0)}k`
  return `${(n / 1e6).toFixed(1)}M`
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

export const PROVIDER_NAME: Record<ProviderId, string> = { claude: 'Claude Code', codex: 'Codex' }
export const providerName = (p: string) => PROVIDER_NAME[p as ProviderId] || p || 'Agent'

/** /home/me/Projects/x → ~/Projects/x (the home is guessed from the path). */
export function shortPath(p: string | undefined): string {
  if (!p) return ''
  const m = p.match(/^\/(home\/[^/]+|Users\/[^/]+|root)(\/.*)?$/)
  return m ? '~' + (m[2] || '') : p
}

export function baseName(p: string | undefined): string {
  if (!p) return ''
  const s = p.replace(/\/+$/, '').split('/').pop() || p
  return s === '' ? '/' : s
}

export function threadTitle(t: Pick<Thread, 'title' | 'preview'>): string {
  return t.title?.trim() || t.preview?.trim().slice(0, 80) || 'New thread'
}


/** {{name}} placeholders in a prompt, in order, once each. */
export function promptVars(prompt: string): string[] {
  const out: string[] = []
  for (const m of prompt.matchAll(/\{\{\s*([\w .-]+?)\s*\}\}/g)) if (!out.includes(m[1])) out.push(m[1])
  return out
}

export const isTouch = () => window.matchMedia('(hover: none) and (pointer: coarse)').matches
