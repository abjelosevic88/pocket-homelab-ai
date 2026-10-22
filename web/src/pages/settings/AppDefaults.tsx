import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { api, V1 } from '../../lib/api'
import { Select } from '../../components/Select'
import { Loading } from '../../components/ui'
import { Section, useSaveSettings } from './common'

interface ModelCatalog {
  models: { id: string; name: string; description?: string }[]
  model_efforts: Record<string, string[]>
  default_effort: string
  default_model: string
  default_name: string
  claude_default_model: string
  claude_default_name: string
}

export function AppDefaultsTab() {
  const { settings } = useApp()
  const save = useSaveSettings()
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    let active = true
    setLoading(true); setError(''); setCatalog(null)
    api.get<ModelCatalog>(`${V1}/settings/app-models`)
      .then(data => { if (active) setCatalog(data) })
      .catch(e => { if (active) setError((e as Error).message) })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [settings?.claude.account_id, settings?.claude.model, revision])
  if (!settings) return <Loading />
  const value = settings.app_model || ''
  const models = catalog?.models || []
  const missing = !!value && !!catalog && !models.some(m => m.id === value)
  const options = [
    { value: '', label: `Follow Claude default${catalog ? ` · ${catalog.claude_default_name}` : ''}` },
    ...models.map(m => ({ value: m.id, label: m.name, description: m.description })),
    ...(value && !models.some(m => m.id === value) ? [{ value, label: `${value}${missing ? ' (unavailable)' : ' (saved)'}` }] : []),
  ]
  const effectiveModel = value || catalog?.claude_default_model || ''
  const efforts = catalog?.model_efforts[effectiveModel] || []
  const effort = settings.app_effort || ''
  const effortNames: Record<string, string> = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' }
  const automatic = efforts.includes('medium') ? 'Medium' : 'Model default'
  const unsupported = !!effort && !!catalog && !efforts.includes(effort)
  const effortOptions = [
    { value: '', label: `Automatic · ${automatic}` },
    ...efforts.map(level => ({ value: level, label: effortNames[level] || level })),
    ...(effort && !efforts.includes(effort) ? [{ value: effort, label: `${effortNames[effort] || effort} (${catalog ? 'unavailable for this model' : 'saved'})` }] : []),
  ]
  const current = value ? models.find(m => m.id === value)?.name || value : catalog?.claude_default_name
  return (
    <div className="stack loose">
      <Section title="Connected apps" sub="Your default for text generation in Travel, Money, Day, and Bookshelf.">
        <div className="stack">
          <div className="field">
            <span className="label">Default model for apps</span>
            <Select label="Default model for apps" value={value} options={options} disabled={loading || saving} onChange={async model => {
              setSaving(true)
              try {
                const supported = catalog?.model_efforts[model || catalog.claude_default_model] || []
                await save({ app_model: model, app_effort: !effort || supported.includes(effort) ? effort : '' }, 'App default saved')
              } finally { setSaving(false) }
            }} />
            <span className="help">Changes save automatically and apply to your next request in any app set to “Use Pocket AI default”. A model selected in an individual app takes precedence.</span>
          </div>
          <div className="field">
            <span className="label">Effort level for apps</span>
            <Select label="Effort level for apps" value={effort} options={effortOptions} disabled={loading || saving || !catalog || !efforts.length} onChange={async level => {
              setSaving(true)
              try { await save({ app_effort: level }, 'App effort saved') } finally { setSaving(false) }
            }} />
            <span className="help">Higher effort allows more reasoning and may take longer. Automatic uses Medium when supported, otherwise the model’s own default.</span>
            {!loading && catalog && !efforts.length && <span className="help">This model does not offer an effort setting.</span>}
            {unsupported && <span className="help" role="status">Your saved effort is not supported by this model; Automatic applies.</span>}
            <span className="help">Applies across connected apps, including apps with their own model choice. Unsupported levels use Automatic. Changing the default model resets an unsupported effort to Automatic.</span>
          </div>
          {loading && <p className="muted small" role="status">Loading models from your Claude account…</p>}
          {saving && <p className="muted small" role="status">Saving…</p>}
          {missing && <p className="muted small" role="status">Your saved model is no longer offered by this account. Choose another model or follow the Claude default.</p>}
          {error && <div className="row wrap"><span className="muted small" role="status">{error}</span><button type="button" className="btn sm" onClick={() => setRevision(v => v + 1)}>Retry models</button></div>}
          {!loading && !error && current && <p className="muted small" role="status">Current app default: <strong>{current}</strong>.</p>}
          <p className="muted small">Uses your Claude account selected under <Link to="/settings/agents">Agents</Link>. “Follow Claude default” follows the model there; when no specific model is selected, connected apps use Sonnet.</p>
        </div>
      </Section>
      <Section title="Your model preferences" sub="These settings belong to your account.">
        <p className="muted small">Set defaults for new Claude and Codex chats under <Link to="/settings/agents">Agents</Link>. App defaults control text generation requested by connected apps. Existing chats keep their own model.</p>
      </Section>
    </div>
  )
}
