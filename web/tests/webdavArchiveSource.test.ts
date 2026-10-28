import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [component, service, settings] = await Promise.all([
  readFile(new URL('../src/components/settings/WebDAVArchiveSettings.vue', import.meta.url), 'utf8'),
  readFile(new URL('../src/services/system.ts', import.meta.url), 'utf8'),
  readFile(new URL('../src/views/Settings.vue', import.meta.url), 'utf8'),
])

test('WebDAV settings use production APIs and preserve stored credentials', () => {
  assert.match(service, /get<ArchiveSettings>\('\/settings\/archive'\)/)
  assert.match(service, /post\('\/settings\/archive\/test'/)
  assert.match(component, /留空保留现有密码/)
  assert.match(component, /clear_password/)
  assert.doesNotMatch(component, /mock|fixture|demo/i)
})

test('archive cleanup requires preview and explicit confirmation', () => {
  assert.match(component, /previewArchiveCleanup\(\)/)
  assert.match(component, /cleanupPreview/)
  assert.match(component, /ElMessageBox\.confirm/)
  assert.match(service, /cleanupArchive\(previewToken: string\)/)
  assert.match(service, /preview_token: previewToken/)
  assert.match(component, /:disabled="busy \|\| hasUnsavedChanges"/)
})

test('archive settings remain responsive and touch safe', () => {
  assert.match(settings, /<WebDAVArchiveSettings \/>/)
  assert.match(component, /min-height: 44px/)
  assert.match(component, /@media \(max-width: 640px\)/)
  assert.doesNotMatch(component, /overflow-x:\s*auto/)
})
