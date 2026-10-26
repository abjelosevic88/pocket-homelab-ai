// The prompt box: text, images (paste / drop / camera / photo library), dictation, slash menu (recipes + the CLI's skills).
import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { api, uploadWithProgress, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { isTouch } from '../lib/format'
import { parentContext, parentContextItem, parentTabs, type ParentContextItem } from '../lib/embed'
import type { ProviderId, Recipe, Skill, Upload } from '../lib/types'
import { Icon } from './icons'
import { errMsg, Spinner, useDismiss, useToast, noGrammarly } from './ui'

interface Attachment {
  lid: string
  name: string
  preview: string
  image?: boolean
  progress: number
  upload?: Upload
  error?: string
  abort?: () => void
}

export interface ComposerHandle { focus: () => void; setText: (t: string) => void; addFiles: (f: File[]) => void; restoreDraft: (text: string, uploads: Upload[]) => void }

const MAX_EDGE = 2000
const MAX_BYTES = 20 * 1024 * 1024
const DOC_TYPES = '.pdf,.docx,.xlsx,.pptx,.odt,.ods,.odp,.txt,.md,.csv,.tsv,.json,.xml'

/** Big photos are scaled down in the browser before upload: faster on mobile data, and
 *  the CLIs refuse very large images anyway. Screenshots under the limit pass as they are. */
async function shrink(file: File): Promise<File> {
  if (!file.type.startsWith('image/') || file.type === 'image/gif' || file.type === 'image/svg+xml') return file
  try {
    const bmp = await createImageBitmap(file)
    const edge = Math.max(bmp.width, bmp.height)
    const heic = /heic|heif/i.test(file.type) || /\.hei[cf]$/i.test(file.name)
    if (edge <= MAX_EDGE && file.size <= 3 * 1024 * 1024 && !heic) { bmp.close(); return file }
    const scale = Math.min(1, MAX_EDGE / edge)
    const c = document.createElement('canvas')
    c.width = Math.round(bmp.width * scale); c.height = Math.round(bmp.height * scale)
    c.getContext('2d')!.drawImage(bmp, 0, 0, c.width, c.height)
    bmp.close()
    const png = file.type === 'image/png' && !heic
    const blob = await new Promise<Blob | null>(r => c.toBlob(r, png ? 'image/png' : 'image/jpeg', 0.88))
    if (!blob || (png && blob.size > file.size && edge <= MAX_EDGE)) return file
    const name = (file.name || 'image').replace(/\.[^.]+$/, '') + (png ? '.png' : '.jpg')
    return new File([blob], name, { type: blob.type })
  } catch { return file }
}

let lidSeq = 0

/** Where a message would run: decides which skills the CLI offers. */
export interface SkillScope { provider: ProviderId; cwd?: string; account_id?: number; host_id?: number }

const skillCache = new Map<string, { at: number; skills: Skill[] }>()

/** The skills for a scope, fetched once the slash menu opens (cached a minute). */
function useSkills(scope: SkillScope | undefined, wanted: boolean): Skill[] {
  const key = scope ? `${scope.provider}|${scope.cwd || ''}|${scope.account_id || 0}|${scope.host_id || 0}` : ''
  const [skills, setSkills] = useState<Skill[]>(() => skillCache.get(key)?.skills || [])
  useEffect(() => {
    if (!scope || !wanted) return
    const hit = skillCache.get(key)
    if (hit) setSkills(hit.skills)
    if (hit && Date.now() - hit.at < 60_000) return
    let live = true
    const q = new URLSearchParams({ provider: scope.provider, cwd: scope.cwd || '', account: String(scope.account_id || 0), host: String(scope.host_id || 0) })
    api.get<{ skills: Skill[] }>(`${V1}/skills?${q}`).then(r => {
      skillCache.set(key, { at: Date.now(), skills: r.skills || [] })
      if (live) setSkills(r.skills || [])
    }).catch(() => {})
    return () => { live = false }
  }, [key, wanted]) // eslint-disable-line react-hooks/exhaustive-deps
  return scope ? skills : []
}

function draftAttachments(key?: string): Attachment[] {
  if (!key) return []
  try {
    const uploads: Upload[] = JSON.parse(sessionStorage.getItem('pai-draft-uploads-' + key) || '[]')
    return uploads.map(upload => ({ lid: 'a' + ++lidSeq, name: upload.name, preview: upload.url, image: upload.mime.startsWith('image/'), progress: 1, upload }))
  } catch { return [] }
}

type SpeechCtor = new () => { lang: string; continuous: boolean; interimResults: boolean; start: () => void; stop: () => void; onresult: ((e: { resultIndex: number; results: ArrayLike<{ isFinal: boolean; 0: { transcript: string } }> }) => void) | null; onend: (() => void) | null; onerror: ((e: { error: string }) => void) | null }

export const Composer = React.forwardRef<ComposerHandle, {
  onSend: (text: string, uploadIds: string[], sendNow?: boolean) => Promise<boolean>
  placeholder?: string
  autoFocus?: boolean
  running?: boolean
  onStop?: () => void
  disabled?: boolean
  footer?: React.ReactNode
  draftKey?: string
  big?: boolean
  /** Offer this agent's skills in the slash menu. */
  skills?: SkillScope
}>(function Composer({ onSend, placeholder = 'Ask anything…', autoFocus, running, onStop, disabled, footer, draftKey, big, skills: skillScope }, ref) {
  const { settings, recipes } = useApp()
  const { toast } = useToast()
  const [text, setText] = useState(() => (draftKey && sessionStorage.getItem('pai-draft-' + draftKey)) || '')
  const [atts, setAtts] = useState<Attachment[]>(() => draftAttachments(draftKey))
  const [sending, setSending] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [listening, setListening] = useState(false)
  const [slashHi, setSlashHi] = useState(0)
  // "@": the framing page's items (Pocket Projects: the issue's notes, files, links)
  const [caret, setCaret] = useState(0)
  const [atHi, setAtHi] = useState(0)
  const [atShut, setAtShut] = useState('')
  const [ctx, setCtx] = useState<{ label: string; items: ParentContextItem[] } | null>(null)
  const [ctxBusy, setCtxBusy] = useState(false)
  // the menus open above the box; with no room there (a draft filling the panel) they open inside it
  const box = useRef<HTMLDivElement>(null)
  const [menuInside, setMenuInside] = useState(false)
  const ta = useRef<HTMLTextAreaElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const photoInput = useRef<HTMLInputElement>(null)
  const cameraInput = useRef<HTMLInputElement>(null)
  const docInput = useRef<HTMLInputElement>(null)
  const attachMenu = useRef<HTMLDivElement>(null)
  const [attachOpen, setAttachOpen] = useState(false)
  useDismiss(attachMenu, () => setAttachOpen(false), attachOpen)
  const rec = useRef<InstanceType<SpeechCtor> | null>(null)
  const Speech = (window as unknown as { SpeechRecognition?: SpeechCtor; webkitSpeechRecognition?: SpeechCtor }).SpeechRecognition || (window as unknown as { webkitSpeechRecognition?: SpeechCtor }).webkitSpeechRecognition

  // drafts survive switching threads
  useEffect(() => { if (draftKey) { setText(sessionStorage.getItem('pai-draft-' + draftKey) || ''); setAtts(draftAttachments(draftKey)) } }, [draftKey])
  useEffect(() => { if (draftKey) { if (text) sessionStorage.setItem('pai-draft-' + draftKey, text); else sessionStorage.removeItem('pai-draft-' + draftKey) } }, [text, draftKey])

  useEffect(() => {
    if (!draftKey) return
    const uploads = atts.flatMap(a => a.upload ? [a.upload] : [])
    if (uploads.length) sessionStorage.setItem('pai-draft-uploads-' + draftKey, JSON.stringify(uploads))
    else sessionStorage.removeItem('pai-draft-uploads-' + draftKey)
  }, [atts, draftKey])

  useLayoutEffect(() => {
    const el = ta.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, big ? 360 : 280) + 'px'
  }, [text, big])
  useEffect(() => { if (autoFocus && !isTouch()) ta.current?.focus() }, [autoFocus, draftKey])

  const addFiles = useCallback(async (files: File[]) => {
    const imgs = files.filter(f => f.type.startsWith('image/') || /\.(png|jpe?g|gif|webp|hei[cf]|pdf|docx|xlsx|pptx|odt|ods|odp|txt|md|csv|tsv|json|xml)$/i.test(f.name))
    if (imgs.length < files.length) toast('Attach images, PDF, Office documents, CSV or text', 'err')
    for (const raw of imgs) {
      const lid = 'a' + ++lidSeq
      const preview = URL.createObjectURL(raw)
      setAtts(a => [...a, { lid, name: raw.name || 'image', image: raw.type.startsWith('image/'), preview, progress: 0 }])
      const file = await shrink(raw)
      if (file.size > MAX_BYTES) { setAtts(a => a.map(x => x.lid === lid ? { ...x, error: 'Too big (20 MB max)' } : x)); continue }
      const up = uploadWithProgress<Upload[] | Upload>(`${V1}/uploads`, 'file', [file], p => setAtts(a => a.map(x => x.lid === lid ? { ...x, progress: p } : x)))
      setAtts(a => a.map(x => x.lid === lid ? { ...x, abort: up.abort } : x))
      up.promise.then(r => {
        const u = Array.isArray(r) ? r[0] : r
        setAtts(a => a.map(x => x.lid === lid ? { ...x, upload: u, progress: 1, abort: undefined } : x))
      }).catch(e => setAtts(a => a.map(x => x.lid === lid ? { ...x, error: (e as Error).message, abort: undefined } : x)))
    }
  }, [toast])

  React.useImperativeHandle(ref, () => ({
    focus: () => ta.current?.focus(),
    setText: (t: string) => { setText(t); setTimeout(() => { ta.current?.focus(); ta.current?.setSelectionRange(t.length, t.length) }, 0) },
    addFiles,
    restoreDraft: (restoredText, uploads) => {
      // Keep any draft already being written below the restored message.
      setText(current => restoredText + (current ? '\n\n' + current : ''))
      setAtts(current => [...uploads.filter(u => !current.some(a => a.upload?.id === u.id)).map(upload => ({ lid: 'a' + ++lidSeq, name: upload.name, preview: upload.url, image: upload.mime.startsWith('image/'), progress: 1, upload })), ...current])
      setTimeout(() => ta.current?.focus(), 0)
    },
  }), [addFiles])

  const picked = (e: React.ChangeEvent<HTMLInputElement>) => { addFiles(Array.from(e.target.files || [])); e.target.value = '' }

  const remove = (a: Attachment) => {
    a.abort?.()
    if (a.upload) api.del(`${V1}/uploads/${a.upload.id}`).catch(() => {})
    URL.revokeObjectURL(a.preview)
    setAtts(list => list.filter(x => x.lid !== a.lid))
  }

  // drag & drop anywhere on the page while this composer is mounted
  useEffect(() => {
    if (disabled) return
    let depth = 0
    const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types || []).includes('Files')
    const enter = (e: DragEvent) => { if (!hasFiles(e)) return; depth++; setDragging(true) }
    const leave = (e: DragEvent) => { if (!hasFiles(e)) return; depth = Math.max(0, depth - 1); if (!depth) setDragging(false) }
    const over = (e: DragEvent) => { if (hasFiles(e)) e.preventDefault() }
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault(); depth = 0; setDragging(false)
      addFiles(Array.from(e.dataTransfer?.files || []))
    }
    window.addEventListener('dragenter', enter); window.addEventListener('dragleave', leave)
    window.addEventListener('dragover', over); window.addEventListener('drop', drop)
    return () => { window.removeEventListener('dragenter', enter); window.removeEventListener('dragleave', leave); window.removeEventListener('dragover', over); window.removeEventListener('drop', drop) }
  }, [addFiles, disabled])

  const onPaste = (e: React.ClipboardEvent) => {
    const files = Array.from(e.clipboardData.items).filter(i => i.kind === 'file').map(i => i.getAsFile()).filter((f): f is File => !!f)
    if (files.length) { e.preventDefault(); addFiles(files.map((f, i) => f.name && f.name !== 'image.png' ? f : new File([f], `pasted-${Date.now()}-${i}.${(f.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`, { type: f.type }))) }
  }

  const uploading = atts.some(a => !a.upload && !a.error)
  const ready = atts.filter(a => a.upload)
  const canSend = !disabled && !sending && !uploading && (text.trim() !== '' || ready.length > 0)

  const defaultNow = !!settings?.enter_send_now
  const send = async (sendNow = defaultNow) => {
    if (!canSend) return
    stopDictation()
    setSending(true)
    try {
      const ok = await onSend(text.trim(), ready.map(a => a.upload!.id), sendNow)
      if (ok) {
        setText('')
        atts.forEach(a => URL.revokeObjectURL(a.preview))
        setAtts([])
      }
    } finally { setSending(false) }
  }

  // "/" at the start: recipes and skills by name ("$" too for Codex, its own skill syntax)
  const trigger = text[0] === '/' || (text[0] === '$' && skillScope?.provider === 'codex')
  const slash = trigger && !/\s/.test(text) ? text.slice(1).toLowerCase() : null
  const allSkills = useSkills(skillScope, slash !== null)
  const recipeList: Recipe[] = slash === null || text[0] === '$' ? [] : recipes.filter(r => r.name.toLowerCase().includes(slash)).slice(0, 6)
  const skillList: Skill[] = slash === null ? [] : allSkills
    .filter(k => k.name.toLowerCase().includes(slash) || (slash.length > 2 && k.description.toLowerCase().includes(slash)))
    .sort((a, b) => Number(!a.name.toLowerCase().startsWith(slash)) - Number(!b.name.toLowerCase().startsWith(slash)))
    .slice(0, 8)
  type SlashPick = { recipe: Recipe } | { skill: Skill }
  const slashList: SlashPick[] = [...recipeList.map(recipe => ({ recipe })), ...skillList.map(skill => ({ skill }))]
  useEffect(() => setSlashHi(0), [slash])
  const pick = (p: SlashPick) => {
    setText('recipe' in p ? p.recipe.prompt : p.skill.invoke + ' ')
    setTimeout(() => ta.current?.focus(), 0)
  }

  const before = text.slice(0, caret)
  const atMatch = parentTabs && slash === null ? /(^|\s)@([^\s@]*)$/.exec(before) : null
  const atQ = atMatch && before !== atShut ? atMatch[2].toLowerCase() : null
  useEffect(() => {
    if (atQ === null) { setCtx(null); return } // asked again next time: the page's items change
    let live = true
    parentContext().then(c => { if (live) setCtx(c) }).catch(() => { if (live) setCtx({ label: '', items: [] }) })
    return () => { live = false }
  }, [atQ !== null]) // eslint-disable-line react-hooks/exhaustive-deps
  const atList: ParentContextItem[] = atQ === null || !ctx ? [] : ctx.items.filter(i => i.title.toLowerCase().includes(atQ) || (i.sub || '').toLowerCase().includes(atQ)).slice(0, 10)
  useEffect(() => setAtHi(0), [atQ])
  const pickAt = async (item: ParentContextItem) => {
    if (!atMatch) return
    const start = caret - atMatch[2].length - 1
    const rest = text.slice(caret)
    setText(text.slice(0, start) + rest)
    setCaret(start)
    setTimeout(() => { ta.current?.focus(); ta.current?.setSelectionRange(start, start) }, 0)
    setCtxBusy(true)
    try {
      const got = await parentContextItem(item.id)
      if (got.file) addFiles([got.file])
      else if (got.text) setText(t => t.slice(0, start) + got.text + ' ' + t.slice(start))
    } catch (err) { toast(errMsg(err), 'err') } finally { setCtxBusy(false) }
  }

  const menuOpen = slashList.length > 0 || (atQ !== null && (atList.length > 0 || !ctx || ctxBusy))
  useLayoutEffect(() => {
    if (!menuOpen || !box.current) return
    // room above the box, inside the nearest scrolling area (or the window)
    let top = 0
    for (let el = box.current.parentElement; el; el = el.parentElement) {
      const o = getComputedStyle(el).overflowY
      if (o === 'auto' || o === 'scroll' || o === 'hidden') { top = el.getBoundingClientRect().top; break }
    }
    setMenuInside(box.current.getBoundingClientRect().top - Math.max(top, 0) < 220)
  }, [menuOpen])

  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return
    if (atList.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setAtHi(h => (h + 1) % atList.length); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setAtHi(h => (h - 1 + atList.length) % atList.length); return }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); void pickAt(atList[atHi]); return }
      if (e.key === 'Escape') { e.preventDefault(); setAtShut(before); return }
    }
    if (slashList.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setSlashHi(h => (h + 1) % slashList.length); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setSlashHi(h => (h - 1 + slashList.length) % slashList.length); return }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); pick(slashList[slashHi]); return }
      if (e.key === 'Escape') { e.preventDefault(); setText(''); return }
    }
    if (e.key !== 'Enter' || e.nativeEvent.isComposing) return
    if (e.shiftKey || e.altKey) return
    if (e.metaKey || e.ctrlKey) { e.preventDefault(); void send(!defaultNow); return }
    if ((settings?.enter_sends ?? true) && !isTouch()) { e.preventDefault(); send() }
  }

  const stopDictation = () => { rec.current?.stop(); rec.current = null; setListening(false) }
  const toggleDictation = () => {
    if (!Speech) return
    if (listening) { stopDictation(); return }
    const r = new Speech()
    r.lang = navigator.language || 'en-US'
    r.continuous = true
    r.interimResults = false
    r.onresult = e => {
      let add = ''
      for (let i = e.resultIndex; i < e.results.length; i++) if (e.results[i].isFinal) add += e.results[i][0].transcript
      if (add) setText(t => (t && !/\s$/.test(t) ? t + ' ' : t) + add.trim())
    }
    r.onerror = e => { if (e.error !== 'aborted' && e.error !== 'no-speech') toast(`Dictation: ${e.error}`, 'err'); setListening(false) }
    r.onend = () => setListening(false)
    try { r.start(); rec.current = r; setListening(true) } catch { toast('Dictation is not available here', 'err') }
  }
  useEffect(() => () => rec.current?.stop(), [])

  return (
    <div ref={box} className={`composer ${big ? 'big' : ''} ${disabled ? 'disabled' : ''}`}>
      {slashList.length > 0 && (
        <div className={`slash-menu ${menuInside ? 'inside' : ''}`} role="listbox" aria-label="Recipes and skills">
          {slashList.map((p, i) => {
            const first = i === 0 || ('recipe' in p) !== ('recipe' in slashList[i - 1])
            return (
              <React.Fragment key={'recipe' in p ? 'r' + p.recipe.id : 's' + p.skill.name}>
                {first && <div className="slash-h">{'recipe' in p ? 'Recipes' : 'Skills'}</div>}
                <button type="button" role="option" aria-selected={i === slashHi} className={`slash-row ${i === slashHi ? 'hi' : ''}`} onMouseEnter={() => setSlashHi(i)} onMouseDown={e => e.preventDefault()} onClick={() => pick(p)}>
                  {'recipe' in p ? <>
                    <span className="slash-ico">{p.recipe.icon || '✦'}</span>
                    <span className="grow"><span className="slash-name">{p.recipe.name}</span><span className="slash-sub">{p.recipe.prompt.slice(0, 90)}</span></span>
                  </> : <>
                    <span className="slash-ico"><Icon name="sparkles" size={14} /></span>
                    <span className="grow"><span className="slash-name">{p.skill.invoke}{p.skill.argument_hint ? <span className="slash-hint"> {p.skill.argument_hint}</span> : null}</span><span className="slash-sub">{p.skill.description.slice(0, 110)}</span></span>
                  </>}
                </button>
              </React.Fragment>
            )
          })}
        </div>
      )}
      {atts.length > 0 && (
        <div className="att-row">
          {atts.map(a => (
            <div key={a.lid} className={`att ${a.error ? 'err' : ''}`} title={a.error || a.name}>
              {a.image ? <img src={a.preview} alt={a.name} /> : <span className="attachment-file" title={a.name}>📄 {a.name}</span>}
              {!a.upload && !a.error && <div className="att-progress"><span style={{ height: `${Math.round((1 - a.progress) * 100)}%` }} /><Spinner size={16} /></div>}
              {a.error && <div className="att-error"><Icon name="alert" size={16} /></div>}
              <button type="button" className="att-x" aria-label={`Remove ${a.name}`} onClick={() => remove(a)}><Icon name="x" size={12} /></button>
            </div>
          ))}
        </div>
      )}
      {atQ !== null && (atList.length > 0 || !ctx || ctxBusy) && (
        <div className={`slash-menu at-menu ${menuInside ? 'inside' : ''}`} role="listbox" aria-label="Add from the page">
          <div className="slash-h">{ctx?.label ? `From ${ctx.label}` : 'Add as context'}</div>
          {!ctx ? <div className="slash-row muted"><Spinner size={14} /> Looking…</div>
            : atList.map((item, i) => (
              <button key={item.id} type="button" role="option" aria-selected={i === atHi} className={`slash-row ${i === atHi ? 'hi' : ''}`} onMouseEnter={() => setAtHi(i)} onMouseDown={e => e.preventDefault()} onClick={() => { void pickAt(item) }}>
                <span className={`slash-ico at-ico ${item.kind}`}><Icon name={item.kind === 'note' ? 'note' : item.kind === 'file' ? 'paperclip' : 'link'} size={14} /></span>
                <span className="grow"><span className="slash-name">{item.title}</span><span className="slash-sub">{[item.kind === 'note' ? 'Note' : item.kind === 'file' ? 'File' : 'Link', item.sub].filter(Boolean).join(' · ')}</span></span>
              </button>
            ))}
        </div>
      )}
      <textarea {...noGrammarly} ref={ta} rows={big ? 3 : 1} value={text} disabled={disabled} placeholder={placeholder} aria-label="Message"
        onSelect={e => setCaret(e.currentTarget.selectionStart)}
        onChange={e => { setText(e.target.value); setCaret(e.target.selectionStart) }} onKeyDown={onKey} onPaste={onPaste} enterKeyHint={isTouch() ? 'enter' : 'send'} />
      <div className="composer-bar">
        {/* Phones get Camera / Photos / Files like the Codex app: one input each, so iOS opens
            the camera or the document browser directly. A computer opens one file dialog. */}
        <input ref={fileInput} type="file" accept={'image/*,' + DOC_TYPES} multiple hidden onChange={picked} />
        <input ref={photoInput} type="file" accept="image/*" multiple hidden onChange={picked} />
        <input ref={cameraInput} type="file" accept="image/*" capture="environment" hidden onChange={picked} />
        <input ref={docInput} type="file" accept={DOC_TYPES} multiple hidden onChange={picked} />
        <button type="button" className={`btn ghost icon-only sm attach-btn ${attachOpen ? 'on' : ''}`} aria-label="Attach" title="Attach images or files (or paste / drop them)" aria-haspopup={isTouch() ? 'menu' : undefined} aria-expanded={isTouch() ? attachOpen : undefined} data-keep-open disabled={disabled}
          onClick={() => { if (isTouch()) setAttachOpen(o => !o); else fileInput.current?.click() }}><Icon name="plus" size={18} /></button>
        <div className="composer-extra">{footer}</div>
        {Speech && <button type="button" className={`btn ghost icon-only sm mic-btn ${listening ? 'rec' : ''}`} aria-label={listening ? 'Stop dictation' : 'Dictate'} title={listening ? 'Stop dictation' : 'Dictate'} disabled={disabled} onClick={toggleDictation}><Icon name="mic" size={17} /></button>}
        {running && onStop && !text.trim() && atts.length === 0 ? (
          <button type="button" className="stop-round" aria-label="Stop" title="Stop (⌘.)" onClick={onStop}><span /></button>
        ) : (
          <button type="button" className="btn primary icon-only send-btn" aria-label={running ? defaultNow ? 'Send now' : 'Queue message' : 'Send'} title={running ? defaultNow ? 'Send now — interrupts the current turn' : 'Queue — sent when the agent is done' : 'Send'} disabled={!canSend} onClick={() => { void send() }}>
            {sending ? <Spinner size={14} /> : <Icon name="arrow-up" size={17} strokeWidth={2.2} />}
          </button>
        )}
      </div>
      {attachOpen && (
        <div ref={attachMenu} className="attach-menu" role="menu" aria-label="Attach">
          {([['camera', 'Camera', cameraInput], ['image', 'Photos', photoInput], ['paperclip', 'Files', docInput]] as const).map(([icon, label, input]) => (
            <button key={label} type="button" role="menuitem" className="attach-row" onClick={() => { setAttachOpen(false); input.current?.click() }}>
              <span className="attach-ico"><Icon name={icon} size={19} /></span>{label}
            </button>
          ))}
        </div>
      )}
      {dragging && <div className="drop-overlay"><div><Icon name="image" size={28} /><b>Drop images to attach</b></div></div>}
    </div>
  )
})
