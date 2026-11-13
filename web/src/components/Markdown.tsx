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
    })
  }, [html])
  return <div ref={ref} className={`md ${className}`} onClick={place ? onClick : undefined} dangerouslySetInnerHTML={{ __html: html }} />
})
