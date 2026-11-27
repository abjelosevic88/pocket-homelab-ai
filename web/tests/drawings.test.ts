import { test } from 'node:test'
import assert from 'node:assert/strict'
import { drawingsIn, svgFileName, svgUrl, withoutDrawings } from '../src/lib/drawings.ts'

const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><title>Pocket &amp; spark</title><rect width="512" height="512"/></svg>'
const answer = `**Convergence grid.** Four tiles.\n\n\`\`\`svg\n<svg viewBox="0 0 10 10"><rect/></svg>\n\`\`\`\n\n**Pocket + spark** idea\n\n\`\`\`svg\n${svg}\n\`\`\`\n\nStill writing\n\n\`\`\`svg\n<svg viewBox="0 0 10 10"><rect/>`

test('finished drawings, named by their title or the bold words above', () => {
  const d = drawingsIn(answer)
  assert.equal(d.length, 2)
  assert.equal(d[0].title, 'Convergence grid')
  assert.equal(d[1].title, 'Pocket & spark')
})

test('no drawings in plain text, other code blocks are not drawings', () => {
  assert.deepEqual(drawingsIn('```xml\n<svg></svg>\n```'), [])
  assert.deepEqual(drawingsIn(undefined), [])
})

test('word counts leave drawings out', () => {
  assert.ok(!withoutDrawings(answer.slice(0, answer.lastIndexOf('Still'))).includes('<svg'))
})

test('image address carries the namespace', () => {
  assert.ok(decodeURIComponent(svgUrl('<svg viewBox="0 0 1 1"></svg>')).includes('xmlns="http://www.w3.org/2000/svg"'))
  assert.equal(svgFileName('Pocket + spark!'), 'pocket-spark.svg')
})
