// Side questions: ask about a chat without interrupting it. The answer comes
// from a quick, tool-less model call that reads the chat so far; the agent
// keeps working, and nothing is added to its conversation or queue.
// Answers stay on this device (per tab) until dismissed.
import { useCallback, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { errMsg, Spinner, useToast } from './ui'

export interface SideQuestion { id: string; question: string; answer?: string; error?: string; model?: string }

const storeKey = (threadId: string) => 'pai-side-' + threadId
const load = (threadId: string): SideQuestion[] => {
  try { return (JSON.parse(sessionStorage.getItem(storeKey(threadId)) || '[]') as SideQuestion[]).filter(q => q.answer || q.error) } catch { return [] }
}

export function useSideQuestions(threadId: string) {
  const [list, setList] = useState<SideQuestion[]>(() => load(threadId))
  useEffect(() => setList(load(threadId)), [threadId])
  useEffect(() => {
    const done = list.filter(q => q.answer || q.error)
    if (done.length) sessionStorage.setItem(storeKey(threadId), JSON.stringify(done.slice(-10)))
    else sessionStorage.removeItem(storeKey(threadId))
  }, [list, threadId])
  const ask = useCallback(async (question: string) => {
    const id = Math.random().toString(36).slice(2)
    setList(l => [...l, { id, question }])
    try {
      const r = await api.post<{ answer: string; model: string }>(`${V1}/threads/${threadId}/side-question`, { question })
      setList(l => l.map(q => q.id === id ? { ...q, answer: r.answer || '(no answer)', model: r.model } : q))
    } catch (e) {
      setList(l => l.map(q => q.id === id ? { ...q, error: errMsg(e) } : q))
    }
  }, [threadId])
  const dismiss = useCallback((id: string) => setList(l => l.filter(q => q.id !== id)), [])
  return { list, ask, dismiss }
}

export function SideQuestions({ list, onDismiss, onRetry }: { list: SideQuestion[]; onDismiss: (id: string) => void; onRetry: (q: SideQuestion) => void }) {
  const { toast } = useToast()
  if (!list.length) return null
  return (
    <div className="side-qs" aria-label="Side questions">
      {list.map(q => (
        <div key={q.id} className={`side-q ${q.error ? 'err' : ''}`}>
          <div className="side-q-h">
            <Icon name="help-circle" size={13} />
            <span className="side-q-text grow" title={q.question}>{q.question}</span>
            {q.answer && <button type="button" className="btn ghost icon-only xs" aria-label="Copy answer" title="Copy answer" onClick={() => { void navigator.clipboard?.writeText(q.answer!).then(() => toast('Copied', 'ok')) }}><Icon name="copy" size={13} /></button>}
            <button type="button" className="btn ghost icon-only xs" aria-label="Dismiss" title="Dismiss" onClick={() => onDismiss(q.id)}><Icon name="x" size={13} /></button>
          </div>
          {q.answer ? <div className="side-q-a"><Markdown text={q.answer} /></div>
            : q.error ? <div className="side-q-a small"><span className="danger-text">{q.error}</span> <button type="button" className="link-btn small" onClick={() => { onDismiss(q.id); onRetry(q) }}>Try again</button></div>
            : <div className="side-q-a muted small"><Spinner size={12} /> Answering on the side — the agent keeps working…</div>}
          {q.answer && <div className="side-q-foot muted xsmall">Side answer{q.model ? ` · ${q.model}` : ''} · not added to the chat</div>}
        </div>
      ))}
    </div>
  )
}
