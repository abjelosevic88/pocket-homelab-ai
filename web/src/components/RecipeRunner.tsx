// Running a recipe from anywhere: ask for its {{placeholders}} first, then open the new thread.
import React, { createContext, useCallback, useContext, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { promptVars } from '../lib/format'
import type { Recipe, Thread } from '../lib/types'
import { Icon } from './icons'
import { errMsg, Field, Modal, Spinner, useToast } from './ui'

const Ctx = createContext<(r: Recipe) => void>(() => {})
export const useRunRecipe = () => useContext(Ctx)

export function RecipeRunnerProvider({ children }: { children: React.ReactNode }) {
  const { upsertThread, reloadRecipes, user } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [asking, setAsking] = useState<Recipe | null>(null)
  const [vals, setVals] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const start = useCallback(async (r: Recipe, vars: Record<string, string>) => {
    setBusy(true)
    try {
      const t = await api.post<Thread>(`${V1}/recipes/${r.id}/run`, { vars })
      upsertThread(t)
      setAsking(null)
      navigate(`/t/${t.id}`)
      reloadRecipes().catch(() => {})
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }, [navigate, toast, upsertThread, reloadRecipes])

  const run = useCallback((r: Recipe) => {
    if (user && user.role !== 'admin' && !user.can_run) { toast('An admin has to allow you to run agents first', 'err'); return }
    const vars = promptVars(r.prompt)
    if (vars.length) { setVals(Object.fromEntries(vars.map(v => [v, '']))); setAsking(r); return }
    start(r, {})
  }, [start, toast, user])

  const vars = asking ? promptVars(asking.prompt) : []
  return (
    <Ctx.Provider value={run}>
      {children}
      {asking && (
        <Modal title={<span className="row"><span>{asking.icon || '✦'}</span>{asking.name}</span>} onClose={() => setAsking(null)} size="sm" footer={<>
          <button type="button" className="btn" onClick={() => setAsking(null)}>Cancel</button>
          <button type="button" className="btn primary" disabled={busy || vars.some(v => !vals[v]?.trim())} onClick={() => start(asking, vals)}>{busy ? <Spinner /> : <Icon name="play" size={12} />}Run</button>
        </>}>
          <form className="stack" onSubmit={e => { e.preventDefault(); if (!vars.some(v => !vals[v]?.trim())) start(asking, vals) }}>
            {vars.map((v, i) => (
              <Field key={v} label={v}><input autoFocus={i === 0} value={vals[v] || ''} onChange={e => setVals(x => ({ ...x, [v]: e.target.value }))} /></Field>
            ))}
            <button type="submit" hidden />
          </form>
        </Modal>
      )}
    </Ctx.Provider>
  )
}
