// Settings → Machines: where agents run and as whom. This server first (its
// CLIs and logins), then a card per other machine with its own logins, laid
// out the same way, and a last card to add a machine.
import { AccountsSection } from './Accounts'
import { HostsSection } from './Hosts'

export function MachinesTab() {
  return (
    <div className="stack loose">
      <AccountsSection />
      <HostsSection />
    </div>
  )
}
