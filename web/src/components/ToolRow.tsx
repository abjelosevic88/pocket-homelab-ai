// One tool call: a one-line summary that opens to its input and output.
import { memo, useState } from 'react'
import type { ToolInfo, ToolKind } from '../lib/types'
import { toolLabel } from '../lib/tool-activity'
import { Icon } from './icons'
import { copyText, Spinner } from './ui'

const KIND_ICON: Record<ToolKind, string> = {
  bash: 'terminal', edit: 'file-edit', write: 'file-edit', read: 'file', search: 'search', web: 'globe', task: 'bot', mcp: 'layers', other: 'wrench',
}

const LIMIT = 4000

function str(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function field(input: unknown, ...keys: string[]): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  for (const k of keys) if (typeof o[k] === 'string' && o[k]) return o[k] as string
  return ''
}

export function Diff({ text }: { text: string }) {
  const lines = text.split('\n')
  return (
    <pre className="diff">
      {lines.map((l, i) => {
        const cls = l.startsWith('+++') || l.startsWith('---') ? 'dh' : l.startsWith('@@') ? 'dh' : l.startsWith('+') ? 'da' : l.startsWith('-') ? 'dd' : ''
        return <span key={i} className={cls}>{l}{'\n'}</span>
      })}
    </pre>
  )
}

function Clip({ text, label }: { text: string; label: string }) {
  const [all, setAll] = useState(false)
  const [copied, setCopied] = useState(false)
  if (!text) return null
  const long = text.length > LIMIT
  const shown = all || !long ? text : text.slice(0, LIMIT)
  return (
    <div className="tool-sec">
      <div className="tool-sec-h">
        <span>{label}</span>
        <button type="button" className="link-btn xsmall" onClick={async () => { setCopied(await copyText(text)); setTimeout(() => setCopied(false), 1200) }}>{copied ? 'Copied' : 'Copy'}</button>
      </div>
      <pre className="tool-pre">{shown}{long && !all && '\n…'}</pre>
      {long && <button type="button" className="link-btn xsmall" onClick={() => setAll(a => !a)}>{all ? 'Show less' : `Show all (${Math.round(text.length / 1000)}k characters)`}</button>}
    </div>
  )
}

export const ToolRow = memo(function ToolRow({ tool }: { tool: ToolInfo }) {
  const [open, setOpen] = useState(false)
  const kind = tool.kind || 'other'
  const label = toolLabel(tool)
  const friendly = label !== tool.name
  const status = tool.status || 'done'
  const command = kind === 'bash' ? field(tool.input, 'command', 'cmd') : ''
  const path = field(tool.input, 'file_path', 'path', 'notebook_path')
  // "Write demo.go" under the name "Write" reads twice: drop the repeated word
  const rawTitle = command || tool.title || path || tool.name
  const title = rawTitle.startsWith(tool.name + ' ') ? rawTitle.slice(tool.name.length + 1) : rawTitle
  const shownName = kind === 'bash' ? 'Shell' : label
  const restInput = (() => {
    if (kind === 'bash' && command) {
      const o = { ...(tool.input as Record<string, unknown>) }
      delete o.command; delete o.cmd; delete o.description
      return Object.keys(o).length ? str(o) : ''
    }
    if ((kind === 'edit' || kind === 'write') && tool.diff) return ''
    return str(tool.input)
  })()
  return (
    <div className={`tool ${status} ${open ? 'open' : ''}`}>
      <button type="button" className="tool-head" onClick={() => setOpen(o => !o)} aria-expanded={open}>
        <span className="tool-ico"><Icon name={KIND_ICON[kind] || 'wrench'} size={14} /></span>
        <span className={`tool-name ${friendly ? 'friendly' : ''}`} title={label}>{shownName}</span>
        {!friendly && <span className="tool-title ellipsis" title={title}>{title !== tool.name ? title : ''}</span>}
        <span className="tool-state">
          {status === 'running' && <Spinner size={12} />}
          {status === 'error' && <span className="badge danger">{tool.exit_code !== undefined && tool.exit_code !== 0 ? `exit ${tool.exit_code}` : 'error'}</span>}
          {status === 'denied' && <span className="badge warn">denied</span>}
        </span>
        <Icon name="chevron-down" size={14} className="tool-caret" />
      </button>
      {open && (
        <div className="tool-body">
          {friendly && <div className="muted xsmall tool-raw-name">{tool.name}</div>}
          {command && <div className="tool-sec"><pre className="tool-pre cmd">$ {command}</pre></div>}
          {tool.diff && <div className="tool-sec"><div className="tool-sec-h"><span>{path || 'Changes'}</span></div><Diff text={tool.diff} /></div>}
          {restInput && <Clip text={restInput} label="Input" />}
          <Clip text={tool.output} label={status === 'error' ? 'Error' : 'Output'} />
          {!tool.output && status === 'running' && <div className="muted xsmall">Running…</div>}
        </div>
      )}
    </div>
  )
})
