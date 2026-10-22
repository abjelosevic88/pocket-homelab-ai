import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { api, V1 } from '../../lib/api'
import { Select } from '../../components/Select'
import { Loading } from '../../components/ui'
import { Section, useSaveSettings } from './common'

interface ModelCatalog {
  models: { id: string; name: string; description?: string }[]
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
  const current = value ? models.find(m => m.id === value)?.name || value : catalog?.claude_default_name
  return (
    <div className="stack loose">
      <Section title="Connected apps" sub="Your default for text generation in Travel, Money, Day, and Bookshelf.">
        <div className="stack">
          <div className="field">
            <span className="label">Default model for apps</span>
            <Select label="Default model for apps" value={value} options={options} disabled={loading || saving} onChange={async model => {
              setSaving(true)
              try { await save({ app_model: model }, 'App default saved') } finally { setSaving(false) }
            }} />
            <span className="help">Changes save automatically and apply to your next request in any app set to “Use Pocket AI default”. A model selected in an individual app takes precedence.</span>
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
