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
      if (pre.querySelector(':scope > .code-copy')) return
      const b = document.createElement('button')
      b.type = 'button'
      b.className = 'code-copy'
      b.textContent = 'Copy'
      b.addEventListener('click', async () => {
        const code = pre.querySelector('code')?.textContent ?? pre.textContent ?? ''
        b.textContent = (await copyText(code)) ? 'Copied' : 'Failed'
        setTimeout(() => { b.textContent = 'Copy' }, 1400)
      })
      pre.appendChild(b)
    })
  }, [html])
  return <div ref={ref} className={`md ${className}`} dangerouslySetInnerHTML={{ __html: html }} />
})
