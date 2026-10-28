<script setup lang="ts">
import { ref } from 'vue'
import { mcpService, type McpDiagnostics } from '../../services/mcp'
import { errorMessage } from '../../services/http'
import { formatDeviceDateTime } from '../../utils/deviceTime'

const checking = ref(false)
const error = ref('')
const diagnostics = ref<McpDiagnostics>()

async function check() {
  if (checking.value) return
  checking.value = true
  error.value = ''
  diagnostics.value = undefined
  try { diagnostics.value = await mcpService.diagnostics() }
  catch (cause) { error.value = errorMessage(cause, '接入检查失败') }
  finally { checking.value = false }
}
</script>

<template>
  <div class="mcp-diagnostics" aria-label="MCP 接入诊断" :aria-busy="checking">
    <el-button :loading="checking" :disabled="checking" @click="check">检查接入</el-button>
    <p v-if="error" class="diagnostic-error" role="alert">{{ error }}</p>
    <div v-if="diagnostics" class="diagnostic-result" role="status">
      <p>{{ diagnostics.message }}</p>
      <p class="diagnostic-detail">检查时间：{{ formatDeviceDateTime(diagnostics.checked_at) }}</p>
      <p v-if="diagnostics.initialized" class="diagnostic-detail">
        {{ diagnostics.tool_count }} 项工具 · 输出契约 {{ diagnostics.output_schema_count }}/{{ diagnostics.tool_count }} · {{ diagnostics.version }}
      </p>
      <p class="diagnostic-detail">仅检查服务端；客户端网络和 TLS 以实际连接结果为准。以下记录在重启后清空。</p>
      <ul v-if="diagnostics.tokens.length" class="diagnostic-tokens" aria-label="令牌接入记录">
        <li v-for="token in diagnostics.tokens" :key="token.id">
          <strong>{{ token.name }}{{ token.revoked ? ' · 已撤销' : '' }}</strong>
          <span v-if="token.activity">最近鉴权请求：{{ formatDeviceDateTime(token.activity.last_request_at) }}</span>
          <span v-else>本次运行暂无鉴权请求。</span>
          <span v-if="token.activity?.last_tool">
            {{ token.activity.last_tool.name }} · {{ token.activity.last_tool.failed ? '调用失败' : '调用成功' }} · {{ token.activity.last_tool.duration_ms }} ms · {{ formatDeviceDateTime(token.activity.last_tool.at) }}
          </span>
        </li>
      </ul>
      <p class="diagnostic-detail">401：检查令牌或撤销状态；403：检查 Host/Origin；503：检查 MCP 开关或存储状态。</p>
      <p class="diagnostic-detail">工具调用成功只表示该请求成功，短信投递和异步任务完成仍需查询。</p>
    </div>
  </div>
</template>

<style scoped>
.mcp-diagnostics { margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--ui-border); }
.diagnostic-result { margin-top: 12px; overflow-wrap: anywhere; font-size: 13px; line-height: 1.6; }
.diagnostic-result p + p { margin-top: 8px; }
.diagnostic-detail { color: var(--ui-text-muted); }
.diagnostic-error { margin-top: 12px; color: var(--el-color-danger); }
.diagnostic-tokens { display: grid; gap: 12px; margin: 12px 0; list-style: none; }
.diagnostic-tokens li { display: grid; gap: 4px; }
.diagnostic-tokens span { color: var(--ui-text-muted); }
@media (max-width: 640px) { .mcp-diagnostics :deep(.el-button) { min-height: 44px; } }
</style>
