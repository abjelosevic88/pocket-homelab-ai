import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import { baseName, shortPath } from '../../lib/format'
import type { Workspace } from '../../lib/types'
import { FolderButton } from '../../components/FolderPicker'
import { Icon } from '../../components/icons'
import { Confirm, Empty, errMsg, Field, MenuButton, Modal, Spinner, useToast, type MenuItem, useContextMenu } from '../../components/ui'
import { Section } from './common'

const COLORS = ['#7c3aed', '#2563eb', '#0891b2', '#059669', '#ca8a04', '#ea580c', '#dc2626', '#db2777', '#64748b']

export function WorkspacesTab() {
  const { workspaces, reloadWorkspaces } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<Workspace> | null>(null)
  const [del, setDel] = useState<Workspace | null>(null)
  const ctx = useContextMenu()
  return (
    <div className="stack loose">
      <Section title="Your folders" sub="They show up in the folder picker and as a filter above the thread list."
        actions={<button type="button" className="btn primary sm" onClick={() => setEdit({ color: COLORS[workspaces.length % COLORS.length] })}><Icon name="plus" size={14} />Add folder</button>}>
        {workspaces.length === 0 ? <Empty title="No folders yet" icon="folder">Add the places you work in most — your notes repo, ~/Projects/…, the server’s config folder.</Empty> : (
          <div className="set-people">
            {ctx.menu}{workspaces.map(w => {
              const more: MenuItem[] = [
                { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(w) },
                'sep',
                { label: 'Remove…', icon: 'trash', danger: true, onSelect: () => setDel(w) },
              ]
              return (
              <div key={w.id} className="set-person" onContextMenu={e => ctx.open(e, more)}>
                <span className="ws-dot" style={{ background: w.color || 'var(--accent)' }}><Icon name="folder" size={14} /></span>
                <div className="grow" style={{ minWidth: 0 }}>
                  <div className="set-person-name">{w.name}</div>
                  <div className="muted small mono ellipsis">{shortPath(w.path)}{w.threads ? <span className="faint"> · {w.threads} threads</span> : null}</div>
                </div>
                <MenuButton label={`More for ${w.name}`} align="right" items={more}><Icon name="more" size={16} /></MenuButton>
              </div>
            )})}
          </div>
        )}
      </Section>
      {edit && <WorkspaceDialog ws={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadWorkspaces() }} />}
      {del && <Confirm title={`Remove “${del.name}”?`} message="Only the shortcut goes; the folder and the threads in it stay." confirmLabel="Remove" onCancel={() => setDel(null)}
        onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/workspaces/${d.id}`); reloadWorkspaces() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function WorkspaceDialog({ ws, onClose, onSaved }: { ws: Partial<Workspace>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: ws.name || '', path: ws.path || '', color: ws.color || COLORS[0] })
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try {
      const body = { ...f, name: f.name.trim() || baseName(f.path) }
      if (ws.id) await api.put(`${V1}/workspaces/${ws.id}`, body); else await api.post(`${V1}/workspaces`, body)
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={ws.id ? 'Edit folder' : 'Add a folder'} onClose={onClose} size="sm" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy || !f.path} onClick={save}>{busy && <Spinner />}{ws.id ? 'Save' : 'Add'}</button>
    </>}>
      <div className="stack">
        <Field label="Folder" group><FolderButton value={f.path} onChange={p => setF(x => ({ ...x, path: p, name: x.name || baseName(p) }))} className="btn" />{f.path && <span className="muted small mono">{f.path}</span>}</Field>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder={baseName(f.path) || 'Homelab'} /></Field>
        <Field label="Colour" group>
          <div className="palette">{COLORS.map(c => <button key={c} type="button" className={`swatch ${f.color === c ? 'active' : ''}`} style={{ background: c }} aria-label={c} onClick={() => setF({ ...f, color: c })} />)}</div>
        </Field>
      </div>
    </Modal>
  )
}
