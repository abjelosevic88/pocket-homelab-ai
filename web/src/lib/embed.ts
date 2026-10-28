// Embedded in another Pocket app (?embed=1): no sidebar or tabs, and the
// framing page hears which thread is open. Remembered for the frame's life,
// so navigating inside the frame stays embedded.

const KEY = 'pai-embed'

const TABS = 'pai-embed-tabs'

function detect(): boolean {
  if (window.parent === window) return false
  const q = new URLSearchParams(location.search)
  if (q.get('embed') === '1') sessionStorage.setItem(KEY, '1')
  if (q.get('tabs') === '1') sessionStorage.setItem(TABS, '1')
  return sessionStorage.getItem(KEY) === '1'
}

export const embedded = detect()
/** The framing page shows conversations in tabs of its own (?tabs=1, Pocket Projects' panel):
 *  a new conversation (a fork) opens there instead of in a new browser tab. */
export const parentTabs = embedded && sessionStorage.getItem(TABS) === '1'

/** Running as the installed app (Chrome/Edge "Install", iOS home screen):
 *  window.open would leave the app for a browser tab, so new conversations
 *  open inside it, in its own tab bar. */
export const installedApp = () => ['standalone', 'window-controls-overlay', 'minimal-ui', 'fullscreen'].some(m => matchMedia(`(display-mode: ${m})`).matches)
  || (navigator as unknown as { standalone?: boolean }).standalone === true

/** Asks the framing page to open a conversation in a tab of its own. */
export function openInParent(threadId: string, title: string) {
  window.parent.postMessage({ type: 'pocket-ai', open: { thread_id: threadId, url: `${location.origin}/t/${threadId}`, title } }, '*')
}
// the framing app has its own Ask: hide Core's floating launcher in here
if (embedded) document.documentElement.classList.add('pai-embedded')

/** Tells the framing page where the frame is (thread id once one is open) and
 *  how that thread is doing (running / waiting / idle / error), on every change. */
export function tellParent(path: string, status = '') {
  if (!embedded) return
  const m = path.match(/^\/t\/([^/]+)/)
  // frame-ancestors (Caddy) limits who can frame us; ids and states carry no content
  window.parent.postMessage({ type: 'pocket-ai', path, thread_id: m ? m[1] : null, status }, '*')
}

/** Something of the framing page's item the person can add to a message with "@" (Pocket
 *  Projects: the issue's notes, attached files and links). */
export interface ParentContextItem { id: string; kind: 'note' | 'file' | 'link'; title: string; sub?: string }

let ctxSeq = 0
/** Asks the framing page and waits for its answer (only the parent window may answer). */
function askParent<T>(want: string, extra: Record<string, unknown>, timeout = 20000): Promise<T> {
  return new Promise((resolve, reject) => {
    const req = `c${++ctxSeq}`
    const done = (e: MessageEvent) => {
      if (e.source !== window.parent || e.data?.type !== 'pocket-ai-context' || e.data.req !== req) return
      window.removeEventListener('message', done); window.clearTimeout(t)
      if (e.data.error) reject(new Error(String(e.data.error))); else resolve(e.data as T)
    }
    const t = window.setTimeout(() => { window.removeEventListener('message', done); reject(new Error('No answer from the page around this conversation')) }, timeout)
    window.addEventListener('message', done)
    window.parent.postMessage({ type: 'pocket-ai', want, req, ...extra }, '*')
  })
}

/** The framing page's items for "@" (empty outside such a page). */
export async function parentContext(): Promise<{ label: string; items: ParentContextItem[] }> {
  if (!parentTabs) return { label: '', items: [] }
  const r = await askParent<{ label?: string; items?: ParentContextItem[] }>('context', {}, 8000)
  return { label: r.label || '', items: Array.isArray(r.items) ? r.items : [] }
}

/** One item's content: a file to attach (notes come as Markdown files) or text to insert. */
export async function parentContextItem(id: string): Promise<{ file?: File; text?: string }> {
  const r = await askParent<{ file?: File; text?: string }>('context-item', { id })
  return { file: r.file instanceof File ? r.file : undefined, text: typeof r.text === 'string' ? r.text : undefined }
}
