// A small text diff for comparing two versions of a plan: by line, and by
// word inside a line that changed.

export type DiffOp<T> = { kind: 'same' | 'add' | 'del'; item: T }

/** Longest-common-subsequence diff of two lists. */
export function diffList<T>(a: T[], b: T[], eq: (x: T, y: T) => boolean = (x, y) => x === y): DiffOp<T>[] {
  // trim what is equal at both ends first: versions of one text mostly are
  let start = 0
  while (start < a.length && start < b.length && eq(a[start], b[start])) start++
  let endA = a.length, endB = b.length
  while (endA > start && endB > start && eq(a[endA - 1], b[endB - 1])) { endA--; endB-- }
  const x = a.slice(start, endA), y = b.slice(start, endB)
  const out: DiffOp<T>[] = a.slice(0, start).map(item => ({ kind: 'same' as const, item }))
  const n = x.length, m = y.length
  if (n * m > 4_000_000) {
    // too big to line up: all of the old, then all of the new
    out.push(...x.map(item => ({ kind: 'del' as const, item })), ...y.map(item => ({ kind: 'add' as const, item })))
  } else {
    const w = m + 1
    const t = new Uint32Array((n + 1) * w)
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) t[i * w + j] = eq(x[i], y[j]) ? t[(i + 1) * w + j + 1] + 1 : Math.max(t[(i + 1) * w + j], t[i * w + j + 1])
    let i = 0, j = 0
    while (i < n && j < m) {
      if (eq(x[i], y[j])) { out.push({ kind: 'same', item: x[i] }); i++; j++ }
      else if (t[(i + 1) * w + j] >= t[i * w + j + 1]) out.push({ kind: 'del', item: x[i++] })
      else out.push({ kind: 'add', item: y[j++] })
    }
    while (i < n) out.push({ kind: 'del', item: x[i++] })
    while (j < m) out.push({ kind: 'add', item: y[j++] })
  }
  out.push(...a.slice(endA).map(item => ({ kind: 'same' as const, item })))
  return out
}

export type DiffLine =
  | { kind: 'same'; text: string }
  | { kind: 'add' | 'del'; text: string; parts?: { text: string; hit: boolean }[] }
  | { kind: 'skip'; count: number }

const words = (s: string) => s.split(/(\s+)/).filter(x => x !== '')

/** Two texts as lines to show: removed, added (with the changed words marked when a line
 *  was reworded), and unchanged ones around them; long unchanged stretches are folded. */
export function diffLines(before: string, after: string, context = 2): DiffLine[] {
  const ops = diffList(before.split('\n'), after.split('\n'))
  const rows: DiffLine[] = []
  for (let i = 0; i < ops.length;) {
    if (ops[i].kind === 'same') { rows.push({ kind: 'same', text: ops[i].item }); i++; continue }
    const dels: string[] = [], adds: string[] = []
    while (i < ops.length && ops[i].kind !== 'same') { (ops[i].kind === 'del' ? dels : adds).push(ops[i].item); i++ }
    // a line for a line: mark the words that differ
    const pair = dels.length === adds.length
    dels.forEach((d, k) => {
      const w = pair ? diffList(words(d), words(adds[k])) : null
      const changed = w ? w.filter(o => o.kind !== 'same').length / Math.max(1, w.length) : 1
      rows.push({ kind: 'del', text: d, parts: w && changed < 0.7 ? w.filter(o => o.kind !== 'add').map(o => ({ text: o.item, hit: o.kind === 'del' })) : undefined })
      if (pair) rows.push({ kind: 'add', text: adds[k], parts: w && changed < 0.7 ? w.filter(o => o.kind !== 'del').map(o => ({ text: o.item, hit: o.kind === 'add' })) : undefined })
    })
    if (!pair) adds.forEach(a => rows.push({ kind: 'add', text: a }))
  }
  // fold long runs of unchanged lines
  const out: DiffLine[] = []
  for (let i = 0; i < rows.length;) {
    if (rows[i].kind !== 'same') { out.push(rows[i++]); continue }
    let j = i
    while (j < rows.length && rows[j].kind === 'same') j++
    const head = i === 0 ? 0 : context, tail = j === rows.length ? 0 : context
    if (j - i > head + tail + 3) {
      out.push(...rows.slice(i, i + head), { kind: 'skip', count: j - i - head - tail }, ...rows.slice(j - tail, j))
    } else out.push(...rows.slice(i, j))
    i = j
  }
  return out
}

/** How much changed between two texts, in lines. */
export function diffCount(before: string, after: string): { added: number; removed: number } {
  const ops = diffList(before.split('\n'), after.split('\n'))
  return { added: ops.filter(o => o.kind === 'add').length, removed: ops.filter(o => o.kind === 'del').length }
}
