import { createContext, memo, type MouseEvent, useContext, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { codePath, type FileLinkPlace, linkPath } from '../lib/filelinks'
import { filePath, filesPath } from '../lib/files'
import { renderMarkdown } from '../lib/markdown'
import { copyText } from './ui'

/** Set around agent Markdown: paths in it open in the file viewer on this machine (lib/filelinks.ts). */
export const FileLinkContext = createContext<FileLinkPlace | null>(null)

// A folder ("…/") opens in Files, anything else in the file viewer.
const viewerPath = (host: number, p: string) => p.endsWith('/') ? filesPath(host, p.replace(/\/+$/, '') || '/') : filePath(host, p)

// Triple-click in a code block selects that line's text only. The browser's
// own line selection also takes the line break (and paints the empty row
// below it), so a copied command would come with a newline.
function selectLine(e: globalThis.MouseEvent) {
  if (e.detail < 3 || e.button !== 0) return
  const pre = e.currentTarget as HTMLElement
  const doc = document as Document & { caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null }
  const pos = doc.caretPositionFromPoint?.(e.clientX, e.clientY)
  const r = pos ? null : document.caretRangeFromPoint?.(e.clientX, e.clientY)
  const node = pos ? pos.offsetNode : r?.startContainer
  const offset = pos ? pos.offset : r?.startOffset ?? 0
  if (!node || !pre.contains(node)) return
  const before = document.createRange()
  before.setStart(pre, 0)
  before.setEnd(node, offset)
  const at = before.toString().length
  const text = pre.textContent || ''
  const from = text.lastIndexOf('\n', at - 1) + 1
  let to = text.indexOf('\n', at)
  if (to < 0) to = text.length
  const a = textPoint(pre, from), b = textPoint(pre, to)
  if (!a || !b) return
  e.preventDefault()
  getSelection()?.setBaseAndExtent(a[0], a[1], b[0], b[1])
}

// The text node and offset at character n of el's text.
function textPoint(el: HTMLElement, n: number): [Node, number] | null {
  const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
  let last: Text | null = null
  for (let t = w.nextNode() as Text | null; t; t = w.nextNode() as Text | null) {
    const len = t.data.length
    if (n <= len) return [t, n]
    n -= len
    last = t
  }
  return last ? [last, last.data.length] : null
}

/** Agent Markdown, sanitized, with a copy button on every code block. */
export const Markdown = memo(function Markdown({ text, className = '' }: { text: string; className?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const html = renderMarkdown(text)
  const place = useContext(FileLinkContext)
  const navigate = useNavigate()
  // Paths open in Pocket AI's file viewer on the conversation's machine
  // (lib/filelinks.ts): file links, and absolute paths written as inline code.
  useEffect(() => {
    const el = ref.current
    if (!el || !place) return
    const mark = (a: HTMLAnchorElement, p: string) => {
      a.href = viewerPath(place.host, p)
      a.removeAttribute('target')
      a.classList.add('file-link')
      a.dataset.file = p
      a.title = p
    }
    el.querySelectorAll('a[href]').forEach(n => {
      const a = n as HTMLAnchorElement
      if (a.dataset.file) return
      const p = linkPath(a.getAttribute('href') || '', place.cwd)
      if (p) mark(a, p)
    })
    el.querySelectorAll('code').forEach(c => {
      if (c.closest('pre, a')) return
      const p = codePath(c.textContent || '')
      if (!p) return
      const a = document.createElement('a')
      mark(a, p)
      c.replaceWith(a)
      a.appendChild(c)
    })
  }, [html, place])
  const onClick = (e: MouseEvent<HTMLDivElement>) => {
    const a = (e.target as HTMLElement).closest('a.file-link') as HTMLAnchorElement | null
    if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    navigate(a.getAttribute('href') || '/')
  }
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.querySelectorAll('pre').forEach(pre => {
      if (pre.parentElement?.classList.contains('code-wrap')) return
      // The button sits on a wrapper, not in the <pre>: a long line scrolls
      // the <pre> sideways and would carry the button into the code.
      const wrap = document.createElement('div')
      wrap.className = 'code-wrap'
      if (!(pre.textContent || '').trim().includes('\n')) wrap.classList.add('one-line')
      pre.replaceWith(wrap)
      wrap.appendChild(pre)
      const b = document.createElement('button')
      b.type = 'button'
      b.className = 'code-copy'
      b.textContent = 'Copy'
      b.addEventListener('click', async () => {
        const code = pre.querySelector('code')?.textContent ?? pre.textContent ?? ''
        b.textContent = (await copyText(code)) ? 'Copied' : 'Failed'
        setTimeout(() => { b.textContent = 'Copy' }, 1400)
      })
      wrap.appendChild(b)
      pre.addEventListener('mousedown', selectLine)
    })
  }, [html])
  return <div ref={ref} className={`md ${className}`} onClick={place ? onClick : undefined} dangerouslySetInnerHTML={{ __html: html }} />
})
