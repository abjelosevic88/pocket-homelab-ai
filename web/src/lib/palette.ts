// The open page's own actions for the palette: the thread page puts its menu
// here (rename, archive, delete…) so ⌘K can run them without the mouse.
import { useEffect } from 'react'
import type { MenuItem } from '../components/ui'

export interface PageActions { title: string; items: MenuItem[] }

let source: (() => PageActions | null) | null = null
export const pageActions = () => source?.() || null

/** Offer what get() returns while the calling page is mounted (read when the palette opens). */
export function usePageActions(get: () => PageActions | null) {
  useEffect(() => {
    source = get
    return () => { if (source === get) source = null }
  }, [get])
}
