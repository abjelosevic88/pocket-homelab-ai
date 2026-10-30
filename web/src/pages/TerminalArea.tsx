// Terminals: a shell on this server or any of your machines, as tabs next to
// your conversations. They keep running when you leave: coming back replays
// what they printed.
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
import { RunDots } from '../components/RunningList'
import { idsOf, isLeaf, type Node, type Term, type TermGroup, useMachineItems, useTerminals } from '../lib/terminals'
import { Empty, errMsg, Loading, type MenuItem, MenuButton, Modal, Spinner, useMediaQuery, useToast } from '../components/ui'


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
// light: the palette itself keeps "white" and light grey readable on white
// (xterm's minimumContrastRatio is not used: it measures reverse video, like
// bash's pasted-text highlight, against the page instead of the highlight
// and turns that text grey on dark)
const xtermLook = (l: 'light' | 'dark') => ({ theme: l === 'light' ? light : dark, minimumContrastRatio: 1 })

const wsURL = (id: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${V1}/terminals/${id}/ws`

const LAST = 'pai-term-last'
const asking = { key: '', at: 0 }

// Every terminal tab, mounted once and kept (shells stay connected while you
// are in a conversation); shown when the address is /term/<tab>.
export default function TerminalArea({ onMenu, shown, gid, tabsOn }: { onMenu?: () => void; shown: boolean; gid: string; tabsOn: boolean }) {
  const { hosts, theme: appTheme, terms } = useApp()
  const tm = useTerminals()
  const { list, groups, byId, opening } = tm
  const navigate = useNavigate()
  const [params] = useSearchParams()
  // colors follow the app's light/dark theme unless picked here (per device)
  const [look, setLookRaw] = useState<Look>(() => (localStorage.getItem(LOOK_KEY) as Look) || 'app')
  const setLook = (l: Look) => { setLookRaw(l); if (l === 'app') localStorage.removeItem(LOOK_KEY); else localStorage.setItem(LOOK_KEY, l) }
  const shade: 'light' | 'dark' = look === 'app' ? appTheme : look
  const [renaming, setRenaming] = useState<Term | null>(null)

  const openTab = useCallback(async (host_id: number, cwd = '') => {
    const g = await tm.open(host_id, cwd)
    if (g) navigate(`/term/${g}`)
  }, [tm, navigate])
  const machines = useMachineItems(h => { openTab(h) })

  // /term?host=<id>&cwd=…: "Open a terminal here" from a conversation (once:
  // the page can mount twice while it loads, so the guard is not per mount)
  useEffect(() => {
    const h = params.get('host')
    const key = `${h}|${params.get('cwd') || ''}`
    if (!shown || h === null || (asking.key === key && Date.now() - asking.at < 10e3)) return
    asking.key = key; asking.at = Date.now()
    tm.open(Number(h) || 0, params.get('cwd') || '').then(g => navigate(g ? `/term/${g}` : '/term', { replace: true }))
  }, [shown, params, tm, navigate])
  // /term alone, or a tab that is gone: the last one used, else the empty page
  useEffect(() => {
    if (!shown || !list || params.get('host') !== null) return
    if (gid && groups.some(g => g.id === gid)) { localStorage.setItem(LAST, gid); return }
    const last = localStorage.getItem(LAST)
    const to = groups.find(g => g.id === last) || groups[groups.length - 1]
    if (to) navigate(`/term/${to.id}`, { replace: true })
    else if (gid) navigate('/term', { replace: true })
  }, [shown, gid, list, groups, params, navigate])

  const g = groups.find(x => x.id === gid)
  const cur = g ? byId.get(g.focus) : undefined
  const busy = new Set(terms.busy_ids)

  const renderNode = (grp: TermGroup, n: Node, path: string, many: boolean): React.ReactNode => {
    if (isLeaf(n)) {
      const t = byId.get(n.t)
      if (!t) return null
      const focused = t.id === grp.focus
      return (
        <div key={t.id} className={`term-pane ${many ? 'many' : ''} ${focused ? 'focused' : ''}`} onFocusCapture={() => tm.focus(grp.id, t.id)} onMouseDown={() => tm.focus(grp.id, t.id)}>
          {many && (
            <div className="term-pane-h">
              {!t.ended && busy.has(t.id) ? <span className="term-busy"><RunDots size={9} /></span> : <span className={`term-dot ${t.ended ? 'off' : ''}`} />}
              <span className="term-pane-name" onDoubleClick={() => setRenaming(t)} title={t.cwd ? `${t.host_name} · ${t.cwd}` : t.host_name}>{t.title}</span>
              <button type="button" className="term-pane-btn" title="Split right" aria-label="Split right" onClick={() => tm.split(grp.id, t.id, 'row')}><Icon name="split-right" size={13} /></button>
              <button type="button" className="term-pane-btn" title="Split down" aria-label="Split down" onClick={() => tm.split(grp.id, t.id, 'col')}><Icon name="split-down" size={13} /></button>
              <button type="button" className="term-pane-btn" title="Close this pane" aria-label={`Close ${t.title}`} onClick={() => tm.closePanes([t.id])}><Icon name="x" size={13} /></button>
            </div>
          )}
          <TermView term={t} shade={shade} visible={shown && grp.id === gid} focused={focused} onEnded={tm.ended}
            onAgain={() => tm.again(t)} onClose={() => tm.closePanes([t.id])} />
        </div>
      )
    }
    return (
      <div key={path || 'root'} className={`term-split term-${n.dir}`}>
        <div className="term-side" style={{ flex: `${n.r} 1 0` }}>{renderNode(grp, n.a, path + 'a', true)}</div>
        <Divider dir={n.dir} onRatio={r => tm.setRatio(grp.id, path, r)} />
        <div className="term-side" style={{ flex: `${1 - n.r} 1 0` }}>{renderNode(grp, n.b, path + 'b', true)}</div>
      </div>
    )
  }

  const tabTitle = (x: TermGroup) => byId.get(x.focus)?.title || byId.get(idsOf(x.root)[0])?.title || 'Terminal'
  return (
    <div className="page term-page" data-term={shade} hidden={!shown}>
      <PageHead title={cur ? cur.title : 'Terminal'} icon="terminal" onMenu={onMenu}
        sub={cur ? <>{cur.host_name}{cur.cwd ? <> · <span className="mono">{cur.cwd}</span></> : null}</> : 'A shell on this server or any of your machines'}
        actions={<>
          {g && cur && <>
            <button type="button" className="btn ghost icon-only" title="Split right" aria-label="Split right" disabled={opening} onClick={() => tm.split(g.id, cur.id, 'row')}><Icon name="split-right" size={16} /></button>
            <button type="button" className="btn ghost icon-only" title="Split down" aria-label="Split down" disabled={opening} onClick={() => tm.split(g.id, cur.id, 'col')}><Icon name="split-down" size={16} /></button>
          </>}
          <MenuButton items={[
            { heading: 'Terminal colors' },
            { label: 'Same as the app', icon: 'palette', checked: look === 'app', onSelect: () => setLook('app') },
            { label: 'Light', icon: 'sun', checked: look === 'light', onSelect: () => setLook('light') },
            { label: 'Dark', icon: 'moon', checked: look === 'dark', onSelect: () => setLook('dark') },
          ]} label="Terminal colors" align="right"><Icon name={shade === 'light' ? 'sun' : 'moon'} size={16} /></MenuButton>
          <MenuButton items={machines} className="btn primary sm" label="New terminal" align="right">{opening ? <Spinner /> : <Icon name="plus" size={15} />} New</MenuButton>
        </>} />
      {list === null ? <Loading /> : groups.length === 0 ? (
        <div className="page-scroll"><div className="page-inner">
          <Empty title="No terminal open" icon="terminal">
            <p>Pick a machine. A terminal opens as a tab next to your conversations and keeps running when you switch away.</p>
            <div className="row term-start">
              <button type="button" className="btn" disabled={opening} onClick={() => openTab(0)}><Icon name="terminal" size={15} /> This server</button>
              {hosts.map(h => <button key={h.id} type="button" className="btn" disabled={opening} onClick={() => openTab(h.id)}><Icon name="terminal" size={15} /> {h.name}</button>)}
            </div>
          </Empty>
        </div></div>
      ) : <>
        {/* without the tab bar (phone, tabs off) the terminal tabs show here */}
        {!tabsOn && <div className="term-tabs" role="tablist">
          {groups.map(x => {
            const ids = idsOf(x.root)
            const ts = ids.map(id => byId.get(id)).filter((t): t is Term => !!t)
            return (
              <div key={x.id} role="tab" aria-selected={x.id === gid} className={`term-tab ${x.id === gid ? 'on' : ''} ${ts.every(t => t.ended) ? 'ended' : ''}`}
                onClick={() => navigate(`/term/${x.id}`)}>
                {ts.some(t => !t.ended && busy.has(t.id)) ? <span className="term-busy"><RunDots size={10} /></span> : <span className="term-dot" />}
                <span className="term-tab-name">{tabTitle(x)}</span>
                {ts.length > 1 && <span className="term-tab-n"><Icon name="split-right" size={11} />{ts.length}</span>}
                <button type="button" className="term-tab-x" aria-label={`Close ${tabTitle(x)}`} onClick={e => { e.stopPropagation(); tm.closeGroups([x.id]) }}><Icon name="x" size={12} /></button>
              </div>
            )
          })}
        </div>}
        <div className={`term-stage ${tabsOn ? 'flat' : ''}`}>
          {groups.map(x => <div key={x.id} className="term-tabview" hidden={x.id !== gid}>{renderNode(x, x.root, '', false)}</div>)}
        </div>
      </>}
      {renaming && <Rename term={renaming} onClose={() => setRenaming(null)} onDone={t => { tm.renamed(t); setRenaming(null) }} />}
    </div>
  )
}

// the bar between two panes: drag to share the space differently
function Divider({ dir, onRatio }: { dir: 'row' | 'col'; onRatio: (r: number) => void }) {
  const down = (e: React.PointerEvent<HTMLDivElement>) => {
    const box = e.currentTarget.parentElement!.getBoundingClientRect()
    e.currentTarget.setPointerCapture(e.pointerId)
    e.preventDefault()
    const move = (ev: PointerEvent) => {
      const r = dir === 'row' ? (ev.clientX - box.left) / box.width : (ev.clientY - box.top) / box.height
      onRatio(Math.min(.85, Math.max(.15, r)))
    }
    const el = e.currentTarget
    const up = () => { el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up) }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }
  return <div className={`term-divider term-${dir}`} role="separator" aria-orientation={dir === 'row' ? 'vertical' : 'horizontal'} onPointerDown={down} />
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

function TermView({ term, shade, visible, focused, onEnded, onAgain, onClose }: { term: Term; shade: 'light' | 'dark'; visible: boolean; focused: boolean; onEnded: (id: string, code: number) => void; onAgain: () => void; onClose: () => void }) {
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
  useEffect(() => { if (visible) requestAnimationFrame(() => resize()) }, [visible, resize])
  useEffect(() => { if (visible && focused) requestAnimationFrame(() => xt.current?.focus()) }, [visible, focused])

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
    <div className="term-view">
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
      {touch && focused && state !== 'ended' && (
        <div className="term-keys" onMouseDown={e => e.preventDefault()}>
          {KEYS.map(k => <button key={k.label} type="button" className={`term-key ${k.ctrl && ctrlOn ? 'on' : ''}`} onClick={() => key(k)}>{k.label}</button>)}
          <button type="button" className="term-key" onClick={paste} aria-label="Paste"><Icon name="copy" size={14} /></button>
        </div>
      )}
    </div>
  )
}
