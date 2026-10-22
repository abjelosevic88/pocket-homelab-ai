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
   const pending = [], askPrompts = []
   let memories = [], workflows = []
   const records = [{ tool: 'pocket_tasks_summary', checked_at: new Date().toISOString(), duration_ms: 123, data: { source_url: 'https://tasks.example/today', result: { overdue: 3, next: [{ id: 1, title: 'Review a very long invoice title that must wrap on a narrow phone', due: '2026-10-22', item_url: 'https://tasks.example/inbox?task=1' }] } } }]
   records[0].data.result.next.push(...[2,3,4].map(id => ({id,title:`Another task ${id}`,item_url:`https://tasks.example/inbox?task=${id}`})))
   records.push({tool:'pocket_calendar_events',checked_at:new Date().toISOString(),duration_ms:20,data:{source_url:'https://calendar.example',result:[]}})
   records.push({tool:'pocket_collaborator_summary',checked_at:new Date().toISOString(),duration_ms:20,data:{result:{open_reviews:1,waiting:[{review:{id:1,title:'Approve contract'}}],recent:[{id:2,summary:'Started a reading session'}]}}})
   records.push({tool:'pocket_garage_reminders',checked_at:new Date().toISOString(),duration_ms:20,error:'This source could not be read.'})
   const calls = [{ id: 'fixture-action' , thread_id: 'fixture', tool: 'pocket_notes_update', input: { id: 1, data: { title: 'Monthly spending', content: 'A long document\nWith multiple lines', category: 'Quick Notes' } }, result: { structuredContent: { source_url: 'https://notes.example/note/1', result: { title: 'Monthly spending', etag: 'after' } } }, before: { old: true }, is_write: true, status: 'done', duration_ms: 320, created_at: new Date().toISOString() }]
   await c.route(base + '/**', async route => {
    const url = new URL(route.request().url()), p = url.pathname, method = route.request().method()
    const json = v => route.fulfill({ json: v })
    if (p === '/api/v1/ask/threads' && method === 'POST') { askPrompts.push(route.request().postDataJSON().text); return json({id:'fixture'}) }
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
    if (env.AI_TEST_ASSETS && !p.startsWith('/api/') && p !== '/pocket-ask.js') { const file = path.join(env.AI_TEST_ASSETS, p.startsWith('/assets/') || path.extname(p) ? p : 'index.html'); if (fs.existsSync(file)) return route.fulfill({ path: file }) }
    return route.continue()
   })
   const p = await c.newPage(), errors = []; p.on('pageerror', e => errors.push(e.message))
   async function overflow() { assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `Overflow at ${width}: ${p.url()}`) }
   await p.goto(base + '/'); await p.getByRole('button', { name: 'Ask my apps', exact: true }).waitFor(); assert.equal(await p.getByRole('button', { name: 'Ask my apps', exact: true }).getAttribute('aria-pressed'), 'true')
   const memoryChoice = p.getByRole('button', { name: 'Use saved memory', exact: true })
   assert.equal(await memoryChoice.getAttribute('aria-pressed'), 'true'); await memoryChoice.click(); assert.equal(await memoryChoice.getAttribute('aria-pressed'), 'false'); await memoryChoice.click()
   await p.getByRole('button', { name: 'Car expenses', exact: true }).click(); assert.equal(await p.locator('.composer textarea').inputValue(), 'Find my car expenses without counting duplicates')
   await p.locator('.composer textarea').fill(''); await p.locator('.home-hero h1').click()
   if (screenshots) { await p.waitForTimeout(250); await p.screenshot({ path: path.join(screenshots, `home-${width}-${theme}.png`) }) }
   // Every value picker, even a three-item model list, follows Inventory's
   // searchable pattern. These choices stay local or hit workflow fixtures.
   const modelPicker = p.getByRole('button', { name: 'App assistant model', exact: true })
   await modelPicker.click()
   let filter = p.getByRole('combobox', { name: 'Filter App assistant model', exact: true })
   await filter.fill('not-a-model'); await p.getByRole('status').filter({ hasText: 'No matches' }).waitFor()
   await filter.press('Escape'); assert.equal(await modelPicker.evaluate(e => document.activeElement === e), true)
   await modelPicker.press('ArrowDown'); filter = p.getByRole('combobox', { name: 'Filter App assistant model', exact: true })
   await filter.fill('oPu'); assert.equal(await p.getByRole('option').count(), 1)
   if (width < 720) {
    await p.evaluate(() => window.testViewport(375, 110)); await p.waitForTimeout(250)
    const box = await p.locator('.select-pop').boundingBox(); assert(box && box.x >= 0 && box.x + box.width <= width && box.y >= 110 && box.y + box.height <= 486, JSON.stringify(box))
    assert.equal(await filter.evaluate(e => getComputedStyle(e).fontSize), '16px')
    await p.evaluate(() => window.testViewport(844)); await p.waitForTimeout(100)
   }
   await filter.press('Enter'); assert.match(await modelPicker.innerText(), /Opus/)
   await modelPicker.click(); await p.getByRole('option', { name: 'Opus', exact: true }).waitFor()
   assert.equal(await p.getByRole('option', { name: 'Opus', exact: true }).getAttribute('aria-selected'), 'true')
   await p.getByRole('combobox').press('Tab'); assert.equal(await p.locator('.select-pop').count(), 0)
   assert.equal(await p.locator('select').count(), 0)
   await p.getByRole('button', { name: 'Work on the server', exact: true }).click()
   for (const picker of await p.locator('.composer .select-trigger').all()) {
    await picker.click(); await p.getByRole('combobox').fill('no-such-option'); await p.getByText('No matches', { exact: true }).waitFor(); await p.getByRole('combobox').press('Escape')
    assert.equal(await picker.evaluate(e => document.activeElement === e), true)
   }
   await p.getByRole('button', { name: 'Ask my apps', exact: true }).click()
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
    if (page === 'briefing') {
     await p.getByRole('link', { name: 'Open source: Tasks', exact:true }).waitFor()
     assert.equal(await p.locator('.briefing-items').getByText('Started a reading session',{exact:true}).count(),0); await p.locator('.briefing-items').getByText('Approve contract',{exact:true}).waitFor()
     const task = p.locator('.briefing-card').filter({has:p.getByRole('heading',{name:'Tasks',exact:true})})
     assert.equal(await task.locator('.briefing-items li').count(),3)
     await task.getByRole('button',{name:'Show 1 more',exact:true}).click(); assert.equal(await task.locator('.briefing-items li').count(),4)
     await task.getByRole('button',{name:'Show less',exact:true}).click()
     await task.locator('.briefing-source > summary').click(); await task.locator('.data-fields').first().waitFor()
     await p.getByLabel('Search my apps',{exact:true}).fill('Golf'); await p.getByRole('button',{name:'Search',exact:true}).click(); await p.getByRole('heading',{name:'Search results',exact:true}).waitFor()
     await p.getByRole('button',{name:'Back to today',exact:true}).click(); await p.getByRole('button',{name:'Summarize my day',exact:true}).waitFor()
     await p.getByRole('button',{name:'Summarize my day',exact:true}).click(); await p.waitForURL('**/t/fixture'); assert.match(askPrompts.at(-1),/concise briefing for today/)
     await p.goto(base+'/briefing'); await p.locator('.briefing-grid').waitFor(); assert.equal(await p.getByRole('button',{name:'Ask about Car maintenance',exact:true}).isDisabled(),true)
    }
    if (page === 'memory') { await p.getByLabel('Name', { exact: true }).fill('My car'); await p.getByLabel('Preference', { exact: true }).fill('Use the Golf'); await p.getByRole('button', { name: 'Save preference', exact: true }).click(); await p.getByRole('button', { name: 'Forget', exact: true }).waitFor(); await p.getByRole('button', { name: 'Forget', exact: true }).click() }
    if (page === 'actions') { await p.locator('.app-call details > summary').first().click(); await p.getByText('Monthly spending', {exact:true}).first().waitFor() }
    if (page === 'workflows') {
     const run = p.getByRole('button', { name: 'Run', exact: true })
     await run.click(); const search = p.getByRole('combobox', { name: 'Filter Run', exact: true })
     await search.fill('schedule'); await search.press('Enter'); await p.getByLabel('Schedule', { exact: false }).fill('daily 08:00')
     await run.click(); await p.getByRole('combobox', { name: 'Filter Run', exact: true }).fill('new tasks'); await p.getByRole('option', { name: 'When new tasks appear', exact: true }).click()
     await run.click(); await p.getByRole('combobox', { name: 'Filter Run', exact: true }).fill('press'); await p.getByRole('combobox').press('Enter')
     const workflowModel = p.getByRole('button', { name: 'Workflow model', exact: true })
     await workflowModel.click(); await p.getByRole('combobox').fill('hai'); await p.getByRole('combobox').press('Enter'); assert.match(await workflowModel.innerText(), /Haiku/)
     await run.click(); if (screenshots && width === 390) { await p.waitForTimeout(200); await p.screenshot({ path: path.join(screenshots, `dropdown-${theme}.png`) }); } await p.getByRole('heading', { name: 'Workflows', exact: true }).click(); assert.equal(await p.locator('.select-pop').count(), 0)
     assert.equal(await p.locator('select').count(), 0)
     await p.getByLabel('Name' , { exact: true }).fill('Weekly spending'); await p.getByLabel('Instructions', { exact: true }).fill('Summarize spending'); await p.getByRole('button', { name: 'Create workflow', exact: true }).click(); await p.getByRole('button', { name: 'Run now', exact: true }).waitFor() }
    if (screenshots && width === 390) await p.screenshot({ path: path.join(screenshots, `${page}-${theme}.png`) })
   }
   await p.goto(base + '/t/fixture'); await p.locator('.batch-review').waitFor(); assert.equal(await p.locator('.md table').count(), 1); assert.equal(await p.evaluate(() => !!window.badMarkdown), false); assert.equal(await p.locator('a.file-chip').count(), 1)
   if(env.AI_TEST_DEBUG) { console.log(await p.evaluate(() => Object.fromEntries(['.app','.thread-page','.thread-bottom','.req-dock','.batch-review','.batch-review summary','.composer'].map(s => { const e=document.querySelector(s), r=e?.getBoundingClientRect();return [s,r?{top:r.top,bottom:r.bottom,scroll:e.scrollHeight,client:e.clientHeight}:null] })))); await p.screenshot({path:'/tmp/pocket-batch-debug.png'}) }
   await p.locator('.batch-review > summary').click(); await p.locator('.batch-review input[type=checkbox]').first().check(); await p.getByRole('button', { name: 'Allow 1 selected' }).click(); await p.waitForTimeout(150); assert.deepEqual(pending, ['ask-1']); await overflow()
   assert.deepEqual(errors, []); summary.push({ width, theme, checks: 'home, searchable dropdowns, keyboard, briefing expansion/search/summary, unavailable sources, memory, workflow, actions, markdown, attachment, selected approval, overflow' })
  } finally { await c.close() }
 }
 console.log(JSON.stringify(summary, null, 2))
} finally { await browser.close() }
