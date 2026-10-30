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
import { RunDots } from '../components/RunningList'
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
// light: the palette itself keeps "white" and light grey readable on white
// (xterm's minimumContrastRatio is not used: it measures reverse video, like
// bash's pasted-text highlight, against the page instead of the highlight
// and turns that text grey on dark)
const xtermLook = (l: 'light' | 'dark') => ({ theme: l === 'light' ? light : dark, minimumContrastRatio: 1 })

const wsURL = (id: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${V1}/terminals/${id}/ws`

// A tab holds one terminal or a split of two (each side again one or a
// split): t = a terminal's id; dir row = side by side, col = one above the
// other; r = the first side's share.
type Node = { t: string } | { dir: 'row' | 'col'; a: Node; b: Node; r: number }
const isLeaf = (n: Node): n is { t: string } => 't' in n
const idsOf = (n: Node): string[] => isLeaf(n) ? [n.t] : [...idsOf(n.a), ...idsOf(n.b)]
// without the terminals that are gone; a split with one side left becomes that side
const prune = (n: Node, alive: Set<string>): Node | null => {
  if (isLeaf(n)) return alive.has(n.t) ? n : null
  const a = prune(n.a, alive), b = prune(n.b, alive)
  return a && b ? { ...n, a, b } : a || b
}
const replaceLeaf = (n: Node, id: string, by: Node): Node =>
  isLeaf(n) ? (n.t === id ? by : n) : { ...n, a: replaceLeaf(n.a, id, by), b: replaceLeaf(n.b, id, by) }
const setRatio = (n: Node, path: string, r: number): Node =>
  isLeaf(n) ? n : path === '' ? { ...n, r } : path[0] === 'a' ? { ...n, a: setRatio(n.a, path.slice(1), r) } : { ...n, b: setRatio(n.b, path.slice(1), r) }
const validNode = (n: unknown): n is Node => {
  const o = n as Record<string, unknown>
  if (!o || typeof o !== 'object') return false
  if (typeof o.t === 'string') return true
  return (o.dir === 'row' || o.dir === 'col') && typeof o.r === 'number' && validNode(o.a) && validNode(o.b)
}
// the saved arrangement fitted to the terminals there are: gone ones out,
// new ones (opened elsewhere) as tabs of their own
const reconcile = (tabs: Node[], list: Term[]): Node[] => {
  const alive = new Set(list.map(t => t.id))
  const out = tabs.map(n => prune(n, alive)).filter((n): n is Node => !!n)
  const placed = new Set(out.flatMap(idsOf))
  for (const t of list) if (!placed.has(t.id)) out.push({ t: t.id })
  return out
}
const sameTabs = (a: Node[], b: Node[]) => JSON.stringify(a) === JSON.stringify(b)

export default function TerminalPage({ onMenu }: { onMenu?: () => void }) {
  const { hosts, theme: appTheme, terms } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  // colors follow the app's light/dark theme unless picked here (per device)
  const [look, setLookRaw] = useState<Look>(() => (localStorage.getItem(LOOK_KEY) as Look) || 'app')
  const setLook = (l: Look) => { setLookRaw(l); if (l === 'app') localStorage.removeItem(LOOK_KEY); else localStorage.setItem(LOOK_KEY, l) }
  const shade: 'light' | 'dark' = look === 'app' ? appTheme : look
  const [params, setParams] = useSearchParams()
  const [list, setList] = useState<Term[] | null>(null)
  const [tabs, setTabsRaw] = useState<Node[]>([])
  const [opening, setOpening] = useState(false)
  const [renaming, setRenaming] = useState<Term | null>(null)
  const active = params.get('t') || ''
  // a terminal just opened takes the focus: the URL catches up a render later,
  // and the pane that moved into the split must not take it back meanwhile
  const want = useRef('')
  useEffect(() => { if (active === want.current) want.current = '' }, [active])

  // the arrangement is kept on the server (every device shows the same)
  const loaded = useRef(false)
  const saveTimer = useRef(0)
  const setTabs = useCallback((f: (t: Node[]) => Node[]) => setTabsRaw(prev => {
    const next = f(prev)
    if (loaded.current && !sameTabs(prev, next)) {
      clearTimeout(saveTimer.current)
      saveTimer.current = window.setTimeout(() => { api.put(`${V1}/terminals/layout`, { layout: next }).catch(() => {}) }, 400)
    }
    return next
  }), [])

  const load = useCallback(() => Promise.all([
    api.get<Term[]>(`${V1}/terminals`),
    api.get<{ layout: unknown }>(`${V1}/terminals/layout`).catch(() => ({ layout: null })),
  ]).then(([l, lay]) => {
    const saved = Array.isArray(lay.layout) ? (lay.layout as unknown[]).filter(validNode) : []
    setList(l)
    setTabsRaw(reconcile(saved, l))
    loaded.current = true
  }).catch(e => { toast(errMsg(e), 'err'); setList(l => l || []) }), [toast])
  useEffect(() => { load() }, [load])
  useEffect(() => { if (list) setTabs(t => reconcile(t, list)) }, [list, setTabs])

  const select = useCallback((id: string) => setParams(p => {
    if (p.get('t') === id && p.get('host') === null) return p
    const n = new URLSearchParams(p); if (id) n.set('t', id); else n.delete('t'); n.delete('host'); n.delete('cwd'); return n
  }, { replace: true }), [setParams])

  const create = useCallback(async (host_id: number, cwd = '') => {
    setOpening(true)
    try { return await api.post<Term>(`${V1}/terminals`, { host_id, cwd }) } catch (e) { toast(errMsg(e), 'err'); return null } finally { setOpening(false) }
  }, [toast])
  const open = useCallback(async (host_id: number, cwd = '') => {
    const t = await create(host_id, cwd)
    if (!t) return
    want.current = t.id
    setTabs(tb => [...tb, { t: t.id }])
    setList(l => [...(l || []), t])
    select(t.id)
  }, [create, select, setTabs])
  // a new terminal next to the focused one, on the same machine and folder
  const split = async (dir: 'row' | 'col', id = active) => {
    const cur = list?.find(t => t.id === id)
    if (!cur) return
    const t = await create(cur.host_id, cur.cwd)
    if (!t) return
    want.current = t.id
    setTabs(tb => tb.map(n => replaceLeaf(n, id, { dir, a: { t: id }, b: { t: t.id }, r: .5 })))
    setList(l => [...(l || []), t])
    select(t.id)
  }

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
    if (!list || params.get('host') !== null || want.current) return
    if (!list.some(t => t.id === active)) select(tabs[0] ? idsOf(tabs[0])[0] : list[0]?.id || '')
  }, [list, tabs, active, params, select])

  const close = async (ids: string[]) => {
    await Promise.all(ids.map(id => api.del(`${V1}/terminals/${id}`).catch(e => toast(errMsg(e), 'err'))))
    const gone = new Set(ids)
    if (gone.has(active)) {
      // focus moves to the pane beside it, else the tab before
      const i = tabs.findIndex(n => idsOf(n).includes(active))
      const same = i >= 0 ? idsOf(tabs[i]).filter(x => !gone.has(x)) : []
      const others = tabs.flatMap(idsOf).filter(x => !gone.has(x))
      const before = tabs.slice(0, Math.max(i, 0)).flatMap(idsOf).filter(x => !gone.has(x))
      select(same[0] || before[before.length - 1] || others[0] || '')
    }
    setList(l => (l || []).filter(x => !gone.has(x.id)))
  }
  const ended = useCallback((id: string, code: number) => setList(l => l && l.map(t => t.id === id ? { ...t, ended: true, exit_code: code } : t)), [])
  const again = async (t: Term) => {
    const n = await create(t.host_id, t.cwd)
    if (!n) return
    want.current = n.id
    setTabs(tb => tb.map(x => replaceLeaf(x, t.id, { t: n.id })))
    setList(l => [...(l || []).filter(x => x.id !== t.id), n])
    select(n.id)
    api.del(`${V1}/terminals/${t.id}`).catch(() => {})
  }

  const machines: MenuItem[] = [
    { heading: 'Open a terminal on' },
    { label: 'This server', icon: 'terminal', onSelect: () => open(0) },
    ...hosts.map(h => ({ label: h.name, icon: 'terminal', hint: h.info?.ok === false ? 'not reachable' : undefined, onSelect: () => open(h.id) })),
    ...(hosts.length ? [] : ['sep' as const, { label: 'Add a server…', icon: 'plus', onSelect: () => navigate('/settings/agents') }]),
  ]

  const byId = new Map((list || []).map(t => [t.id, t]))
  const cur = byId.get(active)
  const busy = new Set(terms.busy_ids)
  const activeTab = tabs.findIndex(n => idsOf(n).includes(active))

  const renderNode = (n: Node, path: string, many: boolean): React.ReactNode => {
    if (isLeaf(n)) {
      const t = byId.get(n.t)
      if (!t) return null
      const focused = t.id === active
      return (
        <div key={t.id} className={`term-pane ${many ? 'many' : ''} ${focused ? 'focused' : ''}`} onFocusCapture={() => { if (!want.current || want.current === t.id) select(t.id) }} onMouseDown={() => { want.current = ''; select(t.id) }}>
          {many && (
            <div className="term-pane-h">
              {!t.ended && busy.has(t.id) ? <span className="term-busy"><RunDots size={9} /></span> : <span className={`term-dot ${t.ended ? 'off' : ''}`} />}
              <span className="term-pane-name" onDoubleClick={() => setRenaming(t)} title={t.cwd ? `${t.host_name} · ${t.cwd}` : t.host_name}>{t.title}</span>
              <button type="button" className="term-pane-btn" title="Split right" aria-label="Split right" onClick={() => split('row', t.id)}><Icon name="split-right" size={13} /></button>
              <button type="button" className="term-pane-btn" title="Split down" aria-label="Split down" onClick={() => split('col', t.id)}><Icon name="split-down" size={13} /></button>
              <button type="button" className="term-pane-btn" title="Close this pane" aria-label={`Close ${t.title}`} onClick={() => close([t.id])}><Icon name="x" size={13} /></button>
            </div>
          )}
          <TermView term={t} shade={shade} visible={idsOf(tabs[activeTab] || { t: '' }).includes(t.id)} focused={focused} onEnded={ended}
            onAgain={() => again(t)} onClose={() => close([t.id])} />
        </div>
      )
    }
    return (
      <div key={path || 'root'} className={`term-split term-${n.dir}`}>
        <div className="term-side" style={{ flex: `${n.r} 1 0` }}>{renderNode(n.a, path + 'a', true)}</div>
        <Divider dir={n.dir} onRatio={r => setTabs(tb => tb.map(x => idsOf(x).includes(idsOf(n)[0]) ? setRatio(x, path, r) : x))} />
        <div className="term-side" style={{ flex: `${1 - n.r} 1 0` }}>{renderNode(n.b, path + 'b', true)}</div>
      </div>
    )
  }

  return (
    <div className="page term-page" data-term={shade}>
      <PageHead title="Terminal" icon="terminal" onMenu={onMenu}
        sub={cur ? <>{cur.host_name}{cur.cwd ? <> · <span className="mono">{cur.cwd}</span></> : null}</> : 'A shell on this server or any of your machines'}
        actions={<>
          {cur && <>
            <button type="button" className="btn ghost icon-only" title="Split right" aria-label="Split right" disabled={opening} onClick={() => split('row')}><Icon name="split-right" size={16} /></button>
            <button type="button" className="btn ghost icon-only" title="Split down" aria-label="Split down" disabled={opening} onClick={() => split('col')}><Icon name="split-down" size={16} /></button>
          </>}
          <MenuButton items={[
            { heading: 'Terminal colors' },
            { label: 'Same as the app', icon: 'palette', checked: look === 'app', onSelect: () => setLook('app') },
            { label: 'Light', icon: 'sun', checked: look === 'light', onSelect: () => setLook('light') },
            { label: 'Dark', icon: 'moon', checked: look === 'dark', onSelect: () => setLook('dark') },
          ]} label="Terminal colors" align="right"><Icon name={shade === 'light' ? 'sun' : 'moon'} size={16} /></MenuButton>
          <MenuButton items={machines} className="btn primary sm" label="New terminal" align="right">{opening ? <Spinner /> : <Icon name="plus" size={15} />} New</MenuButton>
        </>} />
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
          {tabs.map((n, i) => {
            const ids = idsOf(n)
            const ts = ids.map(id => byId.get(id)).filter((t): t is Term => !!t)
            if (!ts.length) return null
            const lead = ts.find(t => t.id === active) || ts[0]
            const allEnded = ts.every(t => t.ended)
            const anyBusy = ts.some(t => !t.ended && busy.has(t.id))
            return (
              <div key={ids[0]} role="tab" aria-selected={i === activeTab} className={`term-tab ${i === activeTab ? 'on' : ''} ${allEnded ? 'ended' : ''}`}
                onClick={() => { if (i !== activeTab) select(lead.id) }} onDoubleClick={() => setRenaming(lead)}
                title={ts.map(t => t.title + (t.ended ? ` (ended, exit ${t.exit_code})` : '')).join('\n')}>
                {anyBusy ? <span className="term-busy" title="Running a command"><RunDots size={10} /></span> : <span className="term-dot" />}
                <span className="term-tab-name">{lead.title}</span>
                {ts.length > 1 && <span className="term-tab-n" title={`${ts.length} panes`}><Icon name="split-right" size={11} />{ts.length}</span>}
                <button type="button" className="term-tab-x" aria-label={`Close ${lead.title}${ts.length > 1 ? ` and ${ts.length - 1} more` : ''}`} onClick={e => { e.stopPropagation(); close(ids) }}><Icon name="x" size={12} /></button>
              </div>
            )
          })}
        </div>
        <div className="term-stage">
          {tabs.map((n, i) => <div key={idsOf(n)[0]} className="term-tabview" hidden={i !== activeTab}>{renderNode(n, '', false)}</div>)}
        </div>
      </>}
      {renaming && <Rename term={renaming} onClose={() => setRenaming(null)} onDone={t => { setList(l => l && l.map(x => x.id === t.id ? t : x)); setRenaming(null) }} />}
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
