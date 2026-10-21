// The agent asks: may I run this? which option? is this plan OK? — answered right above the composer.
import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { AgentRequest } from '../lib/types'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { Diff } from './ToolRow'
import { errMsg, Spinner, useToast } from './ui'

type Decision = 'allow' | 'allow_session' | 'deny'

function inputPreview(input: unknown): { command?: string; path?: string; diff?: string; rest?: string } {
  if (!input || typeof input !== 'object') return { rest: input ? String(input) : '' }
  const o = input as Record<string, unknown>
  const out: { command?: string; path?: string; diff?: string; rest?: string } = {}
  if (typeof o.command === 'string') out.command = o.command
  const p = o.file_path ?? o.path
  if (typeof p === 'string') out.path = p
  if (typeof o.old_string === 'string' && typeof o.new_string === 'string') {
    out.diff = [...(o.old_string as string).split('\n').map(l => '-' + l), ...(o.new_string as string).split('\n').map(l => '+' + l)].join('\n')
  } else if (typeof o.content === 'string' && out.path) {
    out.diff = (o.content as string).split('\n').slice(0, 80).map(l => '+' + l).join('\n')
  } else if (typeof o.diff === 'string') out.diff = o.diff
  if (!out.command && !out.diff) {
    const rest = { ...o }
    delete rest.file_path; delete rest.path; delete rest.description
    const s = JSON.stringify(rest, null, 2)
    if (s !== '{}') out.rest = s
  }
  return out
}

export function RequestCard({ threadId, req, onDone }: { threadId: string; req: AgentRequest; onDone?: () => void }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState<Decision | null>(null)
  const [denying, setDenying] = useState(false)
  const [message, setMessage] = useState('')
  const [answers, setAnswers] = useState<Record<string, string[]>>({})
  const [other, setOther] = useState<Record<string, string>>({})

  const answer = async (decision: Decision, extra: { message?: string; answers?: Record<string, string[]> } = {}) => {
    setBusy(decision)
    try {
      await api.post(`${V1}/threads/${threadId}/requests/${encodeURIComponent(req.id)}`, { decision, ...extra })
      onDone?.()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(null) }
  }

  if (req.type === 'question' && req.questions?.length) {
    const qs = req.questions
    const complete = qs.every(q => (answers[q.id]?.length || 0) > 0 || (other[q.id] || '').trim())
    const submit = () => {
      const out: Record<string, string[]> = {}
      for (const q of qs) out[q.id] = [...(answers[q.id] || []), ...((other[q.id] || '').trim() ? [other[q.id].trim()] : [])]
      answer('allow', { answers: out })
    }
    return (
      <div className="req-card question">
        <div className="req-h"><Icon name="help-circle" size={16} /><b>The agent has {qs.length > 1 ? `${qs.length} questions` : 'a question'}</b></div>
        {qs.map(q => {
          const picked = answers[q.id] || []
          const toggle = (label: string) => setAnswers(a => {
            const cur = a[q.id] || []
            const next = q.multi ? (cur.includes(label) ? cur.filter(x => x !== label) : [...cur, label]) : (cur[0] === label ? [] : [label])
            if (!q.multi && next.length) setOther(o => ({ ...o, [q.id]: '' }))
            return { ...a, [q.id]: next }
          })
          return (
            <div key={q.id} className="req-q">
              {q.header && <div className="req-q-h">{q.header}</div>}
              <div className="req-q-text">{q.question}</div>
              <div className="req-opts">
                {q.options.map(o => (
                  <button key={o.label} type="button" className={`req-opt ${picked.includes(o.label) ? 'on' : ''}`} aria-pressed={picked.includes(o.label)} onClick={() => toggle(o.label)}>
                    <span className="req-opt-mark">{q.multi ? <Icon name={picked.includes(o.label) ? 'check-square' : 'circle'} size={14} /> : <Icon name={picked.includes(o.label) ? 'check-circle' : 'circle'} size={14} />}</span>
                    <span className="grow"><span className="req-opt-label">{o.label}</span>{o.description && <span className="req-opt-desc">{o.description}</span>}</span>
                  </button>
                ))}
              </div>
              {(q.other ?? true) && <input className="req-other" placeholder="Something else…" value={other[q.id] || ''} onChange={e => { const v = e.target.value; setOther(o => ({ ...o, [q.id]: v })); if (!q.multi && v) setAnswers(a => ({ ...a, [q.id]: [] })) }} />}
            </div>
          )
        })}
        <div className="req-actions">
          <button type="button" className="btn ghost" disabled={!!busy} onClick={() => answer('deny', { message: 'The user skipped the question.' })}>Skip</button>
          <button type="button" className="btn primary" disabled={!complete || !!busy} onClick={submit}>{busy ? <Spinner /> : <Icon name="send" size={14} />}Answer</button>
        </div>
      </div>
    )
  }

  if (req.type === 'plan') {
    return (
      <div className="req-card plan">
        <div className="req-h"><Icon name="list-checks" size={16} /><b>Plan ready for review</b></div>
        <div className="req-plan"><Markdown text={req.plan || ''} /></div>
        {denying && <textarea autoFocus className="req-msg" placeholder="What should change in the plan?" value={message} onChange={e => setMessage(e.target.value)} rows={2} />}
        <div className="req-actions">
          {denying
            ? <><button type="button" className="btn ghost" onClick={() => setDenying(false)}>Back</button><button type="button" className="btn" disabled={!!busy} onClick={() => answer('deny', { message: message.trim() || 'Keep planning.' })}>{busy === 'deny' && <Spinner />}Send feedback</button></>
            : <><button type="button" className="btn" disabled={!!busy} onClick={() => setDenying(true)}>Keep planning</button><button type="button" className="btn primary" disabled={!!busy} onClick={() => answer('allow')}>{busy === 'allow' ? <Spinner /> : <Icon name="check" size={14} />}Approve plan</button></>}
        </div>
      </div>
    )
  }

  const pv = inputPreview(req.input)
  return (
    <div className="req-card permission">
      <div className="req-h"><Icon name="shield" size={16} /><b className="grow">{req.title || `Use ${req.tool_name}?`}</b><span className="badge">{req.tool_name}</span></div>
      {pv.command && <pre className="tool-pre cmd">$ {pv.command}</pre>}
      {pv.path && !pv.command && <div className="mono small ellipsis">{pv.path}</div>}
      {pv.diff && <div className="req-diff"><Diff text={pv.diff} /></div>}
      {pv.rest && <pre className="tool-pre">{pv.rest.slice(0, 3000)}</pre>}
      {denying && <textarea autoFocus className="req-msg" placeholder="Optional: tell the agent why, or what to do instead" value={message} onChange={e => setMessage(e.target.value)} rows={2} />}
      <div className="req-actions">
        {denying ? <>
          <button type="button" className="btn ghost" onClick={() => setDenying(false)}>Back</button>
          <button type="button" className="btn danger" disabled={!!busy} onClick={() => answer('deny', message.trim() ? { message: message.trim() } : {})}>{busy === 'deny' && <Spinner />}Deny</button>
        </> : <>
          <button type="button" className="btn" disabled={!!busy} onClick={() => setDenying(true)}>Deny…</button>
          {req.can_allow_session && <button type="button" className="btn" disabled={!!busy} onClick={() => answer('allow_session')} title="Don't ask again for this in this thread">{busy === 'allow_session' && <Spinner />}Allow for session</button>}
          <button type="button" className="btn primary" disabled={!!busy} onClick={() => answer('allow')} autoFocus>{busy === 'allow' ? <Spinner /> : <Icon name="check" size={14} />}Allow</button>
        </>}
      </div>
    </div>
  )
}

/** A resolved request, as a quiet line in the timeline. */
export function RequestSummary({ req }: { req: AgentRequest }) {
  const label = req.type === 'question' ? 'Question' : req.type === 'plan' ? 'Plan' : (req.title || req.tool_name)
  const st = req.status
  const icon = st === 'allowed' || st === 'answered' ? 'check-circle' : st === 'denied' ? 'x-circle' : st === 'pending' ? 'clock' : 'minus'
  const word = st === 'allowed' ? (req.type === 'plan' ? 'approved' : 'allowed') : st === 'answered' ? 'answered' : st === 'denied' ? (req.type === 'plan' ? 'sent back' : 'denied') : st === 'pending' ? 'waiting for you' : 'expired'
  let detail = ''
  if (req.type === 'question' && req.answer && typeof req.answer === 'object') {
    const a = req.answer as Record<string, unknown>
    const vals = Object.values((a.answers as Record<string, unknown>) || a).flat().filter(x => typeof x === 'string')
    detail = vals.join(', ')
  }
  return (
    <div className={`req-line ${st}`}>
      <Icon name={icon} size={13} />
      <span className="ellipsis"><b>{label}</b> — {word}{detail ? `: ${detail}` : ''}</span>
    </div>
  )
}
