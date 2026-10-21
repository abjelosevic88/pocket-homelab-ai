// EventSource with reconnect + backoff. The browser reconnects on its own after
// a dropped stream, but gives up for good on an HTTP error; this keeps trying.
export type SSEHandlers = Record<string, (data: unknown) => void>

export interface SSEOptions {
  onOpen?: () => void
  onDown?: () => void
}

export function openSSE(url: string, handlers: SSEHandlers, opts: SSEOptions = {}): () => void {
  let es: EventSource | null = null
  let closed = false
  let wait = 1000
  let timer: ReturnType<typeof setTimeout> | null = null

  const connect = () => {
    if (closed) return
    es = new EventSource(url, { withCredentials: true })
    es.onopen = () => { wait = 1000; opts.onOpen?.() }
    es.onerror = () => {
      opts.onDown?.()
      if (es && es.readyState === EventSource.CLOSED) {
        es = null
        timer = setTimeout(connect, wait)
        wait = Math.min(wait * 2, 30000)
      }
    }
    for (const [name, fn] of Object.entries(handlers)) {
      es.addEventListener(name, (e: MessageEvent) => {
        let d: unknown = e.data
        try { d = JSON.parse(e.data) } catch { /* plain text */ }
        fn(d)
      })
    }
  }
  connect()
  // A phone that slept for an hour has a dead socket that still says OPEN:
  // reopen when the page becomes visible again.
  const onShow = () => {
    if (document.hidden || closed) return
    if (!es || es.readyState !== EventSource.OPEN) { if (timer) clearTimeout(timer); es?.close(); wait = 1000; connect() }
  }
  document.addEventListener('visibilitychange', onShow)
  return () => {
    closed = true
    if (timer) clearTimeout(timer)
    es?.close()
    document.removeEventListener('visibilitychange', onShow)
  }
}
