export type McpClientId = 'codex' | 'claude-code' | 'cursor' | 'gemini-cli' | 'hermes' | 'dsh' | 'pi' | 'grok-build'

type McpClient = {
  readonly id: McpClientId
  readonly name: string
  readonly configPath: string
  readonly format: 'TOML' | 'JSON' | 'YAML'
  readonly hint: string
  readonly verify: string
  readonly docsUrl: string
}

export const MCP_TOKEN_PLACEHOLDER = '<MCP_TOKEN>'
// Covers HiDeck's existing five-minute eSIM download operation.
const TOOL_TIMEOUT_SECONDS = 360
const MILLISECONDS_PER_SECOND = 1000

export const MCP_CLIENTS: readonly McpClient[] = [
  {
    id: 'codex', name: 'Codex', configPath: '~/.codex/config.toml', format: 'TOML',
    hint: '保存后重启会话或重载 IDE。', verify: '运行 codex mcp list，或在会话中查看 /mcp。',
    docsUrl: 'https://learn.chatgpt.com/docs/extend/mcp'
  },
  {
    id: 'claude-code', name: 'Claude Code', configPath: '~/.claude.json', format: 'JSON',
    hint: '合并到 mcpServers，保留其他设置。', verify: '运行 claude mcp list，或在会话中查看 /mcp。',
    docsUrl: 'https://code.claude.com/docs/en/mcp'
  },
  {
    id: 'cursor', name: 'Cursor', configPath: '~/.cursor/mcp.json', format: 'JSON',
    hint: '保存后在 MCP 设置中检查连接。', verify: '在 Cursor 的 MCP 设置中确认 hideck 已连接并已发现工具。',
    docsUrl: 'https://cursor.com/docs/context/mcp'
  },
  {
    id: 'gemini-cli', name: 'Gemini CLI', configPath: '~/.gemini/settings.json', format: 'JSON',
    hint: 'Streamable HTTP 使用 httpUrl，保存后重载会话。', verify: '在 Gemini CLI 中运行 /mcp 查看连接。',
    docsUrl: 'https://geminicli.com/docs/tools/mcp-server/'
  },
  {
    id: 'hermes', name: 'Hermes Agent', configPath: '~/.hermes/config.yaml', format: 'YAML',
    hint: '合并到 mcp_servers 后运行 /reload-mcp。', verify: '运行 hermes mcp test hideck；在会话中运行 /reload-mcp 重新加载。',
    docsUrl: 'https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp'
  },
  {
    id: 'dsh', name: 'DeepSeek dsh', configPath: '$DSH_HOME/cordis.patch.yml', format: 'YAML',
    hint: '默认目录 ~/.dsh；合并 insert 补丁后重载。', verify: '在 dsh 会话中确认已发现 mcp__hideck__ 开头的工具。',
    docsUrl: 'https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/mcp/mcp-client/README.md'
  },
  {
    id: 'pi', name: 'Pi Agent', configPath: '~/.pi/agent/mcp.json', format: 'JSON',
    hint: '需支持 pi mcp 的新版 Pi；旧版按扩展文档配置。', verify: '运行 pi mcp list；已打开的会话运行 /reload。',
    docsUrl: 'https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md'
  },
  {
    id: 'grok-build', name: 'Grok Build', configPath: '~/.grok/config.toml', format: 'TOML',
    hint: '支持 GROK_HOME；保存后在 /mcps 中按 r 刷新。', verify: '运行 grok mcp doctor hideck 检查连接，grok mcp list 查看配置。',
    docsUrl: 'https://docs.x.ai/build/features/mcp-servers'
  }
]

type ConnectionInput = { readonly endpoint: string; readonly token?: string }

export function getMcpClient(id: McpClientId): McpClient {
  const client = MCP_CLIENTS.find(client => client.id === id)
  if (!client) throw new Error(`未知 MCP 客户端：${id}`)
  return client
}

export function buildMcpClientConfig(id: McpClientId, input: ConnectionInput): string {
  const authorization = `Bearer ${input.token || MCP_TOKEN_PLACEHOLDER}`
  const headers = { Authorization: authorization }
  if (id === 'codex' || id === 'grok-build') {
    const headerField = id === 'codex' ? 'http_headers' : 'headers'
    return [
      '[mcp_servers.hideck]', `url = ${JSON.stringify(input.endpoint)}`,
      `tool_timeout_sec = ${TOOL_TIMEOUT_SECONDS}`,
      `${headerField} = { Authorization = ${JSON.stringify(authorization)} }`
    ].join('\n')
  }
  if (id === 'hermes') {
    return [
      'mcp_servers:', '  hideck:', `    url: ${JSON.stringify(input.endpoint)}`,
      `    timeout: ${TOOL_TIMEOUT_SECONDS}`, '    headers:', `      Authorization: ${JSON.stringify(authorization)}`
    ].join('\n')
  }
  if (id === 'dsh') {
    return [
      '- insert:', '    - id: mcp-hideck', '      name: "@deepseek-ai/dsh-mcp-client"',
      '      config:', '        serverName: hideck', '        transport: streamable-http',
      `        url: ${JSON.stringify(input.endpoint)}`, `        toolCallTimeoutMs: ${TOOL_TIMEOUT_SECONDS * MILLISECONDS_PER_SECOND}`,
      '        headers:', `          Authorization: ${JSON.stringify(authorization)}`
    ].join('\n')
  }
  const server = jsonServerConfig(id, { url: input.endpoint, headers })
  return JSON.stringify({ mcpServers: { hideck: server } }, null, 2)
}

function jsonServerConfig(id: McpClientId, server: { url: string; headers: { Authorization: string } }) {
  switch (id) {
    case 'claude-code': return { type: 'http', ...server }
    case 'cursor': return server
    case 'gemini-cli': return { httpUrl: server.url, headers: server.headers, timeout: TOOL_TIMEOUT_SECONDS * MILLISECONDS_PER_SECOND }
    case 'pi': return { ...server, timeout: TOOL_TIMEOUT_SECONDS }
    default: throw new Error(`客户端 ${id} 不使用 JSON 配置`)
  }
}

export function buildMcpAgentInstructions(id: McpClientId, input: ConnectionInput): string {
  const client = getMcpClient(id)
  return [
    `请为 ${client.name} 配置名为 hideck 的远程 MCP 连接。`,
    `服务地址：${input.endpoint}`,
    '传输：Streamable HTTP；鉴权：Bearer 令牌，使用下方配置中的 Authorization 字段。',
    `请检查客户端版本，并按官方说明 ${client.docsUrl} 配置。`,
    `默认配置位置：${client.configPath}。如果已设置自定义配置目录，请使用实际目录。`,
    client.hint,
    '请只合并 hideck 连接，保留其他配置；HiDeck 服务已部署，无需下载或启动 HiDeck 服务端。',
    ...(input.token ? [] : ['配置中的 <MCP_TOKEN> 是占位符，请先让我提供有效的 HiDeck MCP 令牌再连接。']),
    '含令牌的配置只保存到本地用户配置中，不要提交到代码仓库或在回复中展示令牌。',
    'Agent 需能访问服务地址；使用本地 CA 的 HTTPS 时，先为客户端配置信任该 CA。',
    '', `\`\`\`${client.format.toLowerCase()}`, buildMcpClientConfig(id, input), '```', '',
    client.verify,
    '连接后调用 hideck_device_list 验证，并报告实际连接结果；失败时保留真实错误原因。',
    '需要网页音频的电话工具，须先在 HiDeck 电话页连接 MCP，再使用 audio_connection_id。'
  ].join('\n')
}

// Keep the original generic JSON helper available to existing callers.
export function mcpClientConfig(endpoint: string, token?: string) {
  return buildMcpClientConfig('claude-code', { endpoint, token })
}
