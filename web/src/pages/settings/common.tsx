import React, { useCallback } from 'react'
import { useApp } from '../../lib/app-context'
import type { Settings } from '../../lib/types'
import { errMsg, useToast } from '../../components/ui'

/** saveSettings with an error toast (the context already rolls back). */
export function useSaveSettings() {
  const { saveSettings } = useApp()
  const { toast } = useToast()
  return useCallback(async (patch: Partial<Settings> & { notify_token?: string }, ok?: string) => {
    try { await saveSettings(patch); if (ok) toast(ok) ; return true } catch (e) { toast(errMsg(e), 'err'); return false }
  }, [saveSettings, toast])
}

/** "3 min ago", "yesterday", "Oct 12". */
export function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const t = Date.parse(iso)
  if (isNaN(t)) return iso
  const s = Math.round((Date.now() - t) / 1000)
  if (s < 0) return 'just now'
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  if (s < 2 * 86400) return 'yesterday'
  if (s < 14 * 86400) return `${Math.round(s / 86400)} days ago`
  return new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: new Date(t).getFullYear() === new Date().getFullYear() ? undefined : 'numeric' })
}

export function StatusDot({ status, title }: { status: 'ok' | 'error' | 'off' | 'warn'; title?: string }) {
  return <span className={`set-status ${status}`} title={title} aria-label={title || status} role="img" />
}

export function Section({ title, sub, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; actions?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      <div className="set-section-body">{children}</div>
    </section>
  )
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}
