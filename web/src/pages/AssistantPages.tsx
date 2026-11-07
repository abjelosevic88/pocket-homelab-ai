import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { PageHead } from '../components/PageHead'
import { AppCall, CallCard, DataView, friendly, safeURL } from '../components/AppEvidence'
import { errMsg, useToast, noGrammarly } from '../components/ui'
import type { Thread } from '../lib/types'

function Frame({ title, sub, onMenu, children }: { title: string; sub: string; onMenu?: () => void; children: ReactNode }) { return <div className="page"><PageHead title={title} sub={sub} onMenu={onMenu} /><div className="page-scroll"><div className="page-inner assistant-page">{children}</div></div></div> }
interface Section { tool: string; data?: unknown; error?: string; checked_at: string; duration_ms: number }
interface Overview { date?: string; timezone?: string; coverage: string; unavailable?: string[]; sections: Section[] }
const sectionTitles: Record<string,string> = { pocket_tasks_summary:'Tasks', pocket_money_bills:'Bills & subscriptions', pocket_calendar_events:'Calendar', pocket_review_summary:'Reviews', pocket_garage_reminders:'Car maintenance', pocket_home_due:'Home upkeep', pocket_renewals_due:'What runs out', pocket_parcels_summary:'Deliveries', pocket_places_summary:'Places', pocket_wallet_summary:'Wallet', pocket_weather_summary:'Weather', pocket_fitness_summary:'Fitness', pocket_nutrition_summary:'Nutrition', pocket_blog_summary:'Blog', pocket_chat_summary:'Chat', pocket_forms_summary:'Forms', pocket_crm_today:'Deals', pocket_voice_summary:'Voice', pocket_sign_summary:'Waiting for your signature' }
function displayWhen(value: unknown) { const s=String(value); if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return new Date(s+'T12:00:00').toLocaleDateString(); if (s.includes('T') && !Number.isNaN(Date.parse(s))) return new Date(s).toLocaleString(); return s }
function formatMinor(amount: number, currency: string) { try { const f = new Intl.NumberFormat(undefined, { style:'currency', currency }); const digits = f.resolvedOptions().maximumFractionDigits ?? 2; return f.format(amount / 10 ** digits) } catch { return `${amount} minor units (${currency})` } }
// Weather's temperatures come in °C; shown in the unit set in Weather
function weatherTemp(c: unknown, unit: unknown) { if (typeof c !== 'number') return ''; return `${Math.round(unit === 'f' ? c * 9 / 5 + 32 : c)}`}
function weatherRange(day: unknown, unit: unknown) { const d = (day && typeof day === 'object' ? day : {}) as Record<string, unknown>; const lo = weatherTemp(d.t_min, unit), hi = weatherTemp(d.t_max, unit); return lo && hi ? `${lo}–${hi} ${unit === 'f' ? '°F' : '°C'}` : '' }
// Fitness: seconds as "45 min" / "1 h 12 min", a date as today, yesterday, a weekday this week or "3 Nov"
function fitnessDuration(sec: unknown) { if (typeof sec !== 'number' || sec <= 0) return ''; const m = Math.round(sec / 60); return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h${m % 60 ? ` ${m % 60} min` : ''}` }
function fitnessDaysAgo(d: unknown, today: unknown) { const ymd = (v: unknown) => { const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(v || '')); return m ? Date.UTC(+m[1], +m[2] - 1, +m[3]) : NaN }; const t = Number.isNaN(ymd(today)) ? ymd(new Date().toLocaleDateString('en-CA')) : ymd(today); return Math.round((t - ymd(d)) / 86400000) }
function fitnessDay(d: unknown, today: unknown) { const n = fitnessDaysAgo(d, today); if (Number.isNaN(n)) return String(d || ''); if (n === 0) return 'today'; if (n === 1) return 'yesterday'; const at = new Date(String(d) + 'T12:00:00'); return n > 1 && n < 7 ? `on ${at.toLocaleDateString(undefined, { weekday: 'long' })}` : `on ${at.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })}` }
// Nutrition: energy as "1,480 kcal", water as "800 ml" / "1.25 l"
function nutritionKcal(v: number) { return `${Math.round(v).toLocaleString()} kcal` }
function nutritionWater(v: number) { return v >= 1000 ? `${+(v / 1000).toFixed(2)} l` : `${Math.round(v)} ml` }
// Blog: a post's date and time as "Fri 7 Nov, 09:00"
function blogWhen(v: unknown) { const s = String(v || ''); if (!s || Number.isNaN(Date.parse(s))) return ''; return new Date(s).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) }
function briefingContent(section: Section) {
 const envelope = (section.data && typeof section.data === 'object' ? section.data : {}) as Record<string, unknown>
 const result = envelope.result ?? section.data
 const items: Record<string, unknown>[] = []
 const visit = (v: unknown, depth = 0) => {
  if (!v || typeof v !== 'object' || depth > 4 || items.length >= 20) return
  if (Array.isArray(v)) { for (const x of v) visit(x, depth + 1); return }
  const o = v as Record<string, unknown>
  if (o.review && typeof o.review === 'object') { visit(o.review, depth + 1); return }
  if (typeof (o.title ?? o.name ?? o.summary) === 'string' && !('omitted' in o)) { items.push(o); return }
  for (const [k, x] of Object.entries(o)) if (!['participants', 'accounts', 'categories', 'members'].includes(k)) visit(x, depth + 1)
 }
 // Review activity is history, not work waiting for the user. Keep it in
 // source details while the briefing lists actionable reviews and tasks.
 if ((section.tool === 'pocket_review_summary' || section.tool === 'pocket_collaborator_summary') && result && typeof result === 'object' && !Array.isArray(result)) {
  const review = result as Record<string, unknown>
  visit(review.waiting); visit(review.tasks)
 } else if (section.tool === 'pocket_parcels_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // what needs the person today; parcels still on their way stay in the source
  const parcels = ((result as Record<string, unknown>).parcels || {}) as Record<string, unknown>
  visit(parcels.today); visit(parcels.pickup); visit(parcels.problems); visit(parcels.returns_due)
 } else if (section.tool === 'pocket_sign_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // what others sent the person to sign; their own documents waiting for others are a count
  visit((result as Record<string, unknown>).to_sign)
 } else if (section.tool === 'pocket_places_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the next planned visits; visits waiting for a rating are a count
  const planned = (result as Record<string, unknown>).planned
  if (Array.isArray(planned)) for (const v of planned.slice(0, 5)) if (v && typeof v === 'object') { const o = v as Record<string, unknown>; items.push({ ...o, title: `${o.place_name}${o.time ? `, ${o.time}` : ''}${o.with_whom ? ` (with ${o.with_whom})` : ''}` }) }
 } else if (section.tool === 'pocket_wallet_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // today's tickets and flights, the next pass, then what runs out within a week, each opening in Wallet
  const r = result as Record<string, unknown>
  const at = (o: Record<string, unknown>) => typeof o.starts_at === 'string' && o.starts_at.length > 10 ? o.starts_at.slice(11, 16) : ''
  const extra = (o: Record<string, unknown>) => [o.seat && `seat ${o.seat}`, o.gate && `gate ${o.gate}`].filter(Boolean).join(', ')
  if (Array.isArray(r.today)) for (const v of r.today) if (v && typeof v === 'object') { const o = v as Record<string, unknown>; items.push({ ...o, title: `Today ${at(o)} ${o.name}${o.ref && o.kind === 'boarding' ? ` (${o.ref})` : ''}${extra(o) ? `, ${extra(o)}` : ''}`.replace(/\s+/g, ' ') }) }
  if (r.next && typeof r.next === 'object') { const o = r.next as Record<string, unknown>; items.push({ ...o, title: `Next: ${o.name}, ${String(o.starts_at || '').slice(0, 10)}${at(o) ? ` ${at(o)}` : ''}` }) }
  if (Array.isArray(r.expiring)) for (const v of r.expiring) if (v && typeof v === 'object') { const o = v as Record<string, unknown>; if (typeof o.days_left === 'number' && o.days_left <= 7) items.push({ ...o, title: `${o.name}${o.holder ? ` (${o.holder})` : ''} ${o.days_left === 0 ? 'runs out today' : o.days_left === 1 ? 'runs out tomorrow' : `runs out in ${o.days_left} days`}` }) }
 } else if (section.tool === 'pocket_weather_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the weather at home: now, today, tomorrow, then the warnings for those two days (nothing without a place)
  const r = result as Record<string, unknown>, unit = (r.units as Record<string, unknown> | undefined)?.temp, deg = unit === 'f' ? '°F' : '°C'
  const obj = (v: unknown) => (v && typeof v === 'object' && !Array.isArray(v) ? v : {}) as Record<string, unknown>
  const place = obj(r.place), now = obj(r.now), today = obj(r.today), tomorrow = obj(r.tomorrow)
  const cond = (o: Record<string, unknown>) => String(o.text || (o.condition ? friendly(String(o.condition)) : ''))
  if (r.place) {
   const t = weatherTemp(now.temp, unit)
   if (t) items.push({ id: 'now', title: `Now ${t} ${deg}${cond(now) ? `, ${cond(now).toLowerCase()}` : ''}`, detail: [place.name, r.stale ? 'may be out of date' : ''].filter(Boolean).join(' · ') })
   if (weatherRange(today, unit) || r.text) items.push({ id: 'today', title: `Today ${weatherRange(today, unit)}`.trim(), detail: r.text })
   if (weatherRange(tomorrow, unit)) items.push({ id: 'tomorrow', title: `Tomorrow ${weatherRange(tomorrow, unit)}${cond(tomorrow) ? `, ${cond(tomorrow).toLowerCase()}` : ''}` })
   const days = [today.date, tomorrow.date].filter(Boolean)
   if (Array.isArray(r.warnings)) for (const v of r.warnings.slice(0, 6)) if (v && typeof v === 'object') { const w = v as Record<string, unknown>; if (w.day && days.length && !days.includes(w.day)) continue; items.push({ id: w.id, title: String(w.title || friendly(String(w.kind || 'warning'))), detail: `${w.level === 'warning' ? 'Warning' : 'Heads-up'}${w.day === tomorrow.date ? ' for tomorrow' : w.day === today.date ? ' for today' : ''}`, level: w.level === 'warning' ? 'warning' : 'advisory' }) }
  }
 } else if (section.tool === 'pocket_fitness_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the workout going on now, today's plan, the week against the goal, the last workout and new records, each opening in Fitness
  const r = result as Record<string, unknown>
  const obj = (v: unknown) => (v && typeof v === 'object' && !Array.isArray(v) ? v : {}) as Record<string, unknown>
  const base = safeURL(envelope.source_url) ? String(envelope.source_url).replace(/\/+$/, '') : ''
  const link = (path: string) => base ? base + path : undefined
  const count = (n: unknown, one: string) => typeof n === 'number' && n > 0 ? `${n} ${n === 1 ? one : one + 's'}` : ''
  const live = obj(r.in_progress), today = obj(r.today), last = obj(r.last_workout)
  if (live.id) { const at = typeof live.started_at === 'string' && !Number.isNaN(Date.parse(live.started_at)) ? new Date(live.started_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''; items.push({ id: `live-${live.id}`, title: `Going on now: ${live.name || 'a workout'}`, detail: at ? `Started at ${at}` : '', item_url: link('/') }) }
  if (today.routine) items.push({ id: 'today', title: today.done ? `Today’s ${today.routine} is done` : `Today: ${today.routine}${today.time ? ` at ${today.time}` : ''}`, detail: [count(today.exercises, 'exercise'), typeof today.minutes === 'number' && today.minutes > 0 ? `about ${today.minutes} min` : ''].filter(Boolean).join(' · '), item_url: link('/') })
  const done = typeof r.week_done === 'number' ? r.week_done : 0, goal = typeof r.week_goal === 'number' ? r.week_goal : 0, streak = typeof r.streak_weeks === 'number' ? r.streak_weeks : 0
  if (goal > 0 || done > 0) items.push({ id: 'week', title: goal > 0 ? `${done} of ${goal} ${goal === 1 ? 'workout' : 'workouts'} this week` : `${count(done, 'workout')} this week`, detail: streak > 1 ? `${streak} weeks in a row` : '', item_url: link('/progress') })
  if (last.name) items.push({ id: `last-${last.id}`, title: `Last workout: ${last.name}, ${fitnessDay(last.date, r.date)}`, detail: [fitnessDuration(last.duration_s), count(last.set_count, 'set')].filter(Boolean).join(' · '), item_url: last.id ? link(`/workouts/${last.id}`) : link('/') })
  if (Array.isArray(r.records)) for (const v of r.records) if (v && typeof v === 'object') { const o = v as Record<string, unknown>, ago = fitnessDaysAgo(o.date, r.date); if (Number.isNaN(ago) || ago < 0 || ago > 6) continue; items.push({ id: `record-${o.workout_id}-${o.exercise}`, title: String(o.text || `${o.exercise}: a new record`), detail: `New record ${fitnessDay(o.date, r.date)}`, item_url: o.workout_id ? link(`/workouts/${o.workout_id}`) : link('/') }) }
 } else if (section.tool === 'pocket_nutrition_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // today's calories against the budget and what's left, protein and water against their goals, the week and the days in a row, each opening in Nutrition
  const r = result as Record<string, unknown>
  const obj = (v: unknown) => (v && typeof v === 'object' && !Array.isArray(v) ? v : {}) as Record<string, unknown>
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0
  const base = safeURL(envelope.source_url) ? String(envelope.source_url).replace(/\/+$/, '') : ''
  const link = (path: string) => base ? base + path : undefined
  const days = (n: number) => n === 1 ? '1 day' : `${n} days`
  const totals = obj(r.totals), goals = obj(r.goals), left = obj(r.remaining), week = obj(r.week)
  const entries = num(r.entries), budget = num(goals.budget), water = num(r.water_ml), streak = num(r.streak)
  const day = link(typeof r.date === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(r.date) ? `/?date=${r.date}` : '/')
  if (entries > 0) {
   const rest = num(left.kcal), burned = num(goals.burned)
   items.push({ id: 'today', title: budget > 0 ? `${nutritionKcal(num(totals.kcal))} of ${nutritionKcal(budget)} today` : `${nutritionKcal(num(totals.kcal))} today`, detail: budget > 0 ? [rest >= 0 ? `${nutritionKcal(rest)} left` : `${nutritionKcal(-rest)} over`, burned > 0 ? `budget includes ${nutritionKcal(burned)} burned` : ''].filter(Boolean).join(' · ') : entries === 1 ? '1 thing logged' : `${entries} things logged`, item_url: day })
   const protein = num(totals.protein), pg = num(goals.protein), pl = num(left.protein)
   items.push({ id: 'protein', title: pg > 0 ? `Protein ${Math.round(protein)} of ${pg} g` : `Protein ${Math.round(protein)} g`, detail: pg > 0 ? (pl > 0 ? `${pl} g to go` : 'Goal reached') : '', item_url: day })
  } else items.push({ id: 'today', title: 'Nothing logged yet today', detail: budget > 0 ? `Budget ${nutritionKcal(budget)}` : '', item_url: day })
  const wg = num(goals.water), wl = num(left.water)
  if (water > 0) items.push({ id: 'water', title: wg > 0 ? `Water ${nutritionWater(water)} of ${nutritionWater(wg)}` : `Water ${nutritionWater(water)}`, detail: wg > 0 ? (wl > 0 ? `${nutritionWater(wl)} to go` : 'Goal reached') : '', item_url: day })
  else if (entries > 0 && wg > 0) items.push({ id: 'water', title: 'No water logged yet', detail: `Goal ${nutritionWater(wg)}`, item_url: day })
  const logged = num(week.logged), avg = num(week.avg_kcal)
  if (logged > 0) items.push({ id: 'week', title: `${days(logged)} logged this week`, detail: [num(week.goal) > 0 ? `${num(week.on_goal)} on goal` : '', avg > 0 ? `about ${nutritionKcal(avg)} a day` : '', streak > 1 ? `${streak} days in a row` : ''].filter(Boolean).join(' · '), item_url: link('/trends') })
  else if (streak > 1) items.push({ id: 'streak', title: `${streak} days in a row logged`, item_url: link('/trends') })
 } else if (section.tool === 'pocket_blog_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // drafts to finish (the newest one named), the next scheduled post, comments waiting and readers in the last 7 days, each opening in Blog
  const r = result as Record<string, unknown>
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0
  const list = (v: unknown) => (Array.isArray(v) ? v.filter(x => x && typeof x === 'object') : []) as Record<string, unknown>[]
  const baseOf = (v: unknown) => safeURL(v) ? String(v).replace(/\/+$/, '') : ''
  const base = baseOf(r.url) || baseOf(envelope.app_url) || baseOf(envelope.source_url)
  const link = (path: string) => base ? base + path : undefined
  const quoted = (o: Record<string, unknown>) => `“${String(o.title || 'Untitled')}”`
  const drafts = list(r.drafts), nd = num(r.drafts_total) || drafts.length
  if (nd > 0) items.push({ id: 'drafts', title: nd === 1 ? '1 draft to finish' : `${nd} drafts to finish`, detail: drafts[0] ? `Newest: ${quoted(drafts[0])}` : '', item_url: drafts[0]?.id != null ? link(`/posts/${drafts[0].id}`) : link('/posts') })
  const next = list(r.scheduled).filter(o => !Number.isNaN(Date.parse(String(o.publish_at || '')))).sort((a, b) => Date.parse(String(a.publish_at)) - Date.parse(String(b.publish_at)))[0]
  if (next) items.push({ id: `scheduled-${next.id}`, title: `Next post: ${quoted(next)}`, detail: `Goes out ${blogWhen(next.publish_at)}`, item_url: link(`/posts/${next.id}`) })
  const pending = num(r.pending_comments)
  if (pending > 0) items.push({ id: 'comments', title: pending === 1 ? '1 comment waiting' : `${pending} comments waiting`, detail: 'For you to approve or answer', item_url: link('/comments') })
  if (typeof r.views_7d === 'number') { const v = num(r.views_7d); items.push({ id: 'readers', title: v > 0 ? `${v.toLocaleString()} ${v === 1 ? 'reader' : 'readers'} in the last 7 days` : 'No readers in the last 7 days', detail: num(r.published_total) > 0 ? `${num(r.published_total)} ${num(r.published_total) === 1 ? 'post' : 'posts'} published` : '', item_url: link('/readers') }) }
 } else if (section.tool === 'pocket_chat_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the conversations with unread messages (the top 3 with their counts and the last message) and who mentioned you (the top 2), each opening in Chat
  const r = result as Record<string, unknown>
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0
  const list = (v: unknown) => (Array.isArray(v) ? v.filter(x => x && typeof x === 'object') : []) as Record<string, unknown>[]
  const baseOf = (v: unknown) => safeURL(v) ? String(v).replace(/\/+$/, '') : ''
  const base = baseOf(r.url) || baseOf(envelope.app_url) || baseOf(envelope.source_url)
  const link = (path: string) => base ? base + path : undefined
  const open = (o: Record<string, unknown>, id: unknown) => safeURL(o.item_url) ? String(o.item_url) : id != null && id !== '' ? link(`/c/${id}`) : link('/')
  const short = (v: unknown) => { const s = String(v || '').replace(/\s+/g, ' ').trim(); return s.length > 90 ? s.slice(0, 89).trimEnd() + '…' : s }
  const waiting = list(r.waiting).filter(o => num(o.unread) > 0)
  for (const o of waiting.slice(0, 3)) { const n = num(o.unread); items.push({ id: `c-${o.id}`, title: `${String(o.name || 'A conversation')}: ${n === 1 ? '1 unread' : `${n} unread`}`, detail: [o.mentioned ? 'mentions you' : '', short(o.last)].filter(Boolean).join(' · '), item_url: open(o, o.id) }) }
  const more = Math.max(num((r.unread as Record<string, unknown> | undefined)?.conversations), waiting.length) - Math.min(waiting.length, 3)
  if (more > 0) items.push({ id: 'more', title: more === 1 ? '1 more conversation with unread messages' : `${more} more conversations with unread messages`, item_url: link('/') })
  for (const o of list(r.mentions).slice(0, 2)) items.push({ id: `m-${o.id}`, title: `${String(o.from || 'Someone')} mentioned you${o.conversation ? ` in ${String(o.conversation)}` : ''}`, detail: short(o.text), item_url: open(o, o.conversation_id) })
 } else if (section.tool === 'pocket_forms_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the new answers waiting (the latest 3: who, which form, a one-line preview), how many more, and the forms taking answers, each opening in Forms
  const r = result as Record<string, unknown>
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0
  const list = (v: unknown) => (Array.isArray(v) ? v.filter(x => x && typeof x === 'object') : []) as Record<string, unknown>[]
  const baseOf = (v: unknown) => safeURL(v) ? String(v).replace(/\/+$/, '') : ''
  const base = baseOf(r.url) || baseOf(envelope.app_url) || baseOf(envelope.source_url)
  const link = (path: string) => base ? base + path : undefined
  const short = (v: unknown) => { const s = String(v || '').replace(/\s+/g, ' ').trim(); return s.length > 90 ? s.slice(0, 89).trimEnd() + '…' : s }
  const form = (emoji: unknown, title: unknown) => [String(emoji || '').trim(), `“${String(title || 'Untitled form')}”`].filter(Boolean).join(' ')
  const fresh = list(r.latest).filter(o => o.status === 'new')
  for (const o of fresh.slice(0, 3)) { const who = String(o.name || o.email || (o.number ? `Answer #${o.number}` : 'Someone')); items.push({ id: `a-${o.id}`, title: `${who} answered ${form(o.form_emoji, o.form_title)}`, detail: short(o.preview), created_at: o.created_at, item_url: o.id != null && o.id !== '' ? link(`/answers/${o.id}`) : link('/?status=new') }) }
  const more = num(r.new) - Math.min(fresh.length, 3)
  if (more > 0) items.push({ id: 'more', title: more === 1 ? '1 more new answer' : `${more} more new answers`, detail: typeof r.oldest_new_at === 'string' && !Number.isNaN(Date.parse(r.oldest_new_at)) ? `Waiting since ${new Date(r.oldest_new_at).toLocaleDateString()}` : '', item_url: link('/?status=new') })
  for (const o of list(r.list).filter(o => o.taking).slice(0, 3)) { const n = num(o.responses), nw = num(o.new), closes = typeof o.close_at === 'string' && !Number.isNaN(Date.parse(o.close_at)) ? `closes ${new Date(o.close_at).toLocaleDateString()}` : ''; items.push({ id: `f-${o.id}`, title: `Taking answers: ${form(o.emoji, o.title)}`, detail: [n === 1 ? '1 answer' : `${n} answers`, nw > 0 ? `${nw} new` : '', closes].filter(Boolean).join(' · '), item_url: safeURL(o.link) ? String(o.link) : o.id != null ? link(`/forms/${o.id}`) : link('/forms') }) }
 } else if (section.tool === 'pocket_crm_today' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the next steps on deals that are overdue and due today (the top 3 of each: deal, step, who), how many more, and a line with the open deals, what they are worth and the deals won this month, each opening in CRM
  const r = result as Record<string, unknown>
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0
  const list = (v: unknown) => (Array.isArray(v) ? v.filter(x => x && typeof x === 'object') : []) as Record<string, unknown>[]
  const lists = (r.lists && typeof r.lists === 'object' ? r.lists : {}) as Record<string, unknown>
  const sum = (r.summary && typeof r.summary === 'object' ? r.summary : {}) as Record<string, unknown>
  const baseOf = (v: unknown) => safeURL(v) ? String(v).replace(/\/+$/, '') : ''
  const base = baseOf(sum.url) || baseOf(envelope.app_url) || baseOf(envelope.source_url)
  const link = (path: string) => base ? base + path : undefined
  const short = (v: unknown) => { const s = String(v || '').replace(/\s+/g, ' ').trim(); return s.length > 90 ? s.slice(0, 89).trimEnd() + '…' : s }
  const day = (v: unknown) => typeof v === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(v) ? new Date(v + 'T12:00:00').toLocaleDateString() : ''
  // CRM keeps money in cents, whatever the currency: {EUR: 4820000} → 48,200 EUR
  const worth = (v: unknown) => v && typeof v === 'object' ? Object.entries(v as Record<string, unknown>).filter(([, c]) => typeof c === 'number' && c !== 0).map(([cur, c]) => `${(Number(c) / 100).toLocaleString(undefined, { maximumFractionDigits: 0 })} ${cur}`).join(' + ') : ''
  const step = (o: Record<string, unknown>, when: string) => [short(o.next_action) || 'No step written', when, String(o.company || o.contact_name || '')].filter(Boolean).join(' · ')
  const open = (o: Record<string, unknown>) => o.id != null && o.id !== '' ? link(`/deals/${o.id}`) : link('/')
  const overdue = list(lists.overdue), due = list(lists.today)
  for (const o of overdue.slice(0, 3)) { const since = day(o.next_due); items.push({ id: `o-${o.id}`, title: String(o.title || 'Untitled deal'), detail: step(o, since ? `overdue since ${since}` : 'overdue'), item_url: open(o) }) }
  for (const o of due.slice(0, 3)) items.push({ id: `t-${o.id}`, title: String(o.title || 'Untitled deal'), detail: step(o, 'today'), item_url: open(o) })
  const more = overdue.length + due.length - Math.min(overdue.length, 3) - Math.min(due.length, 3)
  if (more > 0) items.push({ id: 'more', title: more === 1 ? '1 more step to take' : `${more} more steps to take`, item_url: link('/') })
  const openN = num(sum.open), wonN = num(sum.won_month), value = worth(sum.open_value)
  if (openN > 0 || wonN > 0) items.push({ id: 'pipeline', title: [openN > 0 ? `${openN === 1 ? '1 open deal' : `${openN} open deals`}${value ? ` worth ${value}` : ''}` : 'No open deals', `${wonN} won this month`].join(' · '), item_url: link('/pipeline') })
 } else if (section.tool === 'pocket_voice_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  // the memos waiting in the inbox, each opening in Voice; memos still being written out are a count
  const inbox = (result as Record<string, unknown>).latest_inbox
  if (Array.isArray(inbox)) for (const v of inbox) if (v && typeof v === 'object') { const o = v as Record<string, unknown>; items.push({ ...o, title: o.title || o.summary || 'Untitled memo', item_url: o.url }) }
 } else visit(result)
 let counts = result && !Array.isArray(result) && typeof result === 'object' ? Object.entries(result).filter(([k, v]) => typeof v === 'number' && /^(overdue|due_today|today|total|open|open_reviews|open_tasks|done_today|to_review|to_approve|completed|active|due|waiting)$/.test(k)) : []
 if (section.tool === 'pocket_parcels_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const c = ((result as Record<string, unknown>).counts || {}) as Record<string, unknown>
  counts = (['today', 'pickup', 'problems', 'returns_due', 'on_the_way'] as const).filter(k => typeof c[k] === 'number' && (c[k] as number) > 0).map(k => [k, c[k]] as [string, unknown])
 }
 if (section.tool === 'pocket_places_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const r = result as Record<string, unknown>
  counts = ([['to_rate', r.to_rate], ['planned', Array.isArray(r.planned) ? r.planned.length : 0]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_wallet_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const r = result as Record<string, unknown>
  counts = ([['today', Array.isArray(r.today) ? r.today.length : 0], ['runs_out_soon', Array.isArray(r.expiring) ? r.expiring.length : 0]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_weather_summary' || section.tool === 'pocket_fitness_summary' || section.tool === 'pocket_nutrition_summary' || section.tool === 'pocket_blog_summary') counts = []
 if (section.tool === 'pocket_chat_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const u = ((result as Record<string, unknown>).unread || {}) as Record<string, unknown>
  counts = ([['unread_messages', u.messages], ['mentions', u.mentions]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_forms_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const r = result as Record<string, unknown>
  counts = ([['new_answers', r.new], ['taking_answers', r.open]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_crm_today' && result && typeof result === 'object' && !Array.isArray(result)) {
  const r = ((result as Record<string, unknown>).summary || {}) as Record<string, unknown>
  counts = ([['overdue', r.overdue], ['due_today', r.due_today]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_voice_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const r = result as Record<string, unknown>
  counts = ([['in_your_inbox', r.inbox], ['being_written_out', r.working]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 if (section.tool === 'pocket_sign_summary' && result && typeof result === 'object' && !Array.isArray(result)) {
  const c = ((result as Record<string, unknown>).counts || {}) as Record<string, unknown>
  counts = ([['to_sign', c.to_sign], ['waiting_for_others', c.pending]] as [string, unknown][]).filter(([, n]) => typeof n === 'number' && n > 0)
 }
 const source = safeURL(envelope.source_url) ? envelope.source_url : safeURL(envelope.app_url) ? envelope.app_url : ''
 return { items, counts, source }
}
const sectionIcons: Record<string, string> = { pocket_calendar_events: 'calendar', pocket_tasks_summary: 'check', pocket_money_bills: 'receipt', pocket_review_summary: 'review', pocket_garage_reminders: 'wrench', pocket_home_due: 'home', pocket_renewals_due: 'clock', pocket_parcels_summary: 'package', pocket_sign_summary: 'pen', pocket_places_summary: 'pin', pocket_voice_summary: 'mic', pocket_wallet_summary: 'qr', pocket_weather_summary: 'sun', pocket_fitness_summary: 'activity', pocket_nutrition_summary: 'apple', pocket_blog_summary: 'edit', pocket_chat_summary: 'message', pocket_forms_summary: 'list-checks', pocket_crm_today: 'briefcase' }
function BriefingCard({ section, onAsk, asking }: { section: Section; onAsk: (text: string) => void; asking: boolean }) {
 const [expanded, setExpanded] = useState(false)
 const { items, counts, source } = briefingContent(section)
 const title = sectionTitles[section.tool] || friendly(section.tool)
 const all = section.tool === 'pocket_weather_summary' || section.tool === 'pocket_fitness_summary' || section.tool === 'pocket_nutrition_summary' || section.tool === 'pocket_blog_summary' || section.tool === 'pocket_chat_summary' || section.tool === 'pocket_forms_summary' || section.tool === 'pocket_crm_today' // a few short lines; warnings never hide behind “Show more”
 const visible = expanded || all ? items : items.slice(0, 3)
 return <article className="assistant-card briefing-card">
  <header className="briefing-card-head"><span className="briefing-card-icon"><Icon name={sectionIcons[section.tool] || 'search'} size={17} /></span><h2>{title}</h2><button type="button" className="briefing-ask" disabled={asking || !!section.error} aria-label={`Ask about ${title}`} onClick={() => onAsk(`Help me with ${friendly(section.tool)}. Read the current data and suggest the next action; show any proposed change for approval.`)}><Icon name="sparkles" size={14} />Ask</button></header>
  {section.error ? <p role="alert" className="briefing-empty">{section.error}</p> : <>
   {counts.length > 0 && <div className="briefing-counts">{counts.map(([k, v]) => <span key={k}><b>{String(v)}</b> {friendly(k)}</span>)}</div>}
   {items.length ? <ul className="briefing-items">{visible.map((item, i) => {
    const itemTitle = String(item.title ?? item.name ?? item.summary), href = safeURL(item.item_url) ? item.item_url : source
    const when = item.due ?? item.due_date ?? item.next_date ?? item.start ?? item.date ?? item.return_by ?? item.expected_on ?? (section.tool === 'pocket_sign_summary' ? item.expires_at : undefined)
    const amount = typeof item.amount === 'number' && typeof item.currency === 'string' && section.tool.startsWith('pocket_money_') ? formatMinor(item.amount,item.currency) : ''
    return <li key={`${item.id ?? 'item'}-${i}`} className={item.level === 'warning' ? 'briefing-weather-warning' : item.level === 'advisory' ? 'briefing-weather-advisory' : undefined}><div className="briefing-item-content">{href ? <a href={href} target="_blank" rel="noopener noreferrer">{itemTitle}<Icon name="external" size={12} /></a> : <b>{itemTitle}</b>}<div className="briefing-item-meta">{when ? <span>{displayWhen(when)}</span> : null}{section.tool === 'pocket_sign_summary' ? (item.sender_name ? <span>From {String(item.sender_name)}</span> : null) : section.tool === 'pocket_weather_summary' || section.tool === 'pocket_fitness_summary' || section.tool === 'pocket_nutrition_summary' || section.tool === 'pocket_blog_summary' || section.tool === 'pocket_chat_summary' || section.tool === 'pocket_forms_summary' || section.tool === 'pocket_crm_today' ? (item.detail ? <span>{String(item.detail)}</span> : null) : section.tool === 'pocket_voice_summary' ? (Number(item.suggestions) > 0 ? <span>{Number(item.suggestions) === 1 ? '1 suggestion' : `${item.suggestions} suggestions`}</span> : <span>No suggestions</span>) : item.status ? <span>{friendly(String(item.status))}</span> : null}{amount && <span className="briefing-amount">{amount}</span>}</div></div><button type="button" className="briefing-item-ask" disabled={asking} aria-label={`Ask about ${itemTitle}`} title={`Ask about ${itemTitle}`} onClick={() => onAsk(`Help me with “${itemTitle}” in ${friendly(section.tool)}. Source: ${href}. Record ID: ${item.id ?? 'not supplied'}. Read the current record, then show any proposed change for approval.`)}><Icon name="sparkles" size={14} /><span>Ask</span></button></li>
   })}</ul> : <p className="briefing-empty">{section.tool === 'pocket_calendar_events' ? 'No calendar events in today’s overview.' : section.tool === 'pocket_voice_summary' ? 'No memos waiting in your inbox.' : section.tool === 'pocket_wallet_summary' ? 'No tickets today and nothing runs out this week.' : section.tool === 'pocket_blog_summary' ? 'No drafts to finish, nothing scheduled and no comments waiting.' : section.tool === 'pocket_chat_summary' ? 'All caught up, nothing unread.' : section.tool === 'pocket_forms_summary' ? 'No new answers and no forms taking answers.' : section.tool === 'pocket_crm_today' ? 'No deal steps due today or overdue.' : section.tool === 'pocket_weather_summary' ? String(((section.data as Record<string, unknown> | undefined)?.result as Record<string, unknown> | undefined)?.text || 'No weather for home yet.') : section.tool === 'pocket_fitness_summary' ? String(((section.data as Record<string, unknown> | undefined)?.result as Record<string, unknown> | undefined)?.text || 'No workouts planned or logged yet.') : section.tool === 'pocket_nutrition_summary' ? String(((section.data as Record<string, unknown> | undefined)?.result as Record<string, unknown> | undefined)?.text || 'Nothing logged yet.') : 'No items in this overview.'}</p>}
   {items.length > 3 && !all && <button type="button" className="briefing-expand" aria-expanded={expanded} onClick={() => setExpanded(v => !v)}>{expanded ? 'Show less' : `Show ${items.length - 3} more`}<Icon name={expanded ? 'chevron-up' : 'chevron-down'} size={14} /></button>}
  </>}
  <div className="briefing-source-link">{source && <a href={source} aria-label={`Open source: ${title}`} target="_blank" rel="noopener noreferrer">Open {title}<Icon name="external" size={12} /></a>}</div>
  <details className="briefing-source"><summary>Details &amp; source</summary><p>Checked {new Date(section.checked_at).toLocaleString()} · {(section.duration_ms / 1000).toFixed(1)}s</p><DataView value={section.data} /></details>
 </article>
}
export function BriefingPage({ onMenu }: { onMenu?: () => void }) {
 const [data, setData] = useState<Overview | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false), [q, setQ] = useState(''), [query, setQuery] = useState(''), [asking, setAsking] = useState(false)
 const navigate = useNavigate(), { toast } = useToast()
 async function load(search = false) { const term = search ? q.trim() : ''; if (search && !term) return; setBusy(true); setError(''); try { const next = await api.get<Overview>(`${V1}/${search ? `app-search?q=${encodeURIComponent(term)}` : 'briefing'}`); setData(next); setQuery(term); if (!search) setQ('') } catch (e) { setError(errMsg(e)) } finally { setBusy(false) } }
 useEffect(() => { void load() }, [])
 async function ask(prompt: string) { if (asking) return; setAsking(true); try { const t = await api.post<Thread>(`${V1}/ask/threads`, { text: prompt }); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setAsking(false) } }
 const sections = [...(data?.sections || [])].sort((a,b) => {
  const hasContent = (s: Section) => !s.error && (briefingContent(s).items.length > 0 || briefingContent(s).counts.some(([,n]) => Number(n) > 0))
  return Number(hasContent(b)) - Number(hasContent(a))
 })
 const available = data?.sections.filter(s => !s.error).length || 0
 const dateLabel = data?.date ? new Date(data.date + 'T12:00:00').toLocaleDateString(undefined, { weekday:'long', month:'long', day:'numeric' }) : 'Your day'
 return <Frame title="Briefing" sub="Your day across Pocket apps, in one place." onMenu={onMenu}>
  <div className="briefing-heading"><div><h2>{query ? 'Search results' : dateLabel}</h2><p>{query ? `For “${query}”` : 'Appointments, tasks and upcoming commitments.'}</p></div><button type="button" className="briefing-summarize" disabled={busy || asking || !available} aria-busy={asking} onClick={() => ask(query ? `Search my apps for ${JSON.stringify(query)} and summarize the relevant results with source links. Explain any missing sources.` : 'Give me a concise briefing for today: appointments, overdue and due tasks, bills, review work and maintenance. Link each item and explain any unavailable sources.')}><Icon name="sparkles" size={16} />{asking ? 'Starting…' : query ? 'Ask about results' : 'Summarize my day'}<Icon name="chevron-right" size={14} /></button></div>
  <form className="briefing-search" role="search" aria-label="Search Pocket apps" onSubmit={e => { e.preventDefault(); void load(true) }}><div className="briefing-search-input"><Icon name="search" size={16} /><input aria-label="Search my apps" placeholder="Search across your apps…" value={q} maxLength={200} onChange={e => setQ(e.target.value)} /><button type="submit" disabled={busy || !q.trim()}>Search</button></div><button type="button" className="briefing-refresh" disabled={busy} onClick={() => load()}><Icon name={query ? 'arrow-left' : 'refresh'} size={15} />{query ? 'Back to today' : 'Refresh'}</button></form>
  {error && <p role="alert" className="callout danger">{error}</p>}
  {busy && <p role="status" className="muted small">Reading your apps…</p>}
  {data && <><div className="briefing-overview-meta"><span>{available} of {data.sections.length + (data.unavailable?.length || 0)} sources available</span><details><summary>About this {query ? 'search' : 'briefing'}</summary><p>{data.timezone && `${data.timezone} · `}{data.coverage}</p>{data.unavailable?.length ? <p>Not connected: {data.unavailable.map(friendly).join(', ')}.</p> : null}</details></div>{data.unavailable?.length ? <p className="muted small">Some apps aren’t connected. Open “About this briefing” for details.</p> : null}<div className="briefing-grid" aria-busy={busy}>{sections.map(s => <BriefingCard section={s} onAsk={ask} asking={asking} key={s.tool} />)}</div></>}
 </Frame>
}

interface Memory { id: number; label: string; value: string; enabled: boolean }
export function MemoryPage({ onMenu }: { onMenu?: () => void }) {
 const [items, setItems] = useState<Memory[]>([]), [edit, setEdit] = useState<Memory>({ id: 0, label: '', value: '', enabled: true }), [busy, setBusy] = useState(false)
 const { toast } = useToast()
 async function load() { try { setItems(await api.get<Memory[]>(`${V1}/memory`)) } catch (e) { toast(errMsg(e), 'err') } }
 useEffect(() => { void load() }, [])
 async function save(m: Memory) { setBusy(true); try { if (m.id) await api.put(`${V1}/memory/${m.id}`, m); else await api.post(`${V1}/memory`, m); setEdit({ id: 0, label: '', value: '', enabled: true }); await load() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function remove(id: number) { try { await api.del(`${V1}/memory/${id}`); await load() } catch (e) { toast(errMsg(e), 'err') } }
 return <Frame title="Memory" sub="Preferences you explicitly save. Current app records always take precedence." onMenu={onMenu}><p>Nothing is saved here automatically. Forgetting a preference does not erase earlier conversation messages. Disable a preference to keep it without using it, or forget it. You can turn memory off for one conversation in its menu.</p><form className="assistant-form assistant-card" onSubmit={e => { e.preventDefault(); void save(edit) }}><label>Name<input required maxLength={100} value={edit.label} onChange={e => setEdit({ ...edit, label: e.target.value })} placeholder="My car" /></label><label>Preference<textarea {...noGrammarly} required maxLength={2000} value={edit.value} onChange={e => setEdit({ ...edit, value: e.target.value })} placeholder="When I say my car, I mean the Golf in Garage." rows={3} /></label><div className="row"><button className="btn primary" disabled={busy}>{edit.id ? 'Save changes' : 'Save preference'}</button>{edit.id > 0 && <button className="btn" type="button" onClick={() => setEdit({ id: 0, label: '', value: '', enabled: true })}>Cancel</button>}</div></form>{items.map(m => <article className="assistant-card" key={m.id}><label className="row"><input type="checkbox" checked={m.enabled} disabled={busy} onChange={e => save({ ...m, enabled: e.target.checked })} /><b>{m.label}</b></label><p className="data-text">{m.value}</p><div className="row"><button className="btn sm" onClick={() => setEdit(m)}>Edit</button><button className="btn sm" onClick={() => remove(m.id)}>Forget</button></div></article>)}{!items.length && <p className="muted">No saved preferences yet.</p>}</Frame>
}
export function ActionsPage({ onMenu }: { onMenu?: () => void }) {
 const [calls, setCalls] = useState<AppCall[]>([]), [writes, setWrites] = useState(true), [error, setError] = useState('')
 useEffect(() => { api.get<AppCall[]>(`${V1}/app-calls${writes ? '?writes=1' : ''}`).then(setCalls).catch(e => setError(errMsg(e))) }, [writes])
 const times = calls.filter(c => !c.is_write && c.status === 'done').map(c => c.duration_ms).sort((a, b) => a - b)
 return <Frame title="Action history" sub="The latest 200 app calls, approvals and confirmed results." onMenu={onMenu}><label className="row"><input type="checkbox" checked={writes} onChange={e => setWrites(e.target.checked)} />Only changes</label>{error && <p role="alert">{error}</p>}{times.length > 0 && <p className="muted">Read latency: median {(times[Math.floor(times.length / 2)] / 1000).toFixed(2)}s · 95th percentile {(times[Math.min(times.length - 1, Math.floor(times.length * .95))] / 1000).toFixed(2)}s across {times.length} recorded reads. This measures app calls, excluding model reasoning.</p>}{calls.map(c => <CallCard key={c.id} call={c} />)}{!calls.length && <p>No recorded changes yet.</p>}</Frame>
}
interface Workflow { id: number; name: string; prompt: string; model: string; trigger_kind: string; spec: string; enabled: boolean; notify: boolean; last_thread_id?: string; last_status?: string; next_run_at?: string }
const emptyWorkflow: Workflow = { id: 0, name: '', prompt: '', model: 'sonnet', trigger_kind: 'manual', spec: 'daily 08:00', enabled: true, notify: true }
export function WorkflowsPage({ onMenu }: { onMenu?: () => void }) {
 const [items, setItems] = useState<Workflow[]>([]), [edit, setEdit] = useState<Workflow>({ ...emptyWorkflow }), [busy, setBusy] = useState(false)
 const { toast } = useToast(), navigate = useNavigate(), location = useLocation()
 async function load() { try { setItems(await api.get<Workflow[]>(`${V1}/workflows`)) } catch (e) { toast(errMsg(e), 'err') } }
 useEffect(() => { void load(); const v = location.state as { name?: string; prompt?: string } | null; if (v?.prompt) setEdit({ ...emptyWorkflow, name: (v.name || '').slice(0, 100), prompt: v.prompt }) }, [])
 async function save(x: Workflow) { setBusy(true); try { if (x.id) await api.put(`${V1}/workflows/${x.id}`, x); else await api.post(`${V1}/workflows`, x); setEdit({ ...emptyWorkflow }); await load() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function run(x: Workflow) { setBusy(true); try { const t = await api.post<Thread>(`${V1}/workflows/${x.id}/run`); navigate(`/t/${t.id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
 async function remove(x: Workflow) { try { await api.del(`${V1}/workflows/${x.id}`); await load() } catch (e) { toast(errMsg(e), 'err') } }
 return <Frame title="Workflows" sub="Repeat an app task with fresh data. Every write still needs approval." onMenu={onMenu}><form className="assistant-form assistant-card" onSubmit={e => { e.preventDefault(); void save(edit) }}><label>Name<input required maxLength={100} value={edit.name} onChange={e => setEdit({ ...edit, name: e.target.value })} placeholder="Weekly spending note" /></label><label>Instructions<textarea {...noGrammarly} required rows={4} maxLength={20000} value={edit.prompt} onChange={e => setEdit({ ...edit, prompt: e.target.value })} placeholder="Summarize this week’s spending and prepare a note in Quick Notes." /></label><div className="row wrap"><label>Run<Select label="Run" value={edit.trigger_kind} onChange={trigger_kind => setEdit({ ...edit, trigger_kind })} options={[{ value: 'manual', label: 'When I press Run' }, { value: 'schedule', label: 'On a schedule' }, { value: 'documents', label: 'When new documents appear' }, { value: 'tasks', label: 'When new tasks appear' }]} /></label><label>Model<Select label="Workflow model" value={edit.model} onChange={model => setEdit({ ...edit, model })} options={[{ value: 'haiku', label: 'Haiku' }, { value: 'sonnet', label: 'Sonnet' }, { value: 'opus', label: 'Opus' }]} /></label></div>{edit.trigger_kind === 'schedule' && <label>Schedule<input required value={edit.spec} onChange={e => setEdit({ ...edit, spec: e.target.value })} placeholder="daily 08:00" /><span className="muted small">Examples: daily 08:00, weekly mon 09:00, every 6h. Uses your Settings timezone.</span></label>}{['documents', 'tasks'].includes(edit.trigger_kind) && <p className="muted small">Checks up to 100 recent task events or newly added documents once a minute. Existing items form the initial baseline; only newly observed IDs trigger a run. A large import or burst of activity may exceed this window. Re-enabling starts a fresh baseline.</p>}<label className="row"><input type="checkbox" checked={edit.enabled} onChange={e => setEdit({ ...edit, enabled: e.target.checked })} />Enabled</label><label className="row"><input type="checkbox" checked={edit.notify} onChange={e => setEdit({ ...edit, notify: e.target.checked })} />Notify when finished</label><div className="row"><button className="btn primary" disabled={busy}>{edit.id ? 'Save changes' : 'Create workflow'}</button>{edit.id > 0 && <button type="button" className="btn" onClick={() => setEdit({ ...emptyWorkflow })}>Cancel</button>}</div></form>{items.map(x => <article className="assistant-card" key={x.id}><h2>{x.name}</h2><p>{x.prompt}</p><p className="muted small">{x.enabled ? friendly(x.trigger_kind) : 'Paused'} {x.trigger_kind === 'schedule' && `· ${x.spec}`} · {x.last_status || 'Not run yet'}{x.next_run_at && x.enabled && ` · Next ${new Date(x.next_run_at).toLocaleString()}`}</p><div className="row wrap"><button className="btn primary" disabled={busy} onClick={() => run(x)}>Run now</button><button className="btn" onClick={() => setEdit(x)}>Edit</button><button className="btn" onClick={() => save({ ...x, enabled: !x.enabled })}>{x.enabled ? 'Pause' : 'Enable'}</button><button className="btn" onClick={() => remove(x)}>Delete</button>{x.last_thread_id && <Link to={`/t/${x.last_thread_id}`}>Last run ↗</Link>}</div></article>)}</Frame>
}
