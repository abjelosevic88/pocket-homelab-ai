// Every shared chat in one place: the ones others shared with you, and the
// ones you shared (with who and how, and a button to change it).
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { stamp, threadTitle } from '../lib/format'
import { accessOf } from '../lib/share'
import type { Thread } from '../lib/types'
import { ProviderBadge, ThreadStatus } from '../components/bits'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { ShareDialog } from '../components/ShareDialog'
import { Empty, Loading } from '../components/ui'

export default function SharedPage({ onMenu }: { onMenu?: () => void }) {
  const { threads, user, upsertThread } = useApp()
  const navigate = useNavigate()
  const [editing, setEditing] = useState<Thread | null>(null)
  const { withMe, byMe } = useMemo(() => {
    const list = threads || []
    return {
      withMe: list.filter(t => t.owner_id && t.owner_id !== user?.id),
      byMe: list.filter(t => (!t.owner_id || t.owner_id === user?.id) && t.shares?.length),
    }
  }, [threads, user])

  const row = (t: Thread, mine: boolean) => {
    const access = accessOf(t, user)
    return (
      <div key={t.id} role="link" tabIndex={0} className="srv-row shared-row" onClick={() => navigate(`/t/${t.id}`)}
        onKeyDown={e => { if (e.key === 'Enter') navigate(`/t/${t.id}`) }}>
        <span className={`srv-ico ${t.provider}`}><Icon name={t.provider} size={17} /></span>
        <span className="srv-main">
          <span className="srv-title">{threadTitle(t)}</span>
          <span className="srv-meta">
            <ProviderBadge provider={t.provider} size="xs" />
            {t.source === 'ask' && <span className="shared-kind"><Icon name="sparkles" size={11} />Ask</span>}
            {mine ? (
              <span className="shared-people">{t.shares!.map(s => (
                <span key={s.user_id} className={`shared-person ${s.role}`} title={s.role === 'chat' ? 'Can chat' : 'Can view'}>
                  <Icon name={s.role === 'chat' ? 'message' : 'eye'} size={11} />{s.name}
                </span>
              ))}</span>
            ) : (
              <span className="shared-from">From <b>{t.owner_name || 'someone'}</b> · {access === 'chat' ? 'you can chat' : 'view only'}</span>
            )}
          </span>
        </span>
        <span className="srv-side">
          <span className="srv-time tnum">{stamp(t.activity_at)}</span>
          {mine ? <button type="button" className="btn sm" onClick={e => { e.stopPropagation(); setEditing(t) }}><Icon name="users" size={13} />Change</button>
            : <ThreadStatus t={{ ...t, unread: false }} />}
        </span>
      </div>
    )
  }

  return (
    <div className="page">
      <PageHead title="Shared" icon="users" onMenu={onMenu} sub="Chats other people shared with you, and the ones you shared. Everything else is private to its owner." />
      <div className="page-scroll">
        <div className="page-inner srv">
          {threads === null ? <Loading /> : !withMe.length && !byMe.length ? (
            <Empty title="Nothing is shared" icon="users">Open one of your chats and click its <b>Private</b> badge to share it. Chats others share with you show up here too.</Empty>
          ) : (
            <>
              <section className="srv-group">
                <h3 className="srv-h">Shared with me<span className="muted">{withMe.length}</span></h3>
                {withMe.length ? <div className="srv-list">{withMe.map(t => row(t, false))}</div>
                  : <div className="muted small shared-none">Nobody has shared a chat with you.</div>}
              </section>
              <section className="srv-group">
                <h3 className="srv-h">Shared by me<span className="muted">{byMe.length}</span></h3>
                {byMe.length ? <div className="srv-list">{byMe.map(t => row(t, true))}</div>
                  : <div className="muted small shared-none">You haven’t shared any chat. Click a chat’s <b>Private</b> badge to share it.</div>}
              </section>
            </>
          )}
        </div>
      </div>
      {editing && <ShareDialog thread={editing} onClose={() => setEditing(null)} onSaved={t => { upsertThread(t); setEditing(null) }} />}
    </div>
  )
}
