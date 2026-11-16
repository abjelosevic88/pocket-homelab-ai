// Settings → Machines: where agents run and as whom. A summary strip, then
// This server (its CLIs and logins), a card per other machine laid out the
// same way, and a last card to add a machine.
import { useApp } from '../../lib/app-context'
import { AccountsSection } from './Accounts'
import { HostsSection, useHostLogins } from './Hosts'
import { Stat } from './MachineParts'

export function MachinesTab() {
  const { hosts, accounts, providers } = useApp()
  const logins = useHostLogins(hosts)
  const hereOk = providers.some(p => p.available)
  const online = (hereOk ? 1 : 0) + hosts.filter(h => h.info.ok).length
  const total = 1 + hosts.length
  const remote = hosts.flatMap(h => (logins.byHost[h.id] || []).filter(l => !l.missing && !(l.error && !h.info.ok)))
  const all = [...accounts.map(a => ({ signed: a.signed_in, name: a.label, where: 'this server' })), ...remote.map(l => ({ signed: l.signed_in, name: l.label || 'default login', where: l.host }))]
  const signed = all.filter(x => x.signed).length
  const asked = hosts.every(h => logins.byHost[h.id])
  const down = hosts.filter(h => h.info.checked_at && !h.info.ok)
  const out = all.filter(x => !x.signed)
  const attention = down.length + out.length
  const why = [...down.map(h => `${h.name} unreachable`), ...out.map(x => `${x.name} on ${x.where} not signed in`)]
  return (
    <div className="machines">
      <div className="mstats">
        <Stat icon="server" value={<>{online}<small>/ {total}</small></>} label="Machines online" tone={online === total ? 'ok' : 'warn'} sub={total === 1 ? 'only this server' : `${hosts.length} besides this server`} />
        <Stat icon="key" value={<>{signed}<small>/ {all.length}{asked ? '' : '…'}</small></>} label="Logins signed in" sub={`${all.filter(x => x.where === 'this server').length} here · ${remote.length} elsewhere`} />
        <Stat icon={attention ? 'alert' : 'check-circle'} value={attention || 'All good'} label={attention ? 'Need a look' : 'Nothing to fix'} tone={attention ? 'error' : 'ok'} sub={why[0] ? why.slice(0, 2).join(' · ') + (why.length > 2 ? ` · +${why.length - 2}` : '') : undefined} />
      </div>
      <AccountsSection />
      <HostsSection logins={logins} />
    </div>
  )
}
