import { chromium } from 'playwright'
import assert from 'node:assert/strict'
import fs from 'node:fs'

// Serve the built UI with `npm run preview -- --host 127.0.0.1` first.
// All API traffic is mocked: no agent is started or conversation changed.
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:4173'
const browser = await chromium.launch({ headless: true })
try {
 for (const width of [320, 390, 1280]) for (const fallback of [false, true]) {
  const context = await browser.newContext({ viewport: { width, height: 844 }, isMobile: width < 720, hasTouch: width < 720 })
  const user = { id: 1, name: 'Test', role: 'admin', can_run: true }
  const agents = ['Issues and board', 'Inbox and calendar', 'Settings and onboarding'].map((description, i) => ({ id: `agent-${i}`, description, activity: 'Checking the implementation', started_at: new Date().toISOString() }))
  const thread = { id: 'subagent-fixture', owner_id: 1, provider: 'claude', title: 'Sub-agent display check', status: fallback ? 'running' : 'idle', usage: {}, cwd: '', subagents: fallback ? [] : agents }
  await context.route('**/*', route => {
   const url = new URL(route.request().url()), p = url.pathname
   if (url.origin !== base) return route.abort()
   const json = data => route.fulfill({ json: data })
   if (p.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: ': fixture\n\n' })
   if (p === '/api/v1/auth/status') return json({ authenticated: true, user, auth_mode: 'none' })
   if (p === '/api/v1/auth/me') return json(user)
   if (p === '/api/v1/settings') return json({ usage_widget: false })
   if (p === '/api/v1/threads') return json([thread])
   if (p === '/api/v1/threads/subagent-fixture') return json({ thread, items: [
    { key: 'user', kind: 'user', text: 'Build the app' },
    { key: 'assistant', kind: 'assistant', text: 'The three workers are still running.' },
    ...(fallback ? agents.map(a => ({ key: a.id, kind: 'tool', tool: { kind: 'task', name: 'Agent', title: `Agent: ${a.description}`, status: 'running' } })) : []),
   ] })
   if (p.startsWith('/api/')) return json([])
   if (p === '/pocket-ask.js') return route.fulfill({ body: '' })
   return route.continue()
  })
  const page = await context.newPage(), errors = []
  page.on('pageerror', e => errors.push(e.message))
  await page.goto(base + '/t/subagent-fixture')
  const chip = page.getByRole('button', { name: '3 sub-agents', exact: true })
  await chip.waitFor()
  await page.waitForTimeout(300)
  const before = await page.locator('.composer').boundingBox()
  await chip.click()
  const list = page.getByRole('list', { name: 'Sub-agents', exact: true })
  await list.waitFor()
  await page.waitForTimeout(200)
  async function visibleList() {
   const r = await list.boundingBox()
   const size = page.viewportSize()
   assert(r && r.x >= 0 && r.y >= 0 && r.x + r.width <= size.width + 1 && r.y + r.height <= size.height + 1, JSON.stringify(r))
   // Bounding boxes alone pass even when an ancestor clips the list.
   assert(await list.evaluate(el => { const r = el.getBoundingClientRect(); return el.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) }), 'Sub-agent list is clipped or covered')
  }
  await visibleList()
  assert.equal(await list.locator('li').count(), 3)
  assert.equal((await page.locator('.composer').boundingBox()).y, before.y, 'Opening must not shift the conversation')
  await list.locator('li').first().click()
  assert.equal(await chip.getAttribute('aria-expanded'), 'true', 'Clicking the portal must not dismiss it')
  if (process.env.AI_TEST_SCREENSHOTS && !fallback) {
   fs.mkdirSync(process.env.AI_TEST_SCREENSHOTS, { recursive: true })
   await page.screenshot({ path: `${process.env.AI_TEST_SCREENSHOTS}/subagents-${width}.png` })
  }
  await page.setViewportSize({ width, height: 500 })
  await page.waitForTimeout(200)
  await visibleList()
  await page.keyboard.press('Escape')
  assert.equal(await list.count(), 0)
  assert(await chip.evaluate(el => document.activeElement === el))
  await chip.press('Enter')
  await list.waitFor()
  await chip.click()
  assert.equal(await list.count(), 0)
  await chip.click()
  await page.locator('.composer textarea').click()
  assert.equal(await list.count(), 0)
  assert.deepEqual(errors, [])
  console.log(`${width}px ${fallback ? 'tool fallback' : 'live agents'}: visible, resize, stable layout, toggle, outside click, Escape PASS`)
  await context.close()
 }
} finally { await browser.close() }
