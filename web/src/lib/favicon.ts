// The browser tab's icon shows when an agent is busy: a ring while a prompt
// runs, an amber dot while one waits for you. Painted once on a canvas and set
// once per change of state, each painted icon kept for the next time. Do not animate it: every icon change costs the
// browser a PNG decode and a tab/window redraw and wakes every extension that
// watches tabs; a spinning ring kept Chrome and its extensions hot (and the
// Mac's fan on) for as long as an agent worked.
import { useEffect } from 'react'

const SRC = '/favicon.svg'
const SIZE = 64

let base: HTMLImageElement | null = null
function baseIcon(): Promise<HTMLImageElement> {
  if (base?.complete) return Promise.resolve(base)
  return new Promise(resolve => {
    const img = new Image()
    img.onload = () => { base = img; resolve(img) }
    img.src = SRC
  })
}

function link(): HTMLLinkElement {
  let el = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!el) { el = document.createElement('link'); el.rel = 'icon'; document.head.appendChild(el) }
  return el
}

function setIcon(href: string, type: string) {
  const el = link()
  if (el.getAttribute('href') !== href) { el.type = type; el.href = href }
}

function paint(ctx: CanvasRenderingContext2D, img: HTMLImageElement, running: boolean, waiting: boolean) {
  if (running) {
    const inset = 13 // icon shrinks so the ring fits around it
    ctx.drawImage(img, inset, inset, SIZE - inset * 2, SIZE - inset * 2)
    ctx.lineCap = 'round'
    ctx.lineWidth = 8
    ctx.strokeStyle = 'rgba(167, 139, 250, .25)'
    ctx.beginPath(); ctx.arc(SIZE / 2, SIZE / 2, SIZE / 2 - 5, 0, Math.PI * 2); ctx.stroke()
    ctx.strokeStyle = '#a78bfa'
    ctx.beginPath(); ctx.arc(SIZE / 2, SIZE / 2, SIZE / 2 - 5, -Math.PI / 2, Math.PI * 0.1); ctx.stroke()
  } else {
    ctx.drawImage(img, 0, 0, SIZE, SIZE)
  }
  if (waiting) {
    ctx.fillStyle = '#f59e0b'
    ctx.strokeStyle = '#18181b'
    ctx.lineWidth = 4
    ctx.beginPath(); ctx.arc(SIZE - 12, 12, 10, 0, Math.PI * 2); ctx.fill(); ctx.stroke()
  }
}

const painted = new Map<string, string>()
function busyIcon(img: HTMLImageElement, running: boolean, waiting: boolean): string | null {
  const key = `${running}${waiting}`
  let url = painted.get(key)
  if (!url) {
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = SIZE
    const ctx = canvas.getContext('2d')
    if (!ctx) return null
    paint(ctx, img, running, waiting)
    url = canvas.toDataURL('image/png')
    painted.set(key, url)
  }
  return url
}

export function useBusyFavicon(running: boolean, waiting: boolean) {
  useEffect(() => {
    if (!running && !waiting) { setIcon(SRC, 'image/svg+xml'); return }
    let stale = false
    baseIcon().then(img => {
      const url = stale ? null : busyIcon(img, running, waiting)
      if (url) setIcon(url, 'image/png')
    })
    return () => { stale = true }
  }, [running, waiting])
  // Leaving the app shell puts the plain icon back.
  useEffect(() => () => setIcon(SRC, 'image/svg+xml'), [])
}
