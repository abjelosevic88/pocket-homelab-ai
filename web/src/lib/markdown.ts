import DOMPurify from 'dompurify'
import { Marked } from 'marked'

const md = new Marked({ gfm: true, breaks: false })

// Links open in a new tab; nothing the agent writes can run script.
DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node.tagName === 'A') { node.setAttribute('target', '_blank'); node.setAttribute('rel', 'noopener noreferrer') }
})

const cache = new Map<string, string>()

export function renderMarkdown(src: string): string {
  if (!src) return ''
  const hit = cache.get(src)
  if (hit !== undefined) return hit
  let html: string
  try { html = md.parse(src, { async: false }) as string } catch { html = `<pre>${escapeHtml(src)}</pre>` }
  const clean = DOMPurify.sanitize(html, { USE_PROFILES: { html: true } })
  if (src.length < 20000) {
    if (cache.size > 400) cache.clear()
    cache.set(src, clean)
  }
  return clean
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]!))
}

/** Search snippets: everything escaped except <mark>…</mark>. */
export function safeSnippet(s: string): string {
  return DOMPurify.sanitize(s, { ALLOWED_TAGS: ['mark'], ALLOWED_ATTR: [] })
}
