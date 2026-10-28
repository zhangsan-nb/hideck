import assert from 'node:assert/strict'
import test from 'node:test'
import {
  MCP_CLIENTS, MCP_TOKEN_PLACEHOLDER, buildMcpAgentInstructions, buildMcpClientConfig, getMcpClient, mcpClientConfig
} from '../src/services/mcp-clients'

const endpoint = 'https://hideck.example.com:7576/mcp'
const token = 'test-token-for-config-generation'

test('all eight requested clients have setup paths, official docs and verification instructions', () => {
  assert.deepEqual(MCP_CLIENTS.map(client => client.id), ['codex', 'claude-code', 'cursor', 'gemini-cli', 'hermes', 'dsh', 'pi', 'grok-build'])
  for (const client of MCP_CLIENTS) {
    assert.ok(client.configPath)
    assert.equal(new URL(client.docsUrl).protocol, 'https:')
    assert.ok(client.verify)
  }
  assert.throws(() => getMcpClient('unknown' as never), /未知 MCP 客户端/)
})

test('Claude Code and Cursor receive their respective HTTP definitions', () => {
  const claude = JSON.parse(buildMcpClientConfig('claude-code', { endpoint, token })).mcpServers.hideck
  const cursor = JSON.parse(buildMcpClientConfig('cursor', { endpoint, token })).mcpServers.hideck
  assert.deepEqual(claude, { type: 'http', url: endpoint, headers: { Authorization: `Bearer ${token}` } })
  assert.deepEqual(cursor, { url: endpoint, headers: { Authorization: `Bearer ${token}` } })
})

test('Gemini uses httpUrl rather than its legacy SSE url field', () => {
  const config = JSON.parse(buildMcpClientConfig('gemini-cli', { endpoint, token })).mcpServers.hideck
  assert.equal(config.httpUrl, endpoint)
  assert.equal(config.url, undefined)
  assert.equal(config.headers.Authorization, `Bearer ${token}`)
  assert.equal(config.timeout, 360000)
})

test('Pi native MCP receives the URL, headers and timeout in seconds', () => {
  const config = JSON.parse(buildMcpClientConfig('pi', { endpoint, token })).mcpServers.hideck
  assert.equal(config.url, endpoint)
  assert.equal(config.headers.Authorization, `Bearer ${token}`)
  assert.equal(config.timeout, 360)
  assert.equal(config.command, undefined)
})

test('Codex uses TOML http_headers with enough time for a five-minute operation', () => {
  const config = buildMcpClientConfig('codex', { endpoint, token })
  assert.equal(config, [
    '[mcp_servers.hideck]', 'url = "https://hideck.example.com:7576/mcp"',
    'tool_timeout_sec = 360', 'http_headers = { Authorization = "Bearer test-token-for-config-generation" }'
  ].join('\n'))
})

test('Hermes generates an mcp_servers YAML entry', () => {
  const config = buildMcpClientConfig('hermes', { endpoint, token })
  assert.equal(config, [
    'mcp_servers:', '  hideck:', '    url: "https://hideck.example.com:7576/mcp"',
    '    timeout: 360', '    headers:', '      Authorization: "Bearer test-token-for-config-generation"'
  ].join('\n'))
})

test('Grok Build uses native HTTP headers rather than the Codex http_headers key', () => {
  const config = buildMcpClientConfig('grok-build', { endpoint, token })
  assert.equal(config, [
    '[mcp_servers.hideck]', 'url = "https://hideck.example.com:7576/mcp"',
    'tool_timeout_sec = 360', 'headers = { Authorization = "Bearer test-token-for-config-generation" }'
  ].join('\n'))
  assert.doesNotMatch(config, /http_headers|command =/)
  assert.match(buildMcpAgentInstructions('grok-build', { endpoint, token }), /grok mcp doctor hideck/)
})

test('DeepSeek dsh generates a Cordis insert patch with a Streamable HTTP plugin', () => {
  const config = buildMcpClientConfig('dsh', { endpoint, token })
  assert.equal(config, [
    '- insert:', '    - id: mcp-hideck', '      name: "@deepseek-ai/dsh-mcp-client"',
    '      config:', '        serverName: hideck', '        transport: streamable-http',
    '        url: "https://hideck.example.com:7576/mcp"', '        toolCallTimeoutMs: 360000',
    '        headers:', '          Authorization: "Bearer test-token-for-config-generation"'
  ].join('\n'))
})

test('special characters remain quoted data in JSON, TOML and YAML output', () => {
  const specialEndpoint = 'https://example.com/mcp?q="a"&other=\\value'
  const specialToken = 'quote"\\\nextra: value'
  for (const client of MCP_CLIENTS) {
    const config = buildMcpClientConfig(client.id, { endpoint: specialEndpoint, token: specialToken })
    if (client.format === 'JSON') {
      const server = JSON.parse(config).mcpServers.hideck
      assert.equal(server.url || server.httpUrl, specialEndpoint)
      assert.equal(server.headers.Authorization, `Bearer ${specialToken}`)
      continue
    }
    const url = config.match(/^\s*url\s*[:=]\s*(.+)$/m)?.[1]
    assert.ok(url)
    assert.equal(JSON.parse(url), specialEndpoint)
    const authorization = client.format === 'TOML'
      ? config.match(/(?:http_headers|headers) = \{ Authorization = (.+) \}/)?.[1]
      : config.match(/^\s*Authorization: (.+)$/m)?.[1]
    assert.ok(authorization)
    assert.equal(JSON.parse(authorization), `Bearer ${specialToken}`)
  }
})

test('templates never retain a token from a previous generation', () => {
  for (const client of MCP_CLIENTS) {
    assert.ok(buildMcpClientConfig(client.id, { endpoint, token }).includes(token))
    const template = buildMcpClientConfig(client.id, { endpoint })
    assert.ok(template.includes(MCP_TOKEN_PLACEHOLDER))
    assert.ok(!template.includes(token))
    const instructions = buildMcpAgentInstructions(client.id, { endpoint })
    assert.ok(!instructions.includes(token))
    assert.match(instructions, /占位符/)
  }
})

test('copy-to-agent instructions identify the client, merge scope and an actual validation tool', () => {
  for (const client of MCP_CLIENTS) {
    const instructions = buildMcpAgentInstructions(client.id, { endpoint, token })
    assert.ok(instructions.includes(client.name))
    assert.ok(instructions.includes(client.configPath))
    assert.ok(instructions.includes(client.docsUrl))
    assert.ok(instructions.includes(buildMcpClientConfig(client.id, { endpoint, token })))
    assert.match(instructions, /保留其他配置/)
    assert.match(instructions, /hideck_device_list/)
    assert.match(instructions, /audio_connection_id/)
    assert.doesNotMatch(instructions, /npm install|pip install|--dangerously/)
  }
})

test('the original generic JSON configuration helper remains compatible', () => {
  assert.equal(mcpClientConfig(endpoint, token), buildMcpClientConfig('claude-code', { endpoint, token }))
})
