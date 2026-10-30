// What you can do with a thread from a list (sidebar, tabs, Shared): the
// right-click menu, plus the rename / share / delete dialogs it opens.
import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { threadTitle } from '../lib/format'
import { accessOf } from '../lib/share'
import type { Thread } from '../lib/types'
import { ShareDialog } from './ShareDialog'
import { Confirm, copyText, download, errMsg, type MenuItem, Modal, useToast } from './ui'

type Patch = Partial<Pick<Thread, 'title' | 'pinned' | 'archived'>>

export function useThreadMenu() {
  const { user, upsertThread, dropThread } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const loc = useLocation()
  const [renaming, setRenaming] = useState<Thread | null>(null)
  const [sharing, setSharing] = useState<Thread | null>(null)
  const [deleting, setDeleting] = useState<Thread | null>(null)
  const isOpen = (t: Thread) => loc.pathname.startsWith(`/t/${t.id}`)

  const patch = async (t: Thread, p: Patch) => {
    try {
      const th = await api.patch<Thread>(`${V1}/threads/${t.id}`, p)
      if (th.archived && !isOpen(t)) dropThread(t.id); else upsertThread(th)
      return th
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const remove = async (t: Thread) => {
    setDeleting(null)
    try {
      await api.del(`${V1}/threads/${t.id}`)
      dropThread(t.id)
      if (isOpen(t)) navigate('/')
      toast('Thread deleted')
    } catch (e) { toast(errMsg(e), 'err') }
  }

  const items = (t: Thread): MenuItem[] => {
    const owner = accessOf(t, user) === 'owner'
    const link = () => copyText(`${location.origin}/t/${t.id}`).then(ok => toast(ok ? 'Link copied' : 'Could not copy', ok ? 'ok' : 'err'))
    const exp = { label: 'Export as Markdown', icon: 'download', onSelect: () => download(`${V1}/threads/${t.id}/export.md`) }
    if (!owner) return [{ label: 'Copy link', icon: 'link', onSelect: link }, exp]
    return [
      { label: 'Rename…', icon: 'edit', onSelect: () => setRenaming(t) },
      { label: t.pinned ? 'Unpin' : 'Pin to top', icon: 'bookmark', onSelect: () => patch(t, { pinned: !t.pinned }) },
      { label: t.shares?.length ? 'Sharing…' : 'Share…', icon: 'users', onSelect: () => setSharing(t) },
      'sep',
      { label: 'Copy link', icon: 'link', onSelect: link },
      exp,
      ...(t.cwd && t.source !== 'ask' && (user?.role === 'admin' || user?.can_run) ? [{ label: 'Open a terminal here', icon: 'terminal', onSelect: () => navigate(`/term?host=${t.host_id || 0}&cwd=${encodeURIComponent(t.cwd)}`) }] : []),
      'sep',
      { label: t.archived ? 'Move out of archive' : 'Archive', icon: 'archive', onSelect: async () => { const th = await patch(t, { archived: !t.archived }); if (th?.archived) toast('Archived') } },
      { label: 'Delete…', icon: 'trash', danger: true, onSelect: () => setDeleting(t) },
    ]
  }

  const dialogs = <>
    {renaming && <RenameDialog thread={renaming} onClose={() => setRenaming(null)} onSave={title => { const t = renaming; setRenaming(null); if (title !== t.title) void patch(t, { title }) }} />}
    {sharing && <ShareDialog thread={sharing} onClose={() => setSharing(null)} onSaved={th => { upsertThread(th); setSharing(th) }} />}
    {deleting && <Confirm title="Delete this thread?" message={<>“{threadTitle(deleting)}” disappears from Pocket AI. The CLI’s own session file on the server stays.</>}
      onConfirm={() => remove(deleting)} onCancel={() => setDeleting(null)} />}
  </>
  return { items, dialogs, rename: (t: Thread) => setRenaming(t) }
}

export function RenameDialog({ thread, onClose, onSave }: { thread: Thread; onClose: () => void; onSave: (title: string) => void }) {
  const [title, setTitle] = useState(thread.title || threadTitle(thread))
  return (
    <Modal title="Rename thread" size="sm" onClose={onClose} footer={<>
      <span className="grow" />
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="submit" form="rename-thread" className="btn primary" disabled={!title.trim()}>Rename</button>
    </>}>
      <form id="rename-thread" onSubmit={e => { e.preventDefault(); if (title.trim()) onSave(title.trim()) }}>
        <input autoFocus value={title} onChange={e => setTitle(e.target.value)} onFocus={e => e.currentTarget.select()} aria-label="Thread title" maxLength={200} style={{ width: '100%' }} />
      </form>
    </Modal>
  )
}
