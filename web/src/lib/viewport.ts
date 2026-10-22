// The keyboard can shrink AND pan Safari's visual viewport. Follow both
// dimensions instead of fighting the browser with repeated window.scrollTo().
export function trackViewport() {
  const vv = window.visualViewport
  const root = document.documentElement
  let raf = 0
  const set = () => {
    cancelAnimationFrame(raf)
    raf = requestAnimationFrame(() => {
      // Keep pinch zoom native: resizing/repositioning the app during a pinch
      // would make content chase the user's fingers.
      if (vv && Math.abs(vv.scale - 1) > 0.05) return
      const height = vv?.height ?? window.innerHeight
      const top = vv?.offsetTop ?? 0
      const focused = document.activeElement as HTMLElement | null
      const editing = !!focused?.matches('textarea, input:not([type="checkbox"]):not([type="radio"]), [contenteditable="true"]')
      const keyboard = editing && root.clientHeight - height > 120
      root.style.setProperty('--app-h', `${Math.round(height)}px`)
      root.style.setProperty('--app-top', `${Math.round(top)}px`)
      root.classList.toggle('keyboard-open', keyboard)

      // Safari reveals the caret, not the send/model controls below it. Scroll
      // only the home page's own container to keep the whole composer in view.
      if (keyboard) {
        const composer = focused?.closest('.composer')
        const scroll = composer?.closest('.home-scroll')
        if (composer && scroll) {
          const box = composer.getBoundingClientRect()
          const view = scroll.getBoundingClientRect()
          if (box.bottom > view.bottom - 8) scroll.scrollTop += box.bottom - view.bottom + 8
          else if (box.top < view.top + 8) scroll.scrollTop -= view.top + 8 - box.top
        }
      }
    })
  }
  set()
  vv?.addEventListener('resize', set)
  vv?.addEventListener('scroll', set)
  window.addEventListener('resize', set)
  window.addEventListener('orientationchange', set)
  document.addEventListener('focusin', set)
  document.addEventListener('input', e => {
    if ((e.target as HTMLElement)?.closest('.composer')) set()
  })
  document.addEventListener('focusout', () => setTimeout(set, 60))
}
