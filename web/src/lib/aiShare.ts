import type { ShareAllowance } from './types'

const pct = (n: number) => (n < 10 ? n.toFixed(1).replace(/\.0$/, '') : String(Math.round(n)))

export function minutesLabel(m: number) {
  if (m % 60 === 0) return m === 60 ? '1 hour' : `${m / 60} hours`
  return m > 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`
}

/** One short line about a lent login: what is left, or why it cannot be used now. */
export function shareNote(s: ShareAllowance, short = false): string {
  if (s.paused) return 'Paused'
  const parts: string[] = []
  if (s.minutes_day > 0) {
    const left = Math.max(0, s.minutes_day - s.used_minutes)
    parts.push(left <= 0 ? 'used up today' : `${minutesLabel(Math.max(1, Math.floor(left)))} left today`)
  }
  if (s.percent_week > 0) {
    parts.push(!s.percent_known ? `${pct(s.percent_week)}% of the week` : `${pct(Math.min(s.used_percent, s.percent_week))} of ${pct(s.percent_week)}% this week`)
  }
  if (!parts.length) parts.push(short ? 'no limit' : `no limit · ${Math.round(s.used_minutes)} min today`)
  if (s.blocked && !s.paused && !parts.some(p => p.includes('used up'))) parts.unshift('Can’t be used now')
  return parts.join(' · ')
}
