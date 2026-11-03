import { Modal } from './ui'
import { Snippets } from './Snippets'
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
// Read as plain text by the server (internal/documenttext textExt); keep in sync.
const TEXT_EXT = 'txt md csv tsv json xml html htm yaml yml toml ini conf cfg log py js mjs cjs ts tsx jsx go rs java kt swift c h cpp rb php sh bash zsh sql css scss diff patch srt vtt ass ssa ics vcf eml svg'.split(' ')
// Converted to text on the server; .doc/.xls/.ppt through Collabora.
const OFFICE_EXT = 'pdf docx xlsx pptx odt ods odp doc xls ppt'.split(' ')
const DOC_EXT = [...OFFICE_EXT, ...TEXT_EXT]
const DOC_RE = new RegExp(`(\\.(${DOC_EXT.join('|')})|\\.env\\.example)$`, 'i')
// MIME types next to the extensions: iOS greys files out on unknown extensions alone
const DOC_TYPES = DOC_EXT.map(e => '.' + e).join(',') + ',.example,text/*,application/json,application/msword,application/vnd.ms-excel,application/vnd.ms-powerpoint'

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

// Text pasted while nothing editable has the focus (a click in the conversation, a tab,
// dictation apps that paste with ⌘V) goes to a composer instead of nowhere: the one used
// last if it is on screen, else the first one that is.
type PasteTarget = { el: () => HTMLTextAreaElement | null; insert: (s: string, atEnd: boolean) => void }
const pasteTargets: PasteTarget[] = []
let lastPasteTarget: PasteTarget | null = null
const editable = (el: Element | null) => !!el && (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement || (el as HTMLElement).isContentEditable || !!el.closest('.xterm, [contenteditable]'))
const onScreen = (t: PasteTarget) => { const el = t.el(); return !!el && !el.disabled && el.offsetParent !== null }
function strayPaste(e: ClipboardEvent) {
  if (e.defaultPrevented || editable(e.target as Element) || editable(document.activeElement)) return
  if (document.querySelector('.modal-backdrop')) return
  const text = e.clipboardData?.getData('text/plain')
  if (!text) return
  const t = lastPasteTarget && onScreen(lastPasteTarget) ? lastPasteTarget : pasteTargets.find(onScreen)
  if (!t) return
  e.preventDefault()
  t.insert(text, true)
}

/** Where a message would run: decides which skills the CLI offers. */
export interface SkillScope { provider: ProviderId; cwd?: string; account_id?: number; host_id?: number }

type SkillList = { skills: Skill[]; commands: Skill[] }
const noSkills: SkillList = { skills: [], commands: [] }
const skillCache = new Map<string, { at: number } & SkillList>()

/** The skills and built-in commands for a scope, fetched once the slash menu opens (cached a minute). */
function useSkills(scope: SkillScope | undefined, wanted: boolean): SkillList {
  const key = scope ? `${scope.provider}|${scope.cwd || ''}|${scope.account_id || 0}|${scope.host_id || 0}` : ''
  const [skills, setSkills] = useState<SkillList>(() => skillCache.get(key) || noSkills)
  useEffect(() => {
    if (!scope || !wanted) return
    const hit = skillCache.get(key)
    if (hit) setSkills(hit)
    if (hit && Date.now() - hit.at < 60_000) return
    let live = true
    const q = new URLSearchParams({ provider: scope.provider, cwd: scope.cwd || '', account: String(scope.account_id || 0), host: String(scope.host_id || 0) })
    api.get<{ skills: Skill[]; commands?: Skill[] }>(`${V1}/skills?${q}`).then(r => {
      const got = { skills: r.skills || [], commands: r.commands || [] }
      skillCache.set(key, { at: Date.now(), ...got })
      if (live) setSkills(got)
    }).catch(() => {})
    return () => { live = false }
  }, [key, wanted]) // eslint-disable-line react-hooks/exhaustive-deps
  return scope ? skills : noSkills
}

/** A Pocket app the person can @-mention: the agent is pointed at its tools for that message. */
interface MentionApp { slug: string; name: string; tools: string[]; reads: number }
let mentionApps: { at: number; apps: MentionApp[] } | null = null

/** The person's Pocket apps, fetched when the @ menu first opens (cached 2 minutes). */
function useMentionApps(wanted: boolean): MentionApp[] | null {
  const [apps, setApps] = useState<MentionApp[] | null>(() => mentionApps?.apps ?? null)
  useEffect(() => {
    if (!wanted) return
    if (mentionApps) setApps(mentionApps.apps)
    if (mentionApps && Date.now() - mentionApps.at < 120_000) return
    let live = true
    api.get<{ apps: MentionApp[] }>(`${V1}/pocket-tools/apps`).then(r => {
      mentionApps = { at: Date.now(), apps: r.apps || [] }
      if (live) setApps(mentionApps.apps)
    }).catch(() => { if (live) setApps(a => a ?? []) })
    return () => { live = false }
  }, [wanted])
  return apps
}

/** One item found in a Pocket app ("@documents #tax"); see server pocket_items.go. */
interface PocketItem { app: string; id: string; title: string; subtitle?: string; url?: string; attachable: boolean }
const itemCache = new Map<string, { at: number; items: PocketItem[] }>()

/** Searches one app as the person types after "#" (debounced, cached a minute); null while looking. */
function usePocketItems(app: string | null, q: string): PocketItem[] | null {
  const key = app === null ? '' : app + '\n' + q.trim().toLowerCase()
  const cached = () => { const c = itemCache.get(key); return c && Date.now() - c.at < 60_000 ? c.items : null }
  const [got, setGot] = useState<{ key: string; items: PocketItem[] } | null>(null)
  useEffect(() => {
    if (!app || cached()) return
    let live = true
    const timer = setTimeout(() => {
      api.get<{ items: PocketItem[] }>(`${V1}/pocket/items?app=${encodeURIComponent(app)}&q=${encodeURIComponent(q.trim())}`)
        .then(r => r.items || []).catch(() => [] as PocketItem[])
        .then(items => { itemCache.set(key, { at: Date.now(), items }); if (live) setGot({ key, items }) })
    }, 250)
    return () => { live = false; clearTimeout(timer) }
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!app) return null
  return cached() ?? (got?.key === key ? got.items : null)
}

/** What a picked item becomes in the message: a link the agent can follow (ids are in it), else title + id. */
function itemRef(it: PocketItem): string {
  const title = it.title.replace(/[[\]]/g, m => m === '[' ? '(' : ')')
  return it.url ? `[${title}](${it.url})` : `"${title}" (${it.app} ${it.id})`
}

/** The app's own icon (web/public/app-icons, from the Pocket icon set); a plain grid for an app without one. */
function AppIcon({ slug }: { slug: string }) {
  const [missing, setMissing] = useState(false)
  if (missing) return <span className="slash-ico at-ico app-mention"><Icon name="grid" size={14} /></span>
  return <span className="slash-ico app-icon"><img src={`/app-icons/${slug}.svg`} alt="" width={22} height={22} loading="lazy" onError={() => setMissing(true)} /></span>
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
  /** A slash command Pocket AI runs itself (/mcp): true when handled, so it is not sent. */
  onCommand?: (name: string, args: string) => boolean
}>(function Composer({ onSend, placeholder = 'Ask anything…', autoFocus, running, onStop, disabled, footer, draftKey, big, skills: skillScope, onCommand }, ref) {
  const { settings, recipes } = useApp()
  const { toast } = useToast()
  const [text, setText] = useState(() => (draftKey && sessionStorage.getItem('pai-draft-' + draftKey)) || '')
  const [atts, setAtts] = useState<Attachment[]>(() => draftAttachments(draftKey))
  const [sending, setSending] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [listening, setListening] = useState(false)
  const [slashHi, setSlashHi] = useState(0)
  // "@": the person's Pocket apps, and the framing page's items (Pocket Projects: the issue's notes, files, links)
  const [caret, setCaret] = useState(0)
  const [atHi, setAtHi] = useState(0)
  const [atShut, setAtShut] = useState('')
  const [slashShut, setSlashShut] = useState('')
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
  const [snippetsOpen, setSnippetsOpen] = useState(false)
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

  const fit = useCallback(() => {
    const el = ta.current
    // Not on screen (a hidden tab in another app's side panel): nothing to measure, and a
    // height taken now would be 0 and squash the box once it shows.
    if (!el || !el.clientWidth) return
    el.style.height = 'auto'
    const max = big ? 360 : 280
    // a scrollbar only once the text is taller than the box may grow (CSS may cap it lower, a
    // phone with the keyboard open): otherwise a pixel of rounding shows an empty one
    const cap = Math.min(max, parseFloat(getComputedStyle(el).maxHeight) || max)
    el.style.overflowY = el.scrollHeight > cap + 2 ? 'auto' : 'hidden'
    el.style.height = Math.min(el.scrollHeight, max) + 'px'
  }, [big])
  useLayoutEffect(fit, [text, fit])
  // Measured again when the box changes width: a window resize, or being shown after hidden.
  useEffect(() => {
    const el = ta.current
    if (!el || typeof ResizeObserver === 'undefined') return
    let w = el.clientWidth
    const ro = new ResizeObserver(() => { if (el.clientWidth !== w) { w = el.clientWidth; fit() } })
    ro.observe(el)
    return () => ro.disconnect()
  }, [fit])
  useEffect(() => {
    const again = () => { const el = ta.current; if (el && el.clientWidth) el.style.overflowY = el.scrollHeight > el.clientHeight + 2 ? 'auto' : 'hidden' }
    window.addEventListener('resize', again)
    return () => window.removeEventListener('resize', again)
  }, [])
  useEffect(() => { if (autoFocus && !isTouch()) ta.current?.focus() }, [autoFocus, draftKey])

  const addFiles = useCallback(async (files: File[]) => {
    const imgs = files.filter(f => f.type.startsWith('image/') || /\.(png|jpe?g|gif|webp|hei[cf])$/i.test(f.name) || DOC_RE.test(f.name))
    if (imgs.length < files.length) toast('Attach images, PDF, Office documents, code or text files', 'err')
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

  /** Text in at the caret (or at the end), put into the state directly: the browser's own
   *  paste sometimes never reached it, and the text was lost until pasted a second time. */
  const insert = useCallback((s: string, atEnd: boolean) => {
    const el = ta.current
    if (!el) return
    const focused = document.activeElement === el
    const a = focused && !atEnd ? el.selectionStart : el.value.length
    const b = focused && !atEnd ? el.selectionEnd : el.value.length
    const v = el.value
    const lead = atEnd && v && !/\s$/.test(v) ? ' ' : ''
    const next = v.slice(0, a) + lead + s + v.slice(b)
    const pos = a + lead.length + s.length
    setText(next)
    setCaret(pos)
    requestAnimationFrame(() => { const t = ta.current; if (!t) return; t.focus(); t.setSelectionRange(pos, pos) })
  }, [])

  useEffect(() => {
    const me: PasteTarget = { el: () => ta.current, insert }
    pasteTargets.push(me)
    if (pasteTargets.length === 1) window.addEventListener('paste', strayPaste)
    return () => {
      pasteTargets.splice(pasteTargets.indexOf(me), 1)
      if (lastPasteTarget === me) lastPasteTarget = null
      if (!pasteTargets.length) window.removeEventListener('paste', strayPaste)
    }
  }, [insert])
  const markUsed = () => { lastPasteTarget = pasteTargets.find(p => p.el() === ta.current) || null }

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData.items).filter(i => i.kind === 'file').map(i => i.getAsFile()).filter((f): f is File => !!f)
    if (files.length) { e.preventDefault(); addFiles(files.map((f, i) => f.name && f.name !== 'image.png' ? f : new File([f], `pasted-${Date.now()}-${i}.${(f.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`, { type: f.type }))); return }
    const s = e.clipboardData.getData('text/plain')
    if (!s) return
    e.preventDefault()
    insert(s, false)
  }

  const uploading = atts.some(a => !a.upload && !a.error)
  const ready = atts.filter(a => a.upload)
  const canSend = !disabled && !sending && !uploading && (text.trim() !== '' || ready.length > 0)

  const defaultNow = !!settings?.enter_send_now
  const send = async (sendNow = defaultNow) => {
    if (!canSend) return
    stopDictation()
    const cmd = !ready.length && /^\/(\S+)\s*([\s\S]*)$/.exec(text.trim())
    if (cmd && onCommand?.(cmd[1], cmd[2].trim())) { setText(''); return }
    setSending(true)
    // Empty the box right away (starting an agent can take seconds); the
    // message comes back only if it did not go, above anything typed since.
    const sentText = text.trim(), sentAtts = atts
    setText('')
    setAtts([])
    try {
      const ok = await onSend(sentText, ready.map(a => a.upload!.id), sendNow)
      if (ok) sentAtts.forEach(a => URL.revokeObjectURL(a.preview))
      else {
        setText(cur => sentText + (cur ? '\n\n' + cur : ''))
        setAtts(cur => [...sentAtts, ...cur])
      }
    } finally { setSending(false) }
  }

  // At the start: "/" the agent's commands and skills, then recipes; "#" recipes only; "$" Codex skills (its own syntax)
  const lead = text[0]
  const trigger = lead === '/' || lead === '#' || (lead === '$' && skillScope?.provider === 'codex')
  const slash = trigger && !/\s/.test(text) && text !== slashShut ? text.slice(1).toLowerCase() : null
  const { skills: allSkills, commands: allCommands } = useSkills(skillScope, slash !== null && lead !== '#')
  const byName = (list: Skill[], max: number) => slash === null ? [] : list
    .filter(k => k.name.toLowerCase().includes(slash) || (slash.length > 2 && k.description.toLowerCase().includes(slash)))
    .sort((a, b) => Number(!a.name.toLowerCase().startsWith(slash)) - Number(!b.name.toLowerCase().startsWith(slash)))
    .slice(0, max)
  const commandList: Skill[] = lead === '/' ? byName(allCommands, 8) : []
  const skillList: Skill[] = lead === '/' || lead === '$' ? byName(allSkills, 8) : []
  const recipeList: Recipe[] = slash === null || lead === '$' ? [] : recipes.filter(r => r.name.toLowerCase().includes(slash)).slice(0, lead === '#' ? 12 : 6)
  type SlashPick = { recipe: Recipe } | { skill: Skill; command?: boolean }
  const slashList: SlashPick[] = [...commandList.map(skill => ({ skill, command: true })), ...skillList.map(skill => ({ skill })), ...recipeList.map(recipe => ({ recipe }))]
  const group = (p: SlashPick) => 'recipe' in p ? 'Recipes' : p.command ? 'Commands' : 'Skills'
  useEffect(() => setSlashHi(0), [slash, lead])
  const pick = (p: SlashPick) => {
    if ('skill' in p && p.command && !p.skill.argument_hint && onCommand?.(p.skill.name, '')) { setText(''); return }
    setText('recipe' in p ? p.recipe.prompt : p.skill.invoke + ' ')
    setTimeout(() => ta.current?.focus(), 0)
  }

  const before = text.slice(0, caret)
  // "@documents #tax" (or "@documents#tax"): search inside that app, up to three words
  const hashMatch = slash === null && before !== atShut ? /(^|\s)@([a-z][a-z0-9-]*) ?#((?:[^\s#]+ ?){0,3})$/i.exec(before) : null
  const atMatch = slash === null && !hashMatch ? /(^|\s)@([^\s@]*)$/.exec(before) : null
  const atQ = atMatch && before !== atShut ? atMatch[2].toLowerCase() : null
  const apps = useMentionApps(atQ !== null || hashMatch !== null)
  const hashApp = hashMatch && apps ? apps.find(a => a.slug === hashMatch[2].toLowerCase()) ?? null : null
  const found = usePocketItems(hashApp?.slug ?? null, hashMatch?.[3] ?? '')
  useEffect(() => {
    if (atQ === null || !parentTabs) { setCtx(null); return } // asked again next time: the page's items change
    let live = true
    parentContext().then(c => { if (live) setCtx(c) }).catch(() => { if (live) setCtx({ label: '', items: [] }) })
    return () => { live = false }
  }, [atQ !== null]) // eslint-disable-line react-hooks/exhaustive-deps
  const itemList: ParentContextItem[] = atQ === null || !ctx ? [] : ctx.items.filter(i => i.title.toLowerCase().includes(atQ) || (i.sub || '').toLowerCase().includes(atQ)).slice(0, 10)
  const appList: MentionApp[] = atQ === null || !apps ? [] : apps
    .filter(a => a.slug.includes(atQ) || a.name.toLowerCase().includes(atQ))
    .sort((a, b) => Number(!a.slug.startsWith(atQ)) - Number(!b.slug.startsWith(atQ)))
    .slice(0, parentTabs ? 6 : 10)
  type AtPick = { item: ParentContextItem } | { app: MentionApp } | { found: PocketItem }
  const atList: AtPick[] = hashMatch ? (found || []).map(f => ({ found: f })) : [...itemList.map(item => ({ item })), ...appList.map(app => ({ app }))]
  // still looking: the page's items (in a panel), the apps the first time, or the app's search
  const atLoading = hashMatch ? apps === null || (!!hashApp && found === null) : atQ !== null && ((parentTabs && !ctx) || apps === null || ctxBusy)
  useEffect(() => setAtHi(0), [atQ, hashMatch?.[3]])
  const pickFound = (it: PocketItem) => {
    if (!hashMatch) return
    const hash = caret - hashMatch[3].length - 1 // the "#"
    const rest = text.slice(caret)
    const ins = (text[hash - 1] === ' ' ? '' : ' ') + itemRef(it) + (rest.startsWith(' ') ? '' : ' ')
    const pos = hash + ins.length + (rest.startsWith(' ') ? 1 : 0)
    setText(text.slice(0, hash) + ins + rest)
    setCaret(pos)
    setTimeout(() => { ta.current?.focus(); ta.current?.setSelectionRange(pos, pos) }, 0)
  }
  const pickApp = (app: MentionApp) => {
    if (!atMatch) return
    const start = caret - atMatch[2].length - 1
    const rest = text.slice(caret)
    const ins = '@' + app.slug + (rest.startsWith(' ') ? '' : ' ')
    const pos = start + ins.length + (rest.startsWith(' ') ? 1 : 0)
    setText(text.slice(0, start) + ins + rest)
    setCaret(pos)
    setTimeout(() => { ta.current?.focus(); ta.current?.setSelectionRange(pos, pos) }, 0)
  }
  const pickAt = async (p: AtPick) => {
    if ('found' in p) { pickFound(p.found); return }
    if (!atMatch) return
    if ('app' in p) { pickApp(p.app); return }
    const item = p.item
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

  const atOpen = hashMatch ? !!hashApp || atLoading : atQ !== null && (atList.length > 0 || atLoading)
  const menuOpen = slashList.length > 0 || atOpen
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

  // a click outside the menu and the text box closes it; clicking back into the text reopens it
  useEffect(() => {
    if (!menuOpen) return
    const down = (e: PointerEvent) => {
      const t = e.target as Node
      if (t === ta.current || box.current?.querySelector('.slash-menu')?.contains(t)) return
      setAtShut(before)
      setSlashShut(text)
    }
    document.addEventListener('pointerdown', down, true)
    return () => document.removeEventListener('pointerdown', down, true)
  }, [menuOpen, before, text])
  const reopen = (e: React.MouseEvent<HTMLTextAreaElement>) => { setAtShut(''); setSlashShut(''); setCaret(e.currentTarget.selectionStart) }

  // arrow keys move the highlight: keep it in view (the first row also shows its section heading)
  useEffect(() => {
    const menu = box.current?.querySelector<HTMLElement>('.slash-menu')
    const row = menu?.querySelector<HTMLElement>('.slash-row.hi')
    if (!menu || !row) return
    if ((atOpen ? atHi : slashHi) === 0) menu.scrollTop = 0
    else row.scrollIntoView({ block: 'nearest' })
  }, [atHi, slashHi, atOpen])

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
        <div className={`slash-menu ${menuInside ? 'inside' : ''}`} role="listbox" aria-label="Commands, skills and recipes">
          {slashList.map((p, i) => {
            const first = i === 0 || group(p) !== group(slashList[i - 1])
            return (
              <React.Fragment key={'recipe' in p ? 'r' + p.recipe.id : (p.command ? 'c' : 's') + p.skill.name}>
                {first && <div className="slash-h">{group(p)}{group(p) === 'Recipes' && lead === '/' ? <span className="slash-hint"> · or type #</span> : null}</div>}
                <button type="button" role="option" aria-selected={i === slashHi} className={`slash-row ${i === slashHi ? 'hi' : ''}`} onMouseMove={() => { if (i !== slashHi) setSlashHi(i) }} onMouseDown={e => e.preventDefault()} onClick={() => pick(p)}>
                  {'recipe' in p ? <>
                    <span className="slash-ico">{p.recipe.icon || '✦'}</span>
                    <span className="grow"><span className="slash-name">{p.recipe.name}</span><span className="slash-sub">{p.recipe.prompt.slice(0, 90)}</span></span>
                  </> : <>
                    <span className="slash-ico"><Icon name={p.command ? 'terminal' : 'sparkles'} size={14} /></span>
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
      {atOpen && (
        <div className={`slash-menu at-menu ${menuInside ? 'inside' : ''}`} role="listbox" aria-label={hashApp ? `Search ${hashApp.name}` : 'Mention an app or add from the page'}>
          {atList.map((p, i) => {
            const first = i === 0 || ('app' in p) !== ('app' in atList[i - 1])
            const head = 'found' in p ? hashApp?.name : 'app' in p ? 'Apps' : ctx?.label ? `From ${ctx.label}` : 'Add as context'
            const row = 'found' in p
              ? <>
                <AppIcon slug={p.found.app} />
                <span className="grow"><span className="slash-name">{p.found.title}</span>{p.found.subtitle && <span className="slash-sub">{p.found.subtitle}</span>}</span>
              </>
              : 'app' in p
              ? <>
                <AppIcon slug={p.app.slug} />
                <span className="grow"><span className="slash-name">{p.app.name}</span><span className="slash-sub">@{p.app.slug} · {p.app.tools.length} {p.app.tools.length === 1 ? 'operation' : 'operations'}</span></span>
              </>
              : <>
                <span className={`slash-ico at-ico ${p.item.kind}`}><Icon name={p.item.kind === 'note' ? 'note' : p.item.kind === 'file' ? 'paperclip' : 'link'} size={14} /></span>
                <span className="grow"><span className="slash-name">{p.item.title}</span><span className="slash-sub">{[p.item.kind === 'note' ? 'Note' : p.item.kind === 'file' ? 'File' : 'Link', p.item.sub].filter(Boolean).join(' · ')}</span></span>
              </>
            return (
              <React.Fragment key={'found' in p ? 'f' + p.found.app + p.found.id : 'app' in p ? 'a' + p.app.slug : 'i' + p.item.id}>
                {first && <div className="slash-h">{head}</div>}
                <button type="button" role="option" aria-selected={i === atHi} className={`slash-row ${i === atHi ? 'hi' : ''}`} onMouseMove={() => { if (i !== atHi) setAtHi(i) }} onMouseDown={e => e.preventDefault()} onClick={() => { void pickAt(p) }}>{row}</button>
              </React.Fragment>
            )
          })}
          {atLoading && <div className="slash-row muted"><Spinner size={14} /> Looking…</div>}
          {hashApp && found?.length === 0 && <div className="slash-row muted">{hashMatch?.[3].trim() ? `Nothing in ${hashApp.name} matches “${hashMatch[3].trim()}”` : `Type to search ${hashApp.name}`}</div>}
        </div>
      )}
      <textarea {...noGrammarly} ref={ta} rows={big ? 3 : 1} value={text} disabled={disabled} placeholder={placeholder} aria-label="Message"
        onSelect={e => setCaret(e.currentTarget.selectionStart)} onClick={reopen} onFocus={markUsed}
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
        <button type="button" className="btn ghost icon-only sm" aria-label="Insert snippet" title="Snippets" disabled={disabled} onClick={() => setSnippetsOpen(true)}><Icon name="bookmark" size={17} /></button>
        <div className="composer-extra">{footer}</div>
        {Speech && <button type="button" className={`btn ghost icon-only sm mic-btn ${listening ? 'rec' : ''}`} aria-label={listening ? 'Stop dictation' : 'Dictate'} title={listening ? 'Stop dictation' : 'Dictate'} disabled={disabled} onClick={toggleDictation}><Icon name="mic" size={17} /></button>}
        {running && onStop && !text.trim() && atts.length === 0 ? (
          <button type="button" className="stop-round" aria-label="Stop" title="Stop (⌘.)" onClick={onStop}><span /></button>
        ) : (
          <button type="button" className="btn primary icon-only send-btn" aria-label={running ? defaultNow ? 'Send now' : 'Queue message' : 'Send'} title={running ? defaultNow ? 'Send now — the agent reads it after its current step' : 'Queue — sent when the agent is done' : 'Send'} disabled={!canSend} onClick={() => { void send() }}>
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
      {snippetsOpen && <Modal title="Snippets" onClose={() => setSnippetsOpen(false)} size="lg">
        <Snippets onInsert={snippet => {
          setText(draft => draft + (draft ? draft.endsWith('\n\n') ? '' : draft.endsWith('\n') ? '\n' : '\n\n' : '') + snippet.body)
          setSnippetsOpen(false)
          setTimeout(() => ta.current?.focus(), 0)
        }} />
      </Modal>}
      {dragging && <div className="drop-overlay"><div><Icon name="image" size={28} /><b>Drop images to attach</b></div></div>}
    </div>
  )
})
