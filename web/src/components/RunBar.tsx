// The bar under a working thread: who is working, for how long, sub-agents
// and background commands on the go, and a fork button that opens the fork in
// a new tab. Once the turn is over and only background work is left, it
// switches to the calmer background look.
import { api, V1 } from '../lib/api'
import { BackgroundChip, BgMark, backgroundLabel, backgroundSince, backgroundTitle } from './Background'
import { useApp } from '../lib/app-context'
import { openInParent, parentTabs } from '../lib/embed'
import type { Item, Thread } from '../lib/types'
import { Icon } from './icons'
import { RunDots, elapsed, useNow } from './RunningList'
import { SubagentsChip } from './Subagents'
import { errMsg, useToast } from './ui'

// Fork the thread and open the copy in a new tab. window.open has to happen
// in the click, before the request, or Safari blocks it.
export function useForkNewTab(threadId: string) {
  const { upsertThread } = useApp()
  const { toast } = useToast()
  // after = a turn footer's key: fork as the conversation was right after it.
  return async (after?: string) => {
    // in a panel with tabs (Pocket Projects), the fork opens in a tab there
    const w = parentTabs ? null : window.open('about:blank', '_blank')
    try {
      const n = await api.post<Thread>(`${V1}/threads/${threadId}/fork`, after ? { after } : undefined)
      upsertThread(n)
      if (parentTabs) { openInParent(n.id, n.title); toast('Forked — the copy opened in a new tab') }
      else if (w) w.location.href = `/t/${n.id}`
      else window.location.href = `/t/${n.id}`
    } catch (e) { w?.close(); toast(errMsg(e), 'err') }
  }
}

const WHERE: Record<string, string> = { paseo: 'in Paseo', terminal: 'in a terminal', app: 'in another app' }

// One row whose height never changes: what is working, the sub-agents (a chip
// whose list floats above the bar, so opening it or agents coming and going
// does not move the conversation), fork, run time.
export function RunBar({ t, items }: { t: Thread; items: Item[] }) {
  const { running } = useApp()
  const fork = useForkNewTab(t.id)
  const now = useNow(1000)
  const tools = items.filter(it => it.kind === 'tool' && it.tool?.status === 'running' && it.tool.kind !== 'task')
  const waiting = t.status === 'waiting' || t.pending > 0
  const turnOver = t.status !== 'running' && !waiting
  const since = turnOver ? backgroundSince(t) || t.activity_at : t.prompt_at || t.activity_at
  const origin = running.find(r => r.session_id === t.provider_session_id)?.origin || ''
  const what = waiting ? 'Waiting for you' : t.external ? `Working ${WHERE[origin] || 'elsewhere'}` : 'Working…'
  const tasks = t.background?.length || 0

  return (
    <div className={`run-bar ${turnOver ? 'bg' : ''}`}>
      <div className="run-bar-row">
        {turnOver ? <>
          <BgMark size={14} />
          <span className="run-bar-what ellipsis" title={backgroundTitle(t)}><span className="bg-word">In the background</span>{tasks < 2 && <> · {backgroundLabel(t)}</>}</span>
        </> : <>
          <RunDots waiting={waiting} size={14} />
          <span className="run-bar-what ellipsis" title={!waiting ? tools[0]?.tool?.title : undefined}>{what}</span>
        </>}
        <SubagentsChip t={t} />
        {(!turnOver || tasks > 1) && <BackgroundChip t={t} label={turnOver ? backgroundLabel(t) : undefined} />}
        <span className="grow" />
        {t.provider_session_id && <button type="button" className="btn ghost icon-only xs" onClick={() => fork()} title="Fork into a new tab" aria-label="Fork into a new tab"><Icon name="fork" size={15} /></button>}
        <span className="run-time tnum" title={since ? `Since ${new Date(since).toLocaleTimeString()}` : ''}>{elapsed(since, now)}</span>
      </div>
    </div>
  )
}
