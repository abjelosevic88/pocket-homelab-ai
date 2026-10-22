// Recipes: saved prompts with their agent settings, run with one tap.
import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { promptVars, providerName, shortPath } from '../lib/format'
import type { AgentChoice, Recipe } from '../lib/types'
import { AgentPicker } from '../components/AgentPicker'
import { FolderButton } from '../components/FolderPicker'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { useRunRecipe } from '../components/RecipeRunner'
import { Confirm, Empty, errMsg, Field, Loading, MenuButton, Modal, Spinner, Switch, useAsync, useToast, type MenuItem, useContextMenu } from '../components/ui'

type Draft = Omit<Recipe, 'id' | 'runs' | 'created_at' | 'updated_at' | 'sort_order'> & { id?: number; sort_order?: number }

const blank: Draft = { name: '', prompt: '', icon: '', provider: '', model: '', effort: '', mode: '', speed: '', cwd: '', pinned: true }

export function AgentSummary({ c }: { c: AgentChoice }) {
  const bits = [c.provider ? providerName(c.provider) : 'Default agent']
  if (c.model) bits.push(c.model)
  if (c.effort) bits.push(c.effort)
  if (c.mode) bits.push(c.mode)
  return <span className="muted xsmall ellipsis">{bits.join(' · ')}{c.cwd ? ` · ${shortPath(c.cwd)}` : ''}</span>
}

export default function RecipesPage({ onMenu }: { onMenu?: () => void }) {
  const { recipes, reloadRecipes, user } = useApp()
  const { toast } = useToast()
  const run = useRunRecipe()
  const [edit, setEdit] = useState<Draft | null>(null)
  const [del, setDel] = useState<Recipe | null>(null)
  const ctx = useContextMenu()
  const [starters, setStarters] = useState(false)
  const canRun = !!user && (user.role === 'admin' || user.can_run)

  const togglePin = async (r: Recipe) => {
    try { await api.put(`${V1}/recipes/${r.id}`, { ...r, pinned: !r.pinned }); reloadRecipes() } catch (e) { toast(errMsg(e), 'err') }
  }

  return (
    <div className="page">
      <PageHead title="Recipes" icon="zap" onMenu={onMenu} sub="Prompts you run again and again — pinned ones show on the start page and after / in the composer."
        actions={<>
          <button type="button" className="btn" onClick={() => setStarters(true)}><Icon name="sparkles" size={14} /><span className="hide-sm">Starter recipes</span></button>
          <button type="button" className="btn primary" onClick={() => setEdit({ ...blank })}><Icon name="plus" size={14} />New</button>
        </>} />
      <div className="page-scroll">
        <div className="page-inner">
          {recipes.length === 0 ? (
            <Empty title="No recipes yet" icon="zap">
              Save a prompt like “Check disk space, failed systemd units and unhealthy containers” and run it with one tap.
              <div className="row mt" style={{ justifyContent: 'center' }}>
                <button type="button" className="btn" onClick={() => setStarters(true)}><Icon name="sparkles" size={14} />Add starter recipes</button>
                <button type="button" className="btn primary" onClick={() => setEdit({ ...blank })}><Icon name="plus" size={14} />New recipe</button>
              </div>
            </Empty>
          ) : (
            <div className="list-cards">
              {ctx.menu}{recipes.map(r => {
                const more: MenuItem[] = [
                  { label: 'Edit…', icon: 'edit', onSelect: () => setEdit({ ...r }) },
                  { label: r.pinned ? 'Unpin from start page' : 'Pin to start page', icon: 'bookmark', onSelect: () => togglePin(r) },
                  { label: 'Duplicate', icon: 'copy', onSelect: () => setEdit({ ...r, id: undefined, name: r.name + ' (copy)' }) },
                  'sep',
                  { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDel(r) },
                ]
                return (
                <div key={r.id} className="list-card" onContextMenu={e => ctx.open(e, more)}>
                  <span className="recipe-ico lg">{r.icon || '✦'}</span>
                  <div className="grow" style={{ minWidth: 0 }}>
                    <div className="list-card-title">{r.name}{r.pinned && <Icon name="bookmark" size={12} className="faint" />}{promptVars(r.prompt).length > 0 && <span className="badge">asks {promptVars(r.prompt).length}</span>}</div>
                    <div className="list-card-sub">{r.prompt}</div>
                    <AgentSummary c={r} />
                  </div>
                  <div className="list-card-actions">
                    <button type="button" className="btn sm" disabled={!canRun} onClick={() => run(r)}><Icon name="play" size={11} />Run</button>
                    <MenuButton label={`More for ${r.name}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
                  </div>
                </div>
              )})}
            </div>
          )}
        </div>
      </div>
      {edit && <RecipeDialog draft={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadRecipes() }} />}
      {starters && <StartersDialog onClose={() => setStarters(false)} onAdded={() => { setStarters(false); reloadRecipes() }} />}
      {del && <Confirm title={`Delete “${del.name}”?`} message="Threads it started stay." onCancel={() => setDel(null)}
        onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/recipes/${d.id}`); reloadRecipes(); toast('Recipe deleted') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function RecipeDialog({ draft, onClose, onSaved }: { draft: Draft; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState<Draft>(draft)
  const [busy, setBusy] = useState(false)
  const vars = promptVars(f.prompt)
  const save = async () => {
    setBusy(true)
    try {
      if (f.id) await api.put(`${V1}/recipes/${f.id}`, f); else await api.post(`${V1}/recipes`, f)
      toast(f.id ? 'Recipe saved' : 'Recipe added')
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={f.id ? 'Edit recipe' : 'New recipe'} onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !f.name.trim() || !f.prompt.trim()} onClick={save}>{busy && <Spinner />}{f.id ? 'Save' : 'Add'}</button>
    </>}>
      <div className="stack">
        <div className="row top">
          <Field label="Icon" className="icon-field"><input value={f.icon} onChange={e => setF({ ...f, icon: e.target.value.slice(0, 4) })} placeholder="🩺" aria-label="Icon (an emoji)" /></Field>
          <Field label="Name" className="grow"><input autoFocus value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="Server health check" /></Field>
        </div>
        <Field label="Prompt" help={<>Use <code>{'{{name}}'}</code> for something you fill in each time, e.g. “Why is <code>{'{{app}}'}</code> down?”.{vars.length > 0 && <> Asks for: <b>{vars.join(', ')}</b>.</>}</>}>
          <textarea value={f.prompt} onChange={e => setF({ ...f, prompt: e.target.value })} rows={6} placeholder="Check disk space, memory, failed systemd units and unhealthy Docker containers. Summarise what needs attention." />
        </Field>
        <Field label="Agent" group help="“My default” uses whatever is set in Settings → Agents when the recipe runs.">
          <AgentPicker allowDefault value={f} onChange={p => setF(x => ({ ...x, ...p }))} />
        </Field>
        <Field label="Folder" group help="Where the agent starts. Empty = your default folder.">
          <div className="row"><FolderButton value={f.cwd} onChange={cwd => setF({ ...f, cwd })} className="btn" />{f.cwd && <button type="button" className="btn ghost sm" onClick={() => setF({ ...f, cwd: '' })}>Use default</button>}</div>
        </Field>
        <label className="inline-switch"><Switch checked={f.pinned} onChange={v => setF({ ...f, pinned: v })} label="Pinned" />Show on the start page</label>
      </div>
    </Modal>
  )
}

function StartersDialog({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { recipes } = useApp()
  const { toast } = useToast()
  const list = useAsync(() => api.get<Draft[]>(`${V1}/recipes/suggestions`), [])
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const have = new Set(recipes.map(r => r.name.toLowerCase()))
  const add = async () => {
    setBusy(true)
    try {
      for (const i of picked) await api.post(`${V1}/recipes`, { ...list.data![i], id: undefined })
      toast(picked.size === 1 ? 'Recipe added' : `${picked.size} recipes added`)
      onAdded()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Starter recipes" onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || picked.size === 0} onClick={add}>{busy && <Spinner />}Add {picked.size || ''}</button>
    </>}>
      {list.error ? <div className="callout danger"><Icon name="alert" size={15} /><span>{list.error}</span></div> : !list.data ? <Loading /> : (
        <div className="stack tight">
          {list.data.map((r, i) => {
            const dup = have.has(r.name.toLowerCase())
            const on = picked.has(i)
            return (
              <button key={i} type="button" disabled={dup} className={`starter ${on ? 'on' : ''}`} onClick={() => setPicked(p => { const n = new Set(p); if (n.has(i)) n.delete(i); else n.add(i); return n })}>
                <span className="recipe-ico">{r.icon || '✦'}</span>
                <span className="grow" style={{ minWidth: 0 }}><b>{r.name}</b>{dup && <span className="muted small"> — already added</span>}<span className="list-card-sub">{r.prompt}</span></span>
                <Icon name={on ? 'check-circle' : 'circle'} size={18} className={on ? 'accent-text' : 'faint'} />
              </button>
            )
          })}
        </div>
      )}
    </Modal>
  )
}
