import { test } from 'node:test'
import assert from 'node:assert/strict'
import { groupToolActivity, toolLabel } from '../src/lib/tool-activity.ts'
import type { Item } from '../src/lib/types.ts'

const call = (key: string, status: 'done' | 'running' | 'error' | 'denied' = 'done'): Item => ({
  id: 0, key, kind: 'tool', created_at: '', updated_at: '',
  tool: { name: 'mcp__pocket__pocket_money_transactions', kind: 'mcp', title: '', input: {}, output: 'result', status },
})
const message = (key: string, kind: string): Item => ({ id: 0, key, kind, created_at: '', updated_at: '', text: 'message' })

test('repeated calls collapse without losing order or touching source items', () => {
  const items = [message('u', 'user'), ...Array.from({ length: 30 }, (_, i) => call(String(i))), message('a', 'assistant')]
  const before = JSON.stringify(items), result = groupToolActivity(items)
  assert.equal(result.length, 3)
  assert.equal(result[1].type, 'tools')
  if (result[1].type === 'tools') assert.deepEqual(result[1].items.map(i => i.key), items.slice(1, -1).map(i => i.key))
  assert.equal(JSON.stringify(items), before)
})
test('errors, denied calls, approvals, replies and image previews remain outside groups', () => {
  const image = call('picture'); image.tool!.images = 1
  const legacyImage = call('legacy-picture'); legacyImage.tool!.output = '[image]'
  const separate = [call('error', 'error'), call('denied', 'denied'), message('approval', 'request'), message('reply', 'assistant'), image, legacyImage]
  for (const boundary of separate) {
    const groups = groupToolActivity([call('first'), boundary, call('last')])
    assert.deepEqual(groups.map(x => x.type), ['tools', 'item', 'tools'])
    assert.equal(groups[1].key, boundary.key)
  }
})
test('incoming calls and completion keep the group key stable', () => {
  const start = groupToolActivity([call('first', 'running')])[0]
  const updated = groupToolActivity([call('first'), call('next', 'running')])[0]
  assert.equal(start.key, updated.key)
})
test('tool names are readable while preserving ordinary names and action meaning', () => {
  assert.equal(toolLabel(call('x').tool!), 'Read Money transactions')
  assert.equal(toolLabel({ name: 'pocket.pocket_money_summary' }), 'Read Money summary')
  assert.equal(toolLabel({ name: 'mcp__pocket__pocket_notes_update' }), 'Notes · Update')
  assert.equal(toolLabel({ name: 'mcp__pocket__pocket_list_apps' }), 'List Pocket apps')
  assert.equal(toolLabel({ name: 'mcp__github__search_repositories' }), 'Github · Search repositories')
  assert.equal(toolLabel({ name: 'Bash' }), 'Bash')
})
