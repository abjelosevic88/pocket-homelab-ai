// Which lines of a code block belong together as one command, so the per-line
// copy button can copy all of them: a `for … do … done` loop, an `if … fi`,
// a `case … esac`, `{ … }` and `( … )` groups, lines ending in `\`, `|`, `&&`
// or `||`, quotes left open over a line break, and heredocs (`<<EOF … EOF`).
//
// Shell rules apply to shell blocks (bash, sh, zsh, shell, console) and to
// blocks without a language; other languages only join lines ending in `\`.
// A command that never closes (not shell after all, or cut off) falls back to
// one line each.

const SHELL = new Set(['', 'sh', 'bash', 'zsh', 'shell', 'console', 'shellsession', 'ksh'])

// The language of a <code> element from marked's "language-x" class.
export function codeLang(className: string): string {
  return /(?:^|\s)language-([\w+-]+)/.exec(className)?.[1].toLowerCase() ?? ''
}

/** For every line of text, the [first, last] line of the command it is part of. */
export function commandRanges(text: string, lang = ''): Array<[number, number]> {
  const lines = text.split('\n')
  const ends = SHELL.has(lang) ? shellEnds(lines) : backslashEnds(lines)
  const out: Array<[number, number]> = []
  let start = 0
  for (let i = 0; i < lines.length; i++) {
    if (!ends[i] && i < lines.length - 1) continue
    if (!ends[i]) { // ran off the end still open: lines on their own
      for (let j = start; j <= i; j++) out.push([j, j])
    } else {
      // Leading/trailing blank lines are not part of the command.
      let a = start, b = i
      while (a < b && !lines[a].trim()) out.push([a, a]), a++
      while (b > a && !lines[b].trim()) b--
      for (let j = a; j <= b; j++) out.push([a, b])
      for (let j = b + 1; j <= i; j++) out.push([j, j])
    }
    start = i + 1
  }
  return out
}

// ends[i]: line i ends a command (nothing is left open after it).
function backslashEnds(lines: string[]): boolean[] {
  return lines.map(l => !/(^|[^\\])(\\\\)*\\$/.test(l))
}

type Open = 'loop' | 'if' | 'case' | '{' | '('

const OPENERS: Record<string, Open> = { for: 'loop', while: 'loop', until: 'loop', select: 'loop', if: 'if', case: 'case', '{': '{' }
const CLOSERS: Record<string, Open> = { done: 'loop', fi: 'if', esac: 'case', '}': '{' }
// After these words the next word is a command again.
const LEADS = new Set(['then', 'do', 'else', 'elif', '!', 'time', '{', 'if', 'while', 'until'])

function shellEnds(lines: string[]): boolean[] {
  const stack: Open[] = []
  let quote = '' // "'" or '"' left open over a line break
  let heredocs: Array<{ word: string; tabs: boolean }> = []
  let body: { word: string; tabs: boolean } | null = null
  const ends: boolean[] = []

  for (const line of lines) {
    if (body) { // inside a heredoc: only its end word matters
      const t = body.tabs ? line.replace(/^\t+/, '') : line
      if (t === body.word) body = heredocs.shift() ?? null
      ends.push(!body && !stack.length && !quote)
      continue
    }
    let cmd = !quote // at a word that would be a command name
    let cont = false // the line asks for more (\, |, &&, ||)
    let word = ''
    let wordQuoted = false
    let i = 0
    const endWord = () => {
      if (!word) return
      const w = word
      word = ''
      if (wordQuoted) { wordQuoted = false; cmd = false; return }
      if (cmd && OPENERS[w]) { stack.push(OPENERS[w]); cmd = true; return }
      if (cmd && CLOSERS[w]) {
        if (stack[stack.length - 1] === CLOSERS[w]) stack.pop()
        cmd = false
        return
      }
      cmd = cmd && LEADS.has(w)
    }
    while (i < line.length) {
      const c = line[i]
      if (quote) {
        if (quote === '"' && c === '\\') { i += 2; continue }
        if (c === quote) quote = ''
        i++
        continue
      }
      if (c === '\\') {
        if (i === line.length - 1) { cont = true; i++; continue }
        word += line.slice(i, i + 2); i += 2; continue
      }
      if (c === "'" || c === '"') { quote = c; word += c; wordQuoted = true; i++; continue }
      if (c === '#' && !word) break // comment
      if (c === ' ' || c === '\t') { endWord(); i++; continue }
      if (c === '<' && line.startsWith('<<', i) && !line.startsWith('<<<', i)) {
        endWord()
        let j = i + 2
        const tabs = line[j] === '-'
        if (tabs) j++
        while (line[j] === ' ' || line[j] === '\t') j++
        const m = /^(?:'([^']*)'|"([^"]*)"|\\?([^\s;&|<>()]+))/.exec(line.slice(j))
        if (m) {
          heredocs.push({ word: m[1] ?? m[2] ?? m[3].replace(/['"\\]/g, ''), tabs })
          i = j + m[0].length
          cmd = false
          continue
        }
        i += 2
        continue
      }
      if (c === '$' && line[i + 1] === '(') { // $( … ) and $(( … ))
        word += '$('
        stack.push('(')
        i += 2
        continue
      }
      if (c === '(') { endWord(); stack.push('('); cmd = true; i++; continue }
      if (c === ')') {
        endWord()
        // In a case, "pattern)" closes nothing.
        if (stack[stack.length - 1] === '(') stack.pop()
        cmd = true
        i++
        continue
      }
      if (c === ';' || c === '&' || c === '|') {
        endWord()
        const two = line.slice(i, i + 2)
        const op = two === '&&' || two === '||' || two === ';;' ? two : c
        i += op.length
        // Only a pipe or && / || at the very end carries on to the next line.
        cont = (op === '|' || op === '&&' || op === '||') && !line.slice(i).replace(/\s+#.*$/, '').trim()
        cmd = true
        continue
      }
      if (c === '<' || c === '>') { endWord(); cmd = false; i += line.startsWith('<<<', i) ? 3 : 1; continue }
      word += c
      i++
    }
    endWord()
    if (heredocs.length) body = heredocs.shift()!
    ends.push(!quote && !cont && !body && !stack.length)
  }
  return ends
}
