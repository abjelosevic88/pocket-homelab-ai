import { test } from 'node:test'
import assert from 'node:assert/strict'
import { codePath, linkPath } from '../src/lib/filelinks.ts'

const cwd = '/home/arno/.pocket-ai/worktrees/athena/repo'

test('Markdown links to files become local paths', () => {
  assert.equal(linkPath('/home/arno/x/review.md', cwd), '/home/arno/x/review.md')
  assert.equal(linkPath('/home/arno/x/main.go:42', cwd), '/home/arno/x/main.go')
  assert.equal(linkPath('/home/arno/x/main.go#L10-L20', cwd), '/home/arno/x/main.go')
  assert.equal(linkPath('file:///tmp/a%20b.zip', cwd), '/tmp/a b.zip')
  assert.equal(linkPath('apps/chatbot/review.md', cwd), cwd + '/apps/chatbot/review.md')
  assert.equal(linkPath('../other/x.md', cwd), '/home/arno/.pocket-ai/worktrees/athena/other/x.md')
  assert.equal(linkPath('~/notes/a.md', cwd), '~/notes/a.md')
})

test('web links and Pocket AI pages stay as they are', () => {
  for (const h of ['https://example.com/a.md', 'mailto:a@b.c', '#top', '/t/abc123', '/files', '/settings/profile', '/v1/threads/x', '//cdn.example.com/x.js', 'README'])
    assert.equal(linkPath(h, cwd), '', h)
  assert.equal(linkPath('apps/x.md', ''), '', 'relative without a folder')
})

test('absolute paths in inline code', () => {
  assert.equal(codePath('/tmp/connector-parity-review-documents.zip'), '/tmp/connector-parity-review-documents.zip')
  assert.equal(codePath('/home/arno/.pocket-ai/worktrees/x/visual-overview.md'), '/home/arno/.pocket-ai/worktrees/x/visual-overview.md')
  assert.equal(codePath('/home/arno/x/main.go:12'), '/home/arno/x/main.go')
  assert.equal(codePath('~/Projects/'), '~/Projects/')
  for (const c of ['/users/:id', 'npm run build', '/tmp/*.log', '/api/files', '/usr', 'src/x.ts', '/home/$USER/x', 'rm -rf /tmp/x'])
    assert.equal(codePath(c), '', c)
})
