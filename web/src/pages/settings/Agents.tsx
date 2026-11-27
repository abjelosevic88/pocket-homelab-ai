import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { isAcp } from '../../lib/types'
import { providerName } from '../../lib/format'
import { AgentPicker, offeredProviders, ProviderToggle, settingsFor } from '../../components/AgentPicker'
import { AcpAgentsSection } from './AcpAgents'
import { FolderButton } from '../../components/FolderPicker'
import { Field, Loading, SettingRow, noGrammarly } from '../../components/ui'
import { Section, useSaveSettings } from './common'

export function AgentsTab() {
  const { settings, providers, user } = useApp()
  const save = useSaveSettings()
  const [prompt, setPrompt] = useState(settings?.system_prompt || '')
  useEffect(() => { setPrompt(settings?.system_prompt || '') }, [settings?.system_prompt])
  if (!settings) return <Loading />
  const s = settings

  return (
    <div className="stack loose">
      <Section title="New threads start with">
        <SettingRow title="Agent">
          <ProviderToggle value={s.default_provider} onChange={v => v && save({ default_provider: v })} />
        </SettingRow>
        <SettingRow title="Folder" help="Where agents start when you don’t pick another.">
          <FolderButton value={s.default_cwd} onChange={v => save({ default_cwd: v }, 'Default folder saved')} className="btn" />
        </SettingRow>
      </Section>

      {offeredProviders(providers, s.default_provider).map(pid => {
        const p = providers.find(x => x.id === pid)
        const saved = settingsFor(s, pid) || { model: '', effort: '', mode: '' }
        // an ACP agent shows its own model and mode until the person picks others
        const cur = isAcp(pid) ? { ...saved, model: saved.model || p?.default_model || '', mode: saved.mode || p?.default_mode || '' } : saved
        const put = (patch: object) => isAcp(pid)
          ? save({ agents: { ...(s.agents || {}), [pid]: { ...cur, ...patch } } }, 'Saved')
          : save({ [pid]: { ...cur, ...patch } }, 'Saved')
        return (
          <Section key={pid} title={<span className="row"><span className={`prov-dot prov-${pid}`} />{providerName(pid)}</span>}
            sub={p && !p.available ? <span className="danger-text">{p.error || 'Not installed on this server'}</span> : isAcp(pid) ? 'Model and permissions new threads start with.' : 'Model, effort, permissions and speed new threads start with.'}>
            {p?.available ? (
              <AgentPicker showProvider={false} noAccount={isAcp(pid)} value={{ provider: pid, ...cur }} onChange={put} />
            ) : <p className="muted small" style={{ margin: 0 }}>{!p ? 'Checking…' : isAcp(pid) ? 'Set it up under More agents below.' : <>Install the CLI on this server as the user Pocket AI runs as, then check it again under <Link to="/settings/machines">Machines</Link>.</>}</p>}
          </Section>
        )
      })}

      {user?.role === 'admin' && <AcpAgentsSection />}

      <Section title="Extra instructions" sub="Added to the system prompt of every agent you start (every agent). Good for things like “Answer briefly. This is my home server; ask before restarting containers.”">
        <Field label="Instructions">
          <textarea {...noGrammarly} value={prompt} onChange={e => setPrompt(e.target.value)} rows={5} placeholder="Answer briefly…" />
        </Field>
        <div className="row mt-s"><button type="button" className="btn primary" disabled={prompt === (s.system_prompt || '')} onClick={() => save({ system_prompt: prompt }, 'Instructions saved')}>Save</button></div>
      </Section>
    </div>
  )
}
