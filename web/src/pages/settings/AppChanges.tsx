import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { Icon } from '../../components/icons'
import { errMsg, Loading, SettingRow, Switch, useToast } from '../../components/ui'
import { Select } from '../../components/Select'
import { Section } from './common'

export interface AutoRules { apps: Record<string, 'add' | 'all'>; tools: string[] }
interface AutoOp { name: string; title: string; kind: 'add' | 'change'; description: string }
interface AutoApp { slug: string; name: string; ops: AutoOp[]; adds: number; total: number }
interface AutoData { rules: AutoRules; apps: AutoApp[]; catalog_error?: string }

type Level = 'ask' | 'add' | 'all' | 'custom'

const list = (ops: AutoOp[]) => ops.map(o => o.title).join(', ')

/** One sentence: what happens on its own in this app, and what still asks. */
function explain(a: AutoApp, level: Level, picked: Set<string>): string {
  const adds = a.ops.filter(o => o.kind === 'add'), edits = a.ops.filter(o => o.kind === 'change')
  if (level === 'all') return `Runs on its own: ${list(a.ops)}.`
  const auto = level === 'add' ? [...adds, ...edits.filter(o => picked.has(o.name))] : a.ops.filter(o => picked.has(o.name))
  const asks = a.ops.filter(o => !auto.includes(o))
  if (!auto.length) return `Asks you first: ${list(asks)}.`
  return `Runs on its own: ${list(auto)}. Still asks: ${list(asks)}.`
}

export function AppChangesTab() {
  const { toast } = useToast()
  const [data, setData] = useState<AutoData | null>(null)
  const [open, setOpen] = useState<string | null>(null)
  useEffect(() => {
    api.get<AutoData>(`${V1}/pocket-tools/auto-approve`).then(setData).catch(e => toast(errMsg(e), 'err'))
  }, [toast])
  if (!data) return <Loading />

  const save = async (rules: AutoRules) => {
    const prev = data
    setData({ ...data, rules })
    try { await api.put(`${V1}/pocket-tools/auto-approve`, rules) } catch (e) { setData(prev); toast(errMsg(e), 'err') }
  }
  // "Always ask me" means it: the app's one-by-one picks go too.
  const setLevel = (app: AutoApp, level: Level) => {
    if (level === 'custom') return
    const apps = { ...data.rules.apps }
    if (level === 'ask') delete apps[app.slug]; else apps[app.slug] = level
    const mine = new Set(app.ops.map(o => o.name))
    save({ apps, tools: level === 'ask' ? data.rules.tools.filter(t => !mine.has(t)) : data.rules.tools })
  }
  const anyRule = Object.keys(data.rules.apps).length > 0 || data.rules.tools.length > 0
  const setTool = (name: string, on: boolean) => save({ ...data.rules, tools: on ? [...data.rules.tools, name] : data.rules.tools.filter(t => t !== name) })
  const known = new Set(data.apps.flatMap(a => a.ops.map(o => o.name)))
  const orphans = data.rules.tools.filter(t => !known.has(t))

  return (
    <div className="stack loose">
      <Section title="Run without asking" actions={anyRule && <button type="button" className="btn sm" onClick={() => { if (confirm('Go back to asking before every app change?')) save({ apps: {}, tools: [] }) }}>Ask for everything</button>} sub="Pocket tools normally stop and wait for your Allow. Pick which app changes go through on their own — in every chat, on your apps only.">
        <div className="callout info mb"><Icon name="info" size={15} /><span>Every automatic change still shows in the chat as “allowed automatically” and in your app-call history. Deleting is never offered to agents.</span></div>
        {data.catalog_error && <p className="muted small" role="status">{data.catalog_error}. Your saved rules still apply.</p>}
        {data.apps.map(a => {
          const level = (data.rules.apps[a.slug] || 'ask') as Level
          const custom = level === 'ask' && a.ops.some(o => data.rules.tools.includes(o.name))
          const extra = a.ops.filter(o => data.rules.tools.includes(o.name)).length
          return (
            <div key={a.slug} className="stack">
              <SettingRow title={a.name} help={<>
                {explain(a, level, new Set(data.rules.tools))}
                {' '}<button type="button" className="link-btn small" onClick={() => setOpen(open === a.slug ? null : a.slug)}>{open === a.slug ? 'Done' : 'Choose one by one'}</button>
              </>}>
                <Select size="sm" width={300} label={`${a.name} changes`} value={custom ? 'custom' : level} searchable={false} onChange={v => setLevel(a, v)} options={[
                  ...(custom ? [{ value: 'custom' as Level, label: 'Chosen one by one', description: 'Some operations run on their own (see below)' }] : []),
                  { value: 'ask', label: 'Always ask me', description: 'Every change waits for your Allow' },
                  ...(a.adds ? [{ value: 'add' as Level, label: 'Auto-approve new items', description: 'Creating, adding and commenting run on their own; edits still ask' }] : []),
                  { value: 'all', label: 'Auto-approve everything', description: 'New items and edits run on their own' },
                ]} />
              </SettingRow>
              {open === a.slug && a.ops.map(o => {
                const covered = level === 'all' || (level === 'add' && o.kind === 'add')
                return (
                  <SettingRow key={o.name} title={<span className="small">{o.title} <span className="badge">{o.kind === 'add' ? 'new item' : 'edit'}</span></span>} help={o.description}>
                    <Switch size="sm" label={o.title} checked={covered || data.rules.tools.includes(o.name)} disabled={covered} onChange={v => setTool(o.name, v)} />
                  </SettingRow>
                )
              })}
            </div>
          )
        })}
        {!data.apps.length && !data.catalog_error && <p className="muted">No app changes are available to you.</p>}
        {orphans.length > 0 && <SettingRow title="Other saved operations" help="Allowed earlier but not offered by your apps right now.">
          <button type="button" className="btn sm" onClick={() => save({ ...data.rules, tools: data.rules.tools.filter(t => known.has(t)) })}>Forget {orphans.length}</button>
        </SettingRow>}
      </Section>
    </div>
  )
}
