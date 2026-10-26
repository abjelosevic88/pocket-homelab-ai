import { memo, useEffect, useRef } from 'react'
import { renderMarkdown } from '../lib/markdown'
import { copyText } from './ui'

/** Agent Markdown, sanitized, with a copy button on every code block. */
export const Markdown = memo(function Markdown({ text, className = '' }: { text: string; className?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const html = renderMarkdown(text)
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
  return <div ref={ref} className={`md ${className}`} dangerouslySetInnerHTML={{ __html: html }} />
})
