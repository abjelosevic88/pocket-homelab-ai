// How conversations look on this device: text size, spacing and width. Kept in
// the browser, so the phone and the computer can each have their own.
import { useCallback, useState } from 'react'

export interface Display {
  text: 's' | 'm' | 'l' | 'xl'
  spacing: 'compact' | 'default' | 'roomy'
  width: 'narrow' | 'default' | 'wide' | 'full'
}

const KEY = 'pai-display'
const DEFAULT: Display = { text: 'm', spacing: 'default', width: 'default' }

export function loadDisplay(): Display {
  try { return { ...DEFAULT, ...JSON.parse(localStorage.getItem(KEY) || '{}') } } catch { return DEFAULT }
}

/** The CSS reads these attributes on <html> (styles/ai.css, "Display options"). */
export function applyDisplay(d: Display) {
  const el = document.documentElement
  el.dataset.text = d.text
  el.dataset.spacing = d.spacing
  el.dataset.width = d.width
}

export function useDisplay(): [Display, (patch: Partial<Display>) => void] {
  const [d, setD] = useState(loadDisplay)
  const set = useCallback((patch: Partial<Display>) => setD(cur => {
    const next = { ...cur, ...patch }
    localStorage.setItem(KEY, JSON.stringify(next))
    applyDisplay(next)
    return next
  }), [])
  return [d, set]
}
