import { useEffect, useState } from 'react'
import { Icon } from './icons'
import { embedded } from '../lib/embed'

const desktopQuery = '(min-width: 768px) and (hover: hover) and (pointer: fine)'

/** Picking an item is a mouse gesture: desktop only, never on phones. */
function useDesktop() {
  const [desktop, setDesktop] = useState(() => window.matchMedia(desktopQuery).matches)
  useEffect(() => {
    const mq = window.matchMedia(desktopQuery)
    const change = () => setDesktop(mq.matches)
    mq.addEventListener('change', change)
    return () => mq.removeEventListener('change', change)
  }, [])
  return desktop
}

/** Keep Ask in the header on pages with a composer, clear of typing controls,
 *  plus "pick an item" (⌖, desktop only); the Core launcher has no floating pill. */
export function AskButton() {
  const [available, setAvailable] = useState(() => !!document.querySelector('pocket-ask-launcher'))
  useEffect(() => {
    const ready = () => setAvailable(!!document.querySelector('pocket-ask-launcher'))
    window.addEventListener('pocket-ask-available', ready)
    ready()
    return () => window.removeEventListener('pocket-ask-available', ready)
  }, [])
  const desktop = useDesktop()
  if (!available || embedded) return null // the framing app has its own Ask
  return <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, flexShrink: 0 }}>
    <button type="button" className="btn ghost ask-header-btn" data-pocket-ask-trigger
      aria-label="Ask AI about this page" title="Ask AI about this page" aria-haspopup="dialog"
      onClick={() => window.dispatchEvent(new CustomEvent('pocket-ask'))}>
      <Icon name="sparkles" size={16} /><span>Ask</span>
    </button>
    {desktop && <button type="button" className="btn ghost ask-pick-btn"
      aria-label="Choose an item on the page to ask about" title="Choose an item on the page to ask about"
      style={{ padding: '0 8px', flexShrink: 0 }}
      onClick={() => window.dispatchEvent(new Event('pocket-ask-pick'))}>
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
        <circle cx="12" cy="12" r="6.5" /><path d="M12 2.5v5M12 16.5v5M2.5 12h5M16.5 12h5" />
      </svg>
    </button>}
  </span>
}
