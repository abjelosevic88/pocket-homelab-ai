import { chromium } from 'playwright'
import assert from 'node:assert/strict'
import fs from 'node:fs'

// Run against `npm run preview`. API fixtures never invoke a real agent.
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:4173'
const browser = await chromium.launch({ headless: true })
try {
 for (const width of [390, 1280]) {
  const mobile = width < 720
  const context = await browser.newContext({ viewport: { width, height: 900 }, isMobile: mobile, hasTouch: mobile })
  const user = { id: 1, name: 'Test', role: 'admin', can_run: true }
  let settings = { enter_sends: true, enter_send_now: false, usage_widget: false }
  const upload = { id: 'doc', name: 'notes.txt', mime: 'text/plain', url: '/api/v1/uploads/doc', size: 10 }
  let queued = [{ key: 'q', kind: 'user', text: 'Original queued message', queued: true, files: [upload] }]
  const thread = { id: 'composer-fixture', owner_id: 1, provider: 'claude', title: 'Composer check', status: 'running', usage: {}, cwd: '', queued: 1 }
  const stopped = { ...thread, id: 'stopped', title: 'Earlier conversation', status: 'idle', queued: 0 }
  const continued = { ...stopped }
  const sent = [], rewinds = []
  let retained = [{ key: 'old', kind: 'user', text: 'Earlier message', files: [upload] }, { key: 'reply', kind: 'assistant', text: 'Earlier answer' }]
  await context.route('**/*', route => {
   const req = route.request(), url = new URL(req.url()), p = url.pathname
   if (url.origin !== base) return route.abort()
   const json = data => route.fulfill({ json: data })
   if (p.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: ': fixture\n\n' })
   if (p === '/api/v1/auth/status') return json({ authenticated: true, user, auth_mode: 'none' })
   if (p === '/api/v1/auth/me') return json(user)
   if (p === '/api/v1/settings') { if (req.method() === 'PUT') settings = { ...settings, ...req.postDataJSON() }; return json(settings) }
   if (p === '/api/v1/threads') return json([thread, stopped])
   if (p === '/api/v1/threads/composer-fixture') return json({ thread: { ...thread, queued: queued.length }, items: [{ key: 'a', kind: 'assistant', text: 'Working on your request.' }, ...queued] })
   if (p === '/api/v1/threads/stopped') return json({ thread: stopped, items: retained })
   if (p === '/api/v1/threads/stopped/rewind') { const body = req.postDataJSON(); rewinds.push(body); const index = retained.findIndex(it => it.key === body.key); const target = retained[index]; retained = retained.slice(0, index); return json({ thread: continued, items: retained, draft: { text: target.text, uploads: target.files || [] } }) }
   if (p.endsWith('/queue/q/edit')) { queued = []; return json({ text: 'Original queued message', uploads: [upload] }) }
   if (p.endsWith('/messages')) { sent.push({ thread: p.split('/')[4], ...req.postDataJSON() }); return json({ queued: p.includes('composer-fixture'), thread: p.includes('composer-fixture') ? thread : continued }) }
   if (p.startsWith('/api/')) return json([])
   if (p === '/pocket-ask.js') return route.fulfill({ body: '' })
   return route.continue()
  })
  const page = await context.newPage(), errors = []
  page.on('pageerror', e => errors.push(e.message))
  const box = page.locator('.composer textarea')
  const go = path => page.goto(base + path)
  const noOverflow = async () => assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1))
  await go('/t/composer-fixture')
  await box.fill('Keep my existing draft')
  await page.getByRole('button', { name: 'Edit queued message', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value.startsWith('Original queued message'))
  assert.equal(await box.inputValue(), 'Original queued message\n\nKeep my existing draft')
  await page.getByRole('button', { name: 'Remove notes.txt', exact: true }).waitFor()
  assert.equal(await page.getByRole('dialog').count(), 0)
  assert.equal(await page.locator('.queue-strip').count(), 0)
  await page.reload()
  await box.waitFor()
  assert.equal(await box.inputValue(), 'Original queued message\n\nKeep my existing draft')
  await page.getByRole('button', { name: 'Remove notes.txt', exact: true }).waitFor()
  await box.fill('Edited queue message')
  if (mobile) await page.getByRole('button', { name: 'Queue message', exact: true }).click()
  else await box.press('Enter')
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '')
  assert.equal(sent.at(-1).send_now, false)
  assert.deepEqual(sent.at(-1).upload_ids, ['doc'])
  assert.equal(await page.getByText('Queued — it goes out when the agent is done', { exact: true }).count(), 0)
  for (const key of ['Meta+Enter', 'Control+Enter']) {
   await box.fill(key)
   await page.waitForFunction(() => !document.querySelector('.composer .send-btn')?.disabled)
   await box.press(key)
   await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '')
   assert.equal(sent.at(-1).send_now, true)
  }
  await box.fill('Line one')
  await box.press('Shift+Enter')
  assert.equal(await box.inputValue(), 'Line one\n')
  const before = sent.length
  await box.evaluate(el => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', metaKey: true, isComposing: true, bubbles: true })))
  assert.equal(sent.length, before)
  if (mobile) { await box.press('Enter'); assert.equal(sent.length, before) }
  await go('/settings/general')
  await page.getByRole('button', { name: 'Send behavior', exact: true }).click()
  await page.getByRole('option', { name: /^Send immediately/ }).click()
  assert.equal(settings.enter_send_now, true)
  await page.reload()
  await page.getByRole('button', { name: 'Send behavior', exact: true }).waitFor()
  assert.match(await page.getByRole('button', { name: 'Send behavior', exact: true }).innerText(), /Send immediately/)
  await noOverflow()
  if (process.env.AI_TEST_SCREENSHOTS) {
   fs.mkdirSync(process.env.AI_TEST_SCREENSHOTS, { recursive: true })
   await page.getByRole('button', { name: 'Send behavior', exact: true }).scrollIntoViewIfNeeded()
   await page.screenshot({ path: `${process.env.AI_TEST_SCREENSHOTS}/settings-${width}.png` })
  }
  await go('/t/composer-fixture')
  await box.fill('Immediate default')
  if (mobile) await page.getByRole('button', { name: 'Send now', exact: true }).click()
  else await box.press('Enter')
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '')
  assert.equal(sent.at(-1).send_now, true)
  await box.fill('Alternate queues')
  await box.press('Meta+Enter')
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '')
  assert.equal(sent.at(-1).send_now, false)
  await go('/settings/general')
  await page.getByRole('switch', { name: 'Enter sends', exact: true }).click()
  await go('/t/composer-fixture')
  await box.fill('Newline setting')
  const n = sent.length
  await box.press('Enter')
  assert.equal(await box.inputValue(), 'Newline setting\n')
  assert.equal(sent.length, n)
  await go('/t/stopped')
  await box.fill('Original conversation draft')
  await page.getByRole('button', { name: 'Edit message', exact: true }).click()
  await page.getByRole('button', { name: 'Rewind', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === 'Earlier message\n\nOriginal conversation draft')
  assert.equal(new URL(page.url()).pathname, '/t/stopped')
  assert.equal(await page.getByText('Earlier answer', { exact: true }).count(), 0)
  assert.deepEqual(rewinds, [{ key: 'old' }])
  assert.equal(await page.getByRole('dialog').count(), 0)
  await page.getByRole('button', { name: 'Remove notes.txt', exact: true }).waitFor()
  await box.fill('Modified in composer')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '')
  assert.equal(sent.at(-1).thread, 'stopped')
  assert.equal(sent.at(-1).text, 'Modified in composer')
  assert.deepEqual(sent.at(-1).upload_ids, ['doc'])
  await go('/t/stopped')
  await box.waitFor()
  assert.equal(await box.inputValue(), '')
  assert.equal(await page.getByText('Earlier answer', { exact: true }).count(), 0)
  retained = [
   { key: 'u1', kind: 'user', text: 'First question' },
   { key: 'a1', kind: 'assistant', text: 'Keep this reply' },
   { key: 'turn1', kind: 'turn', turn: { ok: true } },
   { key: 'u2', kind: 'user', text: 'Remove this later question' },
   { key: 'a2', kind: 'assistant', text: 'Remove this later reply' },
   { key: 'turn2', kind: 'turn', turn: { ok: true } },
  ]
  await go('/t/stopped')
  await page.getByRole('button', { name: 'Rewind to here', exact: true }).click()
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  assert.equal(rewinds.length, 1)
  await page.getByRole('button', { name: 'Rewind to here', exact: true }).click()
  await page.getByRole('button', { name: 'Rewind', exact: true }).click()
  await page.getByText('Remove this later reply', { exact: true }).waitFor({ state: 'detached' })
  assert.equal(new URL(page.url()).pathname, '/t/stopped')
  assert.deepEqual(rewinds.at(-1), { key: 'u2' })
  assert.equal(await box.inputValue(), '')
  assert.equal(await page.getByText('Keep this reply', { exact: true }).count(), 1)
  await page.reload()
  await page.getByText('Keep this reply', { exact: true }).waitFor()
  assert.equal(await page.getByText('Remove this later reply', { exact: true }).count(), 0)
  await noOverflow()
  assert.deepEqual(errors, [])
  console.log(`${width}px: queued edit + attachment + existing draft + reload, no toast/modal, keyboard defaults/swap/IME/newline, settings persistence, same-chat edit and reply rewind persist after reload, attachments and drafts preserved PASS`)
  await context.close()
 }
} finally { await browser.close() }
