// Paths in what an agent writes open in Pocket AI's own file viewer, on the
// machine the conversation runs on: Markdown links to a file
// ([review.md](/home/me/x/review.md), file:///…, a path relative to the
// conversation's folder, "…:12" or "#L12" after it) and an absolute path
// written as inline code (`/tmp/report.zip`). Left alone, a link to /home/…
// opened Pocket AI's own address /home/… — a blank page.
// No imports: tests/filelinks.test.ts runs it under plain node.

/** Where paths in a message are: the machine (0 = this server) and the folder relative paths start from. */
export interface FileLinkPlace { host: number; cwd: string }

// First path segments that are Pocket AI's own pages or API, never a file.
const APP = new Set(['t', 'f', 'files', 'term', 'terminal', 'recipes', 'schedules', 'history', 'shared', 'today', 'briefing',
  'memory', 'actions', 'workflows', 'usage', 'skills', 'snippets', 'away', 'worktrees', 'notebooks', 'panels', 'settings', 'login',
  'setup', 'api', 'v1', 'assets', 'share', 's'])

// "…/x.go:12", "…/x.go:12:5", "…/x.go#L12", "…/x.go#L12-L20"
const lineSuffix = /(?::\d+(?::\d+)?|#L\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?)$/

function clean(p: string): string {
  let q = p.replace(lineSuffix, '')
  try { q = decodeURIComponent(q) } catch { /* keep it as written */ }
  return q
}

function normalize(abs: string): string {
  const out: string[] = []
  for (const seg of abs.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') out.pop(); else out.push(seg)
  }
  return '/' + out.join('/') + (abs.endsWith('/') && out.length ? '/' : '')
}

function looksAbsolute(p: string): boolean {
  if (p.startsWith('~/')) return p.length > 2
  if (!p.startsWith('/') || p.startsWith('//')) return false
  const segs = p.split('/').filter(Boolean)
  return segs.length >= 2 && !APP.has(segs[0])
}

/** The local path a Markdown link points at, or '' when it is a web link (or a Pocket AI page). */
export function linkPath(href: string, cwd: string): string {
  const h = href.trim()
  if (!h || h.startsWith('#') || h.startsWith('?')) return ''
  if (/^file:\/\//i.test(h)) {
    const p = clean(h.replace(/^file:\/\/(localhost)?/i, ''))
    return p.startsWith('/') ? normalize(p) : ''
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(h)) return '' // https:, mailto:, …
  const p = clean(h)
  if (p.startsWith('/') || p.startsWith('~/')) return looksAbsolute(p) ? (p.startsWith('~') ? p : normalize(p)) : ''
  // Relative: only with a folder to start from, and only what looks like a file or folder.
  if (!cwd || !/^[\w.@-][^\s]*$/.test(p) || !(/\.[A-Za-z0-9]{1,8}$/.test(p) || p.includes('/'))) return ''
  return normalize(cwd.replace(/\/+$/, '') + '/' + p)
}

/** An absolute path written as inline code, or ''. Narrow on purpose: `/users/:id` or a glob stay code. */
export function codePath(text: string): string {
  const t = text.trim()
  if (!/^(?:~|\/)[^\s`'"<>*?{}$|;&]+$/.test(t)) return ''
  const p = clean(t)
  if (p.includes(':') || !looksAbsolute(p)) return ''
  return p.startsWith('~') ? p : normalize(p)
}
