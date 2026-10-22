import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { User } from '../../lib/types'
import { Icon } from '../../components/icons'
import { Avatar, Confirm, Empty, errMsg, Field, Loading, MenuButton, Modal, Segmented, Switch, useAsync, useToast } from '../../components/ui'
import { Section } from './common'

export function PeopleTab() {
  const { user, status } = useApp()
  const { toast } = useToast()
  const list = useAsync(() => api.get<User[]>(`${V1}/users`), [])
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  if (user?.role !== 'admin') return <Empty title="Only admins manage people" icon="users">Ask an admin on this box to add or change accounts.</Empty>

  return (
    <div className="stack loose">
      <Section title="Accounts on this box" sub="Everyone has their own threads, recipes and schedules; admins never see anyone else’s. Agents run as the server’s own user, so only admins and people you allow may start them."
        actions={<button type="button" className="btn primary sm" onClick={() => setEdit({})}><Icon name="plus" size={14} />Add person</button>}>
        {!list.data ? <Loading /> : (
          <div className="set-people">
            {list.data.map(u => (
              <div key={u.id} className="set-person">
                <Avatar name={u.name} email={u.email} size={34} />
                <div className="grow">
                  <div className="set-person-name">{u.name || u.email}{u.id === user.id && <span className="muted small"> (you)</span>}{u.role === 'admin' ? <span className="badge accent">Admin</span> : u.can_run ? <span className="badge ok">May run agents</span> : <span className="badge">View only</span>}</div>
                  <div className="muted small ellipsis">{u.email}</div>
                </div>
                <MenuButton label={`More for ${u.name || u.email}`} align="right" items={[
                  { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(u) },
                  'sep',
                  { label: 'Remove…', icon: 'trash', danger: true, disabled: u.id === user.id, onSelect: () => setDel(u) },
                ]}><Icon name="more" size={16} /></MenuButton>
              </div>
            ))}
          </div>
        )}
        {status?.auth_mode !== 'local' && <p className="muted small mt">Sign-in mode is <code>{status?.auth_mode}</code>: accounts are also created automatically the first time someone signs in.</p>}
      </Section>
      {edit && <PersonDialog person={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); list.reload() }} />}
      {del && <Confirm title={`Remove ${del.name || del.email}?`} message="Their account, threads, recipes and schedules are deleted."
        onCancel={() => setDel(null)} onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/users/${d.id}`); toast('Removed'); list.reload() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function PersonDialog({ person, onClose, onSaved }: { person: Partial<User>; onClose: () => void; onSaved: () => void }) {
  const { status } = useApp()
  const { toast } = useToast()
  const isNew = !person.id
  const [f, setF] = useState({ email: person.email || '', name: person.name || '', password: '', role: (person.role || 'member') as 'admin' | 'member', can_run: !!person.can_run })
  const [busy, setBusy] = useState(false)
  const needPw = isNew && status?.auth_mode === 'local'
  const save = async () => {
    setBusy(true)
    try {
      const body = { ...f, password: f.password || undefined }
      if (isNew) await api.post(`${V1}/users`, body); else await api.put(`${V1}/users/${person.id}`, body)
      toast(isNew ? 'Person added' : 'Saved')
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={isNew ? 'Add a person' : `Edit ${person.name || person.email}`} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !f.email.trim() || (needPw && f.password.length < 8)} onClick={save}>{isNew ? 'Add' : 'Save'}</button>
    </>}>
      <div className="stack">
        <div className="two-col">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus /></Field>
          <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
        </div>
        <Field label={isNew ? 'Password' : 'New password'} help={isNew ? (needPw ? 'At least 8 characters.' : 'Optional — they can sign in through the configured sign-in.') : 'Leave empty to keep the current one.'}>
          <input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} autoComplete="new-password" />
        </Field>
        <Field label="Role" group help="Admins add people, change box-wide settings and may always run agents.">
          <Segmented value={f.role} onChange={r => setF({ ...f, role: r })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} label="Role" />
        </Field>
        {f.role === 'member' && (
          <label className="inline-switch top">
            <Switch checked={f.can_run} onChange={v => setF({ ...f, can_run: v })} label="May run agents" />
            <span className="grow"><b>May run agents</b><br /><span className="muted small">Agents run as this server’s user account, with its files and logins — only for people you trust with a shell.</span></span>
          </label>
        )}
      </div>
    </Modal>
  )
}
