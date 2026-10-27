import { chromium } from 'playwright'
import assert from 'node:assert/strict'
import fs from 'node:fs'
const base = process.env.AI_TEST_BASE || 'http://127.0.0.1:4173'
const browser = await chromium.launch({ headless: true })
try {
  for (const width of [390, 1280]) {
    const context = await browser.newContext({ viewport: { width, height: 900 }, isMobile: width < 720, hasTouch: width < 720 })
    let snippets = [], nextID = 1, fail = true
    const sends = []
    const user = { id: 1, name: 'Test', role: 'admin', can_run: true }
    const thread = { id: 'snippet-test', owner_id: 1, provider: 'claude', title: 'Snippet check', status: 'idle', usage: {}, cwd: '' }
    await context.route('**/*', route => {
      const req = route.request(), url = new URL(req.url()), p = url.pathname
      if (url.origin !== base) return route.abort()
      const json = data => route.fulfill({ json: data })
      if (p.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: ': fixture\n\n' })
      if (p === '/api/v1/auth/status') return json({ authenticated: true, user, auth_mode: 'none' })
      if (p === '/api/v1/auth/me') return json(user)
      if (p === '/api/v1/settings') return json({ usage_widget: false })
      if (p === '/api/v1/threads') return json([thread])
      if (p === '/api/v1/threads/snippet-test') return json({ thread, items: [
        { key: 'a', kind: 'assistant', text: 'An earlier response' },
        { key: 'failed', kind: 'tool', tool: { kind: 'bash', name: 'Shell', status: 'error', input: { command: 'xcodebuild build' }, output: 'Temporary build failure', exit_code: 65 } },
        { key: 'retry', kind: 'tool', tool: { kind: 'bash', name: 'Shell', status: 'done', input: { command: 'swift test' }, output: 'Passed' } },
        { key: 't', kind: 'turn', turn: { ok: false, error: 'Stopped.', duration_ms: 1000 } },
      ] })
      if (p.endsWith('/messages')) { sends.push(req.postDataJSON()); return json({ thread }) }
      if (p === '/api/v1/snippets') {
        if (req.method() === 'POST') {
          if (fail) { fail = false; return route.fulfill({ status: 503, json: { error: 'Try saving again' } }) }
          const s = { id: nextID++, ...req.postDataJSON() }; snippets.push(s); return json(s)
        }
        return json(snippets)
      }
      if (p.startsWith('/api/v1/snippets/')) {
        const id = Number(p.split('/').at(-1))
        if (req.method() === 'DELETE') { snippets = snippets.filter(s => s.id !== id); return route.fulfill({ status: 204 }) }
        const s = { id, ...req.postDataJSON() }; snippets = snippets.map(row => row.id === id ? s : row); return json(s)
      }
      if (p.startsWith('/api/')) return json([])
      if (p === '/pocket-ask.js') return route.fulfill({ body: '' })
      return route.continue()
    })
    const page = await context.newPage(), errors = []
    page.on('pageerror', e => errors.push(e.message))
    await page.goto(base + '/snippets')
    await page.getByRole('button', { name: 'New snippet', exact: true }).click()
    await page.getByRole('textbox', { name: 'Snippet title', exact: true }).fill('Review changes')
    await page.getByRole('textbox', { name: 'Snippet text', exact: true }).fill('  Review the diff.\nRun checks.\n')
    await page.getByRole('button', { name: 'Save snippet', exact: true }).click()
    await page.getByRole('alert').getByText('Try saving again').waitFor()
    assert.equal(await page.getByRole('textbox', { name: 'Snippet text', exact: true }).inputValue(), '  Review the diff.\nRun checks.\n')
    await page.getByRole('button', { name: 'Save snippet', exact: true }).click()
    await page.getByRole('button', { name: 'Edit snippet: Review changes', exact: true }).waitFor()
    await page.reload()
    await page.getByRole('button', { name: 'Edit snippet: Review changes', exact: true }).click()
    await page.getByRole('textbox', { name: 'Snippet text', exact: true }).fill('  Updated review.\n')
    await page.getByRole('button', { name: 'Save snippet', exact: true }).click()
    await page.getByRole('button', { name: 'Edit snippet: Review changes', exact: true }).waitFor()
    await page.goto(base + '/t/snippet-test')
    const box = page.getByRole('textbox', { name: 'Message', exact: true })
    await box.fill('Existing draft')
    assert.equal(await page.getByText('Stopped.', { exact: true }).count(), 0)
    assert.equal(await page.locator('.tool.error').count(), 0)
    assert.equal(await page.getByText('exit 65', { exact: true }).count(), 0)
    assert.equal(await page.locator('.tool-run').count(), 1)
    await page.getByRole('button', { name: 'Work details', exact: true }).click()
    await page.getByText('Temporary build failure', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Work details', exact: true }).click()
    await page.getByRole('button', { name: 'Insert snippet', exact: true }).click()
    await page.getByRole('button', { name: 'Insert snippet: Review changes', exact: true }).click()
    assert.equal(await box.inputValue(), 'Existing draft\n\n  Updated review.\n')
    assert.equal(sends.length, 0)
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1))
    fs.mkdirSync('/tmp/pocketai-snippet-web-captures', { recursive: true })
    await page.screenshot({ path: `/tmp/pocketai-snippet-web-captures/snippet-${width}.png` })
    await page.goto(base + '/snippets')
    await page.getByRole('button', { name: 'Delete snippet: Review changes', exact: true }).click()
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await page.getByText('No snippets yet', { exact: true }).waitFor()
    assert.equal(snippets.length, 0)
    assert.deepEqual(errors, [])
    console.log(`Snippets CRUD, failed-save recovery, persistence, insert, quiet interruption: ${width}px passed`)
    await context.close()
  }
} finally { await browser.close() }
