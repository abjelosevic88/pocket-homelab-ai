import { useEffect, useState } from 'react'
import { Icon } from './icons'

/** Keep Ask in the header on pages with a composer, clear of typing controls. */
export function AskButton() {
  const [available, setAvailable] = useState(() => !!document.querySelector('pocket-ask-launcher'))
  useEffect(() => {
    const ready = () => setAvailable(!!document.querySelector('pocket-ask-launcher'))
    window.addEventListener('pocket-ask-available', ready)
    ready()
    return () => window.removeEventListener('pocket-ask-available', ready)
  }, [])
  if (!available) return null
  return <button type="button" className="btn ghost ask-header-btn" data-pocket-ask-trigger
    aria-label="Ask AI about this page" title="Ask AI about this page" aria-haspopup="dialog"
    onClick={() => window.dispatchEvent(new CustomEvent('pocket-ask'))}>
    <Icon name="sparkles" size={16} /><span>Ask</span>
  </button>
}
