import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = path.dirname(fileURLToPath(import.meta.url))
const source = fs.readFileSync(
  path.resolve(currentDir, '../src/components/settings/AutoAnswerSettings.vue'),
  'utf8',
)

test('automatic answer settings scale as one searchable device list', () => {
  assert.match(source, /const DEFAULT_AUTO_ANSWER_DELAY_SECONDS = 8/)
  assert.match(source, /const DEVICE_SEARCH_THRESHOLD = 5/)
  assert.match(source, /v-model="searchQuery"/)
  assert.match(source, /v-for="row in filteredRows"/)
  assert.match(source, /class="auto-answer-device-list" role="list"/)
  assert.match(source, /max-height: min\(440px, 55vh\)/)
  assert.match(source, /\.auto-answer-device-list \{ max-height: none; overflow: visible; \}/)
  assert.match(source, /class="auto-answer-list-header"/)
  assert.match(source, /@container \(max-width: 620px\)/)
  assert.match(source, /\.auto-answer-list-header \{ display: none; \}/)
})

test('automatic answer changes use one explicit save action with partial failure retention', () => {
  assert.match(source, /const DEVICE_SAVE_BATCH_SIZE = 4/)
  assert.match(source, /const dirtyRows = computed/)
  assert.match(source, /async function saveAll\(\)/)
  assert.match(source, /pending\.slice\(start, start \+ DEVICE_SAVE_BATCH_SIZE\)/)
  assert.match(source, /savedDevices\.value = nextSaved/)
  assert.match(source, /:disabled="busy \|\| !dirtyRows\.length"/)
  assert.doesNotMatch(source, /saveDevice\(row/)
})

test('automatic answer loading and controls remain explicit and touch safe', () => {
  assert.match(source, /v-else-if="loadError"/)
  assert.match(source, /role="alert"/)
  assert.match(source, />重新加载<\/el-button>/)
  assert.match(source, /:deep\(\.el-button\), :deep\(\.el-input-number\) \{ min-height: 44px; \}/)
  assert.match(source, /class="auto-answer-delay-stepper"/)
  assert.match(source, /class="auto-answer-delay-value"/)
  assert.match(source, /grid-template-columns: 44px minmax\(0, 1fr\) 44px/)
  assert.match(source, /justify-content: center/)
  assert.match(source, /text-align: center/)
  assert.match(source, /box-shadow: none !important/)
  assert.match(source, /:controls="false"/)
  assert.match(source, /<Subtract20Regular \/>/)
  assert.match(source, /<Add20Regular \/>/)
  assert.doesNotMatch(source, /controls-position="right"/)
})
