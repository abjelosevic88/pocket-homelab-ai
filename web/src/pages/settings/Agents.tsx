import { useEffect, useState } from 'react'
import { useApp } from '../../lib/app-context'
import type { ProviderId } from '../../lib/types'
import { AgentPicker, ProviderToggle } from '../../components/AgentPicker'
import { FolderButton } from '../../components/FolderPicker'
import { Icon } from '../../components/icons'
import { Field, Loading, SettingRow, Spinner } from '../../components/ui'
import { Section, StatusDot, useSaveSettings } from './common'

export function AgentsTab() {
  const { settings, providers, reloadProviders } = useApp()
  const save = useSaveSettings()
  const [prompt, setPrompt] = useState(settings?.system_prompt || '')
  const [checking, setChecking] = useState(false)
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

      {(['claude', 'codex'] as ProviderId[]).map(pid => {
        const p = providers.find(x => x.id === pid)
        const cur = s[pid]
        return (
          <Section key={pid} title={<span className="row"><span className={`prov-dot prov-${pid}`} />{pid === 'claude' ? 'Claude Code' : 'Codex'}</span>}
            sub={p ? (p.available ? <>Version {p.version}{p.account ? <> · {p.account}</> : null}</> : <span className="danger-text">{p.error || 'Not installed on the server'}</span>) : 'Checking…'}
            actions={<StatusDot status={!p ? 'off' : p.available ? 'ok' : 'error'} />}>
            {p?.available ? (
              <AgentPicker showProvider={false} value={{ provider: pid, ...cur }} onChange={patch => save({ [pid]: { ...cur, ...patch } }, 'Saved')} />
            ) : <p className="muted small" style={{ margin: 0 }}>Install and sign in to the CLI on the server as the user Pocket AI runs as, then check again.</p>}
          </Section>
        )
      })}
      <div className="row"><button type="button" className="btn" disabled={checking} onClick={async () => { setChecking(true); await reloadProviders().finally(() => setChecking(false)) }}>{checking ? <Spinner /> : <Icon name="refresh" size={14} />}Check the CLIs again</button></div>

      <Section title="Extra instructions" sub="Added to the system prompt of every agent you start (both CLIs). Good for things like “Answer briefly. This is my home server; ask before restarting containers.”">
        <Field label="Instructions">
          <textarea value={prompt} onChange={e => setPrompt(e.target.value)} rows={5} placeholder="Answer briefly…" />
        </Field>
        <div className="row mt-s"><button type="button" className="btn primary" disabled={prompt === (s.system_prompt || '')} onClick={() => save({ system_prompt: prompt }, 'Instructions saved')}>Save</button></div>
      </Section>
    </div>
  )
}
