import { chromium } from 'playwright'
import assert from 'node:assert/strict'

// Serve the built UI with `npm run preview -- --host 127.0.0.1` first.
// All API traffic is mocked: a draft another Pocket app prepared opens on the
// start page, is sent through /drafts/{id}/start and the thread links back.
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:4173'
const browser = process.env.AI_TEST_CDP ? await chromium.connectOverCDP(process.env.AI_TEST_CDP) : await chromium.launch({ headless: true })
try {
  for (const width of [390, 1280]) {
    const context = await browser.newContext({ viewport: { width, height: 844 }, isMobile: width < 720, hasTouch: width < 720 })
    const user = { id: 1, name: 'Test', role: 'admin', can_run: true }
    const settings = { default_provider: 'claude', default_cwd: '/home/t', claude: { model: 'default', effort: 'high', mode: 'default', speed: '', account_id: 0 }, codex: { model: '', effort: '', mode: 'on-request', speed: '', account_id: 0 }, usage_widget: false }
    const providers = [
      { id: 'claude', name: 'Claude Code', available: true, models: [{ id: 'default', name: 'Default', efforts: ['low', 'high'] }, { id: 'opus', name: 'Opus', efforts: ['low', 'high'] }], efforts: ['low', 'high'], modes: [{ id: 'default', name: 'Ask first' }, { id: 'plan', name: 'Plan' }], speeds: [], default_model: 'default', default_effort: 'high', default_mode: 'default' },
      { id: 'codex', name: 'Codex', available: false, error: 'missing', models: [], efforts: [], modes: [], speeds: [] },
    ]
    const draft = { id: 'd1', provider: 'claude', account_id: null, model: 'opus', effort: 'low', mode: 'plan', cwd: '/home/t/Projects/athena', title: 'AS-2 · Fix Node.js and PHP security issues',
      text: 'Investigate AS-2 in this repository.', system: 'Team rules', source: 'projects', ref: 'AS-2', ref_url: 'https://projects.example/issue/AS-2', ref_label: 'AS-2 · Fix Node.js and PHP security issues', thread_id: null }
    const thread = { id: 't1', owner_id: 1, provider: 'claude', title: draft.title, status: 'idle', usage: {}, cwd: draft.cwd, source: 'link:projects', source_ref: 'AS-2', mode: 'plan', model: 'opus', effort: 'low' }
    let started = null, plainThreads = 0
    await context.route('**/*', route => {
      const url = new URL(route.request().url()), p = url.pathname, m = route.request().method()
      if (url.origin !== base) return route.abort()
      const json = (data, status = 200) => route.fulfill({ json: data, status })
      if (p.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: ': fixture\n\n' })
      if (p === '/api/v1/auth/status') return json({ authenticated: true, user, auth_mode: 'none' })
      if (p === '/api/v1/auth/me') return json(user)
      if (p === '/api/v1/settings') return json(settings)
      if (p === '/api/v1/providers') return json(providers)
      if (p === '/api/v1/drafts/d1' ) return json(started ? { ...draft, thread_id: 't1' } : draft)
      if (p === '/api/v1/drafts/d1/start' && m === 'POST') { started = route.request().postDataJSON(); return json(thread, 201) }
      if (p === '/api/v1/threads' && m === 'POST') { plainThreads++; return json(thread, 201) }
      if (p === '/api/v1/threads') return json(started ? [thread] : [])
      if (p === '/api/v1/threads/t1') return json({ thread, items: [{ key: 'u1', kind: 'user', text: started?.text || '' }] })
      if (p === '/api/v1/threads/t1/context') return json({ thread_id: 't1', source: 'projects', ref: 'AS-2', ref_url: draft.ref_url, ref_label: draft.ref_label })
      if (p === '/api/v1/fs/git') return json({})
      if (p.startsWith('/api/')) return json([])
      if (p === '/pocket-ask.js') return route.fulfill({ body: '' })
      return route.continue()
    })
    const page = await context.newPage(), errors = []
    page.on('pageerror', e => errors.push(e.message))
    await page.goto(base + '/?draft=d1')
    await page.getByText('review the prompt and press send').waitFor()
    const box = page.locator('.composer textarea').first()
    await box.waitFor()
    assert.equal(await box.inputValue(), draft.text)
    assert.equal(await page.evaluate(() => localStorage.getItem('pai-new-thread')), null, 'draft choice saved as last choice')
    await box.fill(draft.text + ' Check composer.lock too.')
    await box.press(process.platform === 'darwin' ? 'Meta+Enter' : 'Control+Enter')
    await page.waitForURL('**/t/t1')
    assert.ok(started, 'draft not started')
    assert.equal(plainThreads, 0)
    assert.equal(started.text, draft.text + ' Check composer.lock too.')
    assert.equal(started.model, 'opus'); assert.equal(started.mode, 'plan'); assert.equal(started.effort, 'low'); assert.equal(started.cwd, draft.cwd)
    const chip = page.getByRole('link', { name: 'AS-2 · Pocket Projects' })
    await chip.waitFor()
    assert.equal(await chip.getAttribute('href'), draft.ref_url)
    // Opening the draft link again goes to the thread it became.
    await page.goto(base + '/?draft=d1')
    await page.waitForURL('**/t/t1')
    assert.deepEqual(errors, [])
    await context.close()
  }
  console.log('link-draft: ok')
} finally {
  await browser.close()
}
