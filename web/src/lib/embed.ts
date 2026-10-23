// Embedded in another Pocket app (?embed=1): no sidebar or tabs, and the
// framing page hears which thread is open. Remembered for the frame's life,
// so navigating inside the frame stays embedded.

const KEY = 'pai-embed'

function detect(): boolean {
  if (window.parent === window) return false
  if (new URLSearchParams(location.search).get('embed') === '1') sessionStorage.setItem(KEY, '1')
  return sessionStorage.getItem(KEY) === '1'
}

export const embedded = detect()
// the framing app has its own Ask: hide Core's floating launcher in here
if (embedded) document.documentElement.classList.add('pai-embedded')

/** Tells the framing page where the frame is (thread id once one is open). */
export function tellParent(path: string) {
  if (!embedded) return
  const m = path.match(/^\/t\/([^/]+)/)
  // frame-ancestors (Caddy) limits who can frame us; ids carry no content
  window.parent.postMessage({ type: 'pocket-ai', path, thread_id: m ? m[1] : null }, '*')
}
