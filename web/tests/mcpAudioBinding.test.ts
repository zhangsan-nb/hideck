import assert from 'node:assert/strict'
import test from 'node:test'
import { useMcpAudioBinding } from '../src/components/phone/useMcpAudioBinding'
import type { McpBinding, McpConnection } from '../src/services/mcp'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

const authorization = { token_id: 'token-one', name: '网页电话' }
const connected: McpConnection = { ...authorization, id: 'connection-one', status: 'connected' }

function fixture() {
  const media = { mediaId: 'media-one', lease: 'lease-one', mediaReady: true, mediaState: 'connected' }
  const updates: { input: McpBinding; id?: string }[] = []
  const removed: string[] = []
  const departures: string[] = []
  const service = {
    async bind(input: McpBinding, id?: string): Promise<McpConnection> {
      updates.push({ input, id })
      return { ...connected }
    },
    async disconnect(id: string) { removed.push(id) },
    async disconnectOnPageHide(id: string) { departures.push(id) }
  }
  const dependencies = { service, media: () => media, enableMedia: async () => {} }
  const binding = useMcpAudioBinding(dependencies)
  return { binding, dependencies, service, media, updates, removed, departures }
}

test('MCP media rebuild updates the existing connection with current credentials', async () => {
  const f = fixture()
  await f.binding.connect(authorization)
  f.media.mediaId = 'media-two'
  f.media.lease = 'lease-two'
  await f.binding.synchronize()
  assert.deepEqual(f.updates.at(-1), {
    id: connected.id, input: { ...authorization, media_id: 'media-two', lease: 'lease-two' }
  })
  assert.equal(f.binding.connection.value?.id, connected.id)
})

test('MCP disconnection cannot be undone by a delayed media update response', async () => {
  const f = fixture()
  await f.binding.connect(authorization)
  const updating = deferred<McpConnection>()
  f.service.bind = () => updating.promise
  const pending = f.binding.synchronize()
  await f.binding.disconnect()
  updating.resolve(connected)
  await pending
  assert.equal(f.binding.connection.value, null)
  assert.deepEqual(f.removed, [connected.id])
})

test('page cache restoration does not display or renew a deleted MCP connection', async () => {
  const f = fixture()
  await f.binding.connect(authorization)
  await f.binding.pageHide()
  f.binding.pageShow()
  await f.binding.synchronize()
  assert.equal(f.binding.connection.value, null)
  assert.deepEqual(f.departures, [connected.id])
  assert.equal(f.updates.length, 1)
})

test('a connection created after page departure is explicitly deleted', async () => {
  const f = fixture()
  const creating = deferred<McpConnection>()
  const started = deferred<void>()
  f.service.bind = () => { started.resolve(); return creating.promise }
  const pending = f.binding.connect(authorization)
  const rejected = assert.rejects(pending, /授权已取消/)
  await started.promise
  await f.binding.pageHide()
  f.binding.pageShow()
  creating.resolve(connected)
  await rejected
  assert.equal(f.binding.connection.value, null)
  assert.deepEqual(f.removed, [connected.id])
})

test('page departure while preparing media cancels binding before its request', async () => {
  const f = fixture()
  const preparing = deferred<void>()
  f.dependencies.enableMedia = () => preparing.promise
  const pending = f.binding.connect(authorization)
  const rejected = assert.rejects(pending, /授权已取消/)
  await f.binding.pageHide()
  preparing.resolve()
  await rejected
  assert.deepEqual(f.updates, [])
})

test('failed MCP disconnection remains visible and propagates the error', async () => {
  const f = fixture()
  await f.binding.connect(authorization)
  f.service.disconnect = async () => { throw new Error('server unavailable') }
  await assert.rejects(f.binding.disconnect(), /server unavailable/)
  assert.equal(f.binding.connection.value?.id, connected.id)
})

test('failed browser media removes its MCP authorization', async () => {
  const f = fixture()
  await f.binding.connect(authorization)
  f.media.mediaState = 'failed'
  await f.binding.synchronize()
  assert.equal(f.binding.connection.value, null)
  assert.deepEqual(f.removed, [connected.id])
})
