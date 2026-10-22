// Authenticated browser release checks. See docs/assistant.md for invocation.
// New assistant APIs are deterministic fixtures; no model runs or app writes.
import { chromium } from 'playwright'
import fs from 'node:fs'
import path from 'node:path'
import assert from 'node:assert/strict'
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:8195'
let env = process.env
if (env.AI_TEST_ENV) env = { ...Object.fromEntries(fs.readFileSync(env.AI_TEST_ENV, 'utf8').split('\n').filter(s => s.includes('=') && !s.startsWith('#')).map(s => { const i = s.indexOf('='); return [s.slice(0, i), s.slice(i + 1).replace(/^['"]|['"]$/g, '')] })), ...env }
assert(env.ADMIN_EMAIL && env.ADMIN_PASSWORD, 'Set ADMIN_EMAIL/ADMIN_PASSWORD or AI_TEST_ENV to a test account environment file')
const browser = env.AI_TEST_CDP ? await chromium.connectOverCDP(env.AI_TEST_CDP) : await chromium.launch({ headless: true })
const screenshots = env.AI_TEST_SCREENSHOTS
if (screenshots) fs.mkdirSync(screenshots, { recursive: true })
const summary = []
try {
 for (const width of [320, 390, 1280]) for (const theme of ['light', 'dark']) {
  const c = await browser.newContext({ viewport: { width, height: 844 }, isMobile: width < 720, hasTouch: width < 720, colorScheme: theme, timezoneId: 'Europe/Sarajevo' })
  try {
   const login = await c.request.post(base + '/api/v1/auth/login', { data: { email: env.ADMIN_EMAIL, password: env.ADMIN_PASSWORD } }); assert(login.ok(), 'Test account login failed')
   await c.addInitScript(({theme,width}) => {
    localStorage.setItem('pai-theme', theme)
    const v = new EventTarget(); Object.assign(v, { height: 844, width, offsetTop: 0, offsetLeft: 0, scale: 1 })
    Object.defineProperty(window, 'visualViewport', { value: v })
    window.testViewport = (height, offsetTop = 0, scale = 1) => { Object.assign(v, { height, offsetTop, scale }); v.dispatchEvent(new Event('resize')); v.dispatchEvent(new Event('scroll')) }
   }, {theme,width})
   const pending = []
   let memories = [], workflows = []
   const records = [{ tool: 'pocket_tasks_summary', checked_at: new Date().toISOString(), duration_ms: 123, data: { source_url: 'https://tasks.example/today', result: { overdue: 3, next: [{ id: 1, title: 'Review a very long invoice title that must wrap on a narrow phone', due: '2026-10-22', item_url: 'https://tasks.example/inbox?task=1' }] } } }]
   const calls = [{ id: 'fixture-action', thread_id: 'fixture', tool: 'pocket_notes_update', input: { id: 1, data: { title: 'Monthly spending', content: 'A long document\nWith multiple lines', category: 'Quick Notes' } }, result: { structuredContent: { source_url: 'https://notes.example/note/1', result: { title: 'Monthly spending', etag: 'after' } } }, before: { old: true }, is_write: true, status: 'done', duration_ms: 320, created_at: new Date().toISOString() }]
   await c.route(base + '/**', async route => {
    const url = new URL(route.request().url()), p = url.pathname, method = route.request().method()
    const json = v => route.fulfill({ json: v })
    if (p === '/api/v1/assistant/status') return json({ connected: true })
    if (p === '/api/v1/briefing' || p === '/api/v1/app-search') return json({ date: '2026-10-22', timezone: 'Europe/Sarajevo', coverage: 'Fixture overview', sections: records })
    if (p === '/api/v1/app-calls') return json(calls)
    if (p === '/api/v1/app-calls/fixture-action') return json(calls[0])
    if (p.startsWith('/api/v1/memory')) { if (method === 'POST') memories.push({ ...route.request().postDataJSON(), id: 1 }); if (method === 'DELETE') memories = []; if (method === 'PUT') memories = [route.request().postDataJSON()]; return json(method === 'GET' ? memories : memories[0] || {}) }
    if (p.startsWith('/api/v1/workflows')) { if (method === 'POST') workflows.push({ ...route.request().postDataJSON(), id: 1 }); return json(method === 'GET' ? workflows : workflows[0] || {}) }
    if (p === '/api/v1/threads/fixture') return json({ thread: { id: 'fixture', provider: 'claude', title: 'Fixture app question', source: 'ask', status: 'waiting', cwd: '/app', no_memory: false }, items: [
     { key: 'u', kind: 'user', text: 'Compare these records', files: [{ id: 'doc', name: 'invoice.csv', url: '/api/v1/uploads/doc' }] },
     { key: 'a', kind: 'assistant', text: '| Category | Amount |\n|---|---:|\n| Groceries | 123.45 EUR |\n\n[Open source](https://notes.example/note/1)\n\n<script>window.badMarkdown=true</script>' },
     ...Array.from({ length: 12 }, (_, i) => ({ key: 'tool' + i, kind: 'tool', tool: { name: 'mcp__pocket__pocket_money_transactions', input: { query: { q: 'Car' } }, output: '{}', status: 'done' } })),
     ...[1, 2].map(n => ({ key: 'request' + n, kind: 'request', request: { id: 'ask-' + n, type: 'permission', title: 'Notes · update', tool_name: 'pocket_notes_update', input: { title: 'Monthly spending ' + n, category: 'Quick Notes', content: 'Preview this exact note before approval.' }, status: pending.includes('ask-' + n) ? 'allowed' : 'pending' } })),
    ] })
    if (p.startsWith('/api/v1/threads/fixture/requests/')) { pending.push(decodeURIComponent(p.split('/').pop())); return json({ ok: true }) }
    if (p === '/api/v1/threads/fixture/events') return route.fulfill({ contentType: 'text/event-stream', body: ': fixture\n\n' })
    if (env.AI_TEST_ASSETS && !p.startsWith('/api/') && p !== '/pocket-ask.js') { const file = path.join(env.AI_TEST_ASSETS, p.startsWith('/assets/') ? p : 'index.html'); if (fs.existsSync(file)) return route.fulfill({ path: file }) }
    return route.continue()
   })
   const p = await c.newPage(), errors = []; p.on('pageerror', e => errors.push(e.message))
   async function overflow() { assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `Overflow at ${width}: ${p.url()}`) }
   await p.goto(base + '/'); await p.getByRole('button', { name: 'Ask my apps', exact: true }).waitFor(); assert.equal(await p.getByRole('button', { name: 'Ask my apps', exact: true }).getAttribute('aria-pressed'), 'true')
   await p.locator('.composer textarea').fill('H')
   if (width < 720) {
    await p.evaluate(() => window.testViewport(375, 110)); await p.waitForTimeout(200)
    const m = await p.evaluate(() => { const a = document.querySelector('.app').getBoundingClientRect(), b = document.querySelector('.composer-bar').getBoundingClientRect(); return { top: a.top, bottom: a.bottom, bar: b.bottom, font: getComputedStyle(document.querySelector('textarea')).fontSize } })
    assert(Math.abs(m.top - 110) < 1 && Math.abs(m.bottom - 485) < 1 && m.bar <= 486, JSON.stringify(m)); assert.equal(m.font, '16px')
    await p.evaluate(() => { document.activeElement.blur(); window.testViewport(844) }); await p.waitForTimeout(100)
   }
   await overflow()
   for (const page of ['briefing', 'memory', 'workflows', 'actions']) {
    await p.goto(base + '/' + page); await p.locator('.assistant-page').waitFor(); await p.waitForTimeout(150); await overflow()
    if (page === 'briefing') { await p.getByRole('link', { name: 'Open source' }).first().waitFor(); assert((await p.locator('.data-fields').count()) > 0) }
    if (page === 'memory') { await p.getByLabel('Name', { exact: true }).fill('My car'); await p.getByLabel('Preference', { exact: true }).fill('Use the Golf'); await p.getByRole('button', { name: 'Save preference', exact: true }).click(); await p.getByRole('button', { name: 'Forget', exact: true }).waitFor(); await p.getByRole('button', { name: 'Forget', exact: true }).click() }
    if (page === 'actions') { await p.locator('.app-call details > summary').first().click(); await p.getByText('Monthly spending', {exact:true}).first().waitFor() }
    if (page === 'workflows') { await p.getByLabel('Name', { exact: true }).fill('Weekly spending'); await p.getByLabel('Instructions', { exact: true }).fill('Summarize spending'); await p.getByRole('button', { name: 'Create workflow', exact: true }).click(); await p.getByRole('button', { name: 'Run now', exact: true }).waitFor() }
    if (screenshots && width === 390) await p.screenshot({ path: path.join(screenshots, `${page}-${theme}.png`) })
   }
   await p.goto(base + '/t/fixture'); await p.locator('.batch-review').waitFor(); assert.equal(await p.locator('.md table').count(), 1); assert.equal(await p.evaluate(() => !!window.badMarkdown), false); assert.equal(await p.locator('a.file-chip').count(), 1)
   if(env.AI_TEST_DEBUG) { console.log(await p.evaluate(() => Object.fromEntries(['.app','.thread-page','.thread-bottom','.req-dock','.batch-review','.batch-review summary','.composer'].map(s => { const e=document.querySelector(s), r=e?.getBoundingClientRect();return [s,r?{top:r.top,bottom:r.bottom,scroll:e.scrollHeight,client:e.clientHeight}:null] })))); await p.screenshot({path:'/tmp/pocket-batch-debug.png'}) }
   await p.locator('.batch-review > summary').click(); await p.locator('.batch-review input[type=checkbox]').first().check(); await p.getByRole('button', { name: 'Allow 1 selected' }).click(); await p.waitForTimeout(150); assert.deepEqual(pending, ['ask-1']); await overflow()
   assert.deepEqual(errors, []); summary.push({ width, theme, checks: 'home, keyboard, sources, memory, workflow, actions, markdown, attachment, selected approval, overflow' })
  } finally { await c.close() }
 }
 console.log(JSON.stringify(summary, null, 2))
} finally { await browser.close() }
