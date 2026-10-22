// Share a thread with other people on this Pocket AI: per person, no access,
// read along, or chat (send messages and answer the agent's requests).
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { SharePerson, Thread, ThreadShare } from '../lib/types'
import { Icon } from './icons'
import { Avatar, errMsg, Loading, Modal, Segmented, useToast } from './ui'

type Role = '' | 'view' | 'chat'

export function ShareDialog({ thread, onClose, onSaved }: { thread: Thread; onClose: () => void; onSaved: (t: Thread) => void }) {
  const { toast } = useToast()
  const [people, setPeople] = useState<SharePerson[] | null>(null)
  const [roles, setRoles] = useState<Record<number, Role>>({})
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.get<{ shares: ThreadShare[]; people: SharePerson[] }>(`${V1}/threads/${thread.id}/shares`).then(r => {
      setPeople(r.people || [])
      setRoles(Object.fromEntries((r.shares || []).map(s => [s.user_id, s.role])))
    }).catch(e => { toast(errMsg(e), 'err'); onClose() })
  }, [thread.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setBusy(true)
    try {
      const shares = Object.entries(roles).filter(([, r]) => r).map(([id, role]) => ({ user_id: Number(id), role }))
      const t = await api.put<Thread>(`${V1}/threads/${thread.id}/shares`, { shares })
      onSaved(t)
      toast(shares.length ? 'Sharing saved' : 'Private again')
      onClose()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const count = Object.values(roles).filter(Boolean).length
  const ask = thread.source === 'ask'

  return (
    <Modal title="Share this chat" onClose={onClose} size="sm" footer={<>
      <span className="muted small grow">{count ? `Shared with ${count === 1 ? '1 person' : `${count} people`}` : 'Private — only you'}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={save} disabled={busy || people === null}>Save</button>
    </>}>
      {ask ? (
        <p className="muted small share-intro">
          This chat reads your apps as you, so others can only <b>view</b> it: they read the conversation live, but can’t
          send anything. The raw app results under “Sources & app activity” stay yours.
        </p>
      ) : (
        <p className="muted small share-intro">
          The agent keeps working on your login, in your folder. <b>Can view</b> reads along live. <b>Can chat</b> also sends
          messages and answers what the agent asks — so it may change things on this server.
        </p>
      )}
      {people === null ? <Loading /> : people.length === 0 ? (
        <div className="muted small">Nobody else uses this Pocket AI yet. An admin adds people under Settings → Users.</div>
      ) : (
        <div className="share-people">
          {people.map(p => (
            <div key={p.id} className="share-person">
              <Avatar name={p.name} email={p.email} size={28} />
              <span className="grow share-person-name">
                <span className="ellipsis">{p.name}</span>
                {!p.can_run && !ask && <span className="muted xsmall">May not run agents — view only</span>}
              </span>
              <Segmented size="sm" label={`Access for ${p.name}`} value={roles[p.id] || ''} onChange={v => setRoles(r => ({ ...r, [p.id]: v }))}
                options={[
                  { value: '' as Role, label: <Icon name="lock" size={13} />, title: 'No access' },
                  { value: 'view' as Role, label: 'View', title: 'Can view' },
                  ...(p.can_run && !ask ? [{ value: 'chat' as Role, label: 'Chat', title: 'Can chat' }] : []),
                ]} />
            </div>
          ))}
        </div>
      )}
    </Modal>
  )
}

/** The header chip: Private, Shared with …, or Shared by … */
export function PrivacyBadge({ thread, access, onShare }: { thread: Thread; access: 'owner' | 'chat' | 'view'; onShare?: () => void }) {
  const n = thread.shares?.length || 0
  if (access !== 'owner') {
    return <span className="privacy-badge shared" title={`${thread.owner_name || 'Someone'} shared this chat with you`}>
      <Icon name="users" size={11} />Shared by {thread.owner_name || 'someone'} · {access === 'chat' ? 'can chat' : 'view only'}
    </span>
  }
  const label = n ? <>Shared · {n === 1 ? thread.shares![0].name : `${n} people`}</> : <>Private</>
  const title = n ? `Shared with ${thread.shares!.map(s => `${s.name} (${s.role === 'chat' ? 'can chat' : 'can view'})`).join(', ')} — click to change`
    : 'Only you can see this chat — click to share it'
  return <button type="button" className={`privacy-badge ${n ? 'shared' : 'private'}`} title={title} onClick={onShare} disabled={!onShare}>
    <Icon name={n ? 'users' : 'lock'} size={11} />{label}
  </button>
}
