import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { Icon } from '../../components/icons'
import { errMsg, Loading, Segmented, SettingRow, Switch, useToast } from '../../components/ui'
import { Section } from './common'

export interface AutoRules { apps: Record<string, 'add' | 'all'>; tools: string[] }
interface AutoOp { name: string; title: string; kind: 'add' | 'change'; description: string }
interface AutoApp { slug: string; name: string; ops: AutoOp[]; adds: number; total: number }
interface AutoData { rules: AutoRules; apps: AutoApp[]; catalog_error?: string }

type Level = 'ask' | 'add' | 'all'

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
  const setLevel = (app: string, level: Level) => {
    const apps = { ...data.rules.apps }
    if (level === 'ask') delete apps[app]; else apps[app] = level
    save({ ...data.rules, apps })
  }
  const setTool = (name: string, on: boolean) => save({ ...data.rules, tools: on ? [...data.rules.tools, name] : data.rules.tools.filter(t => t !== name) })
  const known = new Set(data.apps.flatMap(a => a.ops.map(o => o.name)))
  const orphans = data.rules.tools.filter(t => !known.has(t))

  return (
    <div className="stack loose">
      <Section title="Run without asking" sub="Pocket tools normally stop and wait for your Allow. Pick which app changes go through on their own — in every chat, on your apps only.">
        <div className="callout info mb"><Icon name="info" size={15} /><span>Every automatic change still shows in the chat as “allowed automatically” and in your app-call history. Deleting is never offered to agents.</span></div>
        {data.catalog_error && <p className="muted small" role="status">{data.catalog_error}. Your saved rules still apply.</p>}
        {data.apps.map(a => {
          const level: Level = data.rules.apps[a.slug] || 'ask'
          const extra = a.ops.filter(o => data.rules.tools.includes(o.name)).length
          return (
            <div key={a.slug} className="stack">
              <SettingRow title={a.name} help={<>
                {a.adds} that add, {a.total - a.adds} that change{extra > 0 && level !== 'all' ? ` · ${extra} picked one by one` : ''}
                {' · '}<button type="button" className="link-btn small" onClick={() => setOpen(open === a.slug ? null : a.slug)}>{open === a.slug ? 'Hide operations' : 'Show operations'}</button>
              </>}>
                <Segmented size="sm" label={`${a.name} changes`} value={level} onChange={v => setLevel(a.slug, v)} options={[
                  { value: 'ask', label: 'Ask' },
                  ...(a.adds ? [{ value: 'add' as Level, label: 'Adds', title: 'New things run automatically; edits still ask' }] : []),
                  { value: 'all', label: 'All', title: 'Every change runs automatically' },
                ]} />
              </SettingRow>
              {open === a.slug && a.ops.map(o => {
                const covered = level === 'all' || (level === 'add' && o.kind === 'add')
                return (
                  <SettingRow key={o.name} title={<span className="small">{o.title} <span className="badge">{o.kind === 'add' ? 'adds' : 'changes'}</span></span>} help={o.description}>
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
