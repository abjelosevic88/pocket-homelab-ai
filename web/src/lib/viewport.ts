// iOS Safari scrolls the whole page to keep a focused input above the
// keyboard and does not always scroll back, which pushes the header off the
// top. Size the app to the visible viewport and keep the page at the top.
export function trackViewport() {
  const vv = window.visualViewport
  let raf = 0
  const set = () => {
    cancelAnimationFrame(raf)
    raf = requestAnimationFrame(() => {
      const h = vv ? vv.height : window.innerHeight
      document.documentElement.style.setProperty('--app-h', `${Math.round(h)}px`)
      if (window.scrollY !== 0 || window.scrollX !== 0) window.scrollTo(0, 0)
    })
  }
  set()
  vv?.addEventListener('resize', set)
  vv?.addEventListener('scroll', set)
  window.addEventListener('resize', set)
  window.addEventListener('orientationchange', set)
  document.addEventListener('focusout', () => setTimeout(set, 60))
}
