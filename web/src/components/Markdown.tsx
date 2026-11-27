import { createContext, memo, type MouseEvent, useContext, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { codePath, type FileLinkPlace, linkPath } from '../lib/filelinks'
import { filePath, filesPath } from '../lib/files'
import { codeLang, commandRanges } from '../lib/codeLines'
import { renderMarkdown } from '../lib/markdown'
import { drawingName, isDrawing, svgFileName, svgUrl } from '../lib/drawings'
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
    s.dataset.n = String(i)
    s.textContent = l
    code.appendChild(s)
  })
}

const COPY_ICON = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>'
const DONE_ICON = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>'

// Hovering a row of a multi-line code block shows a copy button at the end of
// that line: one click copies the line without its newline, or the whole
// command when the line is part of one that spans lines (a for … done loop,
// lines ending in \, a heredoc; lib/codeLines.ts). Then every line of it
// lights up and the button says how many; Shift-click copies only the line.
function lineCopy(wrap: HTMLElement, blockCopy: HTMLElement, code: Element) {
  const text = code.textContent || ''
  const lines = text.split('\n')
  const ranges = commandRanges(text, codeLang(code.className))
  const spans = new Map<number, HTMLElement>()
  code.querySelectorAll<HTMLElement>('.cl').forEach(s => spans.set(Number(s.dataset.n), s))
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'code-line-copy'
  wrap.appendChild(btn)
  let line: HTMLElement | null = null
  let range: [number, number] = [0, 0]
  let timer = 0
  const count = () => range[1] - range[0] + 1
  const paint = (done: boolean) => {
    const n = count()
    btn.innerHTML = (done ? DONE_ICON : COPY_ICON) + (n > 1 ? `<span>${n}</span>` : '')
    btn.classList.toggle('done', done)
    btn.classList.toggle('multi', n > 1)
    const label = n > 1 ? `Copy these ${n} lines (Shift-click: only this line)` : 'Copy this line'
    btn.title = label
    btn.setAttribute('aria-label', label)
  }
  const light = (on: boolean) => {
    for (let i = range[0]; i <= range[1]; i++) spans.get(i)?.classList.toggle('cl-hot', on)
  }
  const show = (l: HTMLElement | null) => {
    if (l === line) return
    light(false)
    line = l
    clearTimeout(timer)
    if (!l) { btn.classList.remove('on'); return }
    const n = Number(l.dataset.n)
    range = ranges[n] ?? [n, n]
    light(true)
    paint(false)
    // A command's button sits on its first line, so it stays put while the
    // mouse moves over the command (unless that line is scrolled away).
    const first = spans.get(range[0])
    const at = first && first.getBoundingClientRect().top >= 0 ? first : l
    const w = wrap.getBoundingClientRect(), r = at.getBoundingClientRect()
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
  btn.addEventListener('click', async e => {
    if (!line) return
    const whole = !e.shiftKey && count() > 1
    // A line taken out of a command loses its indentation.
    const one = line.textContent || ''
    const ok = await copyText(whole ? lines.slice(range[0], range[1] + 1).join('\n') : count() > 1 ? one.trimStart() : one)
    paint(ok)
    clearTimeout(timer)
    timer = window.setTimeout(() => paint(false), 1200)
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

// A finished ```svg block is a drawing: the picture shows above its code, the
// code folds away (a button shows it again). Click the picture to see it large.
function drawing(wrap: HTMLElement, pre: HTMLElement) {
  const code = pre.querySelector('code.language-svg')
  const svg = code?.textContent || ''
  if (!code || !isDrawing(svg)) return
  const prev = wrap.previousElementSibling
  const name = drawingName(svg, prev ? (prev.querySelector('strong, b')?.textContent ? `**${prev.querySelector('strong, b')!.textContent}**` : /^H[1-6]$/.test(prev.tagName) ? `# ${prev.textContent}` : '') : '')
  const fig = document.createElement('figure')
  fig.className = 'md-drawing'
  const img = document.createElement('img')
  img.src = svgUrl(svg)
  img.alt = name
  img.title = 'Click to see it large'
  img.addEventListener('click', () => fig.classList.toggle('big'))
  const cap = document.createElement('figcaption')
  const t = document.createElement('span')
  t.className = 'md-drawing-name'
  t.textContent = name === 'Drawing' ? '' : name
  const show = document.createElement('button')
  show.type = 'button'
  show.className = 'md-drawing-btn'
  show.textContent = 'Code'
  show.addEventListener('click', () => { wrap.hidden = !wrap.hidden; show.textContent = wrap.hidden ? 'Code' : 'Hide code' })
  const save = document.createElement('a')
  save.className = 'md-drawing-btn'
  save.textContent = 'Download'
  save.href = img.src
  save.download = svgFileName(name)
  cap.append(t, show, save)
  fig.append(img, cap)
  wrap.before(fig)
  wrap.hidden = true
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
      drawing(wrap, pre)
      if (wrap.classList.contains('one-line')) return
      const code = pre.querySelector('code') ?? pre
      splitLines(code)
      lineCopy(wrap, b, code)
    })
  }, [html])
  return <div ref={ref} className={`md ${className}`} onClick={place ? onClick : undefined} dangerouslySetInnerHTML={{ __html: html }} />
})
