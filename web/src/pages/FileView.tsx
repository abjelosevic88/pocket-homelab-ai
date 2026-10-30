// One file in a tab: text and code with line numbers, Markdown rendered,
// images, PDF, audio and video in the browser's own players.
import { useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { fmtBytes } from '../lib/format'
import { baseOf, dirOf, fileKind, filesPath, rawURL } from '../lib/files'
import { useTerminals } from '../lib/terminals'
import { Icon } from '../components/icons'
import { Markdown } from '../components/Markdown'
import { PageHead } from '../components/PageHead'
import { copyText, errMsg, Loading, useToast } from '../components/ui'

const TEXT_MAX = 5 << 20

export default function FileView({ onMenu }: { onMenu?: () => void }) {
  const { hosts } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const term = useTerminals()
  const host = Number(useParams().host) || 0
  const path = useSearchParams()[0].get('path') || ''
  const name = baseOf(path)
  const kind = fileKind(name)
  const [text, setText] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [binary, setBinary] = useState(false)
  const [source, setSource] = useState(false)
  const [n, setN] = useState(0) // reload
  const machine = host ? hosts.find(h => h.id === host)?.name || 'Server' : 'This server'
  const url = rawURL(host, path)

  useEffect(() => {
    setText(null); setErr(''); setBinary(false)
    if (kind !== 'text' && kind !== 'markdown') return
    let live = true
    fetch(url, { credentials: 'same-origin' }).then(async r => {
      if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error || m } catch { /* not JSON */ } throw new Error(m) }
      const len = Number(r.headers.get('content-length') || 0)
      if (len > TEXT_MAX) { if (live) { setBinary(true); setErr(`Too big to show here (${fmtBytes(len)})`) } return }
      const buf = new Uint8Array(await r.arrayBuffer())
      if (!live) return
      if (buf.subarray(0, 8000).includes(0)) { setBinary(true); return }
      setText(new TextDecoder().decode(buf))
    }).catch(e => { if (live) setErr(errMsg(e)) })
    return () => { live = false }
  }, [url, kind, n])

  const openTerminal = async () => { const g = await term.open(host, dirOf(path)); if (g) navigate(`/term/${g}`) }
  const lines = text?.split('\n') || []
  return (
    <div className="page file-page">
      <PageHead title={name} icon={kind === 'image' ? 'image' : kind === 'video' ? 'video' : 'file'} onMenu={onMenu}
        sub={<>{machine} · <span className="mono">{dirOf(path)}</span></>}
        actions={<>
          {kind === 'markdown' && text !== null && <button type="button" className={`btn ghost sm ${source ? 'on' : ''}`} onClick={() => setSource(!source)}><Icon name="text" size={14} /> {source ? 'Rendered' : 'Source'}</button>}
          <button type="button" className="btn ghost icon-only" title="Copy the path" aria-label="Copy the path" onClick={() => copyText(path).then(ok => toast(ok ? 'Path copied' : 'Could not copy', ok ? 'ok' : 'err'))}><Icon name="copy" size={16} /></button>
          <button type="button" className="btn ghost icon-only" title="Show in its folder" aria-label="Show in its folder" onClick={() => navigate(filesPath(host, dirOf(path)))}><Icon name="folder" size={16} /></button>
          <button type="button" className="btn ghost icon-only" title="Open a terminal in its folder" aria-label="Open a terminal in its folder" onClick={openTerminal}><Icon name="terminal" size={16} /></button>
          <button type="button" className="btn ghost icon-only" title="Reload" aria-label="Reload" onClick={() => setN(x => x + 1)}><Icon name="refresh" size={16} /></button>
          <a className="btn ghost icon-only" href={rawURL(host, path, true)} title="Download" aria-label="Download"><Icon name="download" size={16} /></a>
        </>} />
      <div className={`file-body ${kind}`}>
        {kind === 'image' ? <div className="file-media"><img src={`${url}&n=${n}`} alt={name} /></div>
          : kind === 'pdf' ? <iframe className="file-pdf" src={`${url}&n=${n}`} title={name} />
          : kind === 'video' ? <div className="file-media"><video src={url} controls playsInline /></div>
          : kind === 'audio' ? <div className="file-media"><audio src={url} controls /></div>
          : err || binary ? <div className="file-msg"><Icon name={binary ? 'file' : 'alert'} size={22} />
              <p>{err || 'This file is not text, so it cannot be shown here.'}</p>
              <a className="btn" href={rawURL(host, path, true)}><Icon name="download" size={15} /> Download</a></div>
          : text === null ? <Loading />
          : kind === 'markdown' && !source ? <div className="page-scroll"><div className="page-inner file-md"><Markdown text={text} /></div></div>
          : <div className="file-code"><pre><code>{lines.map((l, i) => <span key={i} className="file-line"><span className="file-ln">{i + 1}</span>{l || ' '}{'\n'}</span>)}</code></pre></div>}
      </div>
    </div>
  )
}
