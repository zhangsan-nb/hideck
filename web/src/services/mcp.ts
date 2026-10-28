import { api } from '../stores/auth'
export { mcpClientConfig } from './mcp-clients'

export type McpToken = { id: string; name: string; prefix: string; created_at: string; revoked_at: string | null }
export type McpSettings = { enabled: boolean; path: string }
export type McpConnection = { id: string; name: string; status: string; token_id: string }
export type McpBinding = { token_id: string; name: string; media_id: string; lease: string }
export type McpDiagnostics = {
  version: string; tool_count: number; output_schema_count: number; instructions_available: boolean
  enabled: boolean; initialized: boolean; disclaimer_accepted: boolean; ready: boolean
  status: string; message: string; checked_at: string
  tokens: { id: string; name: string; revoked: boolean; activity: {
    last_request_at: string
    last_tool?: { name: string; at: string; duration_ms: number; failed: boolean }
  } | null }[]
}

export const mcpService = {
  async settings() { return (await api.get<McpSettings>('/settings/mcp')).data },
  async configure(enabled: boolean) { return (await api.put<McpSettings>('/settings/mcp', { enabled })).data },
  async diagnostics() { return (await api.get<McpDiagnostics>('/mcp/diagnostics')).data },
  async tokens() { return (await api.get<{ tokens: McpToken[] }>('/mcp/tokens')).data.tokens },
  async createToken(name: string) { return (await api.post<{ token: McpToken; secret: string }>('/mcp/tokens', { name })).data },
  async revokeToken(id: string) { await api.delete(`/mcp/tokens/${encodeURIComponent(id)}`) },
  async deleteToken(id: string) { await api.delete(`/mcp/tokens/${encodeURIComponent(id)}/record`) },
  async bind(input: McpBinding, id?: string) {
    const path = '/mcp/phone-connections'
    return id
      ? (await api.put<McpConnection>(`${path}/${encodeURIComponent(id)}`, input)).data
      : (await api.post<McpConnection>(path, input)).data
  },
  async disconnect(id: string) { await api.delete(`/mcp/phone-connections/${encodeURIComponent(id)}`) },
  async disconnectOnPageHide(id: string) {
    const authorization = String(api.defaults.headers.common.Authorization || '')
    const response = await fetch(`/api/mcp/phone-connections/${encodeURIComponent(id)}`, {
      method: 'DELETE', headers: { Authorization: authorization }, keepalive: true
    })
    if (!response.ok) throw new Error(`解除 MCP 音频绑定失败（HTTP ${response.status}）`)
  }
}
