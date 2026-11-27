// Settings → Agents: what new conversations start with, then every agent once —
// "Your agents" (ready: Claude Code, Codex and the ACP agents set up) and "Add more
// agents" (the ACP agents not set up yet). A row shows the agent's state and defaults;
// opening it shows its model / effort / permissions and, for ACP agents, sign-in and keys.
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { ACP_IDS, type AcpAgent, type Provider, type ProviderId, hasLogins, isAcp } from '../../lib/types'
import { planName, providerName } from '../../lib/format'
import { AgentPicker, effortLabel, modeName, modelName, ProviderToggle, settingsFor } from '../../components/AgentPicker'
import { AcpInstallButton, acpNeeds, AcpSetup, type AcpChanged, useAcpAgents } from './AcpAgents'
import { FolderButton } from '../../components/FolderPicker'
import { Icon } from '../../components/icons'
import { Field, Loading, SettingRow, Spinner, noGrammarly } from '../../components/ui'
import { Section, StatusDot, useSaveSettings } from './common'

export function AgentsTab() {
  const { settings, providers, user } = useApp()
  const save = useSaveSettings()
  const isAdmin = user?.role === 'admin'
  const acp = useAcpAgents(isAdmin)
  const [prompt, setPrompt] = useState(settings?.system_prompt || '')
  const [open, setOpen] = useState<ProviderId | ''>('')
  useEffect(() => { setPrompt(settings?.system_prompt || '') }, [settings?.system_prompt])
  if (!settings) return <Loading />
  const s = settings

  // every agent once: ready ones under "Your agents", the rest under "Add more agents"
  const ready = (id: ProviderId) => !!providers.find(p => p.id === id)?.available
  const mine: ProviderId[] = ['claude', 'codex', ...ACP_IDS.filter(id => ready(id))]
  const more = ACP_IDS.filter(id => !ready(id))
  const toggle = (id: ProviderId) => setOpen(o => o === id ? '' : id)

  return (
    <div className="stack loose">
      <Section title="New conversations">
        <SettingRow title="Agent" help="Which agent a new conversation starts on. You can switch in the model menu.">
          <ProviderToggle value={s.default_provider} onChange={v => v && save({ default_provider: v })} />
        </SettingRow>
        <SettingRow title="Folder" help="Where agents start when you don’t pick another.">
          <FolderButton value={s.default_cwd} onChange={v => save({ default_cwd: v }, 'Default folder saved')} className="btn" />
        </SettingRow>
      </Section>

      <Section title="Your agents" sub="Ready to use. Open one for the model, effort and permissions it starts with."
        actions={isAdmin ? <button type="button" className="btn sm ghost" onClick={acp.refresh} disabled={acp.refreshing} title="Ask every agent again">{acp.refreshing ? <Spinner /> : <Icon name="refresh" size={14} />}Check again</button> : undefined}>
        <div className="agent-list">
          {mine.map(id => <AgentRow key={id} id={id} p={providers.find(p => p.id === id)} a={acp.list?.find(x => x.id === id)} isDefault={s.default_provider === id}
            open={open === id} onToggle={() => toggle(id)} onChanged={acp.changed} />)}
        </div>
      </Section>

      {isAdmin && more.length > 0 && (
        <Section title="Add more agents" sub="Other coding agents for this server: Google's Gemini on your Google AI plan, and open or pay-per-use models through API keys. Once set up, an agent moves up to Your agents and shows in the model menu.">
          {acp.error && <div className="callout danger"><Icon name="alert" size={15} /><span>{acp.error}</span></div>}
          {!acp.list ? <div className="loading"><Spinner /> Asking the agents…</div> : (
            <div className="agent-list">
              {more.map(id => <AgentRow key={id} id={id} p={providers.find(p => p.id === id)} a={acp.list?.find(x => x.id === id)} isDefault={false}
                open={open === id} onToggle={() => toggle(id)} onChanged={acp.changed} />)}
            </div>
          )}
        </Section>
      )}

      <Section title="Extra instructions" sub="Added to the system prompt of every agent you start. Good for things like “Answer briefly. This is my home server; ask before restarting containers.”">
        <Field label="Instructions">
          <textarea {...noGrammarly} value={prompt} onChange={e => setPrompt(e.target.value)} rows={5} placeholder="Answer briefly…" />
        </Field>
        <div className="row mt-s"><button type="button" className="btn primary" disabled={prompt === (s.system_prompt || '')} onClick={() => save({ system_prompt: prompt }, 'Instructions saved')}>Save</button></div>
      </Section>
    </div>
  )
}

/** One agent: a line with its state and defaults; open, its settings. */
function AgentRow({ id, p, a, isDefault, open, onToggle, onChanged }: {
  id: ProviderId; p?: Provider; a?: AcpAgent; isDefault: boolean; open: boolean; onToggle: () => void; onChanged: AcpChanged
}) {
  const { settings } = useApp()
  const save = useSaveSettings()
  const s = settings!
  const ok = !!p?.available
  const saved = settingsFor(s, id) || { model: '', effort: '', mode: '' }
  // an agent shows its own model and mode until the person picks others
  const cur = { ...saved, model: saved.model || p?.default_model || '', mode: saved.mode || p?.default_mode || '', effort: saved.effort || p?.default_effort || '' }
  const put = (patch: object) => isAcp(id)
    ? save({ agents: { ...(s.agents || {}), [id]: { ...cur, ...patch } } }, 'Saved')
    : save({ [id]: { ...cur, ...patch } }, 'Saved')

  // the second line: what it runs with when ready, what it needs when not
  const needs = a ? acpNeeds(a) : ok ? '' : (p?.error || 'Not installed on this server')
  const summary = ok
    ? [modelName(p, cur.model), cur.effort && (p?.efforts?.length ? effortLabel(cur.effort) : ''), cur.mode && modeName(p, cur.mode)].filter(Boolean).join(' · ')
    : a && !a.installed ? a.note : needs
  const meta = ok ? [p?.version, p?.account && (isAcp(id) ? p.account : planName(p.account.split(' · ')[0]) || p.account)].filter(Boolean).join(' · ') : ''

  return (
    <div className={`agent-row ${open ? 'open' : ''}`}>
      <div className="agent-head" role="button" tabIndex={0} aria-expanded={open} onClick={onToggle} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggle() } }}>
        <Icon name={id} size={18} className={`prov-glyph ${id}`} />
        <div className="grow agent-text">
          <div className="row agent-name">
            <b>{providerName(id)}</b>
            {isDefault && <span className="agent-badge">Default</span>}
            {meta && <span className="muted xsmall ellipsis">{meta}</span>}
          </div>
          <div className="muted small agent-sum">
            {!ok && <StatusDot status={a && !a.installed ? 'off' : 'warn'} />}
            <span className={ok ? 'ellipsis' : ''}>{summary}</span>
          </div>
        </div>
        {a && !a.installed && <AcpInstallButton a={a} onChanged={onChanged} primary />}
        {a && a.installed && !ok && <span className="btn sm primary agent-cta" aria-hidden>Set up</span>}
        <Icon name={open ? 'chevron-up' : 'chevron-down'} size={16} className="muted agent-chev" />
      </div>
      {open && (
        <div className="agent-body">
          {ok ? <AgentPicker showProvider={false} noAccount={!hasLogins(id)} value={{ provider: id, ...cur }} onChange={put} />
            : !isAcp(id) && <p className="muted small" style={{ margin: 0 }}>Install the CLI on this server as the user Pocket AI runs as, then check it again under <Link to="/settings/machines">Machines</Link>.</p>}
          {!isAcp(id) && ok && <p className="muted xsmall" style={{ margin: 0 }}>Logins and plan limits: <Link to="/settings/machines">Machines</Link>.</p>}
          {isAcp(id) && hasLogins(id) && a?.installed && <p className="muted xsmall" style={{ margin: 0 }}>More logins, and this agent on your other machines: <Link to="/settings/machines">Machines</Link>.</p>}
          {a && <>
            {a.note && a.installed && <p className="muted xsmall" style={{ margin: 0 }}>{a.note} <a href={a.site} target="_blank" rel="noopener noreferrer">More</a></p>}
            <AcpSetup a={a} onChanged={onChanged} />
            {a.installed && !a.self_updates && <div className="row"><AcpInstallButton a={a} onChanged={onChanged} /></div>}
          </>}
        </div>
      )}
    </div>
  )
}
