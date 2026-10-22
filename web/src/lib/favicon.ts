// The browser tab's icon shows when an agent is busy: a spinning ring while a
// prompt runs, an amber dot while one waits for you. Chrome draws SVG/GIF
// favicons as still images, so the frames are painted on a canvas.
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

function frame(ctx: CanvasRenderingContext2D, img: HTMLImageElement, running: boolean, waiting: boolean, angle: number) {
  ctx.clearRect(0, 0, SIZE, SIZE)
  if (running) {
    const inset = 13 // icon shrinks so the ring fits around it
    ctx.drawImage(img, inset, inset, SIZE - inset * 2, SIZE - inset * 2)
    ctx.lineCap = 'round'
    ctx.lineWidth = 8
    ctx.strokeStyle = 'rgba(167, 139, 250, .25)'
    ctx.beginPath(); ctx.arc(SIZE / 2, SIZE / 2, SIZE / 2 - 5, 0, Math.PI * 2); ctx.stroke()
    ctx.strokeStyle = '#a78bfa'
    ctx.beginPath(); ctx.arc(SIZE / 2, SIZE / 2, SIZE / 2 - 5, angle, angle + Math.PI * 0.6); ctx.stroke()
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

export function useBusyFavicon(running: boolean, waiting: boolean) {
  useEffect(() => {
    if (!running && !waiting) { setIcon(SRC, 'image/svg+xml'); return }
    let stop = false
    let timer = 0
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = SIZE
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    baseIcon().then(img => {
      if (stop) return
      let angle = -Math.PI / 2
      const draw = () => {
        frame(ctx, img, running, waiting, angle)
        setIcon(canvas.toDataURL('image/png'), 'image/png')
        angle += Math.PI / 6
      }
      draw()
      // background tabs throttle timers to ~1/s: the ring still moves, just slower
      if (running) timer = window.setInterval(draw, 90)
    })
    return () => { stop = true; clearInterval(timer); setIcon(SRC, 'image/svg+xml') }
  }, [running, waiting])
}
