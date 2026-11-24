import { test } from 'node:test'
import assert from 'node:assert/strict'
import { codeLang, commandRanges } from '../src/lib/codeLines.ts'

// The groups as "first-last" strings, one per command, for readable asserts.
const groups = (text: string, lang = '') => {
  const seen = new Set<string>()
  for (const [a, b] of commandRanges(text, lang)) seen.add(a === b ? `${a}` : `${a}-${b}`)
  return [...seen]
}

test('a for loop is one command, the lines around it are their own', () => {
  const t = `cd /tmp/driftcheck
for r in bookshelf cost-split files remote web ai; do
  ./push2.sh $r ~/Projects/pocket-homelab-$r git@github.com:abjelosevic88/pocket-homelab-$r.git
done
./push2.sh webforgejo - ssh://git@git.bjelke.org:2222/Homelab/pocket-homelab-web.git`
  assert.deepEqual(groups(t), ['0', '1-3', '4'])
  assert.deepEqual(groups(t, 'bash'), ['0', '1-3', '4'])
})

test('if, case, while, braces, subshells and functions', () => {
  assert.deepEqual(groups('if [ -f x ]; then\n  echo yes\nelse\n  echo no\nfi\necho after'), ['0-4', '5'])
  assert.deepEqual(groups('case $1 in\n  a|b) echo ab ;;\n  *) echo other ;;\nesac'), ['0-3'])
  assert.deepEqual(groups('while read l\ndo\n  echo "$l"\ndone < f'), ['0-3'])
  assert.deepEqual(groups('f() {\n  echo hi\n}\nf'), ['0-2', '3'])
  assert.deepEqual(groups('(\n  cd a && make\n)'), ['0-2'])
  assert.deepEqual(groups('x=$(\n  ls\n)\necho $x'), ['0-2', '3'])
  assert.deepEqual(groups('for i in 1 2; do echo $i; done\necho ok'), ['0', '1'])
})

test('continuations: backslash, pipe, && and ||', () => {
  assert.deepEqual(groups('docker run \\\n  -p 80:80 \\\n  nginx\necho ok'), ['0-2', '3'])
  assert.deepEqual(groups('cat f |\n  grep x &&\n  echo found ||\n  echo none\nls'), ['0-3', '4'])
  assert.deepEqual(groups('a | b\nc && d'), ['0', '1'])
  assert.deepEqual(groups('echo a # trailing comment |\necho b'), ['0', '1'])
})

test('quotes over line breaks and heredocs', () => {
  assert.deepEqual(groups('git commit -m "one\n\ntwo"\ngit push'), ['0-2', '3'])
  assert.deepEqual(groups("echo 'a\nb'\nls"), ['0-1', '2'])
  assert.deepEqual(groups("cat > f <<'EOF'\nfor x in y; do\n$x \"\nEOF\nls"), ['0-3', '4'])
  assert.deepEqual(groups('cat <<-END | sort\n\tb\n\ta\n\tEND\nls'), ['0-3', '4'])
  assert.deepEqual(groups('grep x <<< "$s"\nls'), ['0', '1'])
  assert.deepEqual(groups('echo "it\'s ${HOME}"\nls'), ['0', '1'])
})

test('blank lines between commands stay separate', () => {
  assert.deepEqual(groups('ls\n\npwd'), ['0', '1', '2'])
  assert.deepEqual(groups('for x in a; do\n\n  echo $x\ndone'), ['0-3'])
})

test('anything left open falls back to one line each', () => {
  assert.deepEqual(groups('for x in y:\n    print(x)\nprint(1)'), ['0', '1', '2'])
  assert.deepEqual(groups("Don't do this\nls"), ['0', '1'])
  assert.deepEqual(groups('if x:\n  y', 'python'), ['0', '1'])
})

test('other languages only join backslash lines', () => {
  assert.deepEqual(groups('RUN apt-get update && \\\n  apt-get install -y x\nCOPY . .', 'dockerfile'), ['0-1', '2'])
  assert.deepEqual(groups('func main() {\n}', 'go'), ['0', '1'])
  assert.deepEqual(groups('a \\\\\nb', 'go'), ['0', '1'])
})

test('language from the code class', () => {
  assert.equal(codeLang('language-bash'), 'bash')
  assert.equal(codeLang('hljs language-Shell'), 'shell')
  assert.equal(codeLang(''), '')
})
