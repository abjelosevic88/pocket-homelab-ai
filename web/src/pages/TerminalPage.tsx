// Terminals: a shell on this server or any of your machines, in the page.
// They keep running when you leave: coming back replays what they printed.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { Icon } from '../components/icons'
import { PageHead } from '../components/PageHead'
import { Empty, errMsg, Loading, type MenuItem, MenuButton, Modal, Spinner, useMediaQuery, useToast } from '../components/ui'

interface Term { id: string; host_id: number; host_name: string; title: string; cwd?: string; created_at: string; ended: boolean; exit_code: number; viewers: number }

const dark = {
  background: '#111113', foreground: '#e6e6ea', cursor: '#a78bfa', cursorAccent: '#111113',
  selectionBackground: 'rgba(167, 139, 250, .35)',
  black: '#1e1e22', red: '#f87171', green: '#4ade80', yellow: '#facc15', blue: '#60a5fa', magenta: '#c084fc', cyan: '#22d3ee', white: '#d4d4d8',
  brightBlack: '#6b6b76', brightRed: '#fca5a5', brightGreen: '#86efac', brightYellow: '#fde047', brightBlue: '#93c5fd', brightMagenta: '#d8b4fe', brightCyan: '#67e8f9', brightWhite: '#fafafa',
}
const light = {
  background: '#ffffff', foreground: '#1f2328', cursor: '#7c3aed', cursorAccent: '#ffffff',
  selectionBackground: 'rgba(124, 58, 237, .2)',
  black: '#24292f', red: '#cf222e', green: '#116329', yellow: '#9a6700', blue: '#0969da', magenta: '#8250df', cyan: '#1b7c83', white: '#6e7781',
  brightBlack: '#57606a', brightRed: '#a40e26', brightGreen: '#1a7f37', brightYellow: '#7d4e00', brightBlue: '#0550ae', brightMagenta: '#6639ba', brightCyan: '#0e6a74', brightWhite: '#424a53',
}
type Look = 'app' | 'light' | 'dark'
const LOOK_KEY = 'pai-terminal-theme'
// light: programs that print light grey or "white" text assume a dark
// background; xterm darkens such text until it can be read
const xtermLook = (l: 'light' | 'dark') => ({ theme: l === 'light' ? light : dark, minimumContrastRatio: l === 'light' ? 4.5 : 1 })

const wsURL = (id: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${V1}/terminals/${id}/ws`

export default function TerminalPage({ onMenu }: { onMenu?: () => void }) {
  const { hosts, theme: appTheme } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  // colors follow the app's light/dark theme unless picked here (per device)
  const [look, setLookRaw] = useState<Look>(() => (localStorage.getItem(LOOK_KEY) as Look) || 'app')
  const setLook = (l: Look) => { setLookRaw(l); if (l === 'app') localStorage.removeItem(LOOK_KEY); else localStorage.setItem(LOOK_KEY, l) }
  const shade: 'light' | 'dark' = look === 'app' ? appTheme : look
  const [params, setParams] = useSearchParams()
  const [list, setList] = useState<Term[] | null>(null)
  const [opening, setOpening] = useState(false)
  const [renaming, setRenaming] = useState<Term | null>(null)
  const active = params.get('t') || ''

  const load = useCallback(() => api.get<Term[]>(`${V1}/terminals`).then(setList).catch(e => { toast(errMsg(e), 'err'); setList(l => l || []) }), [toast])
  useEffect(() => { load() }, [load])

  const select = useCallback((id: string) => setParams(p => { const n = new URLSearchParams(p); if (id) n.set('t', id); else n.delete('t'); n.delete('host'); n.delete('cwd'); return n }, { replace: true }), [setParams])

  const open = useCallback(async (host_id: number, cwd = '') => {
    setOpening(true)
    try {
      const t = await api.post<Term>(`${V1}/terminals`, { host_id, cwd })
      setList(l => [...(l || []), t])
      select(t.id)
    } catch (e) { toast(errMsg(e), 'err') }
    setOpening(false)
  }, [select, toast])

  // ?host=<id>&cwd=…: "Open a terminal here" from elsewhere
  const asked = useRef(false)
  useEffect(() => {
    const h = params.get('host')
    if (h === null || asked.current) return
    asked.current = true
    open(Number(h) || 0, params.get('cwd') || '')
  }, [params, open])

  // the first one when none is picked (or the picked one is gone)
  useEffect(() => {
    if (!list || params.get('host') !== null) return
    if (!list.some(t => t.id === active)) select(list[0]?.id || '')
  }, [list, active, params, select])

  const close = async (t: Term) => {
    try { await api.del(`${V1}/terminals/${t.id}`) } catch (e) { toast(errMsg(e), 'err') }
    setList(l => {
      const rest = (l || []).filter(x => x.id !== t.id)
      if (t.id === active) select(rest[rest.length - 1]?.id || '')
      return rest
    })
  }
  const ended = useCallback((id: string, code: number) => setList(l => l && l.map(t => t.id === id ? { ...t, ended: true, exit_code: code } : t)), [])

  const machines: MenuItem[] = [
    { heading: 'Open a terminal on' },
    { label: 'This server', icon: 'terminal', onSelect: () => open(0) },
    ...hosts.map(h => ({ label: h.name, icon: 'terminal', hint: h.info?.ok === false ? 'not reachable' : undefined, onSelect: () => open(h.id) })),
    ...(hosts.length ? [] : ['sep' as const, { label: 'Add a server…', icon: 'plus', onSelect: () => navigate('/settings/agents') }]),
  ]

  const cur = list?.find(t => t.id === active)
  return (
    <div className="page term-page" data-term={shade}>
      <PageHead title="Terminal" icon="terminal" onMenu={onMenu}
        sub={cur ? <>{cur.host_name}{cur.cwd ? <> · <span className="mono">{cur.cwd}</span></> : null}</> : 'A shell on this server or any of your machines'}
        actions={<><MenuButton items={[
          { heading: 'Terminal colors' },
          { label: 'Same as the app', icon: 'palette', checked: look === 'app', onSelect: () => setLook('app') },
          { label: 'Light', icon: 'sun', checked: look === 'light', onSelect: () => setLook('light') },
          { label: 'Dark', icon: 'moon', checked: look === 'dark', onSelect: () => setLook('dark') },
        ]} label="Terminal colors" align="right"><Icon name={shade === 'light' ? 'sun' : 'moon'} size={16} /></MenuButton>
        <MenuButton items={machines} className="btn primary sm" label="New terminal" align="right">{opening ? <Spinner /> : <Icon name="plus" size={15} />} New</MenuButton></>} />
      {list === null ? <Loading /> : list.length === 0 ? (
        <div className="page-scroll"><div className="page-inner">
          <Empty title="No terminal open" icon="terminal">
            <p>Pick a machine. A terminal keeps running when you leave this page — come back and it carries on where it was.</p>
            <div className="row term-start">
              <button type="button" className="btn" disabled={opening} onClick={() => open(0)}><Icon name="terminal" size={15} /> This server</button>
              {hosts.map(h => <button key={h.id} type="button" className="btn" disabled={opening} onClick={() => open(h.id)}><Icon name="terminal" size={15} /> {h.name}</button>)}
            </div>
          </Empty>
        </div></div>
      ) : <>
        <div className="term-tabs" role="tablist">
          {list.map(t => (
            <div key={t.id} role="tab" aria-selected={t.id === active} className={`term-tab ${t.id === active ? 'on' : ''} ${t.ended ? 'ended' : ''}`}
              onClick={() => select(t.id)} onDoubleClick={() => setRenaming(t)} title={t.ended ? `Ended (exit ${t.exit_code})` : t.title}>
              <span className="term-dot" />
              <span className="term-tab-name">{t.title}</span>
              <button type="button" className="term-tab-x" aria-label={`Close ${t.title}`} onClick={e => { e.stopPropagation(); close(t) }}><Icon name="x" size={12} /></button>
            </div>
          ))}
        </div>
        <div className="term-stage">
          {list.map(t => <TermView key={t.id} term={t} shade={shade} visible={t.id === active} onEnded={ended}
            onAgain={() => { close(t); open(t.host_id, t.cwd) }} onClose={() => close(t)} />)}
        </div>
      </>}
      {renaming && <Rename term={renaming} onClose={() => setRenaming(null)} onDone={t => { setList(l => l && l.map(x => x.id === t.id ? t : x)); setRenaming(null) }} />}
    </div>
  )
}

function Rename({ term, onClose, onDone }: { term: Term; onClose: () => void; onDone: (t: Term) => void }) {
  const [v, setV] = useState(term.title)
  const { toast } = useToast()
  const save = async () => {
    try { onDone(await api.patch<Term>(`${V1}/terminals/${term.id}`, { title: v })) } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <Modal title="Rename terminal" size="sm" onClose={onClose}
      footer={<><button type="button" className="btn ghost" onClick={onClose}>Cancel</button><button type="button" className="btn primary" disabled={!v.trim()} onClick={save}>Save</button></>}>
      <input autoFocus aria-label="Terminal name" style={{ width: '100%' }} value={v} onFocus={e => e.currentTarget.select()} onChange={e => setV(e.target.value)} onKeyDown={e => { if (e.key === 'Enter' && v.trim()) save() }} />
    </Modal>
  )
}

// keys a phone keyboard lacks
const KEYS: { label: string; send?: string; ctrl?: boolean; icon?: string }[] = [
  { label: 'Esc', send: '\x1b' }, { label: 'Tab', send: '\t' }, { label: 'Ctrl', ctrl: true },
  { label: '↑', send: '\x1b[A' }, { label: '↓', send: '\x1b[B' }, { label: '←', send: '\x1b[D' }, { label: '→', send: '\x1b[C' },
  { label: '|', send: '|' }, { label: '~', send: '~' }, { label: '/', send: '/' }, { label: '-', send: '-' },
  { label: '^C', send: '\x03' }, { label: '^D', send: '\x04' },
]

function TermView({ term, shade, visible, onEnded, onAgain, onClose }: { term: Term; shade: 'light' | 'dark'; visible: boolean; onEnded: (id: string, code: number) => void; onAgain: () => void; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  const xt = useRef<Terminal | null>(null)
  const fit = useRef<FitAddon | null>(null)
  const ws = useRef<WebSocket | null>(null)
  const ctrl = useRef(false)
  const [ctrlOn, setCtrlOn] = useState(false)
  const [state, setState] = useState<'connecting' | 'live' | 'lost' | 'ended'>(term.ended ? 'ended' : 'connecting')
  const [code, setCode] = useState(term.exit_code)
  const [gone, setGone] = useState(false)
  const touch = useMediaQuery('(pointer: coarse)')
  const { toast } = useToast()

  const send = useCallback((s: string) => {
    const w = ws.current
    if (w && w.readyState === WebSocket.OPEN) w.send(new TextEncoder().encode(s))
  }, [])
  const resize = useCallback(() => {
    const t = xt.current, f = fit.current, w = ws.current
    if (!t || !f || !box.current || box.current.offsetParent === null) return
    try { f.fit() } catch { return }
    if (w && w.readyState === WebSocket.OPEN) w.send(JSON.stringify({ type: 'resize', cols: t.cols, rows: t.rows }))
  }, [])

  useEffect(() => {
    const t = new Terminal({
      ...xtermLook(shade), cursorBlink: true, allowProposedApi: true, scrollback: 10000, convertEol: false,
      fontFamily: 'ui-monospace, SFMono-Regular, "JetBrains Mono", Menlo, Consolas, "Symbols Nerd Font Mono", monospace',
      fontSize: window.matchMedia('(max-width: 760px)').matches ? 12 : 13,
    })
    const f = new FitAddon()
    t.loadAddon(f)
    t.loadAddon(new WebLinksAddon())
    t.open(box.current!)
    // icons drawn before the Nerd Font arrived: draw them again
    document.fonts?.load('13px "Symbols Nerd Font Mono"', '\uf07b').then(() => { if (xt.current === t) t.refresh(0, t.rows - 1) }).catch(() => {})
    xt.current = t
    fit.current = f
    t.onData(d => {
      if (ctrl.current && d.length === 1) {
        const c = d.toLowerCase().charCodeAt(0)
        if (c >= 97 && c <= 122) d = String.fromCharCode(c - 96)
        else if (d === '[') d = '\x1b'
        else if (d === ' ') d = '\x00'
        ctrl.current = false
        setCtrlOn(false)
      }
      send(d)
    })
    t.onBinary(d => { const w = ws.current; if (w?.readyState === WebSocket.OPEN) w.send(Uint8Array.from(d, c => c.charCodeAt(0))) })

    let dead = false, tries = 0, timer = 0
    const connect = () => {
      if (dead) return
      setState(s => s === 'ended' ? s : 'connecting')
      const w = new WebSocket(wsURL(term.id))
      w.binaryType = 'arraybuffer'
      ws.current = w
      let fresh = true, opened = false
      w.onopen = () => { opened = true; tries = 0; setState('live'); resize() }
      w.onmessage = ev => {
        if (typeof ev.data === 'string') {
          try {
            const m = JSON.parse(ev.data)
            if (m.type === 'end') { dead = true; setCode(m.code); setState('ended'); onEnded(term.id, m.code) }
          } catch { /* not ours */ }
          return
        }
        if (fresh) { t.reset(); fresh = false } // a reconnect replays the scrollback
        t.write(new Uint8Array(ev.data))
      }
      w.onclose = ev => {
        if (ws.current === w) ws.current = null
        if (dead) return
        setState('lost')
        const again = () => { timer = window.setTimeout(connect, Math.min(1000 * 2 ** tries++, 10000)) }
        if (opened) return again()
        // refused: is it still there at all? (a restart of Pocket AI ends every terminal)
        api.get<Term[]>(`${V1}/terminals`).then(l => {
          if (dead) return
          if (l.some(x => x.id === term.id)) return again()
          dead = true; setGone(true); setState('ended')
        }).catch(again)
      }
    }
    connect()
    const ro = new ResizeObserver(() => resize())
    ro.observe(box.current!)
    return () => { dead = true; clearTimeout(timer); ro.disconnect(); ws.current?.close(); t.dispose(); xt.current = null }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [term.id])

  useEffect(() => { if (xt.current) Object.assign(xt.current.options, xtermLook(shade)) }, [shade])
  useEffect(() => { if (visible) { requestAnimationFrame(() => { resize(); xt.current?.focus() }) } }, [visible, resize])

  const key = (k: typeof KEYS[number]) => {
    if (k.ctrl) { ctrl.current = !ctrl.current; setCtrlOn(ctrl.current) }
    else if (k.send) send(k.send)
    xt.current?.focus()
  }
  const paste = async () => {
    try { const s = await navigator.clipboard.readText(); if (s) xt.current?.paste(s) } catch { toast('The browser did not allow reading the clipboard', 'err') }
    xt.current?.focus()
  }

  return (
    <div className="term-view" hidden={!visible}>
      <div className="term-box" ref={box} />
      {state === 'connecting' && <div className="term-note"><Spinner size={12} /> Connecting…</div>}
      {state === 'lost' && <div className="term-note"><Spinner size={12} /> Connection lost — reconnecting…</div>}
      {state === 'ended' && (
        <div className="term-ended">
          <span>{gone ? 'This terminal is gone — Pocket AI was restarted.' : `The shell ended${code ? ` (exit ${code})` : ''}.`}</span>
          <button type="button" className="btn sm primary" onClick={onAgain}><Icon name="refresh" size={14} /> Start again</button>
          <button type="button" className="btn sm ghost" onClick={onClose}>Close</button>
        </div>
      )}
      {touch && state !== 'ended' && (
        <div className="term-keys" onMouseDown={e => e.preventDefault()}>
          {KEYS.map(k => <button key={k.label} type="button" className={`term-key ${k.ctrl && ctrlOn ? 'on' : ''}`} onClick={() => key(k)}>{k.label}</button>)}
          <button type="button" className="term-key" onClick={paste} aria-label="Paste"><Icon name="copy" size={14} /></button>
        </div>
      )}
    </div>
  )
}
