// Drawings: SVG an agent writes in a ```svg code block, shown as a picture.
// Always through an <img> with a data: URL: an SVG in an image runs no script
// and loads nothing from outside, whatever the agent put in it.

export interface Drawing { svg: string; title: string }

const BLOCK = /```svg[^\n]*\n([\s\S]*?)```/g

/** A finished drawing: the block holds one whole <svg>…</svg>. */
export function isDrawing(svg: string): boolean {
  const t = svg.trim()
  return /^(<\?xml[^>]*>\s*)?<svg[\s>]/i.test(t) && /<\/svg>$/i.test(t)
}

/** Its name: the <title> inside, else the bold words or heading just above it. */
export function drawingName(svg: string, before: string): string {
  const t = /<title[^>]*>([\s\S]*?)<\/title>/i.exec(svg)?.[1]
  if (t && t.trim()) return unescape(t.replace(/\s+/g, ' ').trim())
  const lines = before.split('\n').map(l => l.trim()).filter(Boolean).reverse()
  for (const l of lines.slice(0, 4)) {
    const m = /\*\*([^*]+?)\*\*/.exec(l) || /^#{1,6}\s+(.+)$/.exec(l)
    if (m) return m[1].replace(/[.:]+$/, '').trim()
  }
  return 'Drawing'
}

function unescape(s: string): string {
  return s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&')
}

/** Every finished drawing in an answer, in order. */
export function drawingsIn(text: string | undefined): Drawing[] {
  if (!text || !text.includes('```svg')) return []
  const out: Drawing[] = []
  for (const m of text.matchAll(BLOCK)) {
    const svg = m[1].trim()
    if (isDrawing(svg)) out.push({ svg, title: drawingName(svg, text.slice(Math.max(0, m.index - 600), m.index)) })
  }
  return out
}

/** The answer without its drawings (for word counts and short previews). */
export function withoutDrawings(text: string): string {
  return text.includes('```svg') ? text.replace(BLOCK, ' ') : text
}

/** The picture itself, as an image address. */
export function svgUrl(svg: string): string {
  let s = svg.trim()
  if (!/<svg[^>]*\sxmlns=/i.test(s)) s = s.replace(/<svg/i, '<svg xmlns="http://www.w3.org/2000/svg"')
  return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(s)
}

/** A file name for saving it. */
export function svgFileName(title: string): string {
  return (title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 60) || 'drawing') + '.svg'
}
