// Settings → Machines: where agents run and as whom, one machine at a time.
// A row of machines on top (status and signed-in logins at a glance) picks
// which one is shown below: This server (its CLIs and logins) or another
// machine, laid out the same way; the last one adds a machine. ?m= keeps the
// choice (0 = this server, a machine's id, new).
import { useSearchParams } from 'react-router-dom'
import { useApp } from '../../lib/app-context'
import { Icon } from '../../components/icons'
import { AccountsSection } from './Accounts'
import { HostsSection, useHostLogins } from './Hosts'
import { osIcon, type Health } from './MachineParts'

export function MachinesTab() {
  const { user, hosts, accounts, providers } = useApp()
  const logins = useHostLogins(hosts)
  const [params, setParams] = useSearchParams()
  const raw = params.get('m')
  const sel: number | 'new' = raw === 'new' ? 'new' : raw && hosts.some(h => String(h.id) === raw) ? Number(raw) : 0
  const pick = (m: number | 'new') => setParams(p => { const n = new URLSearchParams(p); if (m === 0) n.delete('m'); else n.set('m', String(m)); return n }, { replace: true })
  const mayAdd = user?.role === 'admin' || !!user?.can_run

  const hereHealth: Health = !providers.length ? 'off' : providers.every(p => p.available) ? 'ok' : providers.some(p => p.available) ? 'warn' : 'error'
  const tabs: { id: number; name: string; icon: string; health: Health; sub: string }[] = [
    { id: 0, name: 'This server', icon: osIcon('', true), health: hereHealth, sub: count(accounts.filter(a => a.signed_in).length, accounts.length) },
    ...hosts.map(h => {
      const list = (logins.byHost[h.id] || []).filter(l => !l.missing)
      const health: Health = h.info.ok ? (Object.keys(h.info.missing || {}).length ? 'warn' : 'ok') : h.info.checked_at ? 'error' : 'off'
      return { id: h.id, name: h.name, icon: osIcon(h.info.os), health,
        sub: health === 'error' ? 'Unreachable' : !logins.byHost[h.id] ? 'Asking…' : count(list.filter(l => l.signed_in).length, list.length) }
    }),
  ]

  return (
    <div className="machines">
      <div className="mpick" role="tablist" aria-label="Machines">
        {tabs.map(t => (
          <button key={t.id} type="button" role="tab" aria-selected={sel === t.id} className={`mpick-t h-${t.health} ${sel === t.id ? 'on' : ''}`} onClick={() => pick(t.id)}>
            <span className="mpick-ico"><Icon name={t.icon} size={17} /><span className="mpick-dot" /></span>
            <span className="mpick-main"><span className="mpick-name ellipsis">{t.name}</span><span className="mpick-sub">{t.sub}</span></span>
          </button>
        ))}
        {mayAdd && <button type="button" role="tab" aria-selected={sel === 'new'} className={`mpick-t mpick-add ${sel === 'new' ? 'on' : ''}`} onClick={() => pick('new')}>
          <span className="mpick-ico"><Icon name="plus" size={17} /></span>
          <span className="mpick-main"><span className="mpick-name">Add machine</span><span className="mpick-sub">over ssh</span></span>
        </button>}
      </div>
      {sel === 0 ? <AccountsSection /> : <HostsSection logins={logins} only={sel} onAdded={pick} onRemoved={() => pick(0)} />}
    </div>
  )
}

/** "2 of 3 signed in", "1 signed in", "No logins" */
function count(signed: number, total: number) {
  if (!total) return 'No logins'
  return signed === total ? `${total} signed in` : `${signed} of ${total} signed in`
}
