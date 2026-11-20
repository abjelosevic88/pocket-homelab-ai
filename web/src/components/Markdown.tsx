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

// Each line of a code block in its own span, so it can be found and copied.
// The text (and so Copy and selection) stays exactly the same.
function splitLines(code: Element) {
  if (code.children.length) return // only plain text is split
  const lines = (code.textContent || '').split('\n')
  code.textContent = ''
  lines.forEach((l, i) => {
    if (i) code.appendChild(document.createTextNode('\n'))
    if (!l) return
    const s = document.createElement('span')
    s.className = 'cl'
    s.textContent = l
    code.appendChild(s)
  })
}

const COPY_ICON = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>'
const DONE_ICON = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>'

// Hovering a row of a multi-line code block shows a copy button at the end of
// that line: one click copies just the line, without its newline.
function lineCopy(wrap: HTMLElement, blockCopy: HTMLElement) {
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'code-line-copy'
  btn.title = 'Copy this line'
  btn.setAttribute('aria-label', 'Copy this line')
  btn.innerHTML = COPY_ICON
  wrap.appendChild(btn)
  let line: HTMLElement | null = null
  let timer = 0
  const show = (l: HTMLElement | null) => {
    if (l === line) return
    line?.classList.remove('cl-hot')
    line = l
    clearTimeout(timer)
    btn.innerHTML = COPY_ICON
    btn.classList.remove('done')
    if (!l) { btn.classList.remove('on'); return }
    l.classList.add('cl-hot')
    const w = wrap.getBoundingClientRect(), r = l.getBoundingClientRect()
    const max = w.width - blockCopy.offsetWidth - 7 - 6 - btn.offsetWidth
    btn.style.left = `${Math.max(14, Math.min(r.right - w.left + 8, max))}px`
    btn.style.top = `${r.top - w.top + (r.height - btn.offsetHeight) / 2}px`
    btn.classList.add('on')
  }
  wrap.addEventListener('mousemove', e => {
    if (e.target === btn || btn.contains(e.target as Node)) return
    let hit: HTMLElement | null = null
    for (const l of wrap.querySelectorAll<HTMLElement>('.cl')) {
      const r = l.getBoundingClientRect()
      if (e.clientY >= r.top - 2 && e.clientY < r.bottom + 2) { hit = l; break }
    }
    show(hit)
  })
  wrap.addEventListener('mouseleave', () => show(null))
  btn.addEventListener('mousedown', e => e.preventDefault()) // keep any selection
  btn.addEventListener('click', async () => {
    if (!line) return
    const ok = await copyText(line.textContent || '')
    btn.innerHTML = ok ? DONE_ICON : COPY_ICON
    btn.classList.toggle('done', ok)
    clearTimeout(timer)
    timer = window.setTimeout(() => { btn.innerHTML = COPY_ICON; btn.classList.remove('done') }, 1200)
  })
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
      if (wrap.classList.contains('one-line')) return
      splitLines(pre.querySelector('code') ?? pre)
      lineCopy(wrap, b)
    })
  }, [html])
  return <div ref={ref} className={`md ${className}`} onClick={place ? onClick : undefined} dangerouslySetInnerHTML={{ __html: html }} />
})
