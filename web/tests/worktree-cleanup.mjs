// Run against a disposable local test server, as with the mobile suite.
import { chromium } from 'playwright'
import assert from 'node:assert/strict'
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:8196'
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH })
try {
 for (const width of [390, 1280]) {
  const context = await browser.newContext({ viewport: { width, height: 900 } })
  let cleaned = false, auto = false, previewed = false, latest = null
  const now = new Date().toISOString()
  await context.route(`${base}/api/v1/**`, async route => {
   const req = route.request(), p = new URL(req.url()).pathname
   const json = data => route.fulfill({ json: data })
   if (p === '/api/v1/worktrees/cleanup') {
    if (req.method() === 'GET') return json(latest)
    const input = req.postDataJSON()
    if (input.preview) {
     previewed = true
     return json({ archived: [], skipped: ['unfinished-work'], checked_at: now, items: [
      { id: 1, branch: 'merged-work', status: 'ready', reason: 'Merged and safe to clean up' },
      { id: 2, branch: 'unfinished-work', status: 'skipped', reason: 'Uncommitted or untracked files' },
     ] })
    }
    assert.deepEqual(input.ids, [1], 'only reviewed worktrees are sent')
    assert.equal(input.delete_branches, true)
    cleaned = true
    return json(latest = { archived: ['merged-work'], skipped: [], checked_at: now, items: [{ id: 1, branch: 'merged-work', status: 'archived', reason: 'Cleaned up; conversation kept' }] })
   }
   if (p === '/api/v1/worktrees') return json([1, 2].map(id => ({ id, owner_id: 1, host_id: 0, repo: '/projects/app', path: `/projects/topic-${id}`, branch: id === 1 ? 'merged-work' : 'unfinished-work', base: 'main', status: cleaned && id === 1 ? 'archived' : 'active', setup: '', title: `Task ${id}`, created_at: now, threads: [] })))
   if (p === '/api/v1/worktrees/others' || p === '/api/v1/worktree-groups') return json([])
   if (p === '/api/v1/settings') {
    if (req.method() === 'PUT') auto = !!req.postDataJSON().worktree_auto_cleanup
    const result = await route.fetch()
    return json({ ...await result.json(), worktree_auto_cleanup: auto })
   }
   return route.continue()
  })
  const page = await context.newPage(), errors = []
  page.on('pageerror', e => errors.push(e.message))
  await page.goto(`${base}/worktrees`)
  await page.getByRole('button', { name: 'Clean up merged…', exact: true }).click()
  await page.getByText('Uncommitted or untracked files', { exact: true }).waitFor()
  assert.equal(previewed, true)
  assert.equal(cleaned, false)
  await page.getByRole('button', { name: 'Clean up 1 merged', exact: true }).click()
  await page.getByRole('button', { name: 'Clean up', exact: true }).click()
  await page.getByText('Cleaned up; conversation kept', { exact: true }).waitFor()
  assert.equal(cleaned, true)
  await page.getByRole('checkbox', { name: 'Automatically clean up merged worktrees' }).check()
  await page.getByText('Automatic cleanup enabled; checks run hourly', { exact: true }).waitFor()
  assert.equal(auto, true)
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
  assert.deepEqual(errors, [])
  await page.screenshot({ path: `/tmp/pocket-cleanup-${width}.png`, fullPage: true })
  await context.close()
  console.log(`Worktree cleanup UI passed at ${width}px`)
 }
} finally { await browser.close() }
