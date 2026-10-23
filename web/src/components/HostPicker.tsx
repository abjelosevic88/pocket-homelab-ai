// Where an agent runs: this server or another machine reached over ssh
// (Settings → Agents → Servers). Hidden until a server is added.
import { useApp } from '../lib/app-context'
import { Select } from './Select'
import { Icon } from './icons'

export function HostPicker({ value, onChange, className = 'btn sm' }: { value: number; onChange: (id: number) => void; className?: string }) {
  const { hosts } = useApp()
  if (!hosts.length) return null
  const options = [
    { value: 0, label: 'This server', description: 'Where Pocket AI runs' },
    ...hosts.map(h => ({
      value: h.id, label: h.name,
      description: h.info.ok ? [h.info.hostname || h.target, Object.keys(h.info.versions || {}).map(p => p === 'codex' ? 'Codex' : 'Claude').join(' + ')].filter(Boolean).join(' · ') : (h.info.error || 'Not checked yet'),
    })),
  ]
  return <Select className={`${className} host-picker`} size="sm" label="Where it runs" value={hosts.some(h => h.id === value) ? value : 0} options={options} onChange={onChange} width={280}
    renderValue={o => <span className="ellipsis"><Icon name="server" size={13} className="inline-ico" />{o?.label ?? 'This server'}</span>} />
}

/** The name of a thread's machine, '' for this server. */
export function useHostName(id: number | undefined): string {
  const { hosts } = useApp()
  if (!id) return ''
  return hosts.find(h => h.id === id)?.name || 'Removed server'
}
