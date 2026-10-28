<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { MCP_CLIENTS, buildMcpAgentInstructions, buildMcpClientConfig, getMcpClient, type McpClientId } from '../../services/mcp-clients'
import { copyToClipboard } from '../../utils/clipboard'

const props = defineProps<{ endpoint: string; token?: string }>()
const selected = defineModel<McpClientId>({ required: true })
const selectorId = useId()
const copyError = ref('')
const client = computed(() => getMcpClient(selected.value))
const config = computed(() => buildMcpClientConfig(selected.value, props))
const instructions = computed(() => buildMcpAgentInstructions(selected.value, props))

async function copy(value: string) {
  copyError.value = ''
  if (!await copyToClipboard(value)) copyError.value = '自动复制未完成，请选择配置文本手动复制。'
}
</script>

<template>
  <div class="mcp-client-setup">
    <label class="mcp-client-label" :for="selectorId">Agent / 客户端</label>
    <el-select :id="selectorId" v-model="selected" aria-label="选择 MCP 客户端" class="mcp-client-select">
      <el-option v-for="option in MCP_CLIENTS" :key="option.id" :label="option.name" :value="option.id" />
    </el-select>
    <p class="mcp-client-path">配置文件：<code>{{ client.configPath }}</code></p>
    <p class="mcp-client-hint">{{ client.hint }} <a :href="client.docsUrl" target="_blank" rel="noopener noreferrer">官方说明</a></p>
    <p v-if="!token" class="mcp-client-hint">将 <code>&lt;MCP_TOKEN&gt;</code> 替换为令牌。</p>
    <pre class="mcp-client-config" tabindex="0" :aria-label="`${client.name} ${client.format} 连接配置`">{{ config }}</pre>
    <div class="mcp-client-actions">
      <el-button @click="copy(config)">{{ token ? '复制连接配置' : '复制配置模板' }}</el-button>
      <el-button type="primary" plain @click="copy(instructions)">复制给 Agent</el-button>
    </div>
    <p v-if="copyError" class="mcp-client-error" role="alert">{{ copyError }}</p>
  </div>
</template>

<style scoped>
.mcp-client-setup { margin-top: 20px; min-width: 0; }
.mcp-client-label { display: block; margin-bottom: 8px; font-weight: 600; }
.mcp-client-select { width: 100%; max-width: 320px; }
.mcp-client-path { margin: 12px 0 8px; overflow-wrap: anywhere; }
.mcp-client-hint { margin-top: 8px; color: var(--ui-text-muted); font-size: 13px; line-height: 1.6; }
.mcp-client-hint a { color: var(--el-color-primary); text-decoration: underline; text-underline-offset: 3px; }
.mcp-client-config { max-height: 340px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; margin: 12px 0; padding: 14px; border: 1px solid var(--ui-border); border-radius: 8px; }
.mcp-client-config:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
code, pre { font-family: var(--font-mono, monospace); }
.mcp-client-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.mcp-client-actions :deep(.el-button + .el-button) { margin-left: 0; }
.mcp-client-error { margin-top: 12px; color: var(--el-color-danger); }
@media (max-width: 640px) { .mcp-client-actions :deep(.el-button) { min-height: 44px; } }
</style>
